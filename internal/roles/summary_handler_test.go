package roles

import (
	"testing"
	"time"

	"connection_monitor/internal/monitor"
	"connection_monitor/internal/protocol"
)

func TestSummaryHandlerReportsHistory(t *testing.T) {
	reg := monitor.NewRegistry()
	ping := reg.Register("ping_monitor:10.0.0.5:9000")
	speed := reg.Register("speed_check")

	base := time.UnixMilli(1_700_000_000_000)
	// ping: 3 checks, 2 ok / 1 fail -> history availability 66.67%.
	ping.RecordCheck(base, true)
	ping.RecordCheck(base.Add(time.Second), false)
	ping.RecordCheck(base.Add(2*time.Second), true)
	// speed: three measurements 100/200/300 -> min100 avg200 max300 latest300.
	speed.RecordCheck(base, true)
	speed.RecordSpeed(base, 100)
	speed.RecordSpeed(base.Add(time.Second), 200)
	speed.RecordSpeed(base.Add(2*time.Second), 300)

	resp := NewSummaryHandler(reg).Handle(protocol.Request{Version: protocol.Version, ID: "x", Type: protocol.TypeGetSummary})
	if resp.Type != protocol.TypeSummary || resp.Summary == nil {
		t.Fatalf("bad response: %+v", resp)
	}
	units := resp.Summary.Units
	if len(units) != 2 {
		t.Fatalf("want 2 units, got %d", len(units))
	}

	// Units are in registration order.
	p := units[0]
	if p.Name != "ping_monitor:10.0.0.5:9000" {
		t.Fatalf("unit[0] name: %q", p.Name)
	}
	if len(p.Checks) != 3 {
		t.Fatalf("ping checks: want 3, got %d", len(p.Checks))
	}
	if p.HistoryAvailability < 66.6 || p.HistoryAvailability > 66.7 {
		t.Fatalf("ping history availability: got %v", p.HistoryAvailability)
	}
	if p.Speed != nil {
		t.Fatalf("ping unit should have no speed data, got %+v", p.Speed)
	}

	s := units[1]
	if s.Speed == nil {
		t.Fatal("speed unit missing speed summary")
	}
	if s.Speed.Count != 3 || s.Speed.MinMbps != 100 || s.Speed.MaxMbps != 300 || s.Speed.LatestMbps != 300 {
		t.Fatalf("speed stats: %+v", s.Speed)
	}
	if s.Speed.AvgMbps != 200 {
		t.Fatalf("avg mbps: got %v want 200", s.Speed.AvgMbps)
	}
	if len(s.Speeds) != 3 {
		t.Fatalf("speed samples: want 3, got %d", len(s.Speeds))
	}
	if s.Speeds[0].Mbps != 100 || s.Speeds[2].Mbps != 300 {
		t.Fatalf("speed samples not oldest-first: %+v", s.Speeds)
	}
}

func TestSummaryResponseSurvivesEncodeDecode(t *testing.T) {
	reg := monitor.NewRegistry()
	h := reg.Register("internet_check")
	h.RecordCheck(time.UnixMilli(1_700_000_000_000), true)

	resp := NewSummaryHandler(reg).Handle(protocol.Request{Version: protocol.Version, ID: "rt", Type: protocol.TypeGetSummary})
	line, err := protocol.EncodeResponse(resp)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := protocol.DecodeResponse(line)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Type != protocol.TypeSummary || got.Summary == nil || len(got.Summary.Units) != 1 {
		t.Fatalf("round-trip lost summary: %+v", got)
	}
	if got.Summary.Units[0].Name != "internet_check" {
		t.Fatalf("unit name: %q", got.Summary.Units[0].Name)
	}
}
