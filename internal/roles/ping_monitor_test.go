package roles

import (
	"context"
	"strings"
	"testing"
	"time"

	"connection_monitor/internal/monitor"
)

// newTargetMonitor builds a single pingTargetMonitor for synchronous unit tests.
func newTargetMonitor(target string, failureThreshold int, channels []string, n Notifier) *pingTargetMonitor {
	return &pingTargetMonitor{
		target:   target,
		interval: 100 * time.Millisecond,
		timeout:  200 * time.Millisecond,
		channels: channels,
		notifier: n,
		sm:       monitor.New(failureThreshold),
		logger:   testLogger(),
	}
}

func TestPingMonitorSuccessAgainstPongServer(t *testing.T) {
	addr := startPongServer(t)
	fn := &fakeNotifier{}
	m := newTargetMonitor(addr, 1, []string{"log"}, fn)
	m.runCheck(context.Background())
	if len(fn.snapshot()) != 0 {
		t.Fatalf("healthy probe should not alert, got %+v", fn.snapshot())
	}
}

func TestPingMonitorFailureFiresAlert(t *testing.T) {
	fn := &fakeNotifier{}
	// Nothing listening on this address.
	m := newTargetMonitor("127.0.0.1:1", 2, []string{"log"}, fn)
	ctx := context.Background()
	m.runCheck(ctx) // fail 1
	if len(fn.snapshot()) != 0 {
		t.Fatal("no alert before threshold")
	}
	m.runCheck(ctx) // fail 2 -> alert
	alerts := fn.snapshot()
	if len(alerts) != 1 || alerts[0].State != "UNHEALTHY" {
		t.Fatalf("expected one UNHEALTHY alert, got %+v", alerts)
	}
	if !strings.Contains(alerts[0].Detail, "127.0.0.1:1") {
		t.Fatalf("alert detail should name the target, got %q", alerts[0].Detail)
	}
}

func TestPingMonitorRecoversAfterServerAvailable(t *testing.T) {
	fn := &fakeNotifier{}
	m := newTargetMonitor("127.0.0.1:1", 1, []string{"log"}, fn)
	ctx := context.Background()
	m.runCheck(ctx) // fail -> UNHEALTHY

	// Now point at a live server and probe again.
	addr := startPongServer(t)
	m.target = addr
	m.runCheck(ctx) // success -> HEALTHY

	alerts := fn.snapshot()
	if len(alerts) != 2 || alerts[0].State != "UNHEALTHY" || alerts[1].State != "HEALTHY" {
		t.Fatalf("unexpected alert sequence: %+v", alerts)
	}
}

// TestPingMonitorMultipleTargetsIndependent verifies two targets run on their
// own goroutines with their own settings and do not interfere: one live
// target stays HEALTHY (no alert), one dead target goes UNHEALTHY once at its
// own threshold.
func TestPingMonitorMultipleTargetsIndependent(t *testing.T) {
	liveAddr := startPongServer(t)
	fn := &fakeNotifier{}

	// The live target uses a higher threshold so that a single probe
	// interrupted by shutdown cancellation cannot spuriously trip it; over the
	// run it stays healthy (successes reset the counter).
	pm := NewPingMonitor([]PingTarget{
		{Target: liveAddr, Interval: 50 * time.Millisecond, Timeout: 500 * time.Millisecond, Channels: []string{"log"}, Mon: monitor.Config{FailureThreshold: 5}},
		{Target: "127.0.0.1:1", Interval: 50 * time.Millisecond, Timeout: 200 * time.Millisecond, Channels: []string{"log"}, Mon: monitor.Config{FailureThreshold: 2}},
	}, fn, nil, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pm.Run(ctx)
		close(done)
	}()

	// Give both monitors several cycles to run.
	time.Sleep(600 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("PingMonitor.Run did not stop after cancellation")
	}

	alerts := fn.snapshot()
	// The dead target must fire exactly one UNHEALTHY alert; the live target
	// must never alert.
	var deadAlerts, liveAlerts int
	for _, a := range alerts {
		switch {
		case strings.Contains(a.Detail, "127.0.0.1:1"):
			deadAlerts++
			if a.State != "UNHEALTHY" {
				t.Fatalf("dead target alert should be UNHEALTHY, got %+v", a)
			}
		case strings.Contains(a.Detail, liveAddr):
			liveAlerts++
		}
	}
	if deadAlerts != 1 {
		t.Fatalf("expected exactly one alert for dead target, got %d (all: %+v)", deadAlerts, alerts)
	}
	if liveAlerts != 0 {
		t.Fatalf("live target should not alert, got %d (all: %+v)", liveAlerts, alerts)
	}
}
