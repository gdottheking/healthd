package roles

import (
	"context"
	"time"

	"connection_monitor/internal/monitor"
	"connection_monitor/internal/notify"
)

// Notifier dispatches an alert to a set of named channels. It is satisfied by
// *notify.Dispatcher.
type Notifier interface {
	Dispatch(ctx context.Context, alert notify.Alert, channelNames []string)
}

// stateName maps a monitor.State to the wire string used in alerts.
func stateName(s monitor.State) string {
	if s == monitor.Unhealthy {
		return "UNHEALTHY"
	}
	return "HEALTHY"
}

// dispatchEvent turns a state-machine event into an alert dispatch, if any.
func dispatchEvent(ctx context.Context, n Notifier, channels []string, role, detail string, ev monitor.Event, consecutive int) {
	switch ev {
	case monitor.AlertUnhealthy:
		n.Dispatch(ctx, notify.Alert{
			Role:                role,
			State:               "UNHEALTHY",
			Timestamp:           time.Now(),
			Detail:              detail,
			ConsecutiveFailures: consecutive,
		}, channels)
	case monitor.RecoveryHealthy:
		n.Dispatch(ctx, notify.Alert{
			Role:                role,
			State:               "HEALTHY",
			Timestamp:           time.Now(),
			Detail:              detail,
			ConsecutiveFailures: 0,
		}, channels)
	}
}
