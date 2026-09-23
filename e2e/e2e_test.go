//go:build e2e

// Package e2e drives the built healthd binary end-to-end against the QA success
// criteria. Run with: go test -tags e2e ./e2e/ -v -count=1
//
// These tests build the binary into a temp dir, launch it with temp JSON
// configs on high loopback ports, and assert real TCP/protocol behavior and
// process lifecycle. No product code is imported.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "healthd-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		os.Exit(2)
	}
	binPath = filepath.Join(dir, "healthd")

	// Locate module root (parent of e2e dir).
	wd, _ := os.Getwd()
	moduleRoot := filepath.Dir(wd)

	build := exec.Command("go", "build", "-o", binPath, "./cmd/healthd")
	build.Dir = moduleRoot
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(2)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// writeConfig writes cfg JSON to a temp file and returns its path.
func writeConfig(t *testing.T, cfg string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(f, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return f
}

// launch starts the daemon with the given config; returns the cmd and a
// function returning accumulated combined output. Registers cleanup to kill it.
func launch(t *testing.T, configPath string) (*exec.Cmd, func() string) {
	t.Helper()
	cmd := exec.Command(binPath, "-config", configPath)
	var buf syncBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGKILL)
			_, _ = cmd.Process.Wait()
		}
	})
	return cmd, buf.String
}

// waitForListen dials addr until it connects or times out.
func waitForListen(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server never began listening on %s within %s", addr, timeout)
}

// sendLine dials addr, writes reqLine + \n, reads one response line.
func sendLine(t *testing.T, addr, reqLine string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte(reqLine + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return strings.TrimSpace(line)
}

type resp struct {
	Version string `json:"version"`
	ID      string `json:"id"`
	Type    string `json:"type"`
	Payload struct {
		Timestamp int64  `json:"timestamp"`
		Message   string `json:"message"`
	} `json:"payload"`
}

func decode(t *testing.T, line string) resp {
	t.Helper()
	var r resp
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		t.Fatalf("decode response %q: %v", line, err)
	}
	return r
}

// Scenario 4 + 7: pong round-trip, malformed JSON, wrong version, and the
// server keeps serving subsequent requests on new connections.
func TestPongRoundTripAndRobustness(t *testing.T) {
	addr := "127.0.0.1:19001"
	cfg := `{
      "channels": { "log": { "type": "console" } },
      "roles": {
        "pong_server": { "enabled": true, "listen": "` + addr + `", "read_timeout_ms": 2000 }
      }
    }`
	launch(t, writeConfig(t, cfg))
	waitForListen(t, addr, 3*time.Second)

	// Real ping round-trip.
	ts := time.Now().UnixMilli()
	req := fmt.Sprintf(`{"version":"0.1","id":"qa-1","type":"ping","payload":{"timestamp":%d}}`, ts)
	r := decode(t, sendLine(t, addr, req))
	if r.Type != "pong" {
		t.Errorf("expected pong, got type %q", r.Type)
	}
	if r.ID != "qa-1" {
		t.Errorf("id not echoed: got %q want qa-1", r.ID)
	}
	if r.Payload.Timestamp != ts {
		t.Errorf("timestamp not echoed: got %d want %d", r.Payload.Timestamp, ts)
	}
	if r.Version != "0.1" {
		t.Errorf("version: got %q want 0.1", r.Version)
	}

	// Malformed JSON gets an error response (new connection).
	rm := decode(t, sendLine(t, addr, "this is not json"))
	if rm.Type != "error" {
		t.Errorf("malformed: expected error type, got %q", rm.Type)
	}

	// Wrong version gets an error response echoing the id.
	rv := decode(t, sendLine(t, addr, `{"version":"9.9","id":"qa-v","type":"ping","payload":{"timestamp":1}}`))
	if rv.Type != "error" {
		t.Errorf("wrong version: expected error type, got %q", rv.Type)
	}
	if rv.ID != "qa-v" {
		t.Errorf("wrong version: id not echoed, got %q want qa-v", rv.ID)
	}

	// Server still serves a valid ping after the bad ones (new connection).
	r2 := decode(t, sendLine(t, addr, `{"version":"0.1","id":"qa-2","type":"ping","payload":{"timestamp":42}}`))
	if r2.Type != "pong" || r2.ID != "qa-2" || r2.Payload.Timestamp != 42 {
		t.Errorf("server did not keep serving; got %+v", r2)
	}

	// Multiple sequential requests on the SAME connection.
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	for i := 0; i < 3; i++ {
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		id := fmt.Sprintf("seq-%d", i)
		fmt.Fprintf(conn, `{"version":"0.1","id":"%s","type":"ping","payload":{"timestamp":%d}}`+"\n", id, i)
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("seq read %d: %v", i, err)
		}
		rr := decode(t, strings.TrimSpace(line))
		if rr.Type != "pong" || rr.ID != id || rr.Payload.Timestamp != int64(i) {
			t.Errorf("seq %d: got %+v", i, rr)
		}
	}
}

