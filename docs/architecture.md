# Architecture

jetmon-bridge is a narrow HTTP boundary between Jetmon 1's MySQL database and uptime-bench's `jetmon-v1` adapter.

```text
uptime-bench adapter
        |
        v
  jetmon-bridge HTTP API
        |
        +--> Jetmon MySQL replica for reads
        |
        +--> Jetmon MySQL primary for optional write mode
        |
        +--> bridge-owned SQLite history database, when enabled
```

## Design Principles

- Do not modify Jetmon 1 code or schema for benchmark support.
- Keep read-only mode safe for a replica DSN.
- Make write mode explicit and require a separate primary DSN.
- Preserve Jetmon v1's data limits in the API contract rather than inventing evidence Jetmon did not store.
- Keep uptime-bench-specific behavior in the bridge and adapter, not in Jetmon.

## Operating Modes

### Read-only mode

Default mode. The bridge serves:

- `GET /time`
- `GET /monitors`
- `GET /events`
- `GET /healthz`

`GET /events` synthesizes at most one event from `jetpack_monitor_sites.site_status` and `last_status_change`. This is the maximum precision available from Jetmon v1 without additional bridge-owned history.

### Persistent-history mode

When `-history-path` is set, the bridge polls active monitor rows and records observed transitions in SQLite. Jetmon's database remains unchanged. Use a poll interval shorter than Jetmon's check interval so down and recovery transitions are not overwritten before the bridge sees them.

### Write mode

When `-write=true`, the bridge enables:

- `POST /monitors`
- `DELETE /monitors`

Write mode requires `-write-dsn` and should be protected with `-token`. The write DSN must point at the Jetmon primary and have only the permissions needed for benchmark monitor rows.

## Jetmon v1 Data Model

The bridge uses the v1 `jetpack_monitor_sites` table:

| Column | Purpose |
|---|---|
| `blog_id` | Jetmon monitor identifier |
| `bucket_no` | Worker bucket assignment |
| `monitor_url` | URL being monitored |
| `monitor_active` | Active or deactivated flag |
| `site_status` | `0` down, `1` running, `2` confirmed down |
| `last_status_change` | Timestamp of the latest status change |
| `check_interval` | v1 check interval value |

Jetmon v1 does not have the v2 audit log, check history, keyword column, redirect policy column, maintenance windows, or richer timing fields. The API keeps placeholder response fields stable where uptime-bench expects them, but values such as `keyword` and `redirect_policy` are defaults.
