# Development

## Requirements

- Go 1.26 or newer
- Docker and Docker Compose for local integration smoke tests
- `make`

## Source Layout

```text
cmd/jetmon-bridge/     Go command, handlers, database access, history store, tests
docker/                Dockerfile, Compose files, Docker env sample, local seed SQL
docs/                  API, architecture, deployment, Docker, and development docs
scripts/               Local, provisioning, and production deploy helpers
systemd/               Service unit and environment template
```

Root files are kept for repository conventions: `README.md`, `LICENSE`, `Makefile`, `go.mod`, `go.sum`, and `CLAUDE.md`.

## Build And Test

```bash
make build
make test
make vet
make check
```

The command package is built from `./cmd/jetmon-bridge`.

## Local Docker Smoke

```bash
make up-local
curl http://localhost:7400/healthz
curl "http://localhost:7400/monitors?url=https://bench-target-01.example.com"
curl "http://localhost:7400/events?blog_id=1001&since=2026-04-22T13:59:00Z&until=2026-04-22T14:01:00Z"
make down-local
```

Use [docker.md](docker.md) for the persistent-history smoke sequence.

## Review Checklist

- Write mode must require `-write-dsn` and should use `-token`.
- All database work should use context-aware `database/sql` methods.
- `GET /events` must return `[]`, not `null`, when no events match.
- Do not add Jetmon v2 columns or tables to v1 queries.
- Do not log DSN values, bearer tokens, or request auth headers.
- Run `gofmt`, `go test ./...`, and a Docker smoke test before shipping structural changes.
