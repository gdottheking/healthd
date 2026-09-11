package roles

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connection_monitor/internal/monitor"
)

// captureLogger returns a logger writing to buf and the buf for assertions.
// The buffer is guarded so tests that run checks on multiple goroutines stay
// race-free, though these tests drive checks synchronously.
func captureLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, nil)), buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSnapshotLoggedOnStateChange(t *testing.T) {
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(badSrv.Close)

	logger, buf := captureLogger()
	reg := monitor.NewRegistry()
	c := NewInternetCheck(
		[]string{badSrv.URL}, time.Second, time.Second, monitor.Config{FailureThreshold: 1},
		[]string{"log"}, &fakeNotifier{}, badSrv.Client(), reg, logger,
	)

	c.runCheck(context.Background()) // fail -> UNHEALTHY -> snapshot

	out := buf.String()
	if !strings.Contains(out, "status snapshot") {
		t.Fatalf("expected a status snapshot log on transition, got:\n%s", out)
	}
	if !strings.Contains(out, "internet_check") {
		t.Fatalf("snapshot should name the internet_check unit, got:\n%s", out)
	}
}

func TestNoSnapshotWithoutStateChange(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(okSrv.Close)

	logger, buf := captureLogger()
	reg := monitor.NewRegistry()
	c := NewInternetCheck(
		[]string{okSrv.URL}, time.Second, time.Second, monitor.Config{FailureThreshold: 1},
		[]string{"log"}, &fakeNotifier{}, okSrv.Client(), reg, logger,
	)

	c.runCheck(context.Background()) // healthy, no transition

	if strings.Contains(buf.String(), "status snapshot") {
		t.Fatalf("no snapshot expected without a state change, got:\n%s", buf.String())
	}
}

func TestSpeedCheckLogsResultEveryExecution(t *testing.T) {
	logger, buf := captureLogger()
	sc := NewSpeedCheck("speedtest", time.Second, 50, monitor.Config{FailureThreshold: 1},
		nil, &fakeNotifier{}, fakeRunner(sampleOoklaJSON, nil), nil, logger)

	sc.runCheck(context.Background()) // success, no transition, but must log result

	out := buf.String()
	if !strings.Contains(out, "speed_check result") {
		t.Fatalf("expected a per-execution result log, got:\n%s", out)
	}
	if !strings.Contains(out, "mbps=100") {
		t.Fatalf("result log should include measured mbps, got:\n%s", out)
	}
}
