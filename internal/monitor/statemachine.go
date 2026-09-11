// Package monitor provides a reusable, timer-free alert state machine shared
// by the ping_monitor, internet_check, and speed_check roles, plus a rolling
// availability window and a fleet-wide status registry.
package monitor

import "time"

// Event is emitted by the state machine after each observed check result.
type Event int

const (
	// None means no state transition and no notification is required.
	None Event = iota
	// AlertUnhealthy means the machine just transitioned to UNHEALTHY and the
	// caller should fire a single alert.
	AlertUnhealthy
	// RecoveryHealthy means the machine just transitioned back to HEALTHY and
	// the caller should fire a single recovery notification.
	RecoveryHealthy
)

// State is the health state tracked by the machine.
type State int

const (
	// Healthy is the initial state.
	Healthy State = iota
	// Unhealthy is entered after the configured trigger condition is met.
	Unhealthy
)

// String returns the wire string for a State ("HEALTHY" / "UNHEALTHY").
func (s State) String() string {
	if s == Unhealthy {
		return "UNHEALTHY"
	}
	return "HEALTHY"
}

// Trigger selects the condition that drives UNHEALTHY/HEALTHY transitions.
type Trigger int

const (
	// TriggerConsecutive transitions after failureThreshold consecutive
	// failures (and recovers on the first success). This is the default.
	TriggerConsecutive Trigger = iota
	// TriggerAvailability transitions once the rolling window is full and
	// availability drops below MinAvailability, recovering when it rises back
	// to or above the threshold.
	TriggerAvailability
)

// Config configures a StateMachine.
type Config struct {
	// FailureThreshold is the consecutive-failure count that trips an alert in
	// TriggerConsecutive mode. Clamped to a minimum of 1.
	FailureThreshold int
	// WindowSize, when > 0, enables a rolling availability window of that many
	// samples. Required for TriggerAvailability; optional (reporting only) for
	// TriggerConsecutive.
	WindowSize int
	// Trigger selects the transition condition.
	Trigger Trigger
	// MinAvailability is the availability percentage (0..100) below which
	// TriggerAvailability transitions to UNHEALTHY.
	MinAvailability float64
}

// StateMachine tracks health state, consecutive failures, an optional rolling
// availability window, and the timestamp of the last successful check. It is
// not safe for concurrent use; each unit owns its own instance and drives it
// from a single goroutine.
type StateMachine struct {
	failureThreshold int
	trigger          Trigger
	minAvailability  float64

	state       State
	consecutive int
	window      *Window
	lastSuccess time.Time

	// now is injectable for deterministic tests; nil means time.Now.
	now func() time.Time
}

// New returns a StateMachine in consecutive-failure mode. failureThreshold is
// clamped to a minimum of 1. It is shorthand for NewWithConfig with only
// FailureThreshold set.
func New(failureThreshold int) *StateMachine {
	return NewWithConfig(Config{FailureThreshold: failureThreshold})
}

// NewWithConfig returns a StateMachine in the Healthy state configured by cfg.
func NewWithConfig(cfg Config) *StateMachine {
	ft := cfg.FailureThreshold
	if ft < 1 {
		ft = 1
	}
	var w *Window
	if cfg.WindowSize > 0 {
		w = newWindow(cfg.WindowSize)
	}
	return &StateMachine{
		failureThreshold: ft,
		trigger:          cfg.Trigger,
		minAvailability:  cfg.MinAvailability,
		state:            Healthy,
		window:           w,
	}
}

// Observe records a single check result and returns the resulting Event.
// success=true means the check passed; success=false means it failed. The
// consecutive counter, rolling window, and last-success timestamp are updated
// in both trigger modes; only the configured trigger drives transitions.
func (m *StateMachine) Observe(success bool) Event {
	if success {
		m.consecutive = 0
		m.lastSuccess = m.clock()
	} else {
		m.consecutive++
	}
	if m.window != nil {
		m.window.Add(success)
	}

	if m.trigger == TriggerAvailability {
		return m.transitionAvailability()
	}
	return m.transitionConsecutive(success)
}

// transitionConsecutive drives transitions from the consecutive-failure count.
func (m *StateMachine) transitionConsecutive(success bool) Event {
	if success {
		if m.state == Unhealthy {
			m.state = Healthy
			return RecoveryHealthy
		}
		return None
	}
	if m.state == Healthy && m.consecutive >= m.failureThreshold {
		m.state = Unhealthy
		return AlertUnhealthy
	}
	return None
}

// transitionAvailability drives transitions from the rolling window. It stays
// Healthy during warm-up (until the window is full) to avoid a false alert
// from an early miss.
func (m *StateMachine) transitionAvailability() Event {
	if m.window == nil || !m.window.Full() {
		return None
	}
	avail := m.window.Availability()
	if m.state == Healthy && avail < m.minAvailability {
		m.state = Unhealthy
		return AlertUnhealthy
	}
	if m.state == Unhealthy && avail >= m.minAvailability {
		m.state = Healthy
		return RecoveryHealthy
	}
	return None
}

// clock returns the current time using the injected clock when set.
func (m *StateMachine) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// State returns the current health state.
func (m *StateMachine) State() State { return m.state }

// ConsecutiveFailures returns the current consecutive-failure count.
func (m *StateMachine) ConsecutiveFailures() int { return m.consecutive }

// HasWindow reports whether a rolling availability window is tracked.
func (m *StateMachine) HasWindow() bool { return m.window != nil }

// Availability returns the rolling-window availability percentage (0..100), or
// 100 when no window is configured.
func (m *StateMachine) Availability() float64 {
	if m.window == nil {
		return 100
	}
	return m.window.Availability()
}

// LastSuccess returns the timestamp of the last successful check, or the zero
// time if no check has succeeded yet.
func (m *StateMachine) LastSuccess() time.Time { return m.lastSuccess }
