// Package monitor provides a reusable, timer-free alert state machine shared
// by the ping_monitor, internet_check, and speed_check roles.
package monitor

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
	// Unhealthy is entered after failureThreshold consecutive failures.
	Unhealthy
)

// StateMachine tracks consecutive failures and health state. It is not safe
// for concurrent use; each role owns its own instance and drives it from a
// single goroutine.
type StateMachine struct {
	failureThreshold int
	state            State
	consecutive      int
}

// New returns a StateMachine in the Healthy state. failureThreshold is clamped
// to a minimum of 1.
func New(failureThreshold int) *StateMachine {
	if failureThreshold < 1 {
		failureThreshold = 1
	}
	return &StateMachine{failureThreshold: failureThreshold, state: Healthy}
}

// Observe records a single check result and returns the resulting Event.
// success=true means the check passed; success=false means it failed.
func (m *StateMachine) Observe(success bool) Event {
	if success {
		m.consecutive = 0
		if m.state == Unhealthy {
			m.state = Healthy
			return RecoveryHealthy
		}
		return None
	}

	m.consecutive++
	if m.state == Healthy && m.consecutive >= m.failureThreshold {
		m.state = Unhealthy
		return AlertUnhealthy
	}
	return None
}

// State returns the current health state.
func (m *StateMachine) State() State {
	return m.state
}

// ConsecutiveFailures returns the current consecutive-failure count.
func (m *StateMachine) ConsecutiveFailures() int {
	return m.consecutive
}