// summaryResp decodes a get-summary response, including the summary payload
// the base resp struct omits.
type summaryResp struct {
	Version string `json:"version"`
	ID      string `json:"id"`
	Type    string `json:"type"`
	Summary *struct {
		GeneratedAtMS int64 `json:"generated_at_ms"`
		Units         []struct {
			Name                string  `json:"name"`
			State               string  `json:"state"`
			HistoryAvailability float64 `json:"history_availability_pct"`
			Checks              []struct {
				TimeMS  int64 `json:"time_ms"`
				Success bool  `json:"success"`
			} `json:"checks"`
		} `json:"units"`
	} `json:"summary"`
}

// A monitoring instance (pong_server + ping_monitor) answers get-summary with a
// summary of every unit it monitors, including recorded availability history.
func TestGetSummaryOnMonitoringInstance(t *testing.T) {
	addr := "127.0.0.1:19010"
	cfg := `{
      "retry": { "max_attempts": 1, "base_delay_ms": 0 },
      "channels": { "log": { "type": "console" } },
      "roles": {
        "pong_server": { "enabled": true, "listen": "` + addr + `", "read_timeout_ms": 2000 },
        "ping_monitor": {
          "enabled": true,
          "targets": [
            { "target": "` + addr + `", "interval_s": 1, "timeout_ms": 1000, "failure_threshold": 1, "notify": ["log"] }
          ]
        }
      }
    }`
	launch(t, writeConfig(t, cfg))
	waitForListen(t, addr, 3*time.Second)

	// Let a few ping cycles record history.
	time.Sleep(2500 * time.Millisecond)

	line := sendLine(t, addr, `{"version":"0.1","id":"sum-1","type":"get-summary","payload":{}}`)
	var r summaryResp
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		t.Fatalf("decode summary %q: %v", line, err)
	}
	if r.Type != "summary" || r.ID != "sum-1" {
		t.Fatalf("expected summary response echoing id, got type=%q id=%q", r.Type, r.ID)
	}
	if r.Summary == nil || len(r.Summary.Units) != 1 {
		t.Fatalf("expected exactly one monitored unit, got %+v", r.Summary)
	}
	u := r.Summary.Units[0]
	if !strings.HasPrefix(u.Name, "ping_monitor:") {
		t.Errorf("unit name: got %q want ping_monitor:* prefix", u.Name)
	}
	if len(u.Checks) == 0 {
		t.Errorf("expected recorded check history, got none")
	}
	if u.State != "HEALTHY" {
		t.Errorf("healthy local target should report HEALTHY, got %q", u.State)
	}
}

// A pong-only instance is not monitoring, so it rejects get-summary as an
// unsupported request type while still answering pings.
func TestGetSummaryRejectedWhenNotMonitoring(t *testing.T) {
	addr := "127.0.0.1:19011"
	cfg := `{
      "channels": { "log": { "type": "console" } },
      "roles": {
        "pong_server": { "enabled": true, "listen": "` + addr + `", "read_timeout_ms": 2000 }
      }
    }`
	launch(t, writeConfig(t, cfg))
	waitForListen(t, addr, 3*time.Second)

	r := decode(t, sendLine(t, addr, `{"version":"0.1","id":"sum-x","type":"get-summary","payload":{}}`))
	if r.Type != "error" {
		t.Fatalf("expected error for get-summary on non-monitoring instance, got %q", r.Type)
	}
	if !strings.Contains(r.Payload.Message, "unsupported request type") {
		t.Errorf("message: got %q want unsupported request type", r.Payload.Message)
	}
	// A normal ping still works.
	p := decode(t, sendLine(t, addr, `{"version":"0.1","id":"p","type":"ping","payload":{"timestamp":1}}`))
	if p.Type != "pong" {
		t.Errorf("pong-only instance should still answer pings, got %q", p.Type)
	}
}

