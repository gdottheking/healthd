package protocol

import (
	"errors"
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
