package monitor

// Window is a fixed-capacity rolling window of recent check results. It stores
// one boolean per observed check (true=success) up to a maximum length, after
// which the oldest result is dropped as a new one is added. Availability is the
// fraction of successes in the window, expressed as a percentage.
//
// Window is not safe for concurrent use; it is owned by a single StateMachine
// driven from one goroutine.
type Window struct {
	buf  []bool
	size int
	head int // index of the oldest element when full; next write position
	len  int
	sum  int // count of successes currently in the window
}

// newWindow returns a Window that holds at most size results. size is assumed
// to be >= 1 (callers gate on WindowSize > 0).
func newWindow(size int) *Window {
	return &Window{buf: make([]bool, size), size: size}
}

// Add records one result, evicting the oldest once the window is full.
func (w *Window) Add(success bool) {
	if w.len == w.size {
		// Full: buf[head] is the oldest element being evicted.
		if w.buf[w.head] {
			w.sum--
		}
	} else {
		w.len++
	}
	w.buf[w.head] = success
	if success {
		w.sum++
	}
	w.head = (w.head + 1) % w.size
}

// Full reports whether the window has reached its maximum length.
func (w *Window) Full() bool { return w.len == w.size }

// Len returns the number of results currently held.
func (w *Window) Len() int { return w.len }

// Availability returns the percentage (0..100) of successes in the window. An
// empty window reports 100 (no failures observed yet).
func (w *Window) Availability() float64 {
	if w.len == 0 {
		return 100
	}
	return float64(w.sum) / float64(w.len) * 100
}
