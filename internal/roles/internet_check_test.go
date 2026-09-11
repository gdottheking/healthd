package roles

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connection_monitor/internal/monitor"
)

func TestInternetCheckRoundRobin(t *testing.T) {
	c := NewInternetCheck(
		[]string{"a", "b", "c"},
		time.Second, time.Second, monitor.Config{FailureThreshold: 3},
		nil, &fakeNotifier{}, http.DefaultClient, nil, testLogger(),
	)
	want := []string{"a", "b", "c", "a", "b"}
	for i, w := range want {
		if got := c.nextSite(); got != w {
			t.Fatalf("cycle %d: got %q want %q", i, got, w)
		}
	}
}

func TestInternetCheckSuccessAndFailure(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(okSrv.Close)
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(badSrv.Close)

	c := NewInternetCheck(
		[]string{okSrv.URL}, time.Second, time.Second, monitor.Config{FailureThreshold: 3},
		nil, &fakeNotifier{}, okSrv.Client(), nil, testLogger(),
	)
	if err := c.checkSite(context.Background(), okSrv.URL); err != nil {
		t.Fatalf("2xx should succeed: %v", err)
	}
	if err := c.checkSite(context.Background(), badSrv.URL); err == nil {
		t.Fatal("5xx should fail")
	}
}

func TestInternetCheckFeedsStateMachine(t *testing.T) {
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(badSrv.Close)

	fn := &fakeNotifier{}
	c := NewInternetCheck(
		[]string{badSrv.URL}, time.Second, time.Second, monitor.Config{FailureThreshold: 2},
		[]string{"log"}, fn, badSrv.Client(), nil, testLogger(),
	)
	ctx := context.Background()
	c.runCheck(ctx) // fail 1
	if len(fn.snapshot()) != 0 {
		t.Fatal("no alert should fire before threshold")
	}
	c.runCheck(ctx) // fail 2 -> threshold
	alerts := fn.snapshot()
	if len(alerts) != 1 || alerts[0].State != "UNHEALTHY" {
		t.Fatalf("expected one UNHEALTHY alert, got %+v", alerts)
	}
	if alerts[0].ConsecutiveFailures != 2 {
		t.Fatalf("consecutive: got %d want 2", alerts[0].ConsecutiveFailures)
	}
}

func TestInternetCheckRecovery(t *testing.T) {
	// A server we can flip between healthy and unhealthy.
	healthy := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthy {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(srv.Close)

	fn := &fakeNotifier{}
	c := NewInternetCheck(
		[]string{srv.URL}, time.Second, time.Second, monitor.Config{FailureThreshold: 1},
		[]string{"log"}, fn, srv.Client(), nil, testLogger(),
	)
	ctx := context.Background()
	c.runCheck(ctx) // fail -> UNHEALTHY
	healthy = true
	c.runCheck(ctx) // success -> HEALTHY
	alerts := fn.snapshot()
	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts, got %d: %+v", len(alerts), alerts)
	}
	if alerts[0].State != "UNHEALTHY" || alerts[1].State != "HEALTHY" {
		t.Fatalf("unexpected alert sequence: %+v", alerts)
	}
}
