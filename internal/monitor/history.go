package monitor

import "time"

// historyCap bounds the number of samples retained per unit for each history
// kind (check outcomes and speed measurements). Retention is memory-bounded:
// the oldest sample is evicted once the buffer is full.
const historyCap = 100

// CheckSample is one recorded check outcome.
type CheckSample struct {
	Time    time.Time
	Success bool
}

// SpeedSample is one recorded download-speed measurement in Mbps.
type SpeedSample struct {
	Time time.Time
	Mbps float64
}

// ring is a fixed-capacity FIFO buffer that evicts the oldest element once
// full. It is not safe for concurrent use; the Registry guards it with its
// mutex.
type ring[T any] struct {
	buf  []T
	head int // next write position / index of oldest when full
	len  int
}

func newRing[T any](capacity int) *ring[T] {
	return &ring[T]{buf: make([]T, capacity)}
}

func (r *ring[T]) add(v T) {
	r.buf[r.head] = v
	r.head = (r.head + 1) % len(r.buf)
	if r.len < len(r.buf) {
		r.len++
	}
}

// slice returns the retained elements oldest-first.
func (r *ring[T]) slice() []T {
	out := make([]T, 0, r.len)
	start := (r.head - r.len + len(r.buf)) % len(r.buf)
	for i := 0; i < r.len; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}

// unitHistory holds the retained check and speed samples for one unit.
type unitHistory struct {
	checks *ring[CheckSample]
	speeds *ring[SpeedSample]
}

func newUnitHistory() *unitHistory {
	return &unitHistory{
		checks: newRing[CheckSample](historyCap),
		speeds: newRing[SpeedSample](historyCap),
	}
}
