package roles

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connection_monitor/internal/monitor"
)

// URLTarget describes one independent HTTP health monitor.
type URLTarget struct {
	URL      string
	Interval time.Duration
	Timeout  time.Duration
	Channels []string
	Mon      monitor.Config
	// InsecureSkipVerify disables TLS verification for this target's HTTPS
	// requests (accepts self-signed certs). Unsafe; trusted networks only.
	InsecureSkipVerify bool
}

// URLMonitor runs one independent monitor per configured URL, each with its own
// ticker, HTTP client, state machine, and notify channels. A check succeeds on
// any HTTP 2xx response within the target's timeout.
type URLMonitor struct {
	monitors []*urlTargetMonitor
	logger   *slog.Logger
}

// NewURLMonitor builds a URLMonitor from a set of independent targets. reg may
// be nil, in which case fleet-wide status snapshots are not logged.
func NewURLMonitor(targets []URLTarget, notifier Notifier, reg *monitor.Registry, logger *slog.Logger) *URLMonitor {
	if logger == nil {
		logger = slog.Default()
	}
	um := &URLMonitor{logger: logger}
	for _, t := range targets {
		um.monitors = append(um.monitors, &urlTargetMonitor{
			url:      t.URL,
			interval: t.Interval,
			timeout:  t.Timeout,
			channels: t.Channels,
			notifier: notifier,
			sm:       monitor.NewWithConfig(t.Mon),
			handle:   reg.Register("url_monitor:" + t.URL),
			client:   NewHTTPClient(t.Timeout, t.InsecureSkipVerify),
			logger:   logger,
		})
	}
	return um
}

// Run starts every target monitor on its own goroutine and returns nil once
// all have stopped after ctx cancellation. A URL being unreachable is a normal
// UNHEALTHY alert, not a fatal error.
func (u *URLMonitor) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, m := range u.monitors {
		wg.Add(1)
		go func(m *urlTargetMonitor) {
			defer wg.Done()
			m.run(ctx)
		}(m)
	}
	wg.Wait()
	return nil
}

// urlTargetMonitor is a single independent HTTP health monitor.
type urlTargetMonitor struct {
	url      string
	interval time.Duration
	timeout  time.Duration
	channels []string
	notifier Notifier
	sm       *monitor.StateMachine
	handle   *monitor.Handle
	client   *http.Client
	logger   *slog.Logger
}

func (m *urlTargetMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.runCheck(ctx)
		}
	}
}

func (m *urlTargetMonitor) runCheck(ctx context.Context) {
	err := m.probe(ctx)
	success := err == nil
	detail := fmt.Sprintf("%s healthy", m.url)
	if !success {
		detail = fmt.Sprintf("%s unhealthy: %v", m.url, err)
		m.logger.Warn("url_monitor check failed", slog.String("url", m.url), slog.Any("error", err))
	}
	ev := m.sm.Observe(success)
	m.handle.Update(m.sm)
	m.handle.RecordCheck(time.Now(), success)
	if ev != monitor.None {
		m.handle.LogSnapshot(m.logger)
	}
	dispatchEvent(ctx, m.notifier, m.channels, "url_monitor", detail, ev, m.sm.ConsecutiveFailures())
}

// probe performs one HTTP GET and returns an error unless the response is 2xx.
func (m *urlTargetMonitor) probe(ctx context.Context) error {
	reqCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	return httpProbe(reqCtx, m.client, m.url)
}
