# Project Guide

This is a compact guide for coding agents and maintainers. The public overview is in [../README.md](../README.md), and the full docs map is in [README.md](README.md).

## What This Project Is

jetmon-bridge is a Go HTTP service that exposes Jetmon 1 monitor state to uptime-bench without modifying Jetmon. It is not a general Jetmon API and should not be exposed as an unauthenticated public service.

## Layout

```text
cmd/jetmon-bridge/     service code and tests
docker/                Dockerfile, Compose files, env sample, local seed SQL
docs/                  maintained documentation
scripts/               local and remote deploy helpers
systemd/               service unit and env template
```

## Coding Rules

- Keep Jetmon v1 schema assumptions in [build-context.md](build-context.md).
- Use `database/sql` directly and context-aware query methods.
- Keep write operations behind `-write=true` and `-write-dsn`.
- Keep auth optional for local read-only work, but document it as required for reachable write-mode deployments.
- Do not log DSNs, bearer tokens, or full authorization headers.
- Return empty JSON arrays for empty event sets.
- Preserve the small API surface unless the uptime-bench adapter needs more.

## Verification

Run these before shipping:

```bash
gofmt -w cmd/jetmon-bridge
go test ./...
make build
make up-local
curl http://localhost:7400/healthz
make down-local
```
