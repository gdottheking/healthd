package monitor

import (
	"sync"
	"testing"
)

func TestRegistrySnapshotPreservesRegistrationOrder(t *testing.T) {
	r := NewRegistry()
	hb := r.Register("b")
	ha := r.Register("a")
	hc := r.Register("c")

	// Drive distinct states so the snapshot content is checkable.
	smb := New(1)
	smb.Observe(false) // b -> unhealthy
	hb.Update(smb)

	sma := New(1)
	sma.Observe(true) // a -> healthy with a last-success time
	ha.Update(sma)

	_ = hc // c left at its initial healthy/zero state

	snap := r.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot size: got %d want 3", len(snap))
	}
	wantOrder := []string{"b", "a", "c"}
	for i, w := range wantOrder {
		if snap[i].Name != w {
			t.Fatalf("order[%d]: got %q want %q", i, snap[i].Name, w)
		}
	}
	if snap[0].State != Unhealthy {
		t.Fatalf("b state: got %v want Unhealthy", snap[0].State)
	}
	if snap[1].LastSuccess.IsZero() {
		t.Fatal("a should have a non-zero last success")
	}
	if !snap[2].LastSuccess.IsZero() {
		t.Fatal("c should have a zero last success")
	}
}

func TestRegisterIsIdempotent(t *testing.T) {
	r := NewRegistry()
	r.Register("x")
	r.Register("x")
	if len(r.Snapshot()) != 1 {
		t.Fatalf("duplicate registration should not add a second unit")
	}
}

func TestNilRegistryAndHandleAreNoOps(t *testing.T) {
	var r *Registry
	h := r.Register("anything") // must not panic and returns a usable nil handle
	sm := New(1)
	h.Update(sm)          // no-op
	h.LogSnapshot(nil)    // no-op
	r.LogSnapshot(nil)    // no-op
	if got := r.Snapshot(); got != nil {
		t.Fatalf("nil registry snapshot: got %v want nil", got)
	}
}

func TestRegistryConcurrentUpdates(t *testing.T) {
	r := NewRegistry()
	const n = 20
	handles := make([]*Handle, n)
	for i := 0; i < n; i++ {
		handles[i] = r.Register(string(rune('a'+i)))
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(h *Handle) {
			defer wg.Done()
			sm := New(1)
			for j := 0; j < 100; j++ {
				sm.Observe(j%2 == 0)
				h.Update(sm)
			}
		}(handles[i])
	}
	// Concurrently read snapshots to exercise the mutex under -race.
	for i := 0; i < 50; i++ {
		_ = r.Snapshot()
	}
	wg.Wait()
	if len(r.Snapshot()) != n {
		t.Fatalf("expected %d units", n)
	}
}
