package monitor

import (
	"sync"
	"testing"
	"time"
)

func TestSummaryAggregatesHistory(t *testing.T) {
	r := NewRegistry()
	h := r.Register("speed_check")

	base := time.UnixMilli(1_700_000_000_000)
	// 4 checks: 3 ok, 1 fail -> 75% history availability.
	h.RecordCheck(base, true)
	h.RecordCheck(base.Add(time.Second), true)
	h.RecordCheck(base.Add(2*time.Second), false)
	h.RecordCheck(base.Add(3*time.Second), true)
	h.RecordSpeed(base, 120)
	h.RecordSpeed(base.Add(time.Second), 480)

	sum := r.Summary()
	if len(sum.Units) != 1 {
		t.Fatalf("units: got %d want 1", len(sum.Units))
	}
	u := sum.Units[0]
	if len(u.Checks) != 4 {
		t.Fatalf("checks: got %d want 4", len(u.Checks))
	}
	if u.HistoryAvailability != 75 {
		t.Fatalf("history availability: got %v want 75", u.HistoryAvailability)
	}
	if u.Speed == nil {
		t.Fatal("expected speed stats")
	}
	if u.Speed.Count != 2 || u.Speed.Min != 120 || u.Speed.Max != 480 || u.Speed.Avg != 300 || u.Speed.Latest != 480 {
		t.Fatalf("speed stats: %+v", u.Speed)
	}
}

func TestSummaryNoSpeedSamples(t *testing.T) {
	r := NewRegistry()
	h := r.Register("ping_monitor:x")
	h.RecordCheck(time.Now(), true)

	u := r.Summary().Units[0]
	if u.Speed != nil {
		t.Fatalf("expected nil speed stats, got %+v", u.Speed)
	}
	if u.HistoryAvailability != 100 {
		t.Fatalf("all-success history availability: got %v want 100", u.HistoryAvailability)
	}
}

func TestHistoryAvailabilityEmptyIs100(t *testing.T) {
	r := NewRegistry()
	r.Register("fresh")
	u := r.Summary().Units[0]
	if u.HistoryAvailability != 100 {
		t.Fatalf("empty history availability: got %v want 100", u.HistoryAvailability)
	}
	if len(u.Checks) != 0 {
		t.Fatalf("expected no checks, got %d", len(u.Checks))
	}
}

func TestHistoryRingEvictsOldest(t *testing.T) {
	r := NewRegistry()
	h := r.Register("speed_check")
	// Record more than the cap; only the last historyCap remain, oldest-first.
	total := historyCap + 25
	for i := 0; i < total; i++ {
		h.RecordSpeed(time.UnixMilli(int64(i)), float64(i))
	}
	u := r.Summary().Units[0]
	if len(u.Speeds) != historyCap {
		t.Fatalf("retained: got %d want %d", len(u.Speeds), historyCap)
	}
	// Oldest retained is the (total-historyCap)th sample; newest is total-1.
	wantOldest := float64(total - historyCap)
	if u.Speeds[0].Mbps != wantOldest {
		t.Fatalf("oldest retained: got %v want %v", u.Speeds[0].Mbps, wantOldest)
	}
	if u.Speeds[len(u.Speeds)-1].Mbps != float64(total-1) {
		t.Fatalf("newest retained: got %v want %v", u.Speeds[len(u.Speeds)-1].Mbps, float64(total-1))
	}
	if u.Speed.Latest != float64(total-1) {
		t.Fatalf("latest stat: got %v", u.Speed.Latest)
	}
}

func TestRegistryLenAndNilSummary(t *testing.T) {
	var nilReg *Registry
	if nilReg.Len() != 0 {
		t.Fatalf("nil registry Len: got %d want 0", nilReg.Len())
	}
	if got := nilReg.Summary(); got.Units != nil {
		t.Fatalf("nil registry summary units: got %v want nil", got.Units)
	}

	r := NewRegistry()
	r.Register("a")
	r.Register("b")
	r.Register("a") // idempotent
	if r.Len() != 2 {
		t.Fatalf("Len: got %d want 2", r.Len())
	}
}

func TestRecordOnNilHandleIsNoOp(t *testing.T) {
	var r *Registry
	h := r.Register("x")
	h.RecordCheck(time.Now(), true) // must not panic
	h.RecordSpeed(time.Now(), 1.0)  // must not panic
}

func TestRegistryConcurrentRecordAndSummary(t *testing.T) {
	r := NewRegistry()
	const n = 10
	handles := make([]*Handle, n)
	for i := 0; i < n; i++ {
		handles[i] = r.Register(string(rune('a' + i)))
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(h *Handle) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				h.RecordCheck(time.Now(), j%2 == 0)
				h.RecordSpeed(time.Now(), float64(j))
			}
		}(handles[i])
	}
	for i := 0; i < 50; i++ {
		_ = r.Summary()
		r.LogSummary(nil) // nil logger is a no-op but still exercises guards
	}
	wg.Wait()
	if len(r.Summary().Units) != n {
		t.Fatalf("units: got %d want %d", len(r.Summary().Units), n)
	}
}
