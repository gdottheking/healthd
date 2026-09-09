// Package notify provides notification channels (console, webhook, email,
// smsgate) and a retrying, per-channel-isolated dispatcher.
package notify

import (
	"context"
	"time"
)

// Alert is the payload delivered to notification channels.
type Alert struct {
	Role                string    `json:"role"`
	State               string    `json:"state"` // "UNHEALTHY" or "HEALTHY"
	Timestamp           time.Time `json:"timestamp"`
	Detail              string    `json:"detail"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
}

// INotificationService is a single delivery channel.
type INotificationService interface {
	// Notify delivers the alert. A non-nil error signals a failed delivery so
	// the dispatcher can retry.
	Notify(ctx context.Context, alert Alert) error
}
