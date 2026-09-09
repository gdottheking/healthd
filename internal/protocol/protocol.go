// Package protocol defines the newline-framed JSON ping/pong wire format
// shared by the pong_server and ping_monitor roles.
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Version is the only protocol version this daemon speaks.
const Version = "0.1"

// Message types.
const (
	TypePing  = "ping"
	TypePong  = "pong"
	TypeError = "error"
)

// Payload carries the timestamp (epoch milliseconds) and, for error
// responses, a human-readable message.
type Payload struct {
	Timestamp int64  `json:"timestamp,omitempty"`
	Message   string `json:"message,omitempty"`
}

// Request is a client-to-server message.
type Request struct {
	Version string  `json:"version"`
	ID      string  `json:"id"`
	Type    string  `json:"type"`
	Payload Payload `json:"payload"`
}

// Response is a server-to-client message.
type Response struct {
	Version string  `json:"version"`
	ID      string  `json:"id"`
	Type    string  `json:"type"`
	Payload Payload `json:"payload"`
}

// ErrVersionMismatch is returned by DecodeRequest when the request version
// does not match the supported protocol version.
var ErrVersionMismatch = errors.New("protocol version mismatch")

// EncodeRequest serializes a request to a single JSON line (no trailing
// newline is added; framing is the caller's responsibility).
func EncodeRequest(r Request) ([]byte, error) {
	return json.Marshal(r)
}

// EncodeResponse serializes a response to a single JSON line.
func EncodeResponse(r Response) ([]byte, error) {
	return json.Marshal(r)
}

// DecodeRequest parses a single JSON line into a Request. It returns
// ErrVersionMismatch (wrapped) if the version field is not Version, and a
// parse error for malformed JSON. The parsed request is returned even on a
// version mismatch so callers can echo the id back.
func DecodeRequest(line []byte) (Request, error) {
	var r Request
	if err := json.Unmarshal(line, &r); err != nil {
		return Request{}, fmt.Errorf("decode request: %w", err)
	}
	if r.Version != Version {
		return r, fmt.Errorf("%w: got %q want %q", ErrVersionMismatch, r.Version, Version)
	}
	return r, nil
}

// DecodeResponse parses a single JSON line into a Response.
func DecodeResponse(line []byte) (Response, error) {
	var r Response
	if err := json.Unmarshal(line, &r); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	return r, nil
}

// NewPong builds a pong response echoing the request id and timestamp.
func NewPong(req Request) Response {
	return Response{
		Version: Version,
		ID:      req.ID,
		Type:    TypePong,
		Payload: Payload{Timestamp: req.Payload.Timestamp},
	}
}

// NewError builds an error response for the given id and message.
func NewError(id, msg string) Response {
	return Response{
		Version: Version,
		ID:      id,
		Type:    TypeError,
		Payload: Payload{Message: msg},
	}
}