// Scenario 5: ping_monitor pointed at the local pong_server reports healthy
// (no false UNHEALTHY alerts) via the console channel.
func TestPingMonitorHealthy(t *testing.T) {
	addr := "127.0.0.1:19002"
	cfg := `{
      "retry": { "max_attempts": 1, "base_delay_ms": 0 },
      "channels": { "log": { "type": "console" } },
      "roles": {
        "pong_server": { "enabled": true, "listen": "` + addr + `", "read_timeout_ms": 2000 },
        "ping_monitor": {
          "enabled": true,
          "targets": [
            { "target": "` + addr + `", "interval_s": 1, "timeout_ms": 1000, "failure_threshold": 1, "notify": ["log"] }
          ]
        }
      }
    }`
	_, out := launch(t, writeConfig(t, cfg))
	waitForListen(t, addr, 3*time.Second)

	// Let several ping cycles run (interval 1s).
	time.Sleep(4 * time.Second)

	logs := out()
	if strings.Contains(logs, "UNHEALTHY") {
		t.Errorf("ping_monitor emitted a false UNHEALTHY alert:\n%s", logs)
	}
	if strings.Contains(logs, "ping_monitor check failed") {
		t.Errorf("ping_monitor reported a failed check against healthy local server:\n%s", logs)
	}
}

// ping_monitor with reusable profiles + default_profile: targets that inherit
// from a profile monitor a healthy local server without false alerts, proving
// the binary resolves profiles end-to-end.
func TestPingMonitorProfiles(t *testing.T) {
	addr := "127.0.0.1:19012"
	cfg := `{
      "retry": { "max_attempts": 1, "base_delay_ms": 0 },
      "channels": { "log": { "type": "console" } },
      "roles": {
        "pong_server": { "enabled": true, "listen": "` + addr + `", "read_timeout_ms": 2000 },
        "ping_monitor": {
          "enabled": true,
          "default_profile": "std",
          "profiles": {
            "std": { "interval_s": 1, "timeout_ms": 1000, "failure_threshold": 1, "notify": ["log"] }
          },
          "targets": [
            { "target": "` + addr + `" },
            { "target": "` + addr + `", "profile": "std", "failure_threshold": 2 }
          ]
        }
      }
    }`
	_, out := launch(t, writeConfig(t, cfg))
	waitForListen(t, addr, 3*time.Second)
	time.Sleep(3 * time.Second)

	logs := out()
	if strings.Contains(logs, "UNHEALTHY") {
		t.Errorf("profile-resolved targets emitted a false UNHEALTHY alert:\n%s", logs)
	}
	if strings.Contains(logs, "ping_monitor check failed") {
		t.Errorf("profile-resolved targets reported a failed check:\n%s", logs)
	}
}

// Multi-target ping_monitor: a live target stays healthy (no alert) while an
// independent dead target goes UNHEALTHY and fires exactly once at its own
// threshold (no spam). Confirms per-target independence and clean shutdown.
func TestPingMonitorMultiTargetIndependence(t *testing.T) {
	live := "127.0.0.1:19007"
	dead := "127.0.0.1:19008" // nothing listening here
	cfg := `{
      "retry": { "max_attempts": 1, "base_delay_ms": 0 },
      "channels": { "log": { "type": "console" } },
      "roles": {
        "pong_server": { "enabled": true, "listen": "` + live + `", "read_timeout_ms": 2000 },
        "ping_monitor": {
          "enabled": true,
          "targets": [
            { "target": "` + live + `", "interval_s": 1, "timeout_ms": 1000, "failure_threshold": 1, "notify": ["log"] },
            { "target": "` + dead + `", "interval_s": 1, "timeout_ms": 1000, "failure_threshold": 2, "notify": ["log"] }
          ]
        }
      }
    }`
	cmd, out := launch(t, writeConfig(t, cfg))
	waitForListen(t, live, 3*time.Second)

	// Run several cycles so the dead target crosses its threshold and would
	// spam if fire-once were broken.
	time.Sleep(5 * time.Second)

	logs := out()

	// Exactly one UNHEALTHY *alert*, and it must be for the dead target. Only
	// count alert lines: a status-snapshot line also embeds a unit's state
	// string (and names every unit), so matching bare "state":"UNHEALTHY"
	// would double-count and misattribute.
	nUnhealthy := 0
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, `"msg":"alert"`) || !strings.Contains(line, `"state":"UNHEALTHY"`) {
			continue
		}
		nUnhealthy++
		if !strings.Contains(line, dead) || strings.Contains(line, live) {
			t.Errorf("UNHEALTHY alert not attributed to dead target:\n%s", line)
		}
	}
	if nUnhealthy != 1 {
		t.Errorf("expected exactly 1 UNHEALTHY alert, got %d\nlogs:\n%s", nUnhealthy, logs)
	}
	if !strings.Contains(logs, "ping to "+dead+" failed") {
		t.Errorf("expected a failed-check log for dead target %s\nlogs:\n%s", dead, logs)
	}
	// The live target must never appear in a failed check or UNHEALTHY alert.
	if strings.Contains(logs, "ping to "+live+" failed") {
		t.Errorf("live target %s reported a failed check\nlogs:\n%s", live, logs)
	}

	// Independent monitors, still a clean shutdown.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected exit 0 on SIGTERM, got: %v\nlogs:\n%s", err, out())
		}
	case <-time.After(3 * time.Second):
		t.Errorf("daemon did not shut down within 3s\nlogs:\n%s", out())
	}
}

