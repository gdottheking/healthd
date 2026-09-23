package protocol

import (
	"errors"
	"strings"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	req := Request{
		Version: Version,
		ID:      "42",
		Type:    TypePing,
		Payload: Payload{Timestamp: 1234567890},
	}
	line, err := EncodeRequest(req)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	got, err := DecodeRequest(line)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if got != req {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, req)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	resp := Response{
		Version: Version,
		ID:      "7",
		Type:    TypePong,
		Payload: Payload{Timestamp: 99},
	}
	line, err := EncodeResponse(resp)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	got, err := DecodeResponse(line)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if got != resp {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, resp)
	}
}

func TestDecodeRequestVersionMismatch(t *testing.T) {
	req := Request{Version: "9.9", ID: "5", Type: TypePing}
	line, _ := EncodeRequest(req)
	got, err := DecodeRequest(line)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("expected ErrVersionMismatch, got %v", err)
	}
	// The id must still be recoverable so a caller can echo it.
	if got.ID != "5" {
		t.Fatalf("expected id echoed back, got %q", got.ID)
	}
}

func TestDecodeRequestMalformed(t *testing.T) {
	_, err := DecodeRequest([]byte("{not json"))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if errors.Is(err, ErrVersionMismatch) {
		t.Fatal("malformed JSON should not be a version mismatch")
	}
}

func TestNewPongEchoes(t *testing.T) {
	req := Request{Version: Version, ID: "abc", Type: TypePing, Payload: Payload{Timestamp: 555}}
	resp := NewPong(req)
	if resp.Type != TypePong || resp.ID != "abc" || resp.Payload.Timestamp != 555 {
		t.Fatalf("NewPong did not echo correctly: %+v", resp)
	}
}

func TestNewError(t *testing.T) {
	resp := NewError("id1", "boom")
	if resp.Type != TypeError || resp.ID != "id1" || resp.Payload.Message != "boom" {
		t.Fatalf("NewError unexpected: %+v", resp)
	}
}

func TestPongResponseOmitsSummaryField(t *testing.T) {
	line, err := EncodeResponse(NewPong(Request{Version: Version, ID: "a", Type: TypePing}))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(line), "summary") {
		t.Fatalf("pong response should omit the summary field, got %s", line)
	}
}

func TestSummaryRoundTrip(t *testing.T) {
	resp := NewSummary("s1", Summary{
		GeneratedAtMS: 1_700_000_000_000,
		Units: []UnitSummary{{
			Name:                "speed_check",
			State:               "HEALTHY",
			Availability:        95.5,
			HistoryAvailability: 90,
			ConsecutiveFailures: 0,
			LastSuccessMS:       1_700_000_000_001,
			Speed:               &SpeedSummary{Count: 2, MinMbps: 100, AvgMbps: 200, MaxMbps: 300, LatestMbps: 300},
			Speeds:              []SpeedSample{{TimeMS: 1, Mbps: 100}, {TimeMS: 2, Mbps: 300}},
			Checks:              []CheckSample{{TimeMS: 1, Success: true}},
		}},
	})
	line, err := EncodeResponse(resp)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeResponse(line)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Type != TypeSummary || got.ID != "s1" || got.Summary == nil {
		t.Fatalf("unexpected: %+v", got)
	}
	u := got.Summary.Units[0]
	if u.Name != "speed_check" || u.Speed == nil || u.Speed.LatestMbps != 300 {
		t.Fatalf("unit lost in round-trip: %+v", u)
	}
	if len(u.Speeds) != 2 || len(u.Checks) != 1 {
		t.Fatalf("samples lost: %+v", u)
	}
}
