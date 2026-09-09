package monitor

import "testing"

func TestNoFireBeforeThreshold(t *testing.T) {
	m := New(3)
	if ev := m.Observe(false); ev != None {
		t.Fatalf("failure 1: got %v want None", ev)
	}
	if ev := m.Observe(false); ev != None {
		t.Fatalf("failure 2: got %v want None", ev)
	}
	if m.ConsecutiveFailures() != 2 {
		t.Fatalf("consecutive: got %d want 2", m.ConsecutiveFailures())
	}
}

func TestFireUnhealthyOnceAtThreshold(t *testing.T) {
	m := New(3)
	m.Observe(false)
	m.Observe(false)
	if ev := m.Observe(false); ev != AlertUnhealthy {
		t.Fatalf("at threshold: got %v want AlertUnhealthy", ev)
	}
	if m.State() != Unhealthy {
		t.Fatalf("state: got %v want Unhealthy", m.State())
	}
	// Further failures while unhealthy must not re-fire.
	if ev := m.Observe(false); ev != None {
		t.Fatalf("repeat failure: got %v want None", ev)
	}
	if m.ConsecutiveFailures() != 4 {
		t.Fatalf("consecutive: got %d want 4", m.ConsecutiveFailures())
	}
}

func TestRecoveryFiresOnce(t *testing.T) {
	m := New(2)
	m.Observe(false)
	m.Observe(false) // -> unhealthy
	if ev := m.Observe(true); ev != RecoveryHealthy {
		t.Fatalf("recovery: got %v want RecoveryHealthy", ev)
	}
	if m.State() != Healthy {
		t.Fatalf("state: got %v want Healthy", m.State())
	}
	// Another success while healthy must not re-fire.
	if ev := m.Observe(true); ev != None {
		t.Fatalf("repeat success: got %v want None", ev)
	}
}

func TestCanCycleAgain(t *testing.T) {
	m := New(1)
	if ev := m.Observe(false); ev != AlertUnhealthy {
		t.Fatalf("cycle1 down: got %v", ev)
	}
	if ev := m.Observe(true); ev != RecoveryHealthy {
		t.Fatalf("cycle1 up: got %v", ev)
	}
	if ev := m.Observe(false); ev != AlertUnhealthy {
		t.Fatalf("cycle2 down: got %v", ev)
	}
	if ev := m.Observe(true); ev != RecoveryHealthy {
		t.Fatalf("cycle2 up: got %v", ev)
	}
}

func TestThresholdClamped(t *testing.T) {
	m := New(0)
	if ev := m.Observe(false); ev != AlertUnhealthy {
		t.Fatalf("clamped threshold should fire at 1: got %v", ev)
	}
}

func TestSuccessResetsCounterWhileHealthy(t *testing.T) {
	m := New(3)
	m.Observe(false)
	m.Observe(false)
	if ev := m.Observe(true); ev != None {
		t.Fatalf("success while healthy: got %v want None", ev)
	}
	if m.ConsecutiveFailures() != 0 {
		t.Fatalf("counter should reset, got %d", m.ConsecutiveFailures())
	}
	// Now needs a fresh run of 3 to fire.
	m.Observe(false)
	m.Observe(false)
	if ev := m.Observe(false); ev != AlertUnhealthy {
		t.Fatalf("after reset: got %v want AlertUnhealthy", ev)
	}
}
