package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validConfig = `{
  "retry": { "max_attempts": 3, "base_delay_ms": 10 },
  "channels": {
    "log":  { "type": "console" },
    "hook": { "type": "webhook", "url": "https://example/hook", "timeout_ms": 5000 }
  },
  "roles": {
    "pong_server":  { "enabled": true, "listen": ":9000", "read_timeout_ms": 2000 },
    "ping_monitor": {
      "enabled": true,
      "targets": [
        { "target": "10.0.0.5:9000", "interval_s": 30, "timeout_ms": 2000, "failure_threshold": 3, "notify": ["hook","log"] },
        { "target": "10.0.0.6:9000", "interval_s": 15, "timeout_ms": 1000, "failure_threshold": 2, "notify": ["log"] }
      ]
    }
  }
}`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	path := writeConfig(t, validConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load valid: %v", err)
	}
	if !cfg.Roles.PongServer.Enabled {
		t.Fatal("expected pong_server enabled")
	}
	if cfg.Retry.MaxAttempts != 3 {
		t.Fatalf("retry max_attempts: got %d", cfg.Retry.MaxAttempts)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	body := `{
      "channels": {},
      "roles": { "speed_check": { "enabled": true, "interval_s": 10, "threshold_mbps": 50 } }
    }`
	path := writeConfig(t, body)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Retry.MaxAttempts != 3 || cfg.Retry.BaseDelayMS != 500 {
		t.Fatalf("retry defaults not applied: %+v", cfg.Retry)
	}
	if cfg.Roles.SpeedCheck.Binary != "speedtest" {
		t.Fatalf("binary default not applied: %q", cfg.Roles.SpeedCheck.Binary)
	}
	if cfg.Roles.SpeedCheck.FailureThreshold != 1 {
		t.Fatalf("failure_threshold default not applied: %d", cfg.Roles.SpeedCheck.FailureThreshold)
	}
}

func TestChannelTimeoutDefaulted(t *testing.T) {
	body := `{
      "channels": {
        "hook": { "type": "webhook", "url": "https://x/hook" },
        "sms":  { "type": "smsgate", "base_url": "http://x/v1", "username": "u", "password_env": "P", "recipients": ["+1"] }
      },
      "roles": {}
    }`
	path := writeConfig(t, body)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Channels["hook"].TimeoutMS != 5000 {
		t.Fatalf("webhook timeout default: got %d want 5000", cfg.Channels["hook"].TimeoutMS)
	}
	if cfg.Channels["sms"].TimeoutMS != 5000 {
		t.Fatalf("smsgate timeout default: got %d want 5000", cfg.Channels["sms"].TimeoutMS)
	}
}

func TestChannelNegativeTimeoutRejected(t *testing.T) {
	body := `{
      "channels": { "hook": { "type": "webhook", "url": "https://x/hook", "timeout_ms": -1 } },
      "roles": {}
    }`
	path := writeConfig(t, body)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "timeout_ms") {
		t.Fatalf("expected timeout_ms floor error, got %v", err)
	}
}

func TestUndefinedChannelReference(t *testing.T) {
	body := `{
      "channels": { "log": { "type": "console" } },
      "roles": { "ping_monitor": { "enabled": true, "targets": [
        { "target": "x:1", "interval_s": 1, "timeout_ms": 1, "failure_threshold": 1, "notify": ["missing"] }
      ] } }
    }`
	path := writeConfig(t, body)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for undefined channel")
	}
	if !strings.Contains(err.Error(), "ping_monitor") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error should name role and channel: %v", err)
	}
	// The offending target must be identified (address and/or index).
	if !strings.Contains(err.Error(), "x:1") {
		t.Fatalf("error should identify the offending target: %v", err)
	}
}

func TestPingMonitorMultiTargetValid(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatalf("Load valid multi-target: %v", err)
	}
	if len(cfg.Roles.PingMonitor.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(cfg.Roles.PingMonitor.Targets))
	}
	if cfg.Roles.PingMonitor.Targets[1].IntervalS != 15 {
		t.Fatalf("second target interval: got %d want 15", cfg.Roles.PingMonitor.Targets[1].IntervalS)
	}
}

