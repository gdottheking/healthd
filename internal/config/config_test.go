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
	if cfg.SummaryIntervalS != 900 {
		t.Fatalf("summary_interval_s default: got %d want 900", cfg.SummaryIntervalS)
	}
}

func TestSummaryIntervalOverrideAndRejection(t *testing.T) {
	body := `{
      "summary_interval_s": 60,
      "channels": {},
      "roles": { "speed_check": { "enabled": true, "interval_s": 10, "threshold_mbps": 50 } }
    }`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SummaryIntervalS != 60 {
		t.Fatalf("summary_interval_s override: got %d want 60", cfg.SummaryIntervalS)
	}

	bad := `{
      "summary_interval_s": -1,
      "channels": {},
      "roles": {}
    }`
	if _, err := Load(writeConfig(t, bad)); err == nil || !strings.Contains(err.Error(), "summary_interval_s must be > 0") {
		t.Fatalf("expected summary_interval_s rejection, got %v", err)
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

func TestPingMonitorProfilesResolve(t *testing.T) {
	body := `{
      "channels": { "log": { "type": "console" }, "hook": { "type": "webhook", "url": "https://x/h", "timeout_ms": 1000 } },
      "profiles": {
        "standard": { "interval_s": 30, "timeout_ms": 2000, "failure_threshold": 3, "notify": ["log"] },
        "fast":     { "interval_s": 15, "timeout_ms": 1000, "failure_threshold": 2, "notify": ["hook"] }
      },
      "roles": { "ping_monitor": {
        "enabled": true,
        "default_profile": "standard",
        "targets": [
          { "target": "a:1" },
          { "target": "b:1", "profile": "fast" },
          { "target": "c:1", "failure_threshold": 5 },
          { "target": "d:1", "profile": "fast", "notify": ["log"] }
        ]
      } }
    }`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tg := cfg.Roles.PingMonitor.Targets

	// a: inherits default_profile "standard" wholesale.
	if tg[0].IntervalS != 30 || tg[0].TimeoutMS != 2000 || tg[0].FailureThreshold != 3 || len(tg[0].Notify) != 1 || tg[0].Notify[0] != "log" {
		t.Fatalf("a not resolved from default profile: %+v", tg[0])
	}
	// b: explicit profile "fast" overrides the default.
	if tg[1].IntervalS != 15 || tg[1].TimeoutMS != 1000 || tg[1].FailureThreshold != 2 || tg[1].Notify[0] != "hook" {
		t.Fatalf("b not resolved from fast profile: %+v", tg[1])
	}
	// c: default profile, but inline failure_threshold wins.
	if tg[2].IntervalS != 30 || tg[2].FailureThreshold != 5 {
		t.Fatalf("c inline override not applied: %+v", tg[2])
	}
	// d: fast profile, but inline notify wins over the profile's notify.
	if tg[3].IntervalS != 15 || len(tg[3].Notify) != 1 || tg[3].Notify[0] != "log" {
		t.Fatalf("d inline notify override not applied: %+v", tg[3])
	}
}

func TestPingMonitorUndefinedProfile(t *testing.T) {
	body := `{
      "channels": { "log": { "type": "console" } },
      "profiles": { "standard": { "interval_s": 30, "timeout_ms": 2000, "notify": ["log"] } },
      "roles": { "ping_monitor": {
        "enabled": true,
        "targets": [ { "target": "a:1", "profile": "ghost" } ]
      } }
    }`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "profile") || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected undefined-profile error naming the profile, got %v", err)
	}
	if !strings.Contains(err.Error(), "a:1") {
		t.Fatalf("error should identify the offending target: %v", err)
	}
}

func TestPingMonitorUndefinedDefaultProfile(t *testing.T) {
	body := `{
      "channels": { "log": { "type": "console" } },
      "profiles": { "standard": { "interval_s": 30, "timeout_ms": 2000, "notify": ["log"] } },
      "roles": { "ping_monitor": {
        "enabled": true,
        "default_profile": "missing",
        "targets": [ { "target": "a:1" } ]
      } }
    }`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "default_profile") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected undefined default_profile error, got %v", err)
	}
}

func TestPingMonitorProfileMissingRequiredFieldStillCaught(t *testing.T) {
	// A profile that omits timeout_ms leaves the target with timeout_ms unset;
	// validation must still flag it after resolution.
	body := `{
      "channels": { "log": { "type": "console" } },
      "profiles": { "p": { "interval_s": 30, "notify": ["log"] } },
      "roles": { "ping_monitor": {
        "enabled": true,
        "default_profile": "p",
        "targets": [ { "target": "a:1" } ]
      } }
    }`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "timeout_ms") {
		t.Fatalf("expected timeout_ms error after profile resolution, got %v", err)
	}
}

func TestURLMonitorResolvesAndValidates(t *testing.T) {
	body := `{
      "channels": { "log": { "type": "console" } },
      "profiles": {
        "web":  { "interval_s": 30, "timeout_ms": 5000, "failure_threshold": 2, "notify": ["log"] },
        "fast": { "interval_s": 5,  "timeout_ms": 1000, "failure_threshold": 1, "notify": ["log"] }
      },
      "roles": { "url_monitor": {
        "enabled": true,
        "default_profile": "web",
        "targets": [
          { "url": "http://svc.local/health/live" },
          { "url": "https://svc.local/health/ready", "profile": "fast", "failure_threshold": 3 }
        ]
      } }
    }`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tg := cfg.Roles.URLMonitor.Targets
	if tg[0].IntervalS != 30 || tg[0].TimeoutMS != 5000 || tg[0].FailureThreshold != 2 || tg[0].Notify[0] != "log" {
		t.Fatalf("url target 0 not resolved from web profile: %+v", tg[0])
	}
	// fast profile supplies interval/timeout; inline failure_threshold overrides.
	if tg[1].IntervalS != 5 || tg[1].TimeoutMS != 1000 || tg[1].FailureThreshold != 3 {
		t.Fatalf("url target 1 override not applied: %+v", tg[1])
	}
}

