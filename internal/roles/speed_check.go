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
// configured binary with the license/GDPR auto-accept flags.
func NewSpeedCheck(binary string, interval time.Duration, threshold float64, failureThreshold int, channels []string, notifier Notifier, runner RunnerFunc, logger *slog.Logger) *SpeedCheck {
	if logger == nil {
		logger = slog.Default()
	}
	s := &SpeedCheck{
		binary:    binary,
		interval:  interval,
		threshold: threshold,
		channels:  channels,
		notifier:  notifier,
		sm:        monitor.New(failureThreshold),
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
		s.logger.Warn("speed_check failed", slog.Any("error", err))
	case mbps < s.threshold:
		success = false
		detail = fmt.Sprintf("download %.2f Mbps below threshold %.2f Mbps", mbps, s.threshold)
	default:
		success = true
		detail = fmt.Sprintf("download %.2f Mbps at/above threshold %.2f Mbps", mbps, s.threshold)
	}
	ev := s.sm.Observe(success)
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
