# jetmon-bridge

HTTP API bridge for Jetmon 1. Queries the Jetmon database to expose monitor status and incident events in a format compatible with the [uptime-bench](https://github.com/Automattic/uptime-bench) adapter interface. Enables benchmarking of Jetmon 1 without modifying its code or affecting its operation.

---

## What this is

[uptime-bench](https://github.com/Automattic/uptime-bench) benchmarks uptime monitoring services by injecting controlled failures against target endpoints and measuring how accurately each service detects them. To benchmark Jetmon 1, it needs to query Jetmon's incident data — but Jetmon 1 has no public API, and modifying Jetmon's code to add one would risk affecting the very behavior being measured.

jetmon-bridge solves this by sitting alongside Jetmon's database as an observer. It exposes HTTP endpoints that the uptime-bench `jetmon-v1` adapter calls to look up monitors and retrieve incident data. Jetmon itself is unmodified.

---

## Flags

| Flag | Default | Purpose |
|------|---------|---------|
| `-dsn` | (required) | MySQL DSN for the Jetmon read replica |
| `-addr` | `127.0.0.1:7400` | Listen address (`host:port`) |
| `-read-timeout` | `5s` | Per-request DB query timeout |
| `-write` | `false` | Enable write endpoints: `POST /monitors`, `DELETE /monitors` |
| `-write-dsn` | `""` | MySQL DSN for write operations (must be the primary, not the replica); required when `-write` is set |
| `-token` | `""` | Bearer token required on all requests; empty disables auth |
| `-bucket` | `0` | Jetmon worker bucket number assigned to new monitors (write mode only) |
| `-version` | | Print version and exit |

---

## API

### `GET /time`

Returns the bridge server's current UTC time. Used by the uptime-bench adapter to calibrate the clock offset between the benchmark harness and the Jetmon host before interpreting event timestamps.

```json
{"time": "2026-04-22T14:03:00.123456789Z"}
```

---

### `GET /monitors?url=<url>`

Looks up the active Jetmon monitor for the given URL.

```json
{
  "blog_id": 12345,
  "monitor_url": "https://bench-target-01.example.com",
  "site_status": 1,
  "monitor_active": true,
  "keyword": "",
  "redirect_policy": "follow"
}
```

Returns `404` if no active monitor exists for the URL.

**`site_status` values:**

| Value | Meaning |
|-------|---------|
| `0` | Down — initial detection, unconfirmed |
| `1` | Running |
| `2` | Confirmed down — verified by the veriflier |

---

### `GET /events?blog_id=<id>&since=<rfc3339>&until=<rfc3339>`

Returns status-transition events for the given `blog_id` within the time window. Used by the uptime-bench adapter during `Retrieve`.

```json
[
  {
    "id": 0,
    "blog_id": 12345,
    "event_type": "status_transition",
    "source": "veriflier",
    "http_code": null,
    "old_status": 1,
    "new_status": 2,
    "detail": null,
    "created_at": "2026-04-22T14:05:33Z"
  }
]
```

Returns an empty array `[]` when no events match the window.

**v1 limitation:** Jetmon 1 has no audit log. This endpoint synthesizes a single event from `last_status_change` (timestamp of the most recent transition) and `site_status` (current state). At most one event is returned per call. To observe all transitions, poll at an interval shorter than Jetmon's check interval (5 minutes by default).

**`source` values:**

| Value | When |
|-------|------|
| `"worker"` | Initial unconfirmed down (`site_status = 0`) |
| `"veriflier"` | Confirmed down (`site_status = 2`) |
| `"jetmon"` | Recovery to running (`site_status = 1`) |

---

### `GET /healthz`

Returns `200 OK` when the bridge can reach the database, `503 Service Unavailable` otherwise.

```json
{"status": "ok"}
```

---

### `POST /monitors` (write mode only)

Creates a new monitor or reactivates an existing deactivated one. Requires `-write` to be enabled.

Request body:
```json
{"url": "https://bench-target-01.example.com"}
```

Returns `201 Created` with the monitor object if a new monitor was created or a deactivated one was reactivated. Returns `200 OK` if the monitor already exists and is active. Existing rows are reset to `site_status = 1` with a fresh `last_status_change` before returning, so uptime-bench write-mode runs start from a clean running baseline.

---

### `DELETE /monitors?url=<url>` (write mode only)

Deactivates the monitor for the given URL (sets `monitor_active = 0`) and resets `site_status = 1` with a fresh `last_status_change`. Requires `-write` to be enabled.

Returns `204 No Content` on success, `404` if no monitor exists for the URL.

---

## Authentication

All endpoints can be protected with a Bearer token by passing `-token <secret>`. When set, every request must include:

```
Authorization: Bearer <secret>
```

Requests without a valid token receive `401 Unauthorized`. Strongly recommended when write mode is enabled.

---

## Deployment

### Systemd (bare metal / VM)

**First-time provisioning** — creates the system user, directory layout, and systemd unit on the target server:

```bash
./scripts/provision.sh deploy@your-server
```

The script installs everything but does not start the service. Before starting, set the DSN:

```bash
ssh deploy@your-server 'sudo nano /opt/jetmon-bridge/env'
# Set JETMON_DSN=user:password@tcp(replica-host:3306)/jetmon_db
```

Then start:

```bash
ssh deploy@your-server 'sudo systemctl start jetmon-bridge && sudo systemctl status jetmon-bridge'
```

**Subsequent updates:**

```bash
./scripts/deploy-prod.sh deploy@your-server
```

**Viewing logs:**

```bash
ssh deploy@your-server 'journalctl -u jetmon-bridge -f'
```

See `systemd/env.sample` for all configurable environment variables.

### Docker (real database)

Connects the bridge container to an external Jetmon MySQL instance via the `jetmon-shared` Docker network.

```bash
cp docker/.env-sample docker/.env
# Set JETMON_DSN in docker/.env
make up
```

Stop: `make down`

### Docker (local test data)

Spins up a MySQL container seeded with two monitor sites and a recorded down/recovery sequence. No external database required.

```bash
make up-local
```

Stop: `make down-local` (or `make down-clean` to also wipe the data volume)

In both Docker modes the bridge is reachable at `http://localhost:7400`.

---

## Pre-seeding

When running in read-only mode (default), Jetmon monitors must be created by an operator before running uptime-bench. The bridge does not create them automatically and `GET /monitors` returns `404` for any URL not already registered in `jetpack_monitor_sites`.

When running in write mode (`-write`), the bridge can create monitors on demand via `POST /monitors`. The `-bucket` flag must be set to an active Jetmon worker bucket so the created monitors are picked up for checking.

---

## Non-goals

- **Not a general-purpose Jetmon API.** This bridge exposes only what uptime-bench needs.
- **Not internet-facing.** Designed for private network deployment alongside the benchmark harness.