func TestPingMonitorEmptyTargets(t *testing.T) {
	body := `{
      "channels": {},
      "roles": { "ping_monitor": { "enabled": true, "targets": [] } }
    }`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "ping_monitor") || !strings.Contains(err.Error(), "targets") {
		t.Fatalf("expected non-empty targets error naming role, got %v", err)
	}
}

func TestPingMonitorMissingTargets(t *testing.T) {
	body := `{
      "channels": {},
      "roles": { "ping_monitor": { "enabled": true } }
    }`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "targets") {
		t.Fatalf("expected missing targets error, got %v", err)
	}
}

func TestPingMonitorTargetBadField(t *testing.T) {
	body := `{
      "channels": {},
      "roles": { "ping_monitor": { "enabled": true, "targets": [
        { "target": "10.0.0.5:9000", "interval_s": 1, "timeout_ms": 1, "failure_threshold": -1 }
      ] } }
    }`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "failure_threshold") {
		t.Fatalf("expected failure_threshold error, got %v", err)
	}
	if !strings.Contains(err.Error(), "10.0.0.5:9000") {
		t.Fatalf("error should identify the offending target: %v", err)
	}
}

func TestMissingRoleFieldPongServer(t *testing.T) {
	body := `{
      "channels": {},
      "roles": { "pong_server": { "enabled": true, "read_timeout_ms": 1000 } }
    }`
	path := writeConfig(t, body)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("expected listen required error, got %v", err)
	}
}

func TestMissingRoleFieldPingMonitor(t *testing.T) {
	body := `{
      "channels": {},
      "roles": { "ping_monitor": { "enabled": true, "targets": [
        { "interval_s": 1, "timeout_ms": 1, "failure_threshold": 1 }
      ] } }
    }`
	path := writeConfig(t, body)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("expected target required error, got %v", err)
	}
}

func TestMissingRoleFieldInternetCheck(t *testing.T) {
	body := `{
      "channels": {},
      "roles": { "internet_check": { "enabled": true, "interval_s": 1, "timeout_ms": 1, "failure_threshold": 1 } }
    }`
	path := writeConfig(t, body)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "sites") {
		t.Fatalf("expected sites required error, got %v", err)
	}
}

func TestMissingRoleFieldSpeedCheck(t *testing.T) {
	body := `{
      "channels": {},
      "roles": { "speed_check": { "enabled": true, "interval_s": 1 } }
    }`
	path := writeConfig(t, body)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "threshold_mbps") {
		t.Fatalf("expected threshold_mbps required error, got %v", err)
	}
}

func TestMissingChannelFieldWebhook(t *testing.T) {
	body := `{
      "channels": { "hook": { "type": "webhook" } },
      "roles": {}
    }`
	path := writeConfig(t, body)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "url") {
		t.Fatalf("expected url required error, got %v", err)
	}
}

func TestMissingChannelFieldEmail(t *testing.T) {
	body := `{
      "channels": { "m": { "type": "email", "port": 587, "from": "a@x", "to": ["b@x"], "password_env": "P" } },
      "roles": {}
    }`
	path := writeConfig(t, body)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "smtp_host") {
		t.Fatalf("expected smtp_host required error, got %v", err)
	}
}

func TestMissingChannelFieldSMSGate(t *testing.T) {
	body := `{
      "channels": { "s": { "type": "smsgate", "username": "u", "password_env": "P", "recipients": ["+1"] } },
      "roles": {}
    }`
	path := writeConfig(t, body)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("expected base_url required error, got %v", err)
	}
}

func TestUnknownChannelType(t *testing.T) {
	body := `{
      "channels": { "x": { "type": "carrier-pigeon" } },
      "roles": {}
    }`
	path := writeConfig(t, body)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("expected unknown type error, got %v", err)
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatalf("config.example.json must load and validate: %v", err)
	}
	if !cfg.Roles.PingMonitor.Enabled || len(cfg.Roles.PingMonitor.Targets) != 2 {
		t.Fatalf("example ping_monitor should have 2 targets, got %+v", cfg.Roles.PingMonitor)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