// Scenario 3: graceful shutdown on SIGTERM within a couple seconds, exit 0.
func TestGracefulShutdownSIGTERM(t *testing.T) {
	testShutdown(t, syscall.SIGTERM, "19003")
}

func TestGracefulShutdownSIGINT(t *testing.T) {
	testShutdown(t, syscall.SIGINT, "19004")
}

func testShutdown(t *testing.T, sig os.Signal, port string) {
	addr := "127.0.0.1:" + port
	cfg := `{
      "channels": { "log": { "type": "console" } },
      "roles": {
        "pong_server": { "enabled": true, "listen": "` + addr + `", "read_timeout_ms": 2000 },
        "ping_monitor": {
          "enabled": true,
          "targets": [
            { "target": "` + addr + `", "interval_s": 1, "timeout_ms": 1000, "failure_threshold": 1, "notify": ["log"] }
          ]
        }
      }
    }`
	cmd, out := launch(t, writeConfig(t, cfg))
	waitForListen(t, addr, 3*time.Second)

	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected exit 0, got error: %v\nlogs:\n%s", err, out())
		}
		if !strings.Contains(out(), "healthd stopped") {
			t.Errorf("missing graceful stop log line:\n%s", out())
		}
	case <-time.After(3 * time.Second):
		t.Errorf("process did not exit within 3s of %v\nlogs:\n%s", sig, out())
	}
}

// Scenario 6: config validation rejects bad configs with clear errors and
// exits non-zero. Covers: undefined notify channel, missing required field,
// non-positive webhook/smsgate timeout_ms.
func TestConfigValidationRejections(t *testing.T) {
	cases := []struct {
		name string
		cfg  string
		// wantSubstrs are all asserted present. Kept quote-free where the
		// message embeds quotes, since slog JSON-escapes inner quotes in the
		// logged error line.
		wantSubstrs []string
	}{
		{
			name: "notify references undefined channel",
			cfg: `{
              "channels": { "log": { "type": "console" } },
              "roles": {
                "ping_monitor": { "enabled": true, "targets": [ { "target": "127.0.0.1:1", "interval_s": 1, "timeout_ms": 1000, "notify": ["ghost"] } ] }
              }
            }`,
			wantSubstrs: []string{"undefined channel"},
		},
		{
			name: "missing required field (pong_server listen)",
			cfg: `{
              "channels": {},
              "roles": {
                "pong_server": { "enabled": true, "read_timeout_ms": 2000 }
              }
            }`,
			wantSubstrs: []string{"listen is required"},
		},
		{
			name: "non-positive webhook timeout_ms",
			cfg: `{
              "channels": { "hook": { "type": "webhook", "url": "https://x.test/h", "timeout_ms": -5 } },
              "roles": {
                "pong_server": { "enabled": true, "listen": "127.0.0.1:19099", "read_timeout_ms": 2000 }
              }
            }`,
			wantSubstrs: []string{"timeout_ms must be > 0"},
		},
		{
			name: "non-positive smsgate timeout_ms",
			cfg: `{
              "channels": { "sms": { "type": "smsgate", "base_url": "http://127.0.0.1/x", "username": "u", "password_env": "P", "recipients": ["+1"], "timeout_ms": -1 } },
              "roles": {
                "pong_server": { "enabled": true, "listen": "127.0.0.1:19098", "read_timeout_ms": 2000 }
              }
            }`,
			wantSubstrs: []string{"timeout_ms must be > 0"},
		},
		{
			name: "ping_monitor enabled with empty targets",
			cfg: `{
              "channels": { "log": { "type": "console" } },
              "roles": {
                "ping_monitor": { "enabled": true, "targets": [] }
              }
            }`,
			wantSubstrs: []string{"targets must be non-empty"},
		},
		{
			name: "ping_monitor target notify undefined channel names the target",
			cfg: `{
              "channels": { "log": { "type": "console" } },
              "roles": {
                "ping_monitor": { "enabled": true, "targets": [
                  { "target": "127.0.0.1:1", "interval_s": 1, "timeout_ms": 1000, "notify": ["log"] },
                  { "target": "127.0.0.1:2", "interval_s": 1, "timeout_ms": 1000, "notify": ["ghost"] }
                ] }
              }
            }`,
			// Error must name the offending target (index 1 and its address)
			// AND flag the undefined channel. Quote-free fragments so the
			// assertion is robust to slog JSON-escaping of the logged message.
			wantSubstrs: []string{"target 1 (", "127.0.0.1:2", "references undefined channel", "ghost"},
		},
		{
			name: "old flat ping_monitor schema rejected (breaking change)",
			cfg: `{
              "channels": { "log": { "type": "console" } },
              "roles": {
                "ping_monitor": { "enabled": true, "target": "127.0.0.1:1", "interval_s": 1, "timeout_ms": 1000, "notify": ["log"] }
              }
            }`,
			// DisallowUnknownFields must reject the removed flat fields.
			wantSubstrs: []string{"unknown field"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binPath, "-config", writeConfig(t, tc.cfg))
			outBytes, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected non-zero exit for bad config, got success\noutput:\n%s", outBytes)
			}
			if ee, ok := err.(*exec.ExitError); ok {
				if ee.ExitCode() != 1 {
					t.Errorf("expected exit code 1, got %d", ee.ExitCode())
				}
			}
			for _, want := range tc.wantSubstrs {
				if !strings.Contains(string(outBytes), want) {
					t.Errorf("error message missing %q:\n%s", want, outBytes)
				}
			}
		})
	}
}

