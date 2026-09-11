package monitor

import (
	"log/slog"
	"sync"
	"time"
)

// UnitStatus is a point-in-time snapshot of one monitored unit's health.
type UnitStatus struct {
	Name         string
	State        State
	HasWindow    bool
	Availability float64 // percentage (0..100); meaningful only when HasWindow
	Consecutive  int
	LastSuccess  time.Time // zero when no check has succeeded yet
}

// Registry holds the current status of every monitored unit across all roles
// so a fleet-wide snapshot can be logged whenever any unit changes state. It is
// safe for concurrent use by multiple role goroutines.
type Registry struct {
	mu    sync.Mutex
	order []string
	units map[string]UnitStatus
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{units: make(map[string]UnitStatus)}
}

// Register adds a unit under name and returns a Handle for updating it. The
// registration order fixes the order units appear in a snapshot. Calling
// Register on a nil Registry returns a nil Handle, so roles can run without a
// registry (e.g. in unit tests) without special-casing every call.
func (r *Registry) Register(name string) *Handle {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.units[name]; !ok {
		r.order = append(r.order, name)
		r.units[name] = UnitStatus{Name: name, State: Healthy}
	}
	return &Handle{reg: r, name: name}
}

// set replaces the stored status for name.
func (r *Registry) set(status UnitStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.units[status.Name]; ok {
		r.units[status.Name] = status
	}
}

// Snapshot returns every unit's current status in registration order.
func (r *Registry) Snapshot() []UnitStatus {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]UnitStatus, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.units[name])
	}
	return out
}

// LogSnapshot emits a single Info log line listing every unit and its current
// state, availability, consecutive failures, and last-success time. It is a
// no-op on a nil Registry or logger.
func (r *Registry) LogSnapshot(logger *slog.Logger) {
	if r == nil || logger == nil {
		return
	}
	snap := r.Snapshot()
	attrs := make([]any, 0, len(snap)+1)
	attrs = append(attrs, slog.Int("units", len(snap)))
	for _, u := range snap {
		group := []any{
			slog.String("state", u.State.String()),
			slog.Int("consecutive_failures", u.Consecutive),
		}
		if u.HasWindow {
			group = append(group, slog.Float64("availability_pct", u.Availability))
		}
		if u.LastSuccess.IsZero() {
			group = append(group, slog.String("last_success", "never"))
		} else {
			group = append(group, slog.Time("last_success", u.LastSuccess))
		}
		attrs = append(attrs, slog.Group(u.Name, group...))
	}
	logger.Info("status snapshot", attrs...)
}

// Handle updates one unit's slot in a Registry. A nil Handle is a no-op, which
// lets roles hold a handle unconditionally even without a registry.
type Handle struct {
	reg  *Registry
	name string
}

// Update copies the current status from sm into this unit's registry slot.
func (h *Handle) Update(sm *StateMachine) {
	if h == nil || h.reg == nil {
		return
	}
	h.reg.set(UnitStatus{
		Name:         h.name,
		State:        sm.State(),
		HasWindow:    sm.HasWindow(),
		Availability: sm.Availability(),
		Consecutive:  sm.ConsecutiveFailures(),
		LastSuccess:  sm.LastSuccess(),
	})
}

// LogSnapshot logs a fleet-wide snapshot via this handle's registry.
func (h *Handle) LogSnapshot(logger *slog.Logger) {
	if h == nil || h.reg == nil {
		return
	}
	h.reg.LogSnapshot(logger)
}
