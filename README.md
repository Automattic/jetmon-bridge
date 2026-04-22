# jetmon-bridge

Read-only HTTP API bridge for Jetmon 1. Queries the Jetmon database to expose monitor status and incident events in a format compatible with the [uptime-bench](https://github.com/Automattic/uptime-bench) adapter interface. Enables benchmarking of Jetmon 1 without modifying its code or affecting its operation.

---

## What this is

[uptime-bench](https://github.com/Automattic/uptime-bench) benchmarks uptime monitoring services by injecting controlled failures against target endpoints and measuring how accurately each service detects them. To benchmark Jetmon 1, it needs to query Jetmon's incident data — but Jetmon 1 has no public API, and modifying Jetmon's code to add one would risk affecting the very behavior being measured.

jetmon-bridge solves this by sitting alongside Jetmon's database as a read-only observer. It exposes three HTTP endpoints that the uptime-bench `jetmon-v1` adapter calls to look up monitors and retrieve incident data. Jetmon itself is unmodified.

---

## API

All endpoints are read-only. No authentication required for local / internal deployment (jetmon-bridge is not intended to be internet-facing).

### `GET /time`

Returns the bridge server's current time. Used by the uptime-bench adapter to calibrate the clock offset between the benchmark harness and the Jetmon database host before interpreting event timestamps.

```json
{
  "time": "2026-04-22T14:03:00.123456789Z"
}
```

### `GET /monitors?url=<url>`

Looks up the Jetmon site record for the given URL. Used by the uptime-bench adapter during `Provision` to find the `blog_id` (Jetmon's monitor identifier) for a benchmark target URL.

Jetmon 1 monitors are always-on — they are pre-provisioned by an operator before running benchmarks. uptime-bench does not create or delete Jetmon monitors.

```json
{
  "blog_id": 12345,
  "url": "https://bench-target-01.example.com",
  "status": "running",
  "keyword": "",
  "redirect_policy": "follow"
}
```

Returns `404` if no monitor exists for the given URL.

### `GET /events?blog_id=<id>&since=<rfc3339>&until=<rfc3339>`

Returns all status-transition events Jetmon recorded for the given `blog_id` within the time window. Used by the uptime-bench adapter during `Retrieve`.

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

Timing fields (`dns_ms`, `tcp_ms`, `tls_ms`, `ttfb_ms`) are `null` when not available for the event. They are populated from `jetmon_check_history` when a matching record exists.

---

## Jetmon error codes

The `error_code` field in event responses maps to Jetmon 1's internal error vocabulary:

| Code | Name | Description |
|------|------|-------------|
| 0 | `ErrorNone` | Success |
| 1 | `ErrorConnect` | TCP connection refused or DNS failure |
| 2 | `ErrorTimeout` | Context deadline exceeded |
| 3 | `ErrorSSL` | TLS handshake error |
| 4 | `ErrorTLSExpired` | Certificate past NotAfter date |
| 5 | `ErrorTLSDeprecated` | TLS 1.0/1.1 detected (advisory, not a hard failure) |
| 6 | `ErrorRedirect` | Redirect when redirect_policy=fail |
| 7 | `ErrorKeyword` | Body did not contain required keyword |

---

## Deployment

jetmon-bridge is deployed on a host with read access to Jetmon's MySQL database. It should connect to a **read replica**, not the primary, to avoid any possibility of interfering with Jetmon's write path.

```
jetmon-bridge \
  -dsn "user:pass@tcp(mysql-replica:3306)/jetmon_db" \
  -addr "127.0.0.1:7400"
```

The uptime-bench `fleet.toml` for a Jetmon-enabled run sets `jetmon_bridge_url` to point at this instance. The `jetmon-v1` adapter reads this URL from fleet config; if absent, it returns `RetrieveUnknown` rather than failing hard.

---

## Pre-seeding requirement

Jetmon 1 does not expose an API for creating monitors. Before running uptime-bench scenarios against Jetmon 1, an operator must ensure that all benchmark target URLs are already registered in `jetpack_monitor_sites` with `site_status = 1` (running). The `GET /monitors?url=<url>` endpoint will return 404 for any URL that hasn't been pre-seeded.

See the uptime-bench [OPERATIONS.md](https://github.com/Automattic/uptime-bench/blob/trunk/OPERATIONS.md) for the full pre-seeding procedure.

---

## Non-goals

- **Not a general-purpose Jetmon API.** This bridge exposes only what uptime-bench needs. It is not a replacement for a proper Jetmon public API.
- **Not internet-facing.** No authentication, rate limiting, or public exposure is designed in. Run it on a private network alongside the benchmark harness.
- **Not a write path.** jetmon-bridge has no write access to the database and makes no changes to Jetmon's data.
