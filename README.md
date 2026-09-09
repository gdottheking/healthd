# roled

`roled` is a multi-role connection monitoring daemon written in Go. A single
process runs any subset of the configured roles concurrently, each on its own
goroutine and ticker. Configuration is read once at startup; restart the
process to apply changes. The daemon shuts down gracefully on `SIGINT` or
`SIGTERM` via context cancellation.

## Roles

All roles are optional and enabled per the config file.

- **pong_server** — TCP server. Reads a newline-framed JSON ping request and
  replies with a pong (echoing the request `id` and `payload.timestamp`).
  Malformed JSON or an unsupported protocol version yields an `error` response
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
internet_check, and speed_check:

- HEALTHY, after `failure_threshold` consecutive failing checks, transitions to
  UNHEALTHY and signals `AlertUnhealthy` exactly once.
- UNHEALTHY, on the first successful check, transitions to HEALTHY and signals
  `RecoveryHealthy` exactly once.
- No repeat signals while remaining in the same state.

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

| Key        | Type   | Notes |
|------------|--------|-------|
| `retry`    | object | Dispatcher retry policy. |
| `channels` | object | Map of channel name to channel definition. |
| `roles`    | object | Role definitions. |

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
| `port`         | int      | email             | Required, > 0. |
| `from`         | string   | email             | Required. |
| `to`           | string[] | email             | Required, non-empty. |
| `base_url`     | string   | smsgate           | Required, e.g. `http://127.0.0.1:8080/3rdparty/v1`. |
| `recipients`   | string[] | smsgate           | Required, non-empty. |
| `username`     | string   | email, smsgate    | Required for smsgate. |
| `password_env` | string   | email, smsgate    | Name of the env var holding the secret. Required. |

### `roles.pong_server`

| Key               | Type   | Notes |
|-------------------|--------|-------|
| `enabled`         | bool   | |
| `listen`          | string | Required, e.g. `:9000`. |
| `read_timeout_ms` | int    | Required, > 0. |

### `roles.ping_monitor`

Only `enabled` lives at the role level; every other setting belongs to an
entry in `targets`. Each entry is an independent monitor.

| Key       | Type              | Notes |
|-----------|-------------------|-------|
| `enabled` | bool              | |
| `targets` | object[]          | Required, non-empty. One independent monitor per entry. |

Each `targets[]` entry:

| Key                 | Type     | Notes |
|---------------------|----------|-------|
| `target`            | string   | Required, `host:port`. |
| `interval_s`        | int      | Required, > 0. |
| `timeout_ms`        | int      | Required, > 0. |
| `failure_threshold` | int      | >= 1 (defaults to 1 if omitted). |
| `notify`            | string[] | Channel names; each must exist in `channels`. |

Example:

```json
"ping_monitor": {
  "enabled": true,
  "targets": [
    { "target": "10.0.0.5:9000", "interval_s": 30, "timeout_ms": 2000, "failure_threshold": 3, "notify": ["hook", "log"] },
    { "target": "10.0.0.6:9000", "interval_s": 15, "timeout_ms": 1000, "failure_threshold": 2, "notify": ["log"] }
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
| `failure_threshold` | int      | Required, >= 1 (defaults to 1 if omitted). |
| `notify`            | string[] | Channel names; each must exist in `channels`. |

### `roles.speed_check`

| Key                 | Type     | Notes |
|---------------------|----------|-------|
| `enabled`           | bool     | |
| `interval_s`        | int      | Required, > 0. |
| `threshold_mbps`    | number   | Required, > 0. |
| `binary`            | string   | Path to speedtest CLI (default `speedtest`). |
| `failure_threshold` | int      | >= 1 (defaults to 1 if omitted). |
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

## Build

```
go build ./cmd/roled
```

## Run

```
./roled -config ./config.json
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
docker build -t roled .
docker run --rm \
  -e SMTP_PASS \
  -e SMSGATE_PASS \
  -v "$PWD/config.json:/etc/roled/config.json:ro" \
  -p 9000:9000 \
  roled
```
