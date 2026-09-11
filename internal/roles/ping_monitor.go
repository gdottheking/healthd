package roles

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"connection_monitor/internal/monitor"
	"connection_monitor/internal/protocol"
)

// PingTarget describes one independent ping monitor.
type PingTarget struct {
	Target   string
	Interval time.Duration
	Timeout  time.Duration
	Channels []string
	Mon      monitor.Config
}

// PingMonitor runs one independent monitor per configured target, each with
// its own ticker, state machine, and notify channels.
type PingMonitor struct {
	monitors []*pingTargetMonitor
	logger   *slog.Logger
}

// NewPingMonitor builds a PingMonitor from a set of independent targets. reg
// may be nil, in which case fleet-wide status snapshots are not logged.
func NewPingMonitor(targets []PingTarget, notifier Notifier, reg *monitor.Registry, logger *slog.Logger) *PingMonitor {
	if logger == nil {
		logger = slog.Default()
	}
	pm := &PingMonitor{logger: logger}
	for _, t := range targets {
		pm.monitors = append(pm.monitors, &pingTargetMonitor{
			target:   t.Target,
			interval: t.Interval,
			timeout:  t.Timeout,
			channels: t.Channels,
			notifier: notifier,
			sm:       monitor.NewWithConfig(t.Mon),
			handle:   reg.Register("ping_monitor:" + t.Target),
			logger:   logger,
		})
	}
	return pm
}

// Run starts every target monitor on its own goroutine and returns nil once
// all have stopped after ctx cancellation. A target being unreachable is a
// normal UNHEALTHY alert, not a fatal error.
func (p *PingMonitor) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, m := range p.monitors {
		wg.Add(1)
		go func(m *pingTargetMonitor) {
			defer wg.Done()
			m.run(ctx)
		}(m)
	}
	wg.Wait()
	return nil
}

// pingTargetMonitor is a single independent ping monitor.
type pingTargetMonitor struct {
	target   string
	interval time.Duration
	timeout  time.Duration
	channels []string
	notifier Notifier
	sm       *monitor.StateMachine
	handle   *monitor.Handle
	logger   *slog.Logger

	idCounter atomic.Uint64
}

func (m *pingTargetMonitor) run(ctx context.Context) {
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

func (m *pingTargetMonitor) runCheck(ctx context.Context) {
	err := m.probe(ctx)
	success := err == nil
	detail := fmt.Sprintf("ping to %s succeeded", m.target)
	if !success {
		detail = fmt.Sprintf("ping to %s failed: %v", m.target, err)
		m.logger.Warn("ping_monitor check failed", slog.String("target", m.target), slog.Any("error", err))
	}
	ev := m.sm.Observe(success)
	m.handle.Update(m.sm)
	if ev != monitor.None {
		m.handle.LogSnapshot(m.logger)
	}
	dispatchEvent(ctx, m.notifier, m.channels, "ping_monitor", detail, ev, m.sm.ConsecutiveFailures())
}

// probe performs one ping/pong exchange and returns an error on any failure.
func (m *pingTargetMonitor) probe(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "tcp", m.target)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(m.timeout)
	conn.SetDeadline(deadline)

	id := strconv.FormatUint(m.idCounter.Add(1), 10)
	req := protocol.Request{
		Version: protocol.Version,
		ID:      id,
		Type:    protocol.TypePing,
		Payload: protocol.Payload{Timestamp: time.Now().UnixMilli()},
	}
	line, err := protocol.EncodeRequest(req)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	line = append(line, '\n')
	if _, err := conn.Write(line); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	reader := bufio.NewReader(conn)
	respLine, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	resp, err := protocol.DecodeResponse(respLine)
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if resp.Type != protocol.TypePong {
		return fmt.Errorf("expected pong, got type %q", resp.Type)
	}
	if resp.ID != id {
		return fmt.Errorf("id mismatch: sent %q got %q", id, resp.ID)
	}
	return nil
}
