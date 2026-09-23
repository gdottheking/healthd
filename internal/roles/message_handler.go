package roles

import (
	"connection_monitor/internal/monitor"
	"connection_monitor/internal/protocol"
)

// MessageHandler produces a response for a single decoded request of the type
// it serves. Implementations must never panic on any input; the request has
// already been decoded and version-checked by the MessageDispatcher.
type MessageHandler interface {
	// Type is the request Type this handler serves (e.g. "ping").
	Type() string
	// Handle builds the response for req.
	Handle(req protocol.Request) protocol.Response
}

// PingHandler answers a ping with a pong, echoing the id and timestamp. It is
// the behavior the original pong_server provided.
type PingHandler struct{}

// Type returns the ping request type.
func (PingHandler) Type() string { return protocol.TypePing }

// Handle builds the pong response.
func (PingHandler) Handle(req protocol.Request) protocol.Response {
	return protocol.NewPong(req)
}

// SummaryHandler answers a get-summary request with the current status and
// retained history of every unit this instance monitors, read from the shared
// registry. It is only wired in when at least one monitoring role is enabled.
type SummaryHandler struct {
	reg *monitor.Registry
}

// NewSummaryHandler builds a SummaryHandler backed by reg.
func NewSummaryHandler(reg *monitor.Registry) *SummaryHandler {
	return &SummaryHandler{reg: reg}
}

// Type returns the get-summary request type.
func (h *SummaryHandler) Type() string { return protocol.TypeGetSummary }

// Handle reads a fleet summary from the registry and converts it to the wire
// form, echoing the request id.
func (h *SummaryHandler) Handle(req protocol.Request) protocol.Response {
	return protocol.NewSummary(req.ID, toWireSummary(h.reg.Summary()))
}

// toWireSummary converts a monitor.FleetSummary into the protocol wire type.
func toWireSummary(fs monitor.FleetSummary) protocol.Summary {
	out := protocol.Summary{
		GeneratedAtMS: fs.GeneratedAt.UnixMilli(),
		Units:         make([]protocol.UnitSummary, 0, len(fs.Units)),
	}
	for _, u := range fs.Units {
		wu := protocol.UnitSummary{
			Name:                u.Status.Name,
			State:               u.Status.State.String(),
			Availability:        u.Status.Availability,
			HistoryAvailability: u.HistoryAvailability,
			ConsecutiveFailures: u.Status.Consecutive,
		}
		if !u.Status.LastSuccess.IsZero() {
			wu.LastSuccessMS = u.Status.LastSuccess.UnixMilli()
		}
		if u.Speed != nil {
			wu.Speed = &protocol.SpeedSummary{
				Count:      u.Speed.Count,
				MinMbps:    u.Speed.Min,
				AvgMbps:    u.Speed.Avg,
				MaxMbps:    u.Speed.Max,
				LatestMbps: u.Speed.Latest,
			}
		}
		for _, c := range u.Checks {
			wu.Checks = append(wu.Checks, protocol.CheckSample{TimeMS: c.Time.UnixMilli(), Success: c.Success})
		}
		for _, s := range u.Speeds {
			wu.Speeds = append(wu.Speeds, protocol.SpeedSample{TimeMS: s.Time.UnixMilli(), Mbps: s.Mbps})
		}
		out.Units = append(out.Units, wu)
	}
	return out
}
