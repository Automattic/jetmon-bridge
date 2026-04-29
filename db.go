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

const sqlReactivateMonitorWithBlogID = `
UPDATE jetpack_monitor_sites
SET    blog_id = ?,
       monitor_active = 1,
       site_status = 1,
       last_status_change = NOW()
WHERE  monitor_url = ?`

const sqlDeactivateMonitor = `
UPDATE jetpack_monitor_sites
SET    monitor_active = 0,
       site_status = 1,
       last_status_change = NOW()
WHERE  monitor_url = ?`

const sqlBlogIDExists = `
SELECT 1
FROM   jetpack_monitor_sites
WHERE  blog_id = ?
LIMIT  1`

// Synthetic blog_id range for test monitors: [1.5B, 2B).
//
// The upper bound intentionally stays below signed int32 max. Jetmon v1's
// verifier stores blog_id in an int and parses JSON values with toInt(), so
// IDs above that range cannot reliably round-trip through v1's verification
// and DB update paths.
const (
	jetmonV1MaxSignedInt     = int64(1<<31 - 1)
	blogIDBase               = int64(1_500_000_000)
	blogIDRange              = int64(500_000_000)
	blogIDGenerationAttempts = 32
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

func generateSyntheticBlogID(ctx context.Context, q rowQueryer) (int64, error) {
	for attempt := 0; attempt < blogIDGenerationAttempts; attempt++ {
		blogID := blogIDBase + rand.Int63n(blogIDRange)
		if !isJetmonV1SafeBlogID(blogID) {
			return 0, fmt.Errorf("generated unsafe blog_id %d", blogID)
		}
		exists, err := blogIDExists(ctx, q, blogID)
		if err != nil {
			return 0, err
		}
		if !exists {
			return blogID, nil
		}
	}
	return 0, fmt.Errorf("generate synthetic blog_id: exhausted %d attempts", blogIDGenerationAttempts)
}

func blogIDExists(ctx context.Context, q rowQueryer, blogID int64) (bool, error) {
	var exists int
	err := q.QueryRowContext(ctx, sqlBlogIDExists, blogID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query blog_id collision: %w", err)
	}
	return true, nil
}

func isJetmonV1SafeBlogID(blogID int64) bool {
	return blogID > 0 && blogID <= jetmonV1MaxSignedInt
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
	var lastChange nullableTime
	err := db.QueryRowContext(ctx, q, blogID, since, until).Scan(&siteStatus, &lastChange)
	if errors.Is(err, sql.ErrNoRows) {
		return []event{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	if !lastChange.Valid {
		return []event{}, nil
	}

	oldStatus, newStatus, err := inferStatusTransition(siteStatus)
	if err != nil {
		// Unexpected site_status in the DB — log and treat as no event rather than 500.
		log.Printf("lookupEvents blog_id=%d: %v, skipping synthetic event", blogID, err)
		return []event{}, nil
	}

	source, err := sourceForStatus(siteStatus)
	if err != nil {
		log.Printf("lookupEvents blog_id=%d: %v, skipping synthetic event", blogID, err)
		return []event{}, nil
	}

	e := event{
		ID:        0,
		BlogID:    blogID,
		EventType: "status_transition",
		Source:    source,
		OldStatus: &oldStatus,
		NewStatus: &newStatus,
		CreatedAt: formatAPITime(lastChange.Time),
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

// sourceForStatus reflects how the transition was detected:
// worker = initial down detection (unconfirmed), veriflier = confirmed down, jetmon = recovery.
func sourceForStatus(siteStatus int) (string, error) {
	switch siteStatus {
	case 0:
		return "worker", nil
	case 1:
		return "jetmon", nil
	case 2:
		return "veriflier", nil
	default:
		return "", fmt.Errorf("unexpected site_status %d", siteStatus)
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
		createdOrReactivated := !m.MonitorActive
		if !isJetmonV1SafeBlogID(m.BlogID) {
			blogID, err := generateSyntheticBlogID(ctx, tx)
			if err != nil {
				return nil, false, err
			}
			if _, err := tx.ExecContext(ctx, sqlReactivateMonitorWithBlogID, blogID, monitorURL); err != nil {
				return nil, false, fmt.Errorf("reactivate monitor with safe blog_id: %w", err)
			}
			m.BlogID = blogID
			createdOrReactivated = true
		} else {
			if _, err := tx.ExecContext(ctx, sqlReactivateMonitor, monitorURL); err != nil {
				return nil, false, fmt.Errorf("reactivate monitor: %w", err)
			}
		}
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

	// Assign a random synthetic blog_id. v1 has no UNIQUE on blog_id, so check
	// for an existing row before inserting to avoid silent duplicate IDs.
	blogID, err := generateSyntheticBlogID(ctx, tx)
	if err != nil {
		return nil, false, err
	}
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
