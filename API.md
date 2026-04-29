# jetmon-bridge API Reference

jetmon-bridge is a lightweight HTTP bridge that exposes Jetmon 1's monitor database to external tooling. It is private-network only — never internet-facing.

Base URL (default): `http://127.0.0.1:7400`

---

## Authentication

When the bridge is started with `-token <value>`, every request must include:

```
Authorization: Bearer <token>
```

Requests without a valid token receive **401 Unauthorized**:

```json
{"error": "unauthorized"}
```

When `-token` is not set (the default), no authentication is required.

---

## Modes

### Read-only mode (default, `-write=false`)

Only `GET /monitors`, `GET /events`, `GET /time`, and `GET /healthz` are active. `POST /monitors` and `DELETE /monitors` return **405 Method Not Allowed**.

Monitors must be pre-seeded in `jetpack_monitor_sites` before any provisioning calls. The bridge does not create them.

### Write mode (`-write=true`)

Enables `POST /monitors` and `DELETE /monitors`. The bridge creates new Jetmon monitor records and soft-deletes them.

**Required:** `-write-dsn` must point at the MySQL **primary** (not the read replica). Writes sent to a replica fail immediately with a MySQL "read-only" error. The read DSN (`-dsn`) continues to serve GET requests from the replica.

**Required:** `-bucket` must be set to a `bucket_no` value that has active Jetmon workers assigned. Jetmon distributes checks by bucket; new monitors inserted into an unassigned bucket will never be checked. Check active buckets with:

```sql
SELECT DISTINCT bucket_no FROM jetpack_monitor_sites WHERE monitor_active = 1;
```

### Persistent history (`-history-path=<sqlite-file>`)

When enabled, the bridge starts a background observer that polls active rows in Jetmon's `jetpack_monitor_sites` table and writes observed status transitions to a bridge-owned SQLite database. This does not write to Jetmon's database and does not change Jetmon's checks.

Use `-history-poll-interval` to keep the observer interval shorter than Jetmon's check interval. The default is `15s`. `-history-bootstrap=true` performs one startup poll to seed current monitor state before normal polling.

---

## Error format

All errors use the same JSON shape:

```json
{"error": "<message>"}
```

---

## Endpoints

### `GET /time`

Returns the server's current UTC time. Used to calibrate clock skew between the benchmark harness and the bridge.

**Response 200:**

```json
{"time": "2026-04-24T14:00:00.123456789Z"}
```

---

### `GET /monitors?url=<url>`

Looks up an **active** monitor by its target URL.

**Parameters:**

| Name | In    | Required | Description         |
|------|-------|----------|---------------------|
| url  | query | yes      | Exact monitor URL   |

**Response 200:**

```json
{
  "blog_id":        1001,
  "monitor_url":    "https://example.com",
  "site_status":    1,
  "monitor_active": true,
  "keyword":        "",
  "redirect_policy": "follow"
}
```

| Field            | Type    | Description                                         |
|------------------|---------|-----------------------------------------------------|
| `blog_id`        | integer | Jetmon's internal site identifier                   |
| `monitor_url`    | string  | The monitored URL                                   |
| `site_status`    | integer | Current status: `1` = running, `2` = confirmed_down |
| `monitor_active` | boolean | Always `true` for this endpoint                     |
| `keyword`        | string  | Keyword check string, or empty                      |
| `redirect_policy`| string  | `follow`, `alert`, or `fail`                        |

**Response 404** — monitor not found or is inactive.

**Response 400** — `url` parameter missing.

---

### `POST /monitors`

*Write mode only.*

Creates a new monitor for the given URL, or re-activates a previously deactivated one. Idempotent: if an active monitor already exists, it is returned with status 200.

New monitors are assigned a synthetic `blog_id` in the range `[1,500,000,000, 2,000,000,000)`. The range is reserved for benchmark-created monitors, high enough to avoid typical WordPress blog IDs, and below Jetmon v1's signed 32-bit verifier limit.

**Request body (JSON):**

```json
{"url": "https://example.com"}
```

| Field | Type   | Required | Description         |
|-------|--------|----------|---------------------|
| `url` | string | yes      | URL to monitor      |

**Response 201** — monitor was created or reactivated. Body is the same shape as `GET /monitors`.

**Response 200** — monitor already existed and was active. Body is the same shape as `GET /monitors`.

For existing rows, POST resets `site_status = 1` and `last_status_change = NOW()` before returning. This gives uptime-bench write-mode runs a clean running baseline instead of inheriting a prior test's confirmed-down or transient-down state.

**Response 400** — missing or invalid request body.

**Response 405** — bridge is not running in write mode.

---

### `DELETE /monitors?url=<url>`

*Write mode only.*

Soft-deletes a monitor by setting `monitor_active = 0`, and also resets `site_status = 1` with a fresh `last_status_change`. A subsequent `GET /monitors?url=` for the same URL returns 404. A subsequent `POST /monitors` reactivates it from a clean running baseline.

**Parameters:**

| Name | In    | Required | Description         |
|------|-------|----------|---------------------|
| url  | query | yes      | Exact monitor URL   |

**Response 204** — monitor deactivated.

**Response 404** — monitor not found.

**Response 400** — `url` parameter missing.

**Response 405** — bridge is not running in write mode.

---

### `GET /events?blog_id=<id>&since=<ts>&until=<ts>`

