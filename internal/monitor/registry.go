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
// so a fleet-wide snapshot can be logged whenever any unit changes state, and
// retains a bounded per-unit history of check outcomes and speed measurements
// for the periodic summary and the get-summary request. It is safe for
// concurrent use by multiple role goroutines.
type Registry struct {
	mu      sync.Mutex
	order   []string
	units   map[string]UnitStatus
	history map[string]*unitHistory
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		units:   make(map[string]UnitStatus),
		history: make(map[string]*unitHistory),
	}
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
		r.history[name] = newUnitHistory()
	}
	return &Handle{reg: r, name: name}
}

// Len returns the number of registered units. A nil Registry has none.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.order)
}

// recordCheck appends one check outcome to name's history.
func (r *Registry) recordCheck(name string, t time.Time, success bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.history[name]; ok {
		h.checks.add(CheckSample{Time: t, Success: success})
	}
}

// recordSpeed appends one speed measurement to name's history.
func (r *Registry) recordSpeed(name string, t time.Time, mbps float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.history[name]; ok {
		h.speeds.add(SpeedSample{Time: t, Mbps: mbps})
	}
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

// SpeedStats aggregates a unit's retained speed samples (Mbps).
type SpeedStats struct {
	Count  int
	Min    float64
	Avg    float64
	Max    float64
	Latest float64
}

// UnitSummary is one unit's current status plus its retained history and the
// aggregates derived from it.
type UnitSummary struct {
	Status UnitStatus
	// HistoryAvailability is the percentage of successful checks across the
	// retained check history (distinct from the rolling-window Availability in
	// Status). It is 100 when no checks have been recorded.
	HistoryAvailability float64
	Speed               *SpeedStats // nil when no speed samples recorded
	Checks              []CheckSample
	Speeds              []SpeedSample
}

// FleetSummary is a point-in-time view of every registered unit with history.
type FleetSummary struct {
	GeneratedAt time.Time
	Units       []UnitSummary
}

// Summary returns a full summary of every unit in registration order,
// including retained history and its aggregates. Returns a zero FleetSummary
// on a nil Registry.
func (r *Registry) Summary() FleetSummary {
	if r == nil {
		return FleetSummary{GeneratedAt: time.Now()}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fs := FleetSummary{GeneratedAt: time.Now(), Units: make([]UnitSummary, 0, len(r.order))}
	for _, name := range r.order {
		checks := r.history[name].checks.slice()
		speeds := r.history[name].speeds.slice()
		fs.Units = append(fs.Units, UnitSummary{
			Status:              r.units[name],
			HistoryAvailability: historyAvailability(checks),
			Speed:               speedStats(speeds),
			Checks:              checks,
			Speeds:              speeds,
		})
	}
	return fs
}

// historyAvailability is the percentage of successful checks in samples, or
// 100 when there are none.
func historyAvailability(samples []CheckSample) float64 {
	if len(samples) == 0 {
		return 100
	}
	ok := 0
	for _, s := range samples {
		if s.Success {
			ok++
		}
	}
	return float64(ok) / float64(len(samples)) * 100
}

// speedStats aggregates speed samples, or returns nil when there are none.
func speedStats(samples []SpeedSample) *SpeedStats {
	if len(samples) == 0 {
		return nil
	}
	st := &SpeedStats{
		Count:  len(samples),
		Min:    samples[0].Mbps,
		Max:    samples[0].Mbps,
		Latest: samples[len(samples)-1].Mbps,
	}
	var sum float64
	for _, s := range samples {
		sum += s.Mbps
		if s.Mbps < st.Min {
			st.Min = s.Mbps
		}
		if s.Mbps > st.Max {
			st.Max = s.Mbps
		}
	}
	st.Avg = sum / float64(len(samples))
	return st
}

// LogSummary emits a single Info line aggregating every unit's current state,
// availability, and speed history. It is a no-op on a nil Registry or logger.
func (r *Registry) LogSummary(logger *slog.Logger) {
	if r == nil || logger == nil {
		return
	}
	sum := r.Summary()
	attrs := make([]any, 0, len(sum.Units)+1)
	attrs = append(attrs, slog.Int("units", len(sum.Units)))
	for _, u := range sum.Units {
		group := []any{
			slog.String("state", u.Status.State.String()),
			slog.Int("consecutive_failures", u.Status.Consecutive),
			slog.Float64("history_availability_pct", u.HistoryAvailability),
			slog.Int("checks_recorded", len(u.Checks)),
		}
		if u.Status.HasWindow {
			group = append(group, slog.Float64("availability_pct", u.Status.Availability))
		}
		if u.Speed != nil {
			group = append(group,
				slog.Int("speed_samples", u.Speed.Count),
				slog.Float64("speed_min_mbps", u.Speed.Min),
				slog.Float64("speed_avg_mbps", u.Speed.Avg),
				slog.Float64("speed_max_mbps", u.Speed.Max),
				slog.Float64("speed_latest_mbps", u.Speed.Latest),
			)
		}
		if u.Status.LastSuccess.IsZero() {
			group = append(group, slog.String("last_success", "never"))
		} else {
			group = append(group, slog.Time("last_success", u.Status.LastSuccess))
		}
		attrs = append(attrs, slog.Group(u.Status.Name, group...))
	}
	logger.Info("monitoring summary", attrs...)
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

// RecordCheck appends one check outcome to this unit's retained history.
func (h *Handle) RecordCheck(t time.Time, success bool) {
	if h == nil || h.reg == nil {
		return
	}
	h.reg.recordCheck(h.name, t, success)
}

// RecordSpeed appends one download-speed measurement (Mbps) to this unit's
// retained history.
func (h *Handle) RecordSpeed(t time.Time, mbps float64) {
	if h == nil || h.reg == nil {
		return
	}
	h.reg.recordSpeed(h.name, t, mbps)
}
