package roles

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"connection_monitor/internal/notify"
)

// fakeNotifier records dispatched alerts for assertions.
type fakeNotifier struct {
	mu     sync.Mutex
	alerts []notify.Alert
}

func (f *fakeNotifier) Dispatch(ctx context.Context, alert notify.Alert, channelNames []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, alert)
}

func (f *fakeNotifier) snapshot() []notify.Alert {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]notify.Alert, len(f.alerts))
	copy(out, f.alerts)
	return out
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
