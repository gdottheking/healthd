package roles

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"connection_monitor/internal/protocol"
)

// startPongServer starts a server on an ephemeral port and returns its address.
func startPongServer(t *testing.T) string {
	t.Helper()
	s := NewPongServer(":0", 2*time.Second, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go s.Run(ctx)

	select {
	case <-s.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("server did not become ready")
	}
	return s.Addr().String()
}

func dialAndReadLine(t *testing.T, conn net.Conn, req protocol.Request) protocol.Response {
	t.Helper()
	line, err := protocol.EncodeRequest(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	line = append(line, '\n')
	if _, err := conn.Write(line); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	respLine, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	resp, err := protocol.DecodeResponse(respLine)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestPongServerEchoesPing(t *testing.T) {
	addr := startPongServer(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	req := protocol.Request{Version: protocol.Version, ID: "abc", Type: protocol.TypePing, Payload: protocol.Payload{Timestamp: 12345}}
	resp := dialAndReadLine(t, conn, req)
	if resp.Type != protocol.TypePong {
		t.Fatalf("type: got %q want pong", resp.Type)
	}
	if resp.ID != "abc" {
		t.Fatalf("id: got %q want abc", resp.ID)
	}
	if resp.Payload.Timestamp != 12345 {
		t.Fatalf("timestamp: got %d want 12345", resp.Payload.Timestamp)
	}
}

func TestPongServerSequentialRequests(t *testing.T) {
	addr := startPongServer(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	reader := bufio.NewReader(conn)
	for i := 0; i < 3; i++ {
		req := protocol.Request{Version: protocol.Version, ID: string(rune('0' + i)), Type: protocol.TypePing}
		line, _ := protocol.EncodeRequest(req)
		conn.Write(append(line, '\n'))
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		respLine, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		resp, _ := protocol.DecodeResponse(respLine)
		if resp.ID != string(rune('0'+i)) || resp.Type != protocol.TypePong {
			t.Fatalf("request %d: unexpected response %+v", i, resp)
		}
	}
}

func TestPongServerMalformedJSONSurvives(t *testing.T) {
	addr := startPongServer(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	conn.Write([]byte("{not valid json\n"))
	reader := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	respLine, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	resp, _ := protocol.DecodeResponse(respLine)
	if resp.Type != protocol.TypeError {
		t.Fatalf("expected error response, got %q", resp.Type)
	}

	// The server must still be up; a fresh connection works.
	conn2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	t.Cleanup(func() { conn2.Close() })
	good := dialAndReadLine(t, conn2, protocol.Request{Version: protocol.Version, ID: "z", Type: protocol.TypePing})
	if good.Type != protocol.TypePong {
		t.Fatalf("server not healthy after malformed input: %+v", good)
	}
}

func TestPongServerBoundsOversizedRequest(t *testing.T) {
	// Server with a small request cap so we can exceed it cheaply.
	s := NewPongServer(":0", 2*time.Second, testLogger())
	s.maxRequest = 256
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.Run(ctx)
	select {
	case <-s.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("server did not become ready")
	}
	addr := s.Addr().String()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	// Stream more than the cap with no newline. The server must not buffer it
	// all; it should reply "request too large" and/or close the connection.
	oversized := make([]byte, s.maxRequest*4)
	for i := range oversized {
		oversized[i] = 'A'
	}
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	conn.Write(oversized) // may partially fail once the server closes; that is fine

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	respLine, err := bufio.NewReader(conn).ReadBytes('\n')
	if err == nil {
		resp, derr := protocol.DecodeResponse(respLine)
		if derr != nil {
			t.Fatalf("decode: %v", derr)
		}
		if resp.Type != protocol.TypeError {
			t.Fatalf("expected error response for oversized request, got %q", resp.Type)
		}
	}
	// Whether or not an error line was read, the connection must be closed by
	// the server (further reads hit EOF); a fresh connection still works.
	conn2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	t.Cleanup(func() { conn2.Close() })
	good := dialAndReadLine(t, conn2, protocol.Request{Version: protocol.Version, ID: "ok", Type: protocol.TypePing})
	if good.Type != protocol.TypePong || good.ID != "ok" {
		t.Fatalf("server not healthy after oversized request: %+v", good)
	}
}

func TestPongServerWrongVersion(t *testing.T) {
	addr := startPongServer(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	req := protocol.Request{Version: "9.9", ID: "v", Type: protocol.TypePing}
	resp := dialAndReadLine(t, conn, req)
	if resp.Type != protocol.TypeError {
		t.Fatalf("expected error response for wrong version, got %q", resp.Type)
	}
	if resp.ID != "v" {
		t.Fatalf("error response should echo id, got %q", resp.ID)
	}
}
