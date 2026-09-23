# healthd

`healthd` is a multi-role connection monitoring daemon written in Go. A single
process runs any subset of the configured roles concurrently, each on its own
goroutine and ticker. Configuration is read once at startup; restart the
process to apply changes. The daemon shuts down gracefully on `SIGINT` or
`SIGTERM` via context cancellation.

## Roles

All roles are optional and enabled per the config file.

- **pong_server** — TCP server. Reads newline-framed JSON requests and routes
  each one, by its `type`, through a message dispatcher to a handler. The
  server is layered: a `tcp_listener` owns connection lifecycle, framing,
  timeouts, and the request-size bound; a `message_dispatcher` decodes each
  line, centrally handles malformed JSON and version mismatches, and routes to
  the registered handler; each `message_handler` serves one request type. The
  built-in `ping` handler replies with a pong (echoing the request `id` and
  `payload.timestamp`). When the same process also runs a monitoring role (see
  below), a `get-summary` handler is registered too (see
  [Summary requests](#summary-requests)). Malformed JSON, an unsupported
  protocol version, or an unknown request type yields an `error` response
  instead of crashing the server. One goroutine per connection; multiple
  sequential requests are supported until the client closes or the per-read
  timeout (`read_timeout_ms`) elapses.
- **ping_monitor** — TCP client. Monitors one or more `targets`, each an
  independent monitor with its own interval, timeout, threshold, and notify
  channels. For each target, every `interval_s` it connects to the target's
  address, sends a ping with a unique id and current epoch-ms timestamp, and
  expects a matching pong within `timeout_ms`. After `failure_threshold`
  consecutive failures that target transitions to UNHEALTHY and fires one
  alert; the next success transitions back to HEALTHY and fires one recovery
  alert. No repeat alerts while it stays in the same state. Each target runs
  on its own goroutine and does not interfere with the others; the alert
  Detail names the specific target.
- **url_monitor** — HTTP client. Monitors one or more `targets`, each an
  independent monitor of a single URL (e.g. a `/health/live` endpoint) with its
  own interval, timeout, threshold, and notify channels. Every `interval_s` it
  issues a GET; an HTTP 2xx within `timeout_ms` is a success, anything else
  (non-2xx, connection error, or timeout) a failure. Uses the same fire-once
  alert/recovery state machine, one goroutine per target, with the alert Detail
  naming the specific URL. (Unlike `internet_check`, which round-robins a shared
  list to answer "is the internet up", `url_monitor` tracks each endpoint
  independently.)
- **internet_check** — HTTP client. Each cycle probes the next site in the
  `sites` list in round-robin order. An HTTP 2xx within `timeout_ms` is a
  success; anything else (including timeout) is a failure. Uses the same
  fire-once alert/recovery state machine.
- **speed_check** — Runs the official Ookla speedtest CLI
  (`<binary> --accept-license --accept-gdpr --format=json`), parses the JSON,
  and computes download throughput in Mbps
  (`download.bandwidth` is bytes/sec, so `Mbps = bandwidth * 8 / 1e6`).
  A measurement below `threshold_mbps` is a failing check; at or above is a
  success. A missing binary or exec error is treated as a failing check and
  logged, never a crash. Feeds the same state machine.

## Alert state machine

A reusable, timer-free component (`internal/monitor`) drives ping_monitor,
internet_check, and speed_check. Each monitored unit picks one of two triggers:

**`consecutive`** (default):

- HEALTHY, after `failure_threshold` consecutive failing checks, transitions to
  UNHEALTHY and signals `AlertUnhealthy` exactly once.
- UNHEALTHY, on the first successful check, transitions to HEALTHY and signals
  `RecoveryHealthy` exactly once.

**`availability`** (rolling window):

- Each unit keeps a rolling window of the last `window_size` check results.
  Availability is the percentage of successes in the window.
- Once the window is full, HEALTHY transitions to UNHEALTHY when availability
  drops below `min_availability`, and back to HEALTHY when it climbs to or above
  it. During warm-up (before the window fills) the unit stays HEALTHY.

No repeat signals fire while remaining in the same state, regardless of trigger.

### Rolling window and last-success (all roles)

Every unit — each ping target, internet_check, and speed_check — tracks a
rolling availability window (`window_size` samples, defaulting to 20 when
omitted) and the timestamp of its last successful check, in both trigger modes.
Whenever any unit changes state, a single `status snapshot` line is logged
listing every unit across all roles with its current state, availability
percentage, consecutive-failure count, and last-success time. `speed_check`
additionally logs a `speed_check result` line after every execution (measured
Mbps, threshold, pass/fail).

## Monitoring summary

An instance running any monitoring role (`ping_monitor`, `internet_check`, or
`speed_check`) is a *monitoring instance*. In addition to the state-change
alerts and snapshots above, a monitoring instance:

- **Logs an aggregated summary on a fixed cadence.** Every
  `summary_interval_s` (default 900 s = 15 minutes) it emits one `monitoring
  summary` line covering every unit: current state, consecutive failures,
  rolling-window availability, availability across retained history, number of
  checks recorded, last-success time, and — for `speed_check` — the count and
  min/avg/max/latest of recent download-speed measurements. A pong-only
  instance (no monitoring role) does not log this and does not answer summary
  requests.
- **Retains a bounded per-unit history.** Every unit keeps the most recent
  check outcomes (up to 100), and `speed_check` additionally keeps its recent
  measured Mbps values (up to 100). The oldest sample is evicted once the
  buffer is full, so memory stays bounded. This history backs both the periodic
  log line and the `get-summary` response.

### Summary requests

A monitoring instance's `pong_server` accepts a `get-summary` request on the
same port and protocol as `ping`:

```json
{ "version": "0.1", "id": "s1", "type": "get-summary", "payload": {} }
```

The response echoes the `id` with `type` `summary` and a `summary` object. Each
unit reports its current status and its retained availability/speed history
(timestamps are epoch milliseconds; `speed` and the sample arrays are omitted
when empty):

```json
{
  "version": "0.1",
  "id": "s1",
  "type": "summary",
  "payload": {},
  "summary": {
    "generated_at_ms": 1700000000000,
    "units": [
      {
        "name": "speed_check",
        "state": "HEALTHY",
        "availability_pct": 100,
        "history_availability_pct": 95.0,
        "consecutive_failures": 0,
        "last_success_ms": 1700000000001,
        "speed": { "count": 20, "min_mbps": 410.2, "avg_mbps": 512.8, "max_mbps": 623.1, "latest_mbps": 540.0 },
        "checks": [ { "time_ms": 1700000000000, "success": true } ],
        "speeds": [ { "time_ms": 1700000000000, "mbps": 540.0 } ]
      }
    ]
  }
}
```

A `get-summary` sent to a pong-only (non-monitoring) instance yields an `error`
response with message `unsupported request type`. The response never contains
secrets. Note that any client that can reach the port can read this
availability/infrastructure data; restrict network access to the listener
accordingly.

## Notifications

Each role lists channel names in its `notify` array. When an alert fires, the
Dispatcher fans it out to those channels. Each channel send is retried with
exponential backoff (`base_delay_ms * 2^(attempt-1)`, up to `max_attempts`).
Channels are isolated: one channel's failure or panic never blocks the others,
and retries respect context cancellation.

Channel types:

- **console** — structured, leveled line on stdout via `log/slog`
  (UNHEALTHY at WARN, HEALTHY at INFO). Never logs secrets.
- **webhook** — HTTP POST of the JSON-serialized alert to `url` with
  `timeout_ms`. Non-2xx response counts as a failure and triggers retry.
- **email** — SMTP send via `net/smtp`. Builds a plain RFC822 message whose
  subject includes role and state. The password is read from the environment
  variable named by `password_env`.
- **smsgate** — HTTP POST to a self-hosted
  [SMS Gateway for Android](https://sms-gate.app) REST API at
  `<base_url>/message` using HTTP Basic auth (`username` + the env var named by
  `password_env`). Sends a short SMS-friendly message to `recipients`.

## Configuration reference

Config is JSON. Path is set with `-config` (default `./config.json`). See
[`config.example.json`](./config.example.json) for a full example.

### Top level

| Key                  | Type   | Notes |
|----------------------|--------|-------|
| `retry`              | object | Dispatcher retry policy. |
| `summary_interval_s` | int    | Aggregated-summary cadence for monitoring instances. Optional; defaults to 900 (15 min). Must be > 0. |
| `profiles`           | object | Optional. Reusable, named bundles of monitor settings referenced by `ping_monitor` and `url_monitor`. See [Profiles](#profiles). |
| `channels`           | object | Map of channel name to channel definition. |
| `roles`              | object | Role definitions. |

### `retry`

| Key             | Type | Default | Notes |
|-----------------|------|---------|-------|
| `max_attempts`  | int  | 3       | Must be >= 1. |
| `base_delay_ms` | int  | 500     | Must be >= 0. Backoff base delay. |

### `channels[name]`

| Key            | Type     | Applies to        | Notes |
|----------------|----------|-------------------|-------|
| `type`         | string   | all               | `console`, `webhook`, `email`, or `smsgate`. |
| `url`          | string   | webhook           | Required. |
| `timeout_ms`   | int      | webhook, smsgate  | Per-request timeout. |
| `smtp_host`    | string   | email             | Required. |
| `port`         | int      | email             | Required, > 0. Port `465` uses implicit TLS (TLS from connect, e.g. Gmail SMTPS); any other port (e.g. `587`) connects in cleartext and upgrades via STARTTLS. |
| `from`         | string   | email             | Required. |
| `to`           | string[] | email             | Required, non-empty. |
| `base_url`     | string   | smsgate           | Required, e.g. `http://127.0.0.1:8080/3rdparty/v1`. |
| `recipients`   | string[] | smsgate           | Required, non-empty. |
| `username`     | string   | email, smsgate    | Required for smsgate. |
| `password_env` | string   | email, smsgate    | Name of the env var holding the secret. Required. |

### Profiles

`profiles` is a top-level map of reusable, named setting bundles referenced by
name from `ping_monitor` and `url_monitor` targets — the same way `notify`
references named `channels`. A single profile can be shared across both roles.

Each `profiles[name]` entry may set any of: `interval_s`, `timeout_ms`,
`failure_threshold`, `trigger`, `window_size`, `min_availability`, `notify`,
`insecure_skip_verify` (same meanings as in the target tables below). All are
optional. `insecure_skip_verify` applies only to HTTP roles (`url_monitor`);
`ping_monitor` is TCP and ignores it. Because a bool has no "unset" state, a
profile can only turn `insecure_skip_verify` on — a target cannot switch off a
profile that enables it.

A target resolves each setting in this order: **inline field on the target →
its `profile` → the role's `default_profile`**. An inline field always
overrides the profile, and a profile only fills fields the target leaves unset.
A target with no `profile` and no `default_profile` must supply every required
field inline (the original, still-supported form). Referencing an undefined
profile (via a target's `profile` or a role's `default_profile`) aborts startup.

```json
"profiles": {
  "standard": { "interval_s": 30, "timeout_ms": 2000, "failure_threshold": 3, "notify": ["log"] },
  "fast":     { "interval_s": 15, "timeout_ms": 1000, "failure_threshold": 2, "notify": ["log"] },
  "web":      { "interval_s": 30, "timeout_ms": 5000, "failure_threshold": 2, "notify": ["log"] }
}
```

### `roles.pong_server`

| Key               | Type   | Notes |
|-------------------|--------|-------|
| `enabled`         | bool   | |
| `listen`          | string | Required, e.g. `:9000`. |
| `read_timeout_ms` | int    | Required, > 0. |

### `roles.ping_monitor`

Each entry in `targets` is an independent monitor. Targets may inherit their
settings from a top-level [profile](#profiles).

| Key               | Type     | Notes |
|-------------------|----------|-------|
| `enabled`         | bool     | |
| `default_profile` | string   | Optional. Top-level profile applied to any target that omits `profile`. Must exist in `profiles`. |
| `targets`         | object[] | Required, non-empty. One independent monitor per entry. |

Each `targets[]` entry:

| Key                 | Type     | Notes |
|---------------------|----------|-------|
| `target`            | string   | Required, `host:port`. |
| `profile`           | string   | Optional. Name of a top-level `profiles` entry to inherit from. Falls back to `default_profile`. |
| `interval_s`        | int      | > 0 after resolution (from target or profile). |
| `timeout_ms`        | int      | > 0 after resolution. |
| `failure_threshold` | int      | >= 1 (defaults to 1 if unset everywhere). Used by the `consecutive` trigger. |
| `trigger`           | string   | `consecutive` (default) or `availability`. |
| `window_size`      | int      | Rolling-window length. Defaults to 20 when omitted, so availability is always tracked and shown in the status snapshot. Must be > 0 for `availability`. |
| `min_availability` | number   | Availability percentage (0–100) below which `availability` alerts. Required for `availability`. |
| `notify`            | string[] | Channel names; each must exist in `channels`. |

Example (with the `profiles` block above) — a default, an explicit profile, and
a per-target override:

```json
"ping_monitor": {
  "enabled": true,
  "default_profile": "standard",
  "targets": [
    { "target": "10.0.0.5:9000" },
    { "target": "10.0.0.6:9000", "profile": "fast" },
    { "target": "10.0.0.7:9000", "failure_threshold": 5 }
  ]
}
```

Here `10.0.0.5` uses `standard`, `10.0.0.6` uses `fast`, and `10.0.0.7` uses
`standard` but bumps `failure_threshold` to 5.

### `roles.url_monitor`

Each entry in `targets` is an independent monitor of one HTTP(S) URL; an HTTP
2xx within `timeout_ms` is healthy. Same profile mechanism as `ping_monitor`.

| Key               | Type     | Notes |
|-------------------|----------|-------|
| `enabled`         | bool     | |
| `default_profile` | string   | Optional. Top-level profile applied to any target that omits `profile`. Must exist in `profiles`. |
| `targets`         | object[] | Required, non-empty. One independent monitor per entry. |

Each `targets[]` entry takes the same params as a `ping_monitor` target, except
`target` is replaced by:

| Key       | Type   | Notes |
|-----------|--------|-------|
| `url`     | string | Required. Absolute `http`/`https` URL, e.g. `http://svc.local/health/live`. |
| `profile` | string | Optional. Top-level profile to inherit from; falls back to `default_profile`. |
| `insecure_skip_verify` | bool | Optional. Disables TLS certificate verification for this target's HTTPS requests, so a self-signed cert is accepted. **Unsafe** — accepts any certificate (MITM risk); use only on a trusted network. |

Example:

```json
"url_monitor": {
  "enabled": true,
  "default_profile": "web",
  "targets": [
    { "url": "http://192.168.1.7:8080/health/live" },
    { "url": "https://www.example.com/health/live", "profile": "fast" }
  ]
}
```

### `roles.internet_check`

| Key                 | Type     | Notes |
|---------------------|----------|-------|
| `enabled`           | bool     | |
| `sites`             | string[] | Required, non-empty. |
| `interval_s`        | int      | Required, > 0. |
| `timeout_ms`        | int      | Required, > 0. |
| `failure_threshold` | int      | >= 1 (defaults to 1 if omitted). Used by the `consecutive` trigger. |
| `trigger`           | string   | `consecutive` (default) or `availability`. |
| `window_size`      | int      | See ping_monitor; defaults to 20 (availability always tracked). |
| `min_availability` | number   | Availability percentage (0–100); required for `availability`. |
| `notify`            | string[] | Channel names; each must exist in `channels`. |
| `insecure_skip_verify` | bool | Optional. Disables TLS verification for **all** HTTPS sites this role probes. **Unsafe** (accepts any cert); trusted networks only. |

### `roles.speed_check`

| Key                 | Type     | Notes |
|---------------------|----------|-------|
| `enabled`           | bool     | |
| `interval_s`        | int      | Required, > 0. |
| `threshold_mbps`    | number   | Required, > 0. |
| `binary`            | string   | Path to speedtest CLI (default `speedtest`). |
| `failure_threshold` | int      | >= 1 (defaults to 1 if omitted). Used by the `consecutive` trigger. |
| `trigger`           | string   | `consecutive` (default) or `availability`. |
| `window_size`      | int      | See ping_monitor; defaults to 20 (availability always tracked). |
| `min_availability` | number   | Availability percentage (0–100); required for `availability`. |
| `notify`            | string[] | Channel names; each must exist in `channels`. |

Validation aborts startup with a clear error if a required field is missing or
a role references a channel name that is not defined.

## Environment variables (secrets)

Secrets are never stored in the config file. Each `email` and `smsgate`
channel names an environment variable via `password_env`; the daemon reads the
secret from that variable at startup. Example for the sample config:

```
export SMTP_PASS='...'
export SMSGATE_PASS='...'
```

**Gmail:** use `smtp.gmail.com` with port `465` (implicit TLS) or `587`
(STARTTLS), and set `SMTP_PASS` to an [App Password](https://myaccount.google.com/apppasswords)
(requires 2-Step Verification) — Gmail rejects your normal account password.

## Build

```
go build ./cmd/healthd
```

## Run

```
./healthd -config ./config.json
```

Send `SIGINT` (Ctrl-C) or `SIGTERM` to stop; all roles drain and the process
exits cleanly.

## Test

```
go test ./...
```

Tests are hermetic: no real network, DNS, SMTP, SMS, or speedtest binary is
used. The speedtest command runner is injectable so a fake can supply sample
Ookla JSON.

## Docker

The image is multi-stage: a static Go binary is built in the first stage, and
the final Debian-slim stage installs the official Ookla speedtest CLI from the
Ookla apt repository. The `speed_check` role invokes the CLI with
`--accept-license --accept-gdpr`, so no interactive license prompt is required.

```
docker build -t healthd .
docker run --rm \
  -e SMTP_PASS \
  -e SMSGATE_PASS \
  -v "$PWD/config.json:/etc/healthd/config.json:ro" \
  -p 9000:9000 \
  healthd
```
