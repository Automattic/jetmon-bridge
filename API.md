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

Enables `POST /monitors` and `DELETE /monitors`. The bridge will create new Jetmon monitor records and soft-delete them. Requires a DSN with write access to `jetpack_monitor_sites`.

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

Creates a new monitor for the given URL, or re-activates a previously deactivated one. Idempotent: if an active monitor already exists, it is returned unchanged.

New monitors are assigned a synthetic `blog_id` in the range `[2^62, 2^62 + 2^30)` to avoid colliding with real WordPress blog IDs.

**Request body (JSON):**

```json
{"url": "https://example.com"}
```

| Field | Type   | Required | Description         |
|-------|--------|----------|---------------------|
| `url` | string | yes      | URL to monitor      |

**Response 201** — monitor was created or reactivated. Body is the same shape as `GET /monitors`.

**Response 200** — monitor already existed and was active. Body is the same shape as `GET /monitors`.

**Response 400** — missing or invalid request body.

**Response 405** — bridge is not running in write mode.

---

### `DELETE /monitors?url=<url>`

*Write mode only.*

Soft-deletes a monitor by setting `monitor_active = 0`. A subsequent `GET /monitors?url=` for the same URL returns 404. A subsequent `POST /monitors` reactivates it.

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

Returns all `status_transition` audit log events for a monitor within a time window. Returns an empty array `[]` (not `null`) when no events match.

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
    "id":         42,
    "blog_id":    1001,
    "event_type": "status_transition",
    "source":     "worker-01",
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
| `id`         | integer         | Audit log row ID                                           |
| `blog_id`    | integer         | Monitor blog_id                                            |
| `event_type` | string          | Always `"status_transition"` for this endpoint             |
| `source`     | string          | Jetmon worker or verifier that recorded the event          |
| `http_code`  | integer or null | HTTP status code at time of check, if available            |
| `old_status` | integer or null | Status before transition: `1` = running, `2` = confirmed_down |
| `new_status` | integer or null | Status after transition: same codes as above               |
| `detail`     | string or null  | Optional diagnostic message                                |
| `created_at` | string          | Event timestamp (RFC3339, UTC)                             |

**Status codes:** `1` = running/up, `2` = confirmed_down.

**Response 400** — missing or invalid parameters.

---

### `GET /healthz`

Checks that the bridge can reach the database.

**Response 200:**

```json
{"status": "ok"}
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

Set `write_mode = "true"` to let uptime-bench create and clean up monitors automatically. Requires the bridge to be started with `-write -token your-bearer-token`.