// Regression for DEF-002: an oversized newline-free request must be rejected
// with a "request too large" error and the connection closed, with memory
// bounded; a fresh connection must still get a correct pong afterward.
func TestOversizedRequestRejected(t *testing.T) {
	addr := "127.0.0.1:19005"
	cfg := `{
      "channels": { "log": { "type": "console" } },
      "roles": {
        "pong_server": { "enabled": true, "listen": "` + addr + `", "read_timeout_ms": 2000 }
      }
    }`
	launch(t, writeConfig(t, cfg))
	waitForListen(t, addr, 3*time.Second)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	// Stream ~2 MiB with no newline (well over the 64KB cap).
	chunk := make([]byte, 64*1024)
	for i := range chunk {
		chunk[i] = 'A'
	}
	for i := 0; i < 32; i++ {
		if _, werr := conn.Write(chunk); werr != nil {
			break // server may have closed after the error; acceptable
		}
	}
	line, _ := bufio.NewReader(conn).ReadString('\n')
	r := decode(t, strings.TrimSpace(line))
	if r.Type != "error" || !strings.Contains(r.Payload.Message, "too large") {
		t.Errorf("expected 'request too large' error, got %+v", r)
	}

	// Fresh connection still gets a correct pong.
	r2 := decode(t, sendLine(t, addr, `{"version":"0.1","id":"after","type":"ping","payload":{"timestamp":9}}`))
	if r2.Type != "pong" || r2.ID != "after" || r2.Payload.Timestamp != 9 {
		t.Errorf("server did not recover after oversized request; got %+v", r2)
	}
}

// Regression for DEF-001: a role that fails to start (port already bound) must
// cause the daemon to exit non-zero rather than run degraded.
func TestFailFastOnRoleError(t *testing.T) {
	addr := "127.0.0.1:19006"
	cfg := `{
      "channels": { "log": { "type": "console" } },
      "roles": {
        "pong_server": { "enabled": true, "listen": "` + addr + `", "read_timeout_ms": 2000 }
      }
    }`
	cfgPath := writeConfig(t, cfg)

	// Instance A holds the port.
	launch(t, cfgPath)
	waitForListen(t, addr, 3*time.Second)

	// Instance B with the same config must fail fast.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmdB := exec.CommandContext(ctx, binPath, "-config", cfgPath)
	outB, err := cmdB.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit when port is bound, got success:\n%s", outB)
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() != 1 {
		t.Errorf("expected exit code 1, got %d", ee.ExitCode())
	}
	if !strings.Contains(string(outB), "address already in use") {
		t.Errorf("expected bind failure in output:\n%s", outB)
	}
}

// syncBuffer is a minimal concurrency-safe buffer for capturing child output.
type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
