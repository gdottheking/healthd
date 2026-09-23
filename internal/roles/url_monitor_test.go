package roles

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connection_monitor/internal/monitor"
)

// newURLTargetMonitor builds a single urlTargetMonitor for synchronous tests.
func newURLTargetMonitor(url string, failureThreshold int, n Notifier) *urlTargetMonitor {
	return &urlTargetMonitor{
		url:      url,
		interval: 100 * time.Millisecond,
		timeout:  500 * time.Millisecond,
		channels: []string{"log"},
		notifier: n,
		sm:       monitor.New(failureThreshold),
		client:   &http.Client{Timeout: 500 * time.Millisecond},
		logger:   testLogger(),
	}
}

func TestURLMonitorHealthyDoesNotAlert(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fn := &fakeNotifier{}
	m := newURLTargetMonitor(srv.URL+"/health/live", 1, fn)
	m.runCheck(context.Background())
	if len(fn.snapshot()) != 0 {
		t.Fatalf("healthy 2xx should not alert, got %+v", fn.snapshot())
	}
}

func TestURLMonitorFailureThenRecovery(t *testing.T) {
	var status atomic.Int64
	status.Store(http.StatusServiceUnavailable)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()

	fn := &fakeNotifier{}
	m := newURLTargetMonitor(srv.URL+"/health", 2, fn)
	ctx := context.Background()

	m.runCheck(ctx) // fail 1 (503)
	if len(fn.snapshot()) != 0 {
		t.Fatal("no alert before threshold")
	}
	m.runCheck(ctx) // fail 2 -> UNHEALTHY
	alerts := fn.snapshot()
	if len(alerts) != 1 || alerts[0].State != "UNHEALTHY" {
		t.Fatalf("expected one UNHEALTHY alert, got %+v", alerts)
	}
	if !strings.Contains(alerts[0].Detail, srv.URL) {
		t.Fatalf("alert detail should name the url, got %q", alerts[0].Detail)
	}

	// Recover on the next 2xx.
	status.Store(http.StatusOK)
	m.runCheck(ctx)
	alerts = fn.snapshot()
	if len(alerts) != 2 || alerts[1].State != "HEALTHY" {
		t.Fatalf("expected a HEALTHY recovery alert, got %+v", alerts)
	}
}

func TestURLMonitorSelfSignedNeedsInsecureSkipVerify(t *testing.T) {
	// httptest.NewTLSServer presents a self-signed certificate.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Default client verifies certs, so the self-signed cert fails the check.
	fn := &fakeNotifier{}
	m := newURLTargetMonitor(srv.URL+"/health", 1, fn)
	m.runCheck(context.Background())
	alerts := fn.snapshot()
	if len(alerts) != 1 || alerts[0].State != "UNHEALTHY" {
		t.Fatalf("self-signed cert should fail verification and alert, got %+v", alerts)
	}

	// End-to-end through NewURLMonitor with insecure_skip_verify: the target's
	// client is built with verification disabled, so the check succeeds. Drive a
	// single check synchronously to avoid cancel-mid-request flakiness.
	reg := monitor.NewRegistry()
	fn2 := &fakeNotifier{}
	um := NewURLMonitor([]URLTarget{{
		URL:                srv.URL + "/health",
		Interval:           time.Second,
		Timeout:            500 * time.Millisecond,
		Channels:           []string{"log"},
		Mon:                monitor.Config{FailureThreshold: 1},
		InsecureSkipVerify: true,
	}}, fn2, reg, testLogger())

	um.monitors[0].runCheck(context.Background())

	if a := fn2.snapshot(); len(a) != 0 {
		t.Fatalf("insecure_skip_verify should accept the self-signed cert, got %+v", a)
	}
	if u := reg.Summary().Units; len(u) != 1 || u[0].Status.State != monitor.Healthy {
		t.Fatalf("expected one HEALTHY url unit, got %+v", u)
	}
}

func TestURLMonitorRegistersAndRecordsHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := monitor.NewRegistry()
	fn := &fakeNotifier{}
	um := NewURLMonitor([]URLTarget{
		{URL: srv.URL + "/health", Interval: 30 * time.Millisecond, Timeout: 500 * time.Millisecond, Channels: []string{"log"}, Mon: monitor.Config{FailureThreshold: 1}},
	}, fn, reg, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { um.Run(ctx); close(done) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("URLMonitor.Run did not stop after cancellation")
	}

	sum := reg.Summary()
	if len(sum.Units) != 1 {
		t.Fatalf("expected 1 registered unit, got %d", len(sum.Units))
	}
	u := sum.Units[0]
	if !strings.HasPrefix(u.Status.Name, "url_monitor:") {
		t.Fatalf("unit name: got %q want url_monitor:* prefix", u.Status.Name)
	}
	if len(u.Checks) == 0 {
		t.Fatal("expected recorded check history for the url target")
	}
}
