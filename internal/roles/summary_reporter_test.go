package roles

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"connection_monitor/internal/monitor"
)

// lockedBuffer is a concurrency-safe io.Writer for capturing log output.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSummaryReporterLogsPeriodically(t *testing.T) {
	reg := monitor.NewRegistry()
	h := reg.Register("internet_check")
	h.RecordCheck(time.Now(), true)

	var out lockedBuffer
	logger := slog.New(slog.NewTextHandler(&out, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewSummaryReporter(reg, 20*time.Millisecond, logger)
	go r.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), "monitoring summary") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	logs := out.String()
	if !strings.Contains(logs, "monitoring summary") {
		t.Fatalf("expected a periodic summary log line, got:\n%s", logs)
	}
	if !strings.Contains(logs, "internet_check") {
		t.Fatalf("summary should name the monitored unit:\n%s", logs)
	}
}

func TestSummaryReporterStopsOnCancel(t *testing.T) {
	reg := monitor.NewRegistry()
	reg.Register("internet_check")
	r := NewSummaryReporter(reg, 10*time.Millisecond, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reporter did not stop after context cancel")
	}
}
