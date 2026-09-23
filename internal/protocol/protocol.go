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
	TypePing = "ping"
	TypePong = "pong"
	// TypeGetSummary is a client request asking a monitoring instance for a
	// snapshot of every host it monitors, with availability and speed history.
	TypeGetSummary = "get-summary"
	// TypeSummary is the response carrying that snapshot.
	TypeSummary = "summary"
	TypeError   = "error"
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

// Response is a server-to-client message. Summary is populated only for a
// TypeSummary response and omitted otherwise, so ping/pong and error responses
// keep their existing wire shape.
type Response struct {
	Version string   `json:"version"`
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Payload Payload  `json:"payload"`
	Summary *Summary `json:"summary,omitempty"`
}

// Summary is the payload of a TypeSummary response: a point-in-time view of
// every unit a monitoring instance tracks.
type Summary struct {
	GeneratedAtMS int64         `json:"generated_at_ms"`
	Units         []UnitSummary `json:"units"`
}

// UnitSummary describes one monitored unit's current health plus its retained
// availability and speed history.
type UnitSummary struct {
	Name                string        `json:"name"`
	State               string        `json:"state"`
	Availability        float64       `json:"availability_pct"`
	HistoryAvailability float64       `json:"history_availability_pct"`
	ConsecutiveFailures int           `json:"consecutive_failures"`
	LastSuccessMS       int64         `json:"last_success_ms,omitempty"` // 0 == never
	Speed               *SpeedSummary `json:"speed,omitempty"`           // nil when no speed samples
	Checks              []CheckSample `json:"checks,omitempty"`          // oldest first
	Speeds              []SpeedSample `json:"speeds,omitempty"`          // oldest first
}

// SpeedSummary aggregates a unit's recorded download-speed samples (Mbps).
type SpeedSummary struct {
	Count      int     `json:"count"`
	MinMbps    float64 `json:"min_mbps"`
	AvgMbps    float64 `json:"avg_mbps"`
	MaxMbps    float64 `json:"max_mbps"`
	LatestMbps float64 `json:"latest_mbps"`
}

// CheckSample is one recorded check outcome.
type CheckSample struct {
	TimeMS  int64 `json:"time_ms"`
	Success bool  `json:"success"`
}

// SpeedSample is one recorded download-speed measurement in Mbps.
type SpeedSample struct {
	TimeMS int64   `json:"time_ms"`
	Mbps   float64 `json:"mbps"`
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

// NewSummary builds a summary response echoing the request id.
func NewSummary(id string, s Summary) Response {
	return Response{
		Version: Version,
		ID:      id,
		Type:    TypeSummary,
		Summary: &s,
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
