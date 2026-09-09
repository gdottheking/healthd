package notify

import (
	"context"
	"log/slog"
)

// Console writes structured, leveled log lines to a slog.Logger. It never
// logs secrets (the Alert struct carries none).
type Console struct {
	logger *slog.Logger
}

// NewConsole returns a Console using the given logger, or slog.Default when
// logger is nil.
func NewConsole(logger *slog.Logger) *Console {
	if logger == nil {
		logger = slog.Default()
	}
	return &Console{logger: logger}
}

// Notify logs the alert. UNHEALTHY alerts log at WARN, everything else at INFO.
func (c *Console) Notify(ctx context.Context, alert Alert) error {
	level := slog.LevelInfo
	if alert.State == "UNHEALTHY" {
		level = slog.LevelWarn
	}
	c.logger.LogAttrs(ctx, level, "alert",
		slog.String("role", alert.Role),
		slog.String("state", alert.State),
		slog.Time("timestamp", alert.Timestamp),
		slog.String("detail", alert.Detail),
		slog.Int("consecutive_failures", alert.ConsecutiveFailures),
	)
	return nil
}
