package roles

import (
	"context"
	"log/slog"
	"time"

	"connection_monitor/internal/monitor"
)

// SummaryReporter periodically logs an aggregated summary of every monitored
// unit. It is only run when the instance has at least one monitoring role
// enabled, so a pong-only instance stays quiet.
type SummaryReporter struct {
	reg      *monitor.Registry
	interval time.Duration
	logger   *slog.Logger
}

// NewSummaryReporter builds a reporter that logs the registry summary every
// interval.
func NewSummaryReporter(reg *monitor.Registry, interval time.Duration, logger *slog.Logger) *SummaryReporter {
	if logger == nil {
		logger = slog.Default()
	}
	return &SummaryReporter{reg: reg, interval: interval, logger: logger}
}

// Run logs an aggregated summary every interval until ctx is cancelled.
func (r *SummaryReporter) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.reg.LogSummary(r.logger)
		}
	}
}
