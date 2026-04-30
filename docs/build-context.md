# Build Context

This document keeps the Jetmon v1 facts that matter when modifying jetmon-bridge.

## Purpose

jetmon-bridge lets uptime-bench retrieve Jetmon 1 monitor state without changing Jetmon 1 code. The bridge is intentionally small: it exposes the data uptime-bench needs and leaves checks, verification, alerts, and production status updates to Jetmon.

## Jetmon v1 Schema

The bridge targets Jetmon v1's `jetpack_monitor_sites` table:

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

Status values:

| Value | Meaning |
|---|---|
| `0` | Initial down detection, unconfirmed |
| `1` | Running |
| `2` | Confirmed down |

Do not add v2 assumptions to v1 queries. The v1 table does not include keyword checks, redirect policies, maintenance windows, check history, or audit-log tables.

## Adapter Contract

The uptime-bench `jetmon-v1` adapter uses:

- `GET /time` to calibrate clock offset.
- `GET /monitors?url=` to map a target URL to `blog_id` in read-only mode.
- `POST /monitors` in write mode when benchmark runs should create monitors.
- `GET /events` to retrieve status-transition events for a run window.
- `DELETE /monitors?url=` in write mode to deactivate benchmark-created monitors.

## Event Limits

Without persistent history, v1 can only expose one synthetic event per monitor: the current status and its latest `last_status_change`. With persistent history enabled, the bridge records observed transitions into its own SQLite database and can return multiple events for a window.

## Write Mode Notes

Synthetic `blog_id` values are generated in `[1,500,000,000, 2,000,000,000)` so benchmark-created monitors avoid typical WordPress blog IDs while staying under Jetmon v1's signed 32-bit verifier path.

Write mode must use the primary DSN. The read DSN remains the source for GET endpoints.
