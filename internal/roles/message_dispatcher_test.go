package roles

import (
	"testing"

	"connection_monitor/internal/monitor"
	"connection_monitor/internal/protocol"
)

// dispatchLine encodes req and runs it through d.
func dispatchLine(t *testing.T, d *MessageDispatcher, req protocol.Request) protocol.Response {
	t.Helper()
	line, err := protocol.EncodeRequest(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return d.Dispatch(line)
}

func TestMessageDispatcherRoutesPing(t *testing.T) {
	d := NewMessageDispatcher(PingHandler{})
	resp := dispatchLine(t, d, protocol.Request{
		Version: protocol.Version, ID: "p1", Type: protocol.TypePing,
		Payload: protocol.Payload{Timestamp: 77},
	})
	if resp.Type != protocol.TypePong || resp.ID != "p1" || resp.Payload.Timestamp != 77 {
		t.Fatalf("unexpected pong: %+v", resp)
	}
}

func TestMessageDispatcherUnsupportedType(t *testing.T) {
	d := NewMessageDispatcher(PingHandler{}) // no summary handler registered
	resp := dispatchLine(t, d, protocol.Request{Version: protocol.Version, ID: "g", Type: protocol.TypeGetSummary})
	if resp.Type != protocol.TypeError {
		t.Fatalf("type: got %q want error", resp.Type)
	}
	if resp.ID != "g" {
		t.Fatalf("error should echo id: got %q", resp.ID)
	}
	if resp.Payload.Message != "unsupported request type" {
		t.Fatalf("message: got %q", resp.Payload.Message)
	}
}

func TestMessageDispatcherMalformed(t *testing.T) {
	d := NewMessageDispatcher(PingHandler{})
	resp := d.Dispatch([]byte("{not json"))
	if resp.Type != protocol.TypeError || resp.Payload.Message != "malformed request" {
		t.Fatalf("unexpected: %+v", resp)
	}
}

func TestMessageDispatcherVersionMismatch(t *testing.T) {
	d := NewMessageDispatcher(PingHandler{})
	resp := dispatchLine(t, d, protocol.Request{Version: "9.9", ID: "v", Type: protocol.TypePing})
	if resp.Type != protocol.TypeError || resp.ID != "v" {
		t.Fatalf("unexpected: %+v", resp)
	}
	if resp.Payload.Message != "unsupported protocol version" {
		t.Fatalf("message: got %q", resp.Payload.Message)
	}
}

func TestMessageDispatcherRoutesGetSummary(t *testing.T) {
	reg := monitor.NewRegistry()
	reg.Register("internet_check")
	d := NewMessageDispatcher(PingHandler{}, NewSummaryHandler(reg))
	resp := dispatchLine(t, d, protocol.Request{Version: protocol.Version, ID: "s1", Type: protocol.TypeGetSummary})
	if resp.Type != protocol.TypeSummary || resp.ID != "s1" {
		t.Fatalf("unexpected summary response: %+v", resp)
	}
	if resp.Summary == nil || len(resp.Summary.Units) != 1 {
		t.Fatalf("expected 1 unit in summary, got %+v", resp.Summary)
	}
}
