-- Local test schema for jetmon-bridge.
-- Creates a minimal Jetmon database with seed data for manual and integration testing.
-- This schema is NOT the production Jetpack schema; it exists only for local development.

CREATE TABLE IF NOT EXISTS jetpack_monitor_sites (
    jetpack_monitor_site_id  BIGINT UNSIGNED   NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id                  BIGINT UNSIGNED   NOT NULL,
    bucket_no                INT               NOT NULL DEFAULT 0,
    monitor_url              VARCHAR(255)      NOT NULL,
    monitor_active           TINYINT           NOT NULL DEFAULT 1,
    site_status              TINYINT           NOT NULL DEFAULT 1,  -- 1=running, 2=confirmed_down
    last_status_change       DATETIME          NULL,
    check_interval           INT               NOT NULL DEFAULT 1,
    ssl_expiry_date          DATE              NULL,
    check_keyword            VARCHAR(500)      NULL,
    maintenance_start        DATETIME          NULL,
    maintenance_end          DATETIME          NULL,
    custom_headers           JSON              NULL,
    timeout_seconds          TINYINT UNSIGNED  NULL,
    redirect_policy          ENUM('follow','alert','fail') NULL DEFAULT 'follow',
    alert_cooldown_minutes   SMALLINT UNSIGNED NULL,
    last_checked_at          DATETIME          NULL,
    last_alert_sent_at       DATETIME          NULL,
    UNIQUE KEY idx_blog_id   (blog_id),
    INDEX idx_monitor_url    (monitor_url),
    INDEX idx_bucket_monitor_last_checked (bucket_no, monitor_active, last_checked_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetmon_audit_log (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id      BIGINT UNSIGNED NOT NULL,
    event_type   VARCHAR(64)     NOT NULL,
    source       VARCHAR(255)    NOT NULL DEFAULT 'local',
    http_code    SMALLINT        NULL,
    error_code   TINYINT         NULL,
    rtt_ms       INT             NULL,
    old_status   TINYINT         NULL,
    new_status   TINYINT         NULL,
    detail       TEXT            NULL,
    created_at   TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_blog_id_created (blog_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetmon_check_history (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id    BIGINT UNSIGNED NOT NULL,
    http_code  SMALLINT        NULL,
    error_code TINYINT         NULL,
    rtt_ms     INT             NULL,
    dns_ms     INT             NULL,
    tcp_ms     INT             NULL,
    tls_ms     INT             NULL,
    ttfb_ms    INT             NULL,
    checked_at TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_blog_id_checked (blog_id, checked_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Seed: two benchmark target sites
INSERT INTO jetpack_monitor_sites
    (blog_id, bucket_no, monitor_url, monitor_active, site_status, check_interval, redirect_policy)
VALUES
    (1001, 0, 'https://bench-target-01.example.com', 1, 1, 1, 'follow'),
    (1002, 0, 'https://bench-target-02.example.com', 1, 1, 1, 'follow');

-- Seed: audit log — site 1001 goes down then recovers
INSERT INTO jetmon_audit_log
    (blog_id, event_type, source, http_code, error_code, rtt_ms, old_status, new_status, created_at)
VALUES
    (1001, 'check',             'worker-01', 0,   1, 250,  NULL, NULL, '2026-04-22 14:00:00'),
    (1001, 'retry_dispatched',  'worker-01', 0,   1, NULL, NULL, NULL, '2026-04-22 14:00:05'),
    (1001, 'veriflier_sent',    'worker-01', 0,   1, NULL, NULL, NULL, '2026-04-22 14:00:06'),
    (1001, 'veriflier_result',  'veriflier-01', 0, 1, 300, NULL, NULL, '2026-04-22 14:00:08'),
    (1001, 'veriflier_result',  'veriflier-02', 0, 1, 280, NULL, NULL, '2026-04-22 14:00:09'),
    (1001, 'status_transition', 'worker-01', 0,   1, 250,  1,    2,    '2026-04-22 14:00:10'),
    (1001, 'status_transition', 'worker-01', 200, 0, 180,  2,    1,    '2026-04-22 14:05:30');

-- Seed: check history — timing data for the transition events
INSERT INTO jetmon_check_history
    (blog_id, http_code, error_code, rtt_ms, dns_ms, tcp_ms, tls_ms, ttfb_ms, checked_at)
VALUES
    (1001, 0,   1, 250, 12, 38, NULL, NULL, '2026-04-22 14:00:10'),
    (1001, 200, 0, 180,  8, 22, 45,   92,   '2026-04-22 14:05:30');
