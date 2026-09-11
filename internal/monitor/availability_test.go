package monitor

import (
	"testing"
	"time"
)

func availabilitySM(windowSize int, minAvail float64) *StateMachine {
	return NewWithConfig(Config{
		WindowSize:      windowSize,
		Trigger:         TriggerAvailability,
		MinAvailability: minAvail,
	})
}

func TestAvailabilityStaysHealthyDuringWarmup(t *testing.T) {
	// Window of 4, threshold 75%. Until the window is full, no alert may fire
	// even if every early sample fails.
	m := availabilitySM(4, 75)
	for i := 0; i < 3; i++ {
		if ev := m.Observe(false); ev != None {
			t.Fatalf("warmup failure %d: got %v want None", i, ev)
		}
	}
	if m.State() != Healthy {
		t.Fatalf("state during warmup: got %v want Healthy", m.State())
	}
}

func TestAvailabilityAlertsWhenBelowThreshold(t *testing.T) {
	m := availabilitySM(4, 75)
	// Fill window to full with 3 failures + 1 success = 25% < 75%.
	m.Observe(false)
	m.Observe(false)
	m.Observe(false)
	if ev := m.Observe(true); ev != AlertUnhealthy {
		t.Fatalf("full window below threshold: got %v want AlertUnhealthy", ev)
	}
	if m.State() != Unhealthy {
		t.Fatalf("state: got %v want Unhealthy", m.State())
	}
	// Availability reporting should reflect 1/4 = 25%.
	if got := m.Availability(); got != 25 {
		t.Fatalf("availability: got %v want 25", got)
	}
}

func TestAvailabilityRecoversWhenBackAboveThreshold(t *testing.T) {
	m := availabilitySM(4, 75)
	m.Observe(false)
	m.Observe(false)
	m.Observe(false)
	m.Observe(true) // 25% -> UNHEALTHY
	// Feed successes until availability climbs back to >= 75%.
	// After 2 more successes: window is [f f t t t]-> last 4 = [f t t t] = 75%.
	m.Observe(true) // window: f f t t -> 50%
	if ev := m.Observe(true); ev != RecoveryHealthy {
		t.Fatalf("back at threshold: got %v want RecoveryHealthy", ev)
	}
	if m.State() != Healthy {
		t.Fatalf("state: got %v want Healthy", m.State())
	}
}

func TestAvailabilityDoesNotRefireWhileUnhealthy(t *testing.T) {
	m := availabilitySM(2, 75)
	m.Observe(false)
	if ev := m.Observe(false); ev != AlertUnhealthy { // full, 0%
		t.Fatalf("expected alert, got %v", ev)
	}
	if ev := m.Observe(false); ev != None {
		t.Fatalf("repeat while unhealthy: got %v want None", ev)
	}
}

func TestLastSuccessTracked(t *testing.T) {
	m := New(3)
	if !m.LastSuccess().IsZero() {
		t.Fatal("last success should be zero before any success")
	}
	before := time.Now()
	m.Observe(false)
	if !m.LastSuccess().IsZero() {
		t.Fatal("failure must not set last success")
	}
	m.Observe(true)
	ls := m.LastSuccess()
	if ls.IsZero() || ls.Before(before) {
		t.Fatalf("last success not updated on success: %v", ls)
	}
	// A later failure must not clear the recorded last-success time.
	m.Observe(false)
	if !m.LastSuccess().Equal(ls) {
		t.Fatalf("last success changed on failure: got %v want %v", m.LastSuccess(), ls)
	}
}

func TestConsecutiveModeStillTracksWindow(t *testing.T) {
	// A consecutive-mode machine with a window reports availability but is not
	// driven by it.
	m := NewWithConfig(Config{FailureThreshold: 5, WindowSize: 4})
	if !m.HasWindow() {
		t.Fatal("expected window to be tracked")
	}
	m.Observe(false)
	m.Observe(true)
	if ev := m.Observe(false); ev != None {
		t.Fatalf("consecutive mode should not alert on low availability: got %v", ev)
	}
	if got := m.Availability(); got == 100 {
		t.Fatalf("availability should reflect failures, got %v", got)
	}
}
