package roles

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connection_monitor/internal/monitor"
)

// InternetCheck probes one site per cycle in round-robin order, treating an
// HTTP 2xx response as success.
type InternetCheck struct {
	sites    []string
	interval time.Duration
	timeout  time.Duration
	channels []string
	notifier Notifier
	sm       *monitor.StateMachine
	handle   *monitor.Handle
	client   *http.Client
	logger   *slog.Logger

	next int
}

// NewInternetCheck builds an InternetCheck. A nil client uses a default client
// with the given timeout. reg may be nil, in which case fleet-wide status
// snapshots are not logged.
func NewInternetCheck(sites []string, interval, timeout time.Duration, mon monitor.Config, channels []string, notifier Notifier, client *http.Client, reg *monitor.Registry, logger *slog.Logger) *InternetCheck {
	if logger == nil {
		logger = slog.Default()
	}
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &InternetCheck{
		sites:    sites,
		interval: interval,
		timeout:  timeout,
		channels: channels,
		notifier: notifier,
		sm:       monitor.NewWithConfig(mon),
		handle:   reg.Register("internet_check"),
		client:   client,
		logger:   logger,
	}
}

// Run runs a check every interval until ctx is cancelled.
func (c *InternetCheck) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.runCheck(ctx)
		}
	}
}

// nextSite returns the next site in round-robin order.
func (c *InternetCheck) nextSite() string {
	site := c.sites[c.next%len(c.sites)]
	c.next++
	return site
}

func (c *InternetCheck) runCheck(ctx context.Context) {
	site := c.nextSite()
	err := c.checkSite(ctx, site)
	success := err == nil
	detail := fmt.Sprintf("%s reachable", site)
	if !success {
		detail = fmt.Sprintf("%s unreachable: %v", site, err)
		c.logger.Warn("internet_check failed", slog.String("site", site), slog.Any("error", err))
	}
	ev := c.sm.Observe(success)
	c.handle.Update(c.sm)
	c.handle.RecordCheck(time.Now(), success)
	if ev != monitor.None {
		c.handle.LogSnapshot(c.logger)
	}
	dispatchEvent(ctx, c.notifier, c.channels, "internet_check", detail, ev, c.sm.ConsecutiveFailures())
}

// checkSite performs a single GET and returns nil on a 2xx response.
func (c *InternetCheck) checkSite(ctx context.Context, site string) error {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return httpProbe(reqCtx, c.client, site)
}
