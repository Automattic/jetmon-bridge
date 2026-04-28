package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/go-sql-driver/mysql"
)

// openDB opens a connection pool to the MySQL replica.
// ParseTime is forced on so TIMESTAMP columns scan as time.Time.
func openDB(dsn string) (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.ParseTime = true

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return db, nil
}

// monitor is the /monitors response shape.
type monitor struct {
	BlogID         int64  `json:"blog_id"`
	MonitorURL     string `json:"monitor_url"`
	SiteStatus     int    `json:"site_status"`
	MonitorActive  bool   `json:"monitor_active"`
	Keyword        string `json:"keyword"`
	RedirectPolicy string `json:"redirect_policy"`
}

// event is a single entry in the /events response.
type event struct {
	ID        int64   `json:"id"`
	BlogID    int64   `json:"blog_id"`
	EventType string  `json:"event_type"`
	Source    string  `json:"source"`
	HTTPCode  *int    `json:"http_code"`
	OldStatus *int    `json:"old_status"`
	NewStatus *int    `json:"new_status"`
	Detail    *string `json:"detail"`
	CreatedAt string  `json:"created_at"`
}

// rowQueryer is satisfied by *sql.DB and *sql.Tx, allowing lookups within transactions.
type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const sqlMonitor = `
SELECT blog_id, monitor_url, site_status, monitor_active
FROM   jetpack_monitor_sites
WHERE  monitor_url = ?
  AND  monitor_active = 1
LIMIT  1`

// sqlMonitorAny finds any monitor regardless of active status, preferring active rows.
// Used in write-mode create/reactivate flows to detect duplicates.
const sqlMonitorAny = `
SELECT blog_id, monitor_url, site_status, monitor_active
FROM   jetpack_monitor_sites
WHERE  monitor_url = ?
ORDER BY monitor_active DESC
LIMIT  1`

// sqlInsertMonitor inserts a new monitor into the v1 jetpack_monitor_sites table.
// check_interval=5 matches the v1 default (5-minute check cycle).
const sqlInsertMonitor = `
INSERT INTO jetpack_monitor_sites
    (blog_id, bucket_no, monitor_url, monitor_active, site_status, check_interval)
VALUES (?, ?, ?, 1, 1, 5)`

const sqlReactivateMonitor = `
UPDATE jetpack_monitor_sites
SET    monitor_active = 1,
       site_status = 1,
       last_status_change = NOW()
WHERE  monitor_url = ?`

const sqlDeactivateMonitor = `
UPDATE jetpack_monitor_sites
SET    monitor_active = 0,
       site_status = 1,
       last_status_change = NOW()
WHERE  monitor_url = ?`

// Synthetic blog_id range for test monitors: [2^62, 2^62+2^30).
// Keeps test IDs well clear of real WordPress blog_ids.
const (
	blogIDBase  = int64(1) << 62
	blogIDRange = int64(1) << 30
)

func scanMonitorRow(row *sql.Row) (*monitor, error) {
	var m monitor
	var active int8
	err := row.Scan(&m.BlogID, &m.MonitorURL, &m.SiteStatus, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan monitor: %w", err)
	}
	m.MonitorActive = active != 0
	m.RedirectPolicy = "follow"
	return &m, nil
}

// lookupMonitor returns the active monitor for the URL, or nil if not found or inactive.
func lookupMonitor(ctx context.Context, q rowQueryer, url string) (*monitor, error) {
	m, err := scanMonitorRow(q.QueryRowContext(ctx, sqlMonitor, url))
	if err != nil {
		return nil, fmt.Errorf("query monitor: %w", err)
	}
	return m, nil
}

// lookupAnyMonitor returns any monitor for the URL regardless of active status.
func lookupAnyMonitor(ctx context.Context, q rowQueryer, url string) (*monitor, error) {
	m, err := scanMonitorRow(q.QueryRowContext(ctx, sqlMonitorAny, url))
	if err != nil {
		return nil, fmt.Errorf("query monitor: %w", err)
	}
	return m, nil
}

