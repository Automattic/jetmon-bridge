# jetmon-bridge — Project Guide for Claude

## What jetmon-bridge is

jetmon-bridge is a standalone read-only HTTP API bridge between Jetmon's MySQL database and the [uptime-bench](https://github.com/Automattic/uptime-bench) benchmark harness. It exposes three endpoints (`/time`, `/monitors`, `/events`) so the uptime-bench `jetmon-v1` adapter can retrieve monitor and incident data without modifying Jetmon's code.

It is **not** a general-purpose Jetmon API, not internet-facing, and has no write access to the database.

Key documents:
- [`README.md`](README.md) — API reference, deployment, pre-seeding requirement
- [`CONTEXT.md`](CONTEXT.md) — full build context: database schema, Jetmon internals, adapter interface

---

## Project structure

```
main.go          — flags, HTTP server, SIGINT/SIGTERM graceful shutdown
db.go            — DB connection, query functions, monitor/event types
handlers.go      — HTTP handlers for /time, /monitors, /events, /healthz

Dockerfile       — multi-stage build (golang:1.22-alpine → alpine:3.19)
docker-compose.yml
docker/
  init.sql       — local test schema + seed data (2 sites, transition events)
  .env-sample    — copy to .env for local docker-compose overrides

scripts/
  deploy-local.sh  — build + run the binary directly against a DSN
  deploy-prod.sh   — cross-compile and deploy to a remote host via SSH

systemd/
  jetmon-bridge.service — systemd unit; reads credentials from EnvironmentFile
```

The binary accepts three flags:

| Flag | Default | Purpose |
|------|---------|---------|
| `-dsn` | (required) | MySQL DSN for the read replica |
| `-addr` | `127.0.0.1:7400` | Listen address |
| `-read-timeout` | `5s` | Per-request DB query timeout |

---

## Schema facts (Jetmon v1)

Verified against the `v1` branch of the Jetmon repo (`README.md` schema block and `lib/database.js`). **Do not guess — use these. Do not apply v2 assumptions.**

### jetpack_monitor_sites — the only table in v1

```sql
CREATE TABLE `jetpack_monitor_sites` (
    `jetpack_monitor_site_id` bigint(20) unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY,
    `blog_id`                 bigint(20) unsigned NOT NULL,
    `bucket_no`               smallint(2) unsigned NOT NULL,
    `monitor_url`             varchar(300) NOT NULL,
    `monitor_active`          tinyint(1) unsigned NOT NULL DEFAULT 1,
    `site_status`             tinyint(1) unsigned NOT NULL DEFAULT 1,
    `last_status_change`      timestamp NULL DEFAULT current_timestamp(),
    `check_interval`          tinyint(1) unsigned NOT NULL DEFAULT 5,
    INDEX `blog_id_monitor_url` (`blog_id`, `monitor_url`),
    INDEX `bucket_no_monitor_active_check_interval` (`bucket_no`, `monitor_active`, `check_interval`)
);
```

### Status codes

| Value | Constant | Meaning |
|-------|----------|---------|
| 0 | `SITE_DOWN` | Initial down detection (unconfirmed) |
| 1 | `SITE_RUNNING` | Site is up |
| 2 | `SITE_CONFIRMED_DOWN` | Down, confirmed by veriflier |

### Columns that do NOT exist in v1 (v2 additions — never reference these)

- `check_keyword`, `redirect_policy`, `last_checked_at`, `last_alert_sent_at`
- `ssl_expiry_date`, `maintenance_start`, `maintenance_end`, `custom_headers`
- `timeout_seconds`, `alert_cooldown_minutes`

### Tables that do NOT exist in v1 (v2 only — never reference these)

- `jetmon_audit_log`
- `jetmon_check_history`
- `jetmon_false_positives`
- `jetmon_hosts`

### /events limitation

Jetmon v1 has no audit log. The `/events` endpoint synthesizes a single `status_transition` event from `last_status_change` and `site_status` when the transition timestamp falls within the query window. At most one event is returned per call. The `old_status`/`new_status` values are inferred from the current `site_status` (best-effort). This is the maximum precision available from v1 data.

