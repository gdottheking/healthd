package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeService records calls and returns a configurable error.
type fakeService struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeService) Notify(ctx context.Context, alert Alert) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func (f *fakeService) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDispatcherRetriesUpToMax(t *testing.T) {
	failing := &fakeService{err: errors.New("boom")}
	d := NewDispatcher(map[string]INotificationService{"f": failing}, 3, time.Millisecond, quietLogger())
	d.Dispatch(context.Background(), Alert{Role: "r"}, []string{"f"})
	if failing.count() != 3 {
		t.Fatalf("expected 3 attempts, got %d", failing.count())
	}
}

func TestDispatcherStopsOnSuccess(t *testing.T) {
	ok := &fakeService{}
	d := NewDispatcher(map[string]INotificationService{"ok": ok}, 5, time.Millisecond, quietLogger())
	d.Dispatch(context.Background(), Alert{}, []string{"ok"})
	if ok.count() != 1 {
		t.Fatalf("expected 1 attempt on success, got %d", ok.count())
	}
}

func TestDispatcherIsolatesChannels(t *testing.T) {
	failing := &fakeService{err: errors.New("boom")}
	sibling := &fakeService{}
	d := NewDispatcher(map[string]INotificationService{
		"bad":  failing,
		"good": sibling,
	}, 2, time.Millisecond, quietLogger())
	d.Dispatch(context.Background(), Alert{}, []string{"bad", "good"})
	if sibling.count() != 1 {
		t.Fatalf("sibling should have received alert once, got %d", sibling.count())
	}
	if failing.count() != 2 {
		t.Fatalf("failing channel should have retried twice, got %d", failing.count())
	}
}

// panicService panics to prove one channel cannot take down the dispatcher.
type panicService struct{}

func (panicService) Notify(ctx context.Context, alert Alert) error {
	panic("kaboom")
}

func TestDispatcherIsolatesPanic(t *testing.T) {
	sibling := &fakeService{}
	d := NewDispatcher(map[string]INotificationService{
		"panic": panicService{},
		"good":  sibling,
	}, 1, time.Millisecond, quietLogger())
	d.Dispatch(context.Background(), Alert{}, []string{"panic", "good"})
	if sibling.count() != 1 {
		t.Fatalf("sibling should still receive alert, got %d", sibling.count())
	}
}

// blockingService signals each call and blocks until the context is cancelled.
type blockingService struct {
	calls atomic.Int32
}

func (b *blockingService) Notify(ctx context.Context, alert Alert) error {
	b.calls.Add(1)
	return errors.New("always fails")
}

func TestDispatcherRespectsCancellation(t *testing.T) {
	svc := &blockingService{}
	// Large base delay so cancellation lands during backoff.
	d := NewDispatcher(map[string]INotificationService{"s": svc}, 10, 500*time.Millisecond, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Dispatch(ctx, Alert{}, []string{"s"})
		close(done)
	}()

	// Let the first attempt run, then cancel during backoff.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not stop promptly after cancellation")
	}
	if got := svc.calls.Load(); got > 2 {
		t.Fatalf("expected cancellation to stop retries early, got %d attempts", got)
	}
}

func TestBackoffNeverNegative(t *testing.T) {
	d := NewDispatcher(nil, 100, 500*time.Millisecond, quietLogger())
	// Large attempts would overflow an unclamped shift into a negative delay.
	for attempt := 1; attempt <= 100; attempt++ {
		if got := d.backoff(attempt); got < 0 {
			t.Fatalf("attempt %d produced negative delay %v", attempt, got)
		}
	}
	// The delay must be clamped at the ceiling, not wrap around.
	capped := d.backoff(maxBackoffShift + 5)
	expected := 500 * time.Millisecond * time.Duration(int64(1)<<maxBackoffShift)
	if capped != expected {
		t.Fatalf("clamped backoff: got %v want %v", capped, expected)
	}
}

func TestBackoffGrowsExponentially(t *testing.T) {
	d := NewDispatcher(nil, 5, 10*time.Millisecond, quietLogger())
	if d.backoff(1) != 10*time.Millisecond {
		t.Fatalf("attempt 1: got %v", d.backoff(1))
	}
	if d.backoff(2) != 20*time.Millisecond {
		t.Fatalf("attempt 2: got %v", d.backoff(2))
	}
	if d.backoff(3) != 40*time.Millisecond {
		t.Fatalf("attempt 3: got %v", d.backoff(3))
	}
}

func TestDispatcherUnknownChannelIgnored(t *testing.T) {
	ok := &fakeService{}
	d := NewDispatcher(map[string]INotificationService{"ok": ok}, 1, time.Millisecond, quietLogger())
	// Should not panic on unknown channel name.
	d.Dispatch(context.Background(), Alert{}, []string{"nope", "ok"})
	if ok.count() != 1 {
		t.Fatalf("known channel should still fire, got %d", ok.count())
	}
}