// lookupEvents returns status_transition events for a monitor within the given window.
// Jetmon v1 does not have an audit log table; events are synthesized from the monitor's
// current site_status and last_status_change. At most one event is returned per call.
func lookupEvents(ctx context.Context, db *sql.DB, blogID int64, since, until time.Time) ([]event, error) {
	const q = `
SELECT site_status, last_status_change
FROM   jetpack_monitor_sites
WHERE  blog_id            = ?
  AND  last_status_change >= ?
  AND  last_status_change  < ?
LIMIT  1`

	var siteStatus int
	var lastChange time.Time
	err := db.QueryRowContext(ctx, q, blogID, since, until).Scan(&siteStatus, &lastChange)
	if errors.Is(err, sql.ErrNoRows) {
		return []event{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}

	oldStatus, newStatus, err := inferStatusTransition(siteStatus)
	if err != nil {
		// Unexpected site_status in the DB — log and treat as no event rather than 500.
		log.Printf("lookupEvents blog_id=%d: %v, skipping synthetic event", blogID, err)
		return []event{}, nil
	}

	// Source reflects how the transition was detected:
	// worker = initial down detection (unconfirmed), veriflier = confirmed down, jetmon = recovery.
	source := "jetmon"
	switch siteStatus {
	case 0:
		source = "worker"
	case 2:
		source = "veriflier"
	}

	e := event{
		ID:        0,
		BlogID:    blogID,
		EventType: "status_transition",
		Source:    source,
		OldStatus: &oldStatus,
		NewStatus: &newStatus,
		CreatedAt: lastChange.UTC().Format(time.RFC3339),
	}
	return []event{e}, nil
}

// inferStatusTransition derives old→new status from the current site_status recorded in v1.
// Jetmon v1 only persists the most recent state; this is best-effort for the last transition.
func inferStatusTransition(siteStatus int) (oldStatus, newStatus int, err error) {
	switch siteStatus {
	case 1: // SITE_RUNNING — most recent transition was a recovery from confirmed_down
		return 2, 1, nil
	case 2: // SITE_CONFIRMED_DOWN — most recent transition was going down from running
		return 1, 2, nil
	case 0: // SITE_DOWN — transient unconfirmed down, came from running
		return 1, 0, nil
	default:
		return 0, 0, fmt.Errorf("unexpected site_status %d", siteStatus)
	}
}

// createMonitor upserts a monitor: inserts a new one or re-activates a deactivated one.
// Returns the monitor and true if it was newly created or reactivated; false if already active.
// bucket must match the Jetmon worker bucket that should process this monitor.
func createMonitor(ctx context.Context, db *sql.DB, monitorURL string, bucket int) (*monitor, bool, error) {
	// Pin to a single connection so the advisory lock and the transaction share the same
	// MySQL session. Advisory locks in MySQL are connection-scoped.
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("get connection: %w", err)
	}
	defer conn.Close()

	// Per-URL advisory lock serializes concurrent POSTs for the same URL without
	// locking unrelated rows. v1's composite index (blog_id, monitor_url) can't be
	// used for a WHERE monitor_url=? lookup, so FOR UPDATE would scan the whole table;
	// GET_LOCK avoids that. SHA2 of the prefixed URL keeps the lock name ≤ 64 chars.
	var lockAcquired int
	if err := conn.QueryRowContext(ctx,
		"SELECT GET_LOCK(SHA2(CONCAT('jetmon-bridge:', ?), 256), 5)",
		monitorURL,
	).Scan(&lockAcquired); err != nil {
		return nil, false, fmt.Errorf("acquire advisory lock: %w", err)
	}
	if lockAcquired != 1 {
		return nil, false, fmt.Errorf("acquire advisory lock: timed out waiting for URL lock")
	}
	defer conn.ExecContext(context.Background(), //nolint:errcheck
		"SELECT RELEASE_LOCK(SHA2(CONCAT('jetmon-bridge:', ?), 256))", monitorURL)

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	m, err := lookupAnyMonitor(ctx, tx, monitorURL)
	if err != nil {
		return nil, false, err
	}

	if m != nil {
		if _, err := tx.ExecContext(ctx, sqlReactivateMonitor, monitorURL); err != nil {
			return nil, false, fmt.Errorf("reactivate monitor: %w", err)
		}
		createdOrReactivated := !m.MonitorActive
		m.MonitorActive = true
		m.SiteStatus = 1
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit: %w", err)
		}
		if !createdOrReactivated {
			return m, false, nil
		}
		return m, true, nil
	}

	// Assign a random synthetic blog_id. The range [2^62, 2^62+2^30) makes collision
	// probability ~1/2^30 per insert; v1 has no UNIQUE on blog_id so a collision would
	// silently insert a duplicate, but the probability is negligible in practice.
	blogID := blogIDBase + rand.Int63n(blogIDRange)
	if _, err := tx.ExecContext(ctx, sqlInsertMonitor, blogID, bucket, monitorURL); err != nil {
		return nil, false, fmt.Errorf("insert monitor: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit: %w", err)
	}
	// All field values are known from the INSERT literals — no round-trip needed.
	return &monitor{
		BlogID:         blogID,
		MonitorURL:     monitorURL,
		SiteStatus:     1,
		MonitorActive:  true,
		Keyword:        "",
		RedirectPolicy: "follow",
	}, true, nil
}

// deactivateMonitor soft-deletes a monitor by setting monitor_active=0.
// Returns false if no matching monitor was found.
func deactivateMonitor(ctx context.Context, db *sql.DB, monitorURL string) (bool, error) {
	res, err := db.ExecContext(ctx, sqlDeactivateMonitor, monitorURL)
	if err != nil {
		return false, fmt.Errorf("deactivate monitor: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("deactivate monitor rows affected: %w", err)
	}
	return n > 0, nil
}
