# jetmon-bridge

**A small bridge that lets uptime-bench measure Jetmon 1 without changing Jetmon.**

Jetmon 1 is production uptime monitoring code with a simple database projection and no public API. That is a problem for benchmarking: [uptime-bench](https://github.com/Automattic/uptime-bench) needs to ask every monitored service the same questions after a controlled failure, but modifying Jetmon 1 to add benchmark endpoints would risk changing the behavior being measured.

`jetmon-bridge` solves that by standing beside Jetmon 1 as an observer. It reads Jetmon's MySQL state, optionally keeps bridge-owned SQLite history for status transitions, and exposes a narrow HTTP API that uptime-bench can call through its Jetmon 1 adapter.

```text
controlled failure -> Jetmon 1 checks -> Jetmon MySQL
                                      |
                                      v
                              jetmon-bridge -> uptime-bench reports
```

## Why This Matters

Benchmarks are only useful when they do not perturb the system under test. Jetmon 1 was not designed as an API-first monitoring service, and its v1 schema stores only the current site status plus the most recent status-change time. This bridge gives uptime-bench the smallest stable boundary it needs while keeping Jetmon 1 itself untouched.

| Audience | What Gets Better |
|---|---|
| Benchmark readers | Jetmon 1 results can sit beside Jetmon 2, Pingdom, UptimeRobot, Datadog, and other services in the same uptime-bench model. |
| Jetmon operators | The production monitor does not need benchmark-specific code or schema changes. |
| Adapter maintainers | The bridge presents a small API for monitor lookup, clock calibration, event retrieval, health, and optional write-mode provisioning. |
| Systems reviewers | Read-only mode can run against a replica, while write mode is explicit, authenticated, and isolated behind a separate primary DSN. |

## How It Works

The API has two operating styles:

- **Read-only mode** looks up existing Jetmon monitor rows and synthesizes the latest transition from `site_status` and `last_status_change`.
- **Persistent-history mode** polls active monitor rows and stores observed transitions in a bridge-owned SQLite database so uptime-bench can retrieve multiple transitions from a run window.
- **Write mode** is optional. It lets uptime-bench create and deactivate benchmark monitors through a separate primary DSN.

Jetmon remains the source of truth for checks. The bridge does not perform uptime probes, send notifications, or replace Jetmon's verification flow.

## Try It Locally

The fastest local loop uses Docker Compose with seeded MySQL data:

```bash
make up-local
curl http://localhost:7400/healthz
curl "http://localhost:7400/monitors?url=https://bench-target-01.example.com"
make down-local
```

Build and test from the repository root:

```bash
make build
go test ./...
```

## Documentation

| Document | Start Here For |
|---|---|
| [docs/README.md](docs/README.md) | Complete documentation map |
| [docs/architecture.md](docs/architecture.md) | System shape, modes, and Jetmon v1 data limits |
| [docs/api-reference.md](docs/api-reference.md) | HTTP API, request/response shapes, and adapter notes |
| [docs/docker.md](docs/docker.md) | Docker Compose modes and local smoke commands |
| [docs/development.md](docs/development.md) | Source layout, build, test, and smoke-test workflow |
| [docs/deployment.md](docs/deployment.md) | Systemd deployment, flags, auth, write mode, and history |

## Relationship To Other Repos

- [Automattic/uptime-bench](https://github.com/Automattic/uptime-bench) is the benchmark harness that consumes this bridge.
- [Automattic/jetmon v2](https://github.com/Automattic/jetmon/tree/v2) is the API-first Go rewrite of Jetmon. This bridge exists for Jetmon 1 compatibility during benchmark comparisons.

## Status

The bridge supports monitor lookup, event retrieval, clock calibration, health checks, optional write-mode provisioning, and optional persistent event history. It is intended for private-network use by uptime-bench and trusted operators, not as a public Jetmon API.

## License

GPL v2.0. See [LICENSE](LICENSE) for details.