func TestURLMonitorRejectsBadURL(t *testing.T) {
	body := `{
      "channels": { "log": { "type": "console" } },
      "roles": { "url_monitor": { "enabled": true, "targets": [
        { "url": "ftp://svc.local/x", "interval_s": 5, "timeout_ms": 1000, "notify": ["log"] }
      ] } }
    }`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "scheme must be http or https") {
		t.Fatalf("expected scheme error, got %v", err)
	}
	if !strings.Contains(err.Error(), "ftp://svc.local/x") {
		t.Fatalf("error should identify the offending target: %v", err)
	}
}

func TestURLMonitorMissingURL(t *testing.T) {
	body := `{
      "channels": { "log": { "type": "console" } },
      "roles": { "url_monitor": { "enabled": true, "targets": [
        { "interval_s": 5, "timeout_ms": 1000, "notify": ["log"] }
      ] } }
    }`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("expected url required error, got %v", err)
	}
}

func TestProfilesSharedAcrossRoles(t *testing.T) {
	// One top-level profile referenced by both ping_monitor and url_monitor.
	body := `{
      "channels": { "log": { "type": "console" } },
      "profiles": { "common": { "interval_s": 20, "timeout_ms": 2000, "failure_threshold": 2, "notify": ["log"] } },
      "roles": {
        "ping_monitor": { "enabled": true, "default_profile": "common", "targets": [ { "target": "a:1" } ] },
        "url_monitor":  { "enabled": true, "default_profile": "common", "targets": [ { "url": "http://a/health" } ] }
      }
    }`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Roles.PingMonitor.Targets[0].IntervalS != 20 || cfg.Roles.URLMonitor.Targets[0].IntervalS != 20 {
		t.Fatalf("shared profile not applied to both roles: ping=%+v url=%+v",
			cfg.Roles.PingMonitor.Targets[0], cfg.Roles.URLMonitor.Targets[0])
	}
}

func TestInsecureSkipVerifyResolves(t *testing.T) {
	body := `{
      "channels": { "log": { "type": "console" } },
      "profiles": {
        "insecure_web": { "interval_s": 30, "timeout_ms": 5000, "notify": ["log"], "insecure_skip_verify": true }
      },
      "roles": {
        "url_monitor": {
          "enabled": true,
          "default_profile": "insecure_web",
          "targets": [
            { "url": "https://self-signed.local/health" },
            { "url": "https://public.example/health", "timeout_ms": 3000 }
          ]
        },
        "internet_check": {
          "enabled": true,
          "sites": ["https://self-signed.local"],
          "interval_s": 60, "timeout_ms": 5000, "failure_threshold": 1,
          "notify": ["log"],
          "insecure_skip_verify": true
        }
      }
    }`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ut := cfg.Roles.URLMonitor.Targets
	if !ut[0].InsecureSkipVerify || !ut[1].InsecureSkipVerify {
		t.Fatalf("url targets should inherit insecure_skip_verify from the profile: %+v", ut)
	}
	if !cfg.Roles.InternetCheck.InsecureSkipVerify {
		t.Fatalf("internet_check should carry role-level insecure_skip_verify")
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
	if !cfg.Roles.PingMonitor.Enabled || len(cfg.Roles.PingMonitor.Targets) == 0 {
		t.Fatalf("example ping_monitor should be enabled with at least one target, got %+v", cfg.Roles.PingMonitor)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestAvailabilityTriggerValidAndDefaults(t *testing.T) {
	body := `{
      "channels": { "log": { "type": "console" } },
      "roles": { "internet_check": {
        "enabled": true, "sites": ["https://x"], "interval_s": 10, "timeout_ms": 1000,
        "trigger": "availability", "min_availability": 90, "notify": ["log"]
      } }
    }`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("availability config should be valid: %v", err)
	}
	r := cfg.Roles.InternetCheck
	if r.Trigger != TriggerAvailability {
		t.Fatalf("trigger: got %q want availability", r.Trigger)
	}
	if r.WindowSize != defaultWindowSize {
		t.Fatalf("window_size should default to %d, got %d", defaultWindowSize, r.WindowSize)
	}
}

func TestTriggerDefaultsToConsecutive(t *testing.T) {
	body := `{
      "channels": { "log": { "type": "console" } },
      "roles": { "speed_check": { "enabled": true, "interval_s": 10, "threshold_mbps": 50, "notify": ["log"] } }
    }`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Roles.SpeedCheck.Trigger != TriggerConsecutive {
		t.Fatalf("trigger should default to consecutive, got %q", cfg.Roles.SpeedCheck.Trigger)
	}
}

func TestAvailabilityRequiresMinAvailability(t *testing.T) {
	body := `{
      "channels": { "log": { "type": "console" } },
      "roles": { "internet_check": {
        "enabled": true, "sites": ["https://x"], "interval_s": 10, "timeout_ms": 1000,
        "trigger": "availability", "window_size": 10, "notify": ["log"]
      } }
    }`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "min_availability") {
		t.Fatalf("expected min_availability error, got %v", err)
	}
}

func TestUnknownTriggerRejected(t *testing.T) {
	body := `{
      "channels": { "log": { "type": "console" } },
      "roles": { "ping_monitor": { "enabled": true, "targets": [
        { "target": "10.0.0.5:9000", "interval_s": 1, "timeout_ms": 1, "trigger": "bogus", "notify": ["log"] }
      ] } }
    }`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "unknown trigger") {
		t.Fatalf("expected unknown trigger error, got %v", err)
	}
}
