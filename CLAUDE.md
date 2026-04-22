# jetmon-bridge — Project Guide for Claude

## What jetmon-bridge is

jetmon-bridge is a standalone read-only HTTP API bridge between Jetmon 1's MySQL database and the [uptime-bench](https://github.com/Automattic/uptime-bench) benchmark harness. It exposes three endpoints (`/time`, `/monitors`, `/events`) so the uptime-bench `jetmon-v1` adapter can retrieve monitor and incident data without modifying Jetmon's code.

It is **not** a general-purpose Jetmon API, not internet-facing, and has no write access to the database.

Key documents:
- [`README.md`](README.md) — API reference, deployment, pre-seeding requirement
- [`CONTEXT.md`](CONTEXT.md) — full build context: database schema, Jetmon internals, adapter interface, implementation guidance

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

## Architectural constraints

These are decided. Do not re-litigate them.

- **Three endpoints only:** `/time`, `/monitors`, `/events`. No additions without explicit discussion.
- **Always-on model:** jetmon-bridge never creates or deletes Jetmon monitors. Provision is a lookup, not a write.
- **No authentication:** private network deployment only. Do not add auth middleware.
- **Read replica:** the DSN must point at a replica. The binary should not enforce this, but never suggest or assume primary access.
- **Service ID:** the uptime-bench adapter for this bridge uses service ID `jetmon-v1`. Do not conflate with `jetmon-v2` (the future direct-API adapter).

## What to check before shipping

- No write queries anywhere (`INSERT`, `UPDATE`, `DELETE`, `CREATE`, `DROP`).
- All DB calls use `QueryContext` / `QueryRowContext` (not the non-context variants).
- Graceful shutdown: SIGINT/SIGTERM drains in-flight requests before exit.
- `/time` response is UTC, RFC3339 with sub-second precision.
- `/events` returns an empty array `[]`, not `null`, when no events match.
