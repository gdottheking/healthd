package monitor

import "testing"

func TestWindowGrowsThenEvicts(t *testing.T) {
	w := newWindow(3)
	if w.Full() {
		t.Fatal("empty window should not be full")
	}
	if got := w.Availability(); got != 100 {
		t.Fatalf("empty availability: got %v want 100", got)
	}

	w.Add(true) // [1]
	w.Add(true) // [1 1]
	if w.Full() {
		t.Fatal("window of 2/3 should not be full")
	}
	if got := w.Availability(); got != 100 {
		t.Fatalf("2 successes: got %v want 100", got)
	}

	w.Add(false) // [1 1 0] -> full, 2/3
	if !w.Full() {
		t.Fatal("window should be full at capacity")
	}
	if got := w.Availability(); got < 66.6 || got > 66.7 {
		t.Fatalf("2/3 availability: got %v want ~66.67", got)
	}

	// Evict oldest (a success) as a new failure arrives: [1 0 0] -> 1/3.
	w.Add(false)
	if w.Len() != 3 {
		t.Fatalf("len after eviction: got %d want 3", w.Len())
	}
	if got := w.Availability(); got < 33.3 || got > 33.4 {
		t.Fatalf("1/3 availability: got %v want ~33.33", got)
	}

	// Fill with successes; window slides to full availability.
	w.Add(true)
	w.Add(true)
	w.Add(true)
	if got := w.Availability(); got != 100 {
		t.Fatalf("all successes: got %v want 100", got)
	}
}