---

## Coding conventions

### General

- Match existing style in the file you're editing. No drive-by refactors.
- Comments explain *why*, not *what*.
- No features beyond what the current task requires.

### Go

- Standard Go idioms: `gofmt`, short variable names in small scopes, errors as values, no panics in library code.
- Error wrapping: `fmt.Errorf("context: %w", err)`.
- Context is the first parameter on any function that does I/O or may be cancelled.
- `database/sql` directly — no ORM.

### Database

- Read replica DSN (`-dsn`) is for all GET endpoints. Write DSN (`-write-dsn`) must point at the primary.
- Always use the context-aware query methods (`QueryContext`, `QueryRowContext`, `ExecContext`) so DB calls respect request cancellation.
- Write operations (INSERT/UPDATE via write mode) go through the write DSN only. Never write through the read replica DSN.

---

## Architectural constraints

These are decided. Do not re-litigate them.

- **Three endpoints only:** `/time`, `/monitors`, `/events`. No additions without explicit discussion.
- **Write mode is optional:** read-only mode (default) does lookups only. Write mode (`-write`) enables POST/DELETE. A separate primary DSN (`-write-dsn`) is required for write mode.
- **Authentication is optional:** `-token` enables Bearer token auth on all requests. Strongly recommended when write mode is enabled.
- **Read replica:** the DSN must point at a replica. The binary should not enforce this, but never suggest or assume primary access.
- **Service ID:** the uptime-bench adapter for this bridge uses service ID `jetmon-v1`. Do not conflate with `jetmon-v2` (the future direct-API adapter).

---

## Local development

There are two Docker modes depending on whether you have access to a real Jetmon database.

**Real database** — connects the bridge container to Jetmon 1's actual read replica:

```bash
cp docker/.env-sample docker/.env
# Edit docker/.env and set JETMON_DSN=user:pass@tcp(replica-host:3306)/jetmon_db
make up
```

**Local test data** — spins up a MySQL container seeded with two monitor sites and a recorded down/recovery sequence (no real database needed):

```bash
cp docker/.env-sample docker/.env   # first time only; leave JETMON_DSN unset
make up-local
```

In both cases the bridge is reachable at `http://localhost:7400`. Stop with `make down`; `make down-clean` also wipes the local MySQL data volume.

To run the binary directly (no Docker) against any MySQL instance:

```bash
JETMON_DSN="user:pass@tcp(localhost:3306)/jetmon_db" ./scripts/deploy-local.sh
```

---

## Production deployment (Ubuntu Server 24.04)

The binary is statically linked (`CGO_ENABLED=0`) and has no runtime dependencies on the server — no Go installation or extra packages required.

**First-time setup** — run once to create the system user, directory layout, and systemd unit:

```bash
./scripts/provision.sh deploy@your-server
```

The script uploads the binary, installs the service, and exits without starting it. Before starting, edit the environment file on the server to set the DSN:

```bash
ssh deploy@your-server 'sudo nano /opt/jetmon-bridge/env'
# Set JETMON_DSN=user:password@tcp(replica-host:3306)/jetmon_db
```

Then start:

```bash
ssh deploy@your-server 'sudo systemctl start jetmon-bridge && sudo systemctl status jetmon-bridge'
```

**Subsequent updates** — build and deploy a new binary without touching the env file or user/directory setup:

```bash
./scripts/deploy-prod.sh deploy@your-server
```

**Viewing logs:**

```bash
ssh deploy@your-server 'journalctl -u jetmon-bridge -f'
```

---

## What to check before shipping

- No write queries anywhere (`INSERT`, `UPDATE`, `DELETE`, `CREATE`, `DROP`).
- All DB calls use `QueryContext` / `QueryRowContext` (not the non-context variants).
- Graceful shutdown: SIGINT/SIGTERM drains in-flight requests before exit.
- `/time` response is UTC, RFC3339 with sub-second precision.
- `/events` returns an empty array `[]`, not `null`, when no events match.
- No secrets in committed files. See `.gitignore` — `systemd/env` and all `.env` files are excluded.
