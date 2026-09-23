package roles

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"connection_monitor/internal/monitor"
)

// RunnerFunc executes the speedtest binary and returns its raw stdout. It is
// injectable so tests can supply fakes without the real binary installed.
type RunnerFunc func(ctx context.Context) ([]byte, error)

// SpeedCheck runs the Ookla speedtest CLI each interval and compares the
// measured download throughput against a threshold.
type SpeedCheck struct {
	binary    string
	interval  time.Duration
	threshold float64 // Mbps
	channels  []string
	notifier  Notifier
	sm        *monitor.StateMachine
	handle    *monitor.Handle
	runner    RunnerFunc
	logger    *slog.Logger
}

// ooklaResult is the subset of the Ookla JSON output we consume.
type ooklaResult struct {
	Download struct {
		Bandwidth float64 `json:"bandwidth"` // bytes/sec
	} `json:"download"`
}

// NewSpeedCheck builds a SpeedCheck. A nil runner defaults to executing the
// configured binary with the license/GDPR auto-accept flags. reg may be nil,
// in which case fleet-wide status snapshots are not logged.
func NewSpeedCheck(binary string, interval time.Duration, threshold float64, mon monitor.Config, channels []string, notifier Notifier, runner RunnerFunc, reg *monitor.Registry, logger *slog.Logger) *SpeedCheck {
	if logger == nil {
		logger = slog.Default()
	}
	s := &SpeedCheck{
		binary:    binary,
		interval:  interval,
		threshold: threshold,
		channels:  channels,
		notifier:  notifier,
		sm:        monitor.NewWithConfig(mon),
		handle:    reg.Register("speed_check"),
		runner:    runner,
		logger:    logger,
	}
	if s.runner == nil {
		s.runner = s.execRunner
	}
	return s
}

// execRunner is the real runner: it invokes the Ookla speedtest CLI.
func (s *SpeedCheck) execRunner(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, s.binary,
		"--accept-license", "--accept-gdpr", "--format=json")
	return cmd.Output()
}

// Run runs a check every interval until ctx is cancelled.
func (s *SpeedCheck) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.runCheck(ctx)
		}
	}
}

func (s *SpeedCheck) runCheck(ctx context.Context) {
	mbps, err := s.measure(ctx)
	var success bool
	var detail string
	switch {
	case err != nil:
		success = false
		detail = fmt.Sprintf("speedtest failed: %v", err)
	case mbps < s.threshold:
		success = false
		detail = fmt.Sprintf("download %.2f Mbps below threshold %.2f Mbps", mbps, s.threshold)
	default:
		success = true
		detail = fmt.Sprintf("download %.2f Mbps at/above threshold %.2f Mbps", mbps, s.threshold)
	}
	// Log the outcome of every execution, not just state transitions. A failing
	// check logs at WARN (with the underlying error, if any); a passing one at
	// INFO.
	level := slog.LevelInfo
	if !success {
		level = slog.LevelWarn
	}
	attrs := []slog.Attr{
		slog.Float64("mbps", mbps),
		slog.Float64("threshold_mbps", s.threshold),
		slog.Bool("success", success),
		slog.String("detail", detail),
	}
	if err != nil {
		attrs = append(attrs, slog.Any("error", err))
	}
	s.logger.LogAttrs(ctx, level, "speed_check result", attrs...)

	ev := s.sm.Observe(success)
	s.handle.Update(s.sm)
	now := time.Now()
	s.handle.RecordCheck(now, success)
	// Record the measured throughput whenever a real measurement was taken
	// (err == nil), even if it was below threshold; an exec failure has no
	// meaningful Mbps to record.
	if err == nil {
		s.handle.RecordSpeed(now, mbps)
	}
	if ev != monitor.None {
		s.handle.LogSnapshot(s.logger)
	}
	dispatchEvent(ctx, s.notifier, s.channels, "speed_check", detail, ev, s.sm.ConsecutiveFailures())
}

// measure runs the runner and parses download throughput in Mbps.
func (s *SpeedCheck) measure(ctx context.Context) (float64, error) {
	out, err := s.runner(ctx)
	if err != nil {
		return 0, fmt.Errorf("run speedtest: %w", err)
	}
	var res ooklaResult
	if err := json.Unmarshal(out, &res); err != nil {
		return 0, fmt.Errorf("parse speedtest json: %w", err)
	}
	// Ookla reports download.bandwidth in bytes/sec; Mbps = bytes*8/1e6.
	return res.Download.Bandwidth * 8 / 1e6, nil
}
