-- Local test schema for jetmon-bridge.
-- Matches the Jetmon v1 production schema exactly (from jetmon repo, v1 branch README).
-- Used only for local development with `make up-local`.

CREATE TABLE IF NOT EXISTS jetpack_monitor_sites (
    jetpack_monitor_site_id  BIGINT(20) UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id                  BIGINT(20) UNSIGNED NOT NULL,
    bucket_no                SMALLINT(2) UNSIGNED NOT NULL,
    monitor_url              VARCHAR(300) NOT NULL,
    monitor_active           TINYINT(1) UNSIGNED NOT NULL DEFAULT 1,
    site_status              TINYINT(1) UNSIGNED NOT NULL DEFAULT 1,
    last_status_change       TIMESTAMP NULL DEFAULT CURRENT_TIMESTAMP,
    check_interval           TINYINT(1) UNSIGNED NOT NULL DEFAULT 5,
    INDEX idx_blog_id_monitor_url (blog_id, monitor_url),
    INDEX idx_bucket_no_monitor_active_check_interval (bucket_no, monitor_active, check_interval)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Seed: two benchmark target sites.
-- Site 1001: currently confirmed_down with last_status_change within a known test window.
-- Site 1002: currently running, no recent transition.
INSERT INTO jetpack_monitor_sites
    (blog_id, bucket_no, monitor_url, monitor_active, site_status, check_interval, last_status_change)
VALUES
    (1001, 0, 'https://bench-target-01.example.com', 1, 2, 5, '2026-04-22 14:00:10'),
    (1002, 0, 'https://bench-target-02.example.com', 1, 1, 5, '2026-04-22 12:00:00');