Returns `status_transition` events for a monitor within a time window. Returns an empty array `[]` (not `null`) when no events match.

When persistent history is enabled with `-history-path`, events are returned from the bridge-owned SQLite history database. The response includes every persisted transition for the monitor in `[since, until)`, ordered by `created_at`.

When persistent history is disabled, Jetmon v1's audit-log limitation applies: events are synthesized from `jetpack_monitor_sites.last_status_change` and `site_status`. **At most one event is returned per call** — the most recent transition recorded for the monitor. If that transition's timestamp falls outside `[since, until)`, the response is `[]`. The `old_status` and `new_status` fields are inferred from the current `site_status` value (best-effort).

**History poll frequency requirement:** Because `last_status_change` is overwritten on every transition, the bridge history observer must poll Jetmon more frequently than Jetmon's check interval (default 5 minutes). A poll interval shorter than one check interval allows the bridge to persist down/recovery transitions that would otherwise be overwritten before uptime-bench retrieves events.

**Parameters:**

| Name      | In    | Required | Description                        |
|-----------|-------|----------|------------------------------------|
| `blog_id` | query | yes      | Monitor blog_id from `/monitors`   |
| `since`   | query | yes      | Window start (RFC3339, inclusive)  |
| `until`   | query | yes      | Window end (RFC3339, exclusive)    |

**Response 200:**

```json
[
  {
    "id":         0,
    "blog_id":    1001,
    "event_type": "status_transition",
    "source":     "veriflier",
    "http_code":  null,
    "old_status": 1,
    "new_status": 2,
    "detail":     null,
    "created_at": "2026-04-24T14:00:10Z"
  }
]
```

| Field        | Type            | Description                                                |
|--------------|-----------------|------------------------------------------------------------|
| `id`         | integer         | SQLite history event id when history is enabled; otherwise `0` |
| `blog_id`    | integer         | Monitor blog_id                                            |
| `event_type` | string          | Always `"status_transition"`                               |
| `source`     | string          | `"worker"` for `site_status=0`, `"veriflier"` for `2`, `"jetmon"` for recovery to `1` |
| `http_code`  | integer or null | Always `null` in v1 (not persisted to DB)                  |
| `old_status` | integer or null | Status before transition (inferred): `1` = running, `2` = confirmed_down |
| `new_status` | integer or null | Status after transition (inferred): same codes as above    |
| `detail`     | string or null  | Always `null` in v1                                        |
| `created_at` | string          | `last_status_change` timestamp (RFC3339, UTC)              |

**Status codes:** `1` = running, `2` = confirmed_down. `0` = down (unconfirmed, transient) — this appears in `new_status` when Jetmon's worker has detected a failure but the veriflier has not yet confirmed it. Uptime-bench adapters must handle `0` as a distinct value; treating it as equivalent to `2` (confirmed_down) is the conservative choice.

**Response 400** — missing or invalid parameters.

---

### `GET /healthz`

Checks that the bridge can reach the Jetmon database and, when enabled, the SQLite history database.

**Response 200:**

```json
{"status": "ok"}
```

With history enabled:

```json
{"status": "ok", "history": "ok"}
```

**Response 503:**

```json
{"status": "error", "error": "<db error message>"}
```

---

## Quick-start examples

**Check a monitor (read-only):**

```bash
curl -H "Authorization: Bearer mytoken" \
     "http://localhost:7400/monitors?url=https://example.com"
```

**Create a monitor (write mode):**

```bash
curl -X POST \
     -H "Authorization: Bearer mytoken" \
     -H "Content-Type: application/json" \
     -d '{"url":"https://example.com"}' \
     "http://localhost:7400/monitors"
```

**Deactivate a monitor (write mode):**

```bash
curl -X DELETE \
     -H "Authorization: Bearer mytoken" \
     "http://localhost:7400/monitors?url=https://example.com"
```

**Fetch events for a run window:**

```bash
curl -H "Authorization: Bearer mytoken" \
     "http://localhost:7400/events?blog_id=1001&since=2026-04-24T14:00:00Z&until=2026-04-24T15:00:00Z"
```

---

## Adapter integration notes (uptime-bench)

The `jetmon` adapter in uptime-bench wires directly to this API:

- **`Provision`** — calls `GET /monitors?url=` (read-only mode) or `POST /monitors` (write mode). If write mode is configured on the adapter but the bridge returns 405, it automatically falls back to the read-only lookup.
- **`Retrieve`** — calls `GET /events` using the `blog_id` stored in the monitor handle.
- **`Deprovision`** — calls `DELETE /monitors?url=` (write mode only); no-op in read-only mode.

**services.toml configuration:**

```toml
[[services]]
id         = "jetmon"
type       = "jetmon"
url        = "http://your-bridge-host:7400"
enabled    = true
auth       = { token = "your-bearer-token", write_mode = "false" }
```

Set `write_mode = "true"` to let uptime-bench create and clean up monitors automatically. Requires the bridge to be started with `-write -write-dsn <primary-dsn> -bucket <n> -token your-bearer-token`.

**Timing note:** Jetmon v1 workers pick up all active monitors in their bucket on every work round, with no `last_checked_at` filter. New monitors are immediately eligible after insertion. The default `check_interval` is 5 minutes; allow at least one full round (5–10 minutes) between `Provision` and the start of the test failure window to ensure the monitor has been checked at least once and Jetmon has a baseline status for it.
