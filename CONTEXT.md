# jetmon-bridge — Build Context

This document gives a new Claude session everything it needs to build out jetmon-bridge from scratch. Read this before writing any code.

---

## What jetmon-bridge is

jetmon-bridge is a standalone read-only HTTP API bridge. It sits alongside Jetmon 1's MySQL database and exposes three endpoints that the uptime-bench `jetmon-v1` adapter calls to retrieve benchmark data. Jetmon's code is never modified.

The project that uses this bridge: [uptime-bench](https://github.com/Automattic/uptime-bench) — a benchmark harness that evaluates uptime monitoring services by injecting controlled failures and measuring detection accuracy.

---

## Why it exists

Jetmon 1 has no public API. To benchmark it, uptime-bench needs to read monitor status and incident events from Jetmon's database. Rather than modify Jetmon (which would risk affecting the behavior being measured), jetmon-bridge acts as a read-only observer.

The uptime-bench `jetmon-v1` adapter treats jetmon-bridge as just another HTTP API — no different in structure from how the Pingdom or Datadog adapters call their respective APIs.

---

## The three endpoints to implement

### `GET /time`

Returns the server's current time as RFC3339 with nanoseconds. The uptime-bench adapter calls this before `Retrieve` to compute the clock offset between the harness host and the Jetmon database host. Jetmon event timestamps are in the database server's timezone; the adapter needs to adjust before comparing against its own `RunWindow` times.

Response:
```json
{"time": "2026-04-22T14:03:00.123456789Z"}
```

### `GET /monitors?url=<url>`

Looks up a site record by URL in `jetpack_monitor_sites`. The uptime-bench adapter calls this during `Provision` to find the `blog_id` Jetmon uses as its monitor identifier.

The adapter needs: `blog_id`, `site_status`, `check_keyword`, `redirect_policy`. Return 404 if no record matches.

Jetmon monitors are always-on — uptime-bench does not create or delete them. An operator pre-seeds all benchmark targets in `jetpack_monitor_sites` before running scenarios.

Response (200):
```json
{
  "blog_id": 12345,
  "url": "https://bench-target-01.example.com",
  "status": "running",
  "keyword": "",
  "redirect_policy": "follow"
}
```

Response (404):
```json
{"error": "not found"}
```

### `GET /events?blog_id=<id>&since=<rfc3339>&until=<rfc3339>`

Returns `status_transition` rows from `jetmon_audit_log` for the given `blog_id` within the time window. This is the primary data the uptime-bench adapter uses to determine whether Jetmon detected a failure.

For each event, also attempt to join `jetmon_check_history` to populate per-component timing fields. These are nullable — not every audit log row has a matching history record.

Response:
```json
{
  "events": [
    {
      "blog_id": 12345,
      "event_type": "status_transition",
      "old_status": "running",
      "new_status": "confirmed_down",
      "error_code": 2,
      "http_code": 0,
      "source": "veriflier-01",
      "rtt_ms": 0,
      "dns_ms": null,
      "tcp_ms": null,
      "tls_ms": null,
      "ttfb_ms": null,
      "occurred_at": "2026-04-22T14:05:33Z"
    }
  ]
}
```

---

## Jetmon 1 database schema (read-only)

Connect to a **read replica**, not the primary. The relevant tables:

### `jetpack_monitor_sites`
The core site list.

| Column | Type | Notes |
|--------|------|-------|
| `blog_id` | int | WordPress site identifier, Jetmon's monitor ID |
| `monitor_url` | varchar | URL being monitored |
| `site_status` | int | 1 = running, 2 = confirmed_down |
| `check_keyword` | varchar | Optional body keyword to verify |
| `redirect_policy` | enum | `follow` / `alert` / `fail` |
| `alert_cooldown_minutes` | int | Per-site cooldown override |
| `last_alert_sent_at` | datetime | Tracks cooldown window |
| `maintenance_start` | datetime | Suppress alerts during maintenance |
| `maintenance_end` | datetime | |
| `last_checked_at` | datetime | |

### `jetmon_audit_log`
Immutable event record. The `status_transition` event type is what the adapter cares about most.

| Column | Type | Notes |
|--------|------|-------|
| `id` | bigint | |
| `event_type` | varchar | `check`, `status_transition`, `wpcom_sent`, `retry_dispatched`, `veriflier_sent`, `veriflier_result`, `maintenance_active`, `alert_suppressed` |
| `blog_id` | int | |
| `source` | varchar | Which host or veriflier recorded this |
| `http_code` | int | HTTP status code (0 if connection-level failure) |
| `error_code` | int | See error code table below |
| `rtt_ms` | float | Total round-trip time |
| `old_status` | varchar | For transition events: prior site status |
| `new_status` | varchar | For transition events: new site status |
| `occurred_at` | datetime | Timestamp (database server clock) |

### `jetmon_check_history`
Per-check timing samples. Join to `jetmon_audit_log` on `blog_id` + `occurred_at` (approximate — use a small window like ±1s if needed, or join on `id` if a foreign key exists).

| Column | Type | Notes |
|--------|------|-------|
| `blog_id` | int | |
| `rtt_ms` | float | |
| `dns_ms` | float | DNS resolution time |
| `tcp_ms` | float | TCP connect time |
| `tls_ms` | float | TLS handshake time |
| `ttfb_ms` | float | Time to first byte |
| `checked_at` | datetime | |

---

## Jetmon error codes

```
ErrorNone          0   Success
ErrorConnect       1   TCP connection refused or DNS failure
ErrorTimeout       2   Context deadline exceeded
ErrorSSL           3   TLS handshake error
ErrorTLSExpired    4   Certificate past NotAfter date
ErrorTLSDeprecated 5   TLS 1.0/1.1 (advisory only — IsFailure() returns false)
ErrorRedirect      6   Redirect when redirect_policy=fail
ErrorKeyword       7   Body did not contain required keyword
```

`ErrorTLSDeprecated` is advisory: Jetmon detects it but does not call it a failure. The uptime-bench adapter should map it to `"tls_deprecated_advisory"` in `RawClassification` rather than treating it as a down event.

---

## Jetmon's Veriflier system

Before marking a site `confirmed_down`, Jetmon dispatches to remote Veriflier nodes for multi-location quorum confirmation. In `jetmon_audit_log`, you will see:

1. `retry_dispatched` — local check failed, queued for retry
2. `veriflier_sent` — dispatched to verifliers
3. `veriflier_result` — one result per veriflier (may be success or failure)
4. `status_transition` with `new_status = "confirmed_down"` — quorum confirmed failure

The adapter cares primarily about `status_transition` events. The veriflier events are useful context to expose in `MonitorReport.Metadata` (e.g., `"veriflier_confirmations": 2`, `"veriflier_quorum": 2`).

---

## How the uptime-bench adapter uses this bridge

The uptime-bench adapter interface (defined in [ADAPTER.md](https://github.com/Automattic/uptime-bench/blob/trunk/ADAPTER.md)) has four methods:

- **`Provision(ctx, target, config) (MonitorHandle, error)`** — calls `GET /monitors?url=<target.URL>` to find the `blog_id`. Stores `blog_id` in `MonitorHandle.Fields["blog_id"]`. Returns error if not found (operator must pre-seed).
- **`Retrieve(ctx, handle, window) (RetrieveResult, error)`** — calls `GET /time` to compute clock offset, then `GET /events?blog_id=X&since=Y&until=Z`. Maps each `status_transition` to a `MonitorReport`. Returns `RetrieveUnknown` if the bridge is unreachable.
- **`Deprovision(ctx, handle) error`** — no-op. Jetmon monitors are always-on; nothing to clean up.
- **`Capabilities()`** — returns `MinCheckFrequency: 1*time.Minute, SupportsKeyword: true, SupportsAgentChecks: false, DefaultMaxCallsPerRun: 0`.

The `MonitorReport.RawClassification` the adapter sets for each event:
- `status_transition` to `confirmed_down` → use the `error_code` to set classification: `"timeout"`, `"connect_failed"`, `"ssl_error"`, `"tls_expired"`, `"tls_deprecated_advisory"`, `"redirect_error"`, `"keyword_failed"`
- `status_transition` to `running` (recovery) → `"up"`

---

## Implementation guidance

- **Language:** Go. Match the style of [uptime-bench](https://github.com/Automattic/uptime-bench) — standard idioms, errors as values, context on I/O functions.
- **Binary name:** `jetmon-bridge`
- **Flags:** `-dsn` (MySQL DSN), `-addr` (listen address, default `127.0.0.1:7400`), `-read-timeout` (DB query timeout, default `5s`)
- **Database:** `github.com/go-sql-driver/mysql` is the standard Go MySQL driver. Use `database/sql` directly — no ORM needed for three queries.
- **No writes:** the DSN user should have SELECT-only privileges. The bridge must never issue INSERT/UPDATE/DELETE.
- **No authentication:** intended for private network deployment only. Do not add auth middleware — keep it simple.
- **Graceful shutdown:** respect SIGINT/SIGTERM, drain in-flight requests.
- **Health check:** optionally add `GET /healthz` that pings the DB and returns 200/503. Useful for deployment checks.

---

## What's already decided (do not re-litigate)

- Always-on model: uptime-bench does not provision or deprovision Jetmon monitors. Pre-seeding is an operator responsibility.
- Read replica only: never connect to the primary.
- Three endpoints only: `/time`, `/monitors`, `/events`. No more, no less.
- Separate repo from uptime-bench: this repo exists specifically to keep Jetmon-specific code out of the system-agnostic benchmark harness.
- Service IDs: `jetmon-v1` (this bridge) and `jetmon-v2` (direct public API, future). Both are separate adapters in uptime-bench.
