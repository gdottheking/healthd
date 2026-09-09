package roles

import (
	"context"
	"errors"
	"testing"
	"time"
)

// sampleOoklaJSON reports download.bandwidth in bytes/sec.
// 12500000 bytes/sec * 8 / 1e6 = 100 Mbps.
const sampleOoklaJSON = `{
  "type": "result",
  "download": { "bandwidth": 12500000, "bytes": 100000000, "elapsed": 8000 },
  "upload": { "bandwidth": 2500000 }
}`

func fakeRunner(out string, err error) RunnerFunc {
	return func(ctx context.Context) ([]byte, error) {
		return []byte(out), err
	}
}

func TestSpeedCheckParsesMbps(t *testing.T) {
	sc := NewSpeedCheck("speedtest", time.Second, 50, 1, nil, &fakeNotifier{}, fakeRunner(sampleOoklaJSON, nil), testLogger())
	mbps, err := sc.measure(context.Background())
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if mbps != 100 {
		t.Fatalf("mbps: got %v want 100", mbps)
	}
}

func TestSpeedCheckBelowThresholdAlerts(t *testing.T) {
	fn := &fakeNotifier{}
	// 100 Mbps measured, threshold 200 -> failing check.
	sc := NewSpeedCheck("speedtest", time.Second, 200, 2, []string{"log"}, fn, fakeRunner(sampleOoklaJSON, nil), testLogger())
	ctx := context.Background()
	sc.runCheck(ctx) // fail 1
	if len(fn.snapshot()) != 0 {
		t.Fatal("no alert before threshold")
	}
	sc.runCheck(ctx) // fail 2 -> alert
	alerts := fn.snapshot()
	if len(alerts) != 1 || alerts[0].State != "UNHEALTHY" {
		t.Fatalf("expected one UNHEALTHY alert, got %+v", alerts)
	}
}

func TestSpeedCheckAtOrAboveThresholdSucceeds(t *testing.T) {
	fn := &fakeNotifier{}
	// 100 Mbps measured, threshold 100 -> at threshold = success.
	sc := NewSpeedCheck("speedtest", time.Second, 100, 1, []string{"log"}, fn, fakeRunner(sampleOoklaJSON, nil), testLogger())
	sc.runCheck(context.Background())
	if len(fn.snapshot()) != 0 {
		t.Fatalf("at-threshold should be success, got alerts %+v", fn.snapshot())
	}
}

func TestSpeedCheckRunnerErrorIsFailure(t *testing.T) {
	fn := &fakeNotifier{}
	// Simulates a missing binary / exec error.
	sc := NewSpeedCheck("speedtest", time.Second, 50, 1, []string{"log"}, fn, fakeRunner("", errors.New("exec: \"speedtest\": not found")), testLogger())
	// Must not panic and must record a failing check that fires an alert.
	sc.runCheck(context.Background())
	alerts := fn.snapshot()
	if len(alerts) != 1 || alerts[0].State != "UNHEALTHY" {
		t.Fatalf("runner error should be a failing check, got %+v", alerts)
	}
}

func TestSpeedCheckMalformedJSONIsError(t *testing.T) {
	sc := NewSpeedCheck("speedtest", time.Second, 50, 1, nil, &fakeNotifier{}, fakeRunner("{bad", nil), testLogger())
	if _, err := sc.measure(context.Background()); err == nil {
		t.Fatal("expected parse error for malformed JSON")
	}
}

func TestSpeedCheckDefaultRunnerSet(t *testing.T) {
	sc := NewSpeedCheck("speedtest", time.Second, 50, 1, nil, &fakeNotifier{}, nil, testLogger())
	if sc.runner == nil {
		t.Fatal("expected default runner to be set when nil passed")
	}
}
