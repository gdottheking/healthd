package main

import (
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRunFailsFastOnBindError verifies DEF-001: a role that fails at runtime
// (here, pong_server cannot bind an in-use port) makes run return a non-nil
// error rather than idling with zero working roles.
func TestRunFailsFastOnBindError(t *testing.T) {
	// Occupy a port so the daemon's pong_server cannot bind it.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { busy.Close() })
	addr := busy.Addr().String()

	cfg := `{
      "channels": {},
      "roles": { "pong_server": { "enabled": true, "listen": "` + addr + `", "read_timeout_ms": 1000 } }
    }`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- run(path, quietLogger()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected non-nil error when a role fails to bind")
		}
		if !strings.Contains(err.Error(), "pong_server") {
			t.Fatalf("error should name the failing role, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not fail fast on bind error")
	}
}
