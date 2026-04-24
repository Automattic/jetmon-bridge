package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
UPDATE jetpack_monitor_sites SET monitor_active = 1 WHERE monitor_url = ?`

const sqlDeactivateMonitor = `
UPDATE jetpack_monitor_sites SET monitor_active = 0 WHERE monitor_url = ?`

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
	m.Keyword = ""
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

	oldStatus, newStatus := inferStatusTransition(siteStatus)
	e := event{
		ID:        0,
		BlogID:    blogID,
		EventType: "status_transition",
		Source:    "jetmon",
		OldStatus: &oldStatus,
		NewStatus: &newStatus,
		CreatedAt: lastChange.UTC().Format(time.RFC3339),
	}
	return []event{e}, nil
}

// inferStatusTransition guesses old→new status from current site_status.
// Jetmon v1 only persists the most recent state; this is best-effort for the last recorded transition.
func inferStatusTransition(siteStatus int) (oldStatus, newStatus int) {
	switch siteStatus {
	case 1: // SITE_RUNNING — most recent transition was a recovery
		return 2, 1
	case 2: // SITE_CONFIRMED_DOWN — most recent transition was going down
		return 1, 2
	default: // SITE_DOWN (0) — transient down state, came from running
		return 1, 0
	}
}

// createMonitor upserts a monitor: inserts a new one or re-activates a deactivated one.
// Returns the monitor and true if it was newly created or reactivated; false if already active.
// bucket must match the Jetmon worker bucket that should process this monitor.
func createMonitor(ctx context.Context, db *sql.DB, monitorURL string, bucket int) (*monitor, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	m, err := lookupAnyMonitor(ctx, tx, monitorURL)
	if err != nil {
		return nil, false, err
	}

	if m != nil {
		if m.MonitorActive {
			if err := tx.Commit(); err != nil {
				return nil, false, fmt.Errorf("commit: %w", err)
			}
			return m, false, nil
		}
		if _, err := tx.ExecContext(ctx, sqlReactivateMonitor, monitorURL); err != nil {
			return nil, false, fmt.Errorf("reactivate monitor: %w", err)
		}
		m.MonitorActive = true
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit: %w", err)
		}
		return m, true, nil
	}

	// Insert with a random synthetic blog_id; retry on the rare collision.
	for range 5 {
		blogID := blogIDBase + rand.Int63n(blogIDRange)
		if _, err := tx.ExecContext(ctx, sqlInsertMonitor, blogID, bucket, monitorURL); err != nil {
			if isDuplicateKey(err) {
				continue
			}
			return nil, false, fmt.Errorf("insert monitor: %w", err)
		}
		m, err = lookupAnyMonitor(ctx, tx, monitorURL)
		if err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit: %w", err)
		}
		return m, true, nil
	}
	return nil, false, fmt.Errorf("create monitor: failed to assign unique blog_id after retries")
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

func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
