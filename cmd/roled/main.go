// Command roled is a multi-role connection monitoring daemon. A single
// process runs any subset of the configured roles concurrently. Config is
// read once at startup; restart to apply changes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"connection_monitor/internal/config"
	"connection_monitor/internal/monitor"
	"connection_monitor/internal/notify"
	"connection_monitor/internal/roles"
)

// role is anything with a Run(ctx) that returns when ctx is cancelled.
type role interface {
	Run(ctx context.Context) error
}

func main() {
	configPath := flag.String("config", "./config.json", "path to config JSON file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(*configPath, logger); err != nil {
		logger.Error("roled failed", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(configPath string, logger *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	channels, err := buildChannels(cfg, logger)
	if err != nil {
		return err
	}

	dispatcher := notify.NewDispatcher(
		channels,
		cfg.Retry.MaxAttempts,
		time.Duration(cfg.Retry.BaseDelayMS)*time.Millisecond,
		logger,
	)

	// registry collects the live status of every monitored unit across roles so
	// a fleet-wide snapshot can be logged whenever any unit changes state.
	registry := monitor.NewRegistry()
	active := buildRoles(cfg, dispatcher, registry, logger)
	if len(active) == 0 {
		return fmt.Errorf("no roles enabled; nothing to do")
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// A derived, cancelable context so a fatal role error can bring the whole
	// daemon down, not just a received signal.
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		fatalMu  sync.Mutex
		fatalErr error
	)
	for name, r := range active {
		wg.Add(1)
		go func(name string, r role) {
			defer wg.Done()
			err := r.Run(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				// A role failed at setup/runtime (e.g. bind failure). Record
				// the first such error and cancel so the daemon fails fast.
				logger.Error("role failed", slog.String("role", name), slog.Any("error", err))
				fatalMu.Lock()
				if fatalErr == nil {
					fatalErr = fmt.Errorf("role %s: %w", name, err)
				}
				fatalMu.Unlock()
				cancel()
				return
			}
			logger.Info("role stopped", slog.String("role", name))
		}(name, r)
	}

	logger.Info("roled started", slog.Int("roles", len(active)))
	<-ctx.Done()
	logger.Info("stopping roles")
	wg.Wait()
	logger.Info("roled stopped")

	fatalMu.Lock()
	defer fatalMu.Unlock()
	return fatalErr
}

// buildChannels constructs channel implementations from config, resolving
// secrets from the environment.
func buildChannels(cfg *config.Config, logger *slog.Logger) (map[string]notify.INotificationService, error) {
	out := make(map[string]notify.INotificationService, len(cfg.Channels))
	for name, ch := range cfg.Channels {
		switch ch.Type {
		case config.ChannelConsole:
			out[name] = notify.NewConsole(logger)
		case config.ChannelWebhook:
			out[name] = notify.NewWebhook(ch.URL, time.Duration(ch.TimeoutMS)*time.Millisecond)
		case config.ChannelEmail:
			pass := os.Getenv(ch.PasswordEnv)
			out[name] = notify.NewEmail(ch.SMTPHost, ch.Port, ch.From, ch.To, ch.Username, pass)
		case config.ChannelSMSGate:
			pass := os.Getenv(ch.PasswordEnv)
			out[name] = notify.NewSMSGate(ch.BaseURL, ch.Username, pass, ch.Recipients, time.Duration(ch.TimeoutMS)*time.Millisecond)
		default:
			return nil, fmt.Errorf("channel %q: unknown type %q", name, ch.Type)
		}
	}
	return out, nil
}

// monitorConfig translates a role's config fields into a monitor.Config.
func monitorConfig(trigger string, windowSize, failureThreshold int, minAvailability float64) monitor.Config {
	t := monitor.TriggerConsecutive
	if trigger == config.TriggerAvailability {
		t = monitor.TriggerAvailability
	}
	return monitor.Config{
		FailureThreshold: failureThreshold,
		WindowSize:       windowSize,
		Trigger:          t,
		MinAvailability:  minAvailability,
	}
}

// buildRoles wires enabled roles against the dispatcher and status registry.
func buildRoles(cfg *config.Config, dispatcher *notify.Dispatcher, registry *monitor.Registry, logger *slog.Logger) map[string]role {
	active := make(map[string]role)

	// An instance is "monitoring" if it runs any role that observes hosts and
	// feeds the registry. Such an instance both logs a periodic aggregated
	// summary and answers get-summary requests.
	monitoring := cfg.Roles.PingMonitor.Enabled || cfg.Roles.InternetCheck.Enabled || cfg.Roles.SpeedCheck.Enabled

	if cfg.Roles.PongServer.Enabled {
		r := cfg.Roles.PongServer
		handlers := []roles.MessageHandler{roles.PingHandler{}}
		if monitoring {
			// Only a monitoring instance can answer get-summary; a pong-only
			// instance rejects it as an unsupported request type.
			handlers = append(handlers, roles.NewSummaryHandler(registry))
		}
		msgDispatcher := roles.NewMessageDispatcher(handlers...)
		active["pong_server"] = roles.NewTCPListener(
			r.Listen,
			time.Duration(r.ReadTimeoutMS)*time.Millisecond,
			msgDispatcher,
			logger,
		)
	}

	if cfg.Roles.PingMonitor.Enabled {
		targets := make([]roles.PingTarget, 0, len(cfg.Roles.PingMonitor.Targets))
		for _, t := range cfg.Roles.PingMonitor.Targets {
			targets = append(targets, roles.PingTarget{
				Target:   t.Target,
				Interval: time.Duration(t.IntervalS) * time.Second,
				Timeout:  time.Duration(t.TimeoutMS) * time.Millisecond,
				Channels: t.Notify,
				Mon:      monitorConfig(t.Trigger, t.WindowSize, t.FailureThreshold, t.MinAvailability),
			})
		}
		active["ping_monitor"] = roles.NewPingMonitor(targets, dispatcher, registry, logger)
	}

	if cfg.Roles.InternetCheck.Enabled {
		r := cfg.Roles.InternetCheck
		active["internet_check"] = roles.NewInternetCheck(
			r.Sites,
			time.Duration(r.IntervalS)*time.Second,
			time.Duration(r.TimeoutMS)*time.Millisecond,
			monitorConfig(r.Trigger, r.WindowSize, r.FailureThreshold, r.MinAvailability),
			r.Notify,
			dispatcher,
			nil,
			registry,
			logger,
		)
	}

	if cfg.Roles.SpeedCheck.Enabled {
		r := cfg.Roles.SpeedCheck
		active["speed_check"] = roles.NewSpeedCheck(
			r.Binary,
			time.Duration(r.IntervalS)*time.Second,
			r.ThresholdMbps,
			monitorConfig(r.Trigger, r.WindowSize, r.FailureThreshold, r.MinAvailability),
			r.Notify,
			dispatcher,
			nil,
			registry,
			logger,
		)
	}

	// A monitoring instance logs an aggregated summary on a fixed cadence.
	if monitoring {
		active["summary_reporter"] = roles.NewSummaryReporter(
			registry,
			time.Duration(cfg.SummaryIntervalS)*time.Second,
			logger,
		)
	}

	return active
}
