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

## Schema facts (from the Jetmon repo)

These were verified against `migrations/001_jetmon2.sql` and `internal/checker/checker.go` in the Jetmon repo. **Do not guess — use these.**

### Column names

- `jetmon_audit_log` uses **`created_at`** (TIMESTAMP), not `occurred_at`. The API response key is `occurred_at`; the mapping lives in the SQL query alias and Go struct tag.
- `old_status` and `new_status` in `jetmon_audit_log` are **TINYINT**, not varchar. They are mapped to strings in Go:
  - `1` → `"running"`
  - `2` → `"confirmed_down"`
- All timing columns (`dns_ms`, `tcp_ms`, `tls_ms`, `ttfb_ms`, `rtt_ms`) are **INT**, not float.

### Error codes

Defined in `internal/checker/checker.go` in the Jetmon repo. Note: ErrorTimeout (1) and ErrorConnect (2) are reversed from what you might expect.

| Code | Constant | Description |
|------|----------|-------------|
| 0 | `ErrorNone` | Success |
| 1 | `ErrorTimeout` | Context deadline exceeded |
| 2 | `ErrorConnect` | TCP connection refused or DNS failure |
| 3 | `ErrorSSL` | TLS handshake error |
| 4 | `ErrorRedirect` | Redirect when redirect_policy=fail |
| 5 | `ErrorKeyword` | Body did not contain required keyword |
| 6 | `ErrorTLSExpired` | Certificate past NotAfter date |
| 7 | `ErrorTLSDeprecated` | TLS 1.0/1.1 detected (advisory — not a hard failure) |

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

- SELECT only. No INSERT, UPDATE, or DELETE anywhere in this codebase.
- Always use the context-aware query methods (`QueryContext`, `QueryRowContext`) so DB calls respect request cancellation.
- Connect to a read replica, never the primary.

---

## Architectural constraints

These are decided. Do not re-litigate them.

- **Three endpoints only:** `/time`, `/monitors`, `/events`. No additions without explicit discussion.
- **Always-on model:** jetmon-bridge never creates or deletes Jetmon monitors. Provision is a lookup, not a write.
- **No authentication:** private network deployment only. Do not add auth middleware.
- **Read replica:** the DSN must point at a replica. The binary should not enforce this, but never suggest or assume primary access.
- **Service ID:** the uptime-bench adapter for this bridge uses service ID `jetmon-v1`. Do not conflate with `jetmon-v2` (the future direct-API adapter).

---

## Local development

Start the full local stack (MySQL 8.0 + jetmon-bridge) with seed data:

```bash
cp docker/.env-sample docker/.env   # first time only
docker compose up --build
```

The bridge will be reachable at `http://localhost:7400`. Seed data includes two monitor sites and a status-transition sequence for `blog_id=1001`.

To run the binary directly against an existing MySQL instance:

```bash
JETMON_DSN="user:pass@tcp(localhost:3306)/jetmon_db" ./scripts/deploy-local.sh
```

---

## What to check before shipping

- No write queries anywhere (`INSERT`, `UPDATE`, `DELETE`, `CREATE`, `DROP`).
- All DB calls use `QueryContext` / `QueryRowContext` (not the non-context variants).
- Graceful shutdown: SIGINT/SIGTERM drains in-flight requests before exit.
- `/time` response is UTC, RFC3339 with sub-second precision.
- `/events` returns an empty array `[]`, not `null`, when no events match.
- No secrets in committed files. See `.gitignore` — `systemd/env` and all `.env` files are excluded.
