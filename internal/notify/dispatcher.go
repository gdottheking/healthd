package notify

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Dispatcher fans an alert out to named channels, isolating each channel and
// retrying delivery with exponential backoff.
type Dispatcher struct {
	channels    map[string]INotificationService
	maxAttempts int
	baseDelay   time.Duration
	logger      *slog.Logger
}

// NewDispatcher builds a Dispatcher. maxAttempts is clamped to at least 1 and
// baseDelay to at least 0. logger defaults to slog.Default when nil.
func NewDispatcher(channels map[string]INotificationService, maxAttempts int, baseDelay time.Duration, logger *slog.Logger) *Dispatcher {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	if baseDelay < 0 {
		baseDelay = 0
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		channels:    channels,
		maxAttempts: maxAttempts,
		baseDelay:   baseDelay,
		logger:      logger,
	}
}

// Dispatch sends the alert to each named channel concurrently. A failure or
// panic in one channel never prevents the others from being attempted. It
// blocks until all channels have finished (or ctx is cancelled).
func (d *Dispatcher) Dispatch(ctx context.Context, alert Alert, channelNames []string) {
	var wg sync.WaitGroup
	for _, name := range channelNames {
		svc, ok := d.channels[name]
		if !ok {
			d.logger.Warn("dispatch to unknown channel", slog.String("channel", name))
			continue
		}
		wg.Add(1)
		go func(name string, svc INotificationService) {
			defer wg.Done()
			d.deliver(ctx, name, svc, alert)
		}(name, svc)
	}
	wg.Wait()
}

// deliver retries a single channel with exponential backoff, isolating panics.
func (d *Dispatcher) deliver(ctx context.Context, name string, svc INotificationService, alert Alert) {
	var lastErr error
	for attempt := 1; attempt <= d.maxAttempts; attempt++ {
		if ctx.Err() != nil {
			d.logger.Warn("dispatch cancelled",
				slog.String("channel", name), slog.Any("error", ctx.Err()))
			return
		}

		err := safeNotify(ctx, svc, alert)
		if err == nil {
			return
		}
		lastErr = err

		if attempt < d.maxAttempts {
			delay := d.backoff(attempt)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				d.logger.Warn("dispatch cancelled during backoff",
					slog.String("channel", name), slog.Any("error", ctx.Err()))
				return
			case <-timer.C:
			}
		}
	}
	d.logger.Error("dispatch exhausted retries",
		slog.String("channel", name),
		slog.Int("attempts", d.maxAttempts),
		slog.Any("error", lastErr))
}

// maxBackoffShift caps the exponent so 1<<shift cannot overflow int64 and
// collapse the computed delay to a negative or zero duration.
const maxBackoffShift = 30

// backoff returns the exponential backoff delay for the given attempt (1-based),
// clamping the shift so it never overflows.
func (d *Dispatcher) backoff(attempt int) time.Duration {
	shift := attempt - 1
	if shift > maxBackoffShift {
		shift = maxBackoffShift
	}
	return d.baseDelay * time.Duration(int64(1)<<uint(shift))
}

// safeNotify invokes a channel, converting a panic into an error so one
// channel cannot take down the dispatcher.
func safeNotify(ctx context.Context, svc INotificationService, alert Alert) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("channel panic: %v", r)
		}
	}()
	return svc.Notify(ctx, alert)
}
