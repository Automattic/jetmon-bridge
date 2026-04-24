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
SELECT blog_id, monitor_url, site_status, monitor_active,
       COALESCE(check_keyword, '')         AS keyword,
       COALESCE(redirect_policy, 'follow') AS redirect_policy
FROM   jetpack_monitor_sites
WHERE  monitor_url = ?
  AND  monitor_active = 1
LIMIT  1`

// sqlMonitorAny finds any monitor regardless of active status, preferring active rows.
// Used in write-mode create/reactivate flows to detect duplicates.
const sqlMonitorAny = `
SELECT blog_id, monitor_url, site_status, monitor_active,
       COALESCE(check_keyword, '')         AS keyword,
       COALESCE(redirect_policy, 'follow') AS redirect_policy
FROM   jetpack_monitor_sites
WHERE  monitor_url = ?
ORDER BY monitor_active DESC
LIMIT  1`

const sqlEvents = `
SELECT
    jal.id,
    jal.blog_id,
    jal.event_type,
    jal.source,
    jal.http_code,
    jal.old_status,
    jal.new_status,
    jal.detail,
    jal.created_at
FROM jetmon_audit_log jal
WHERE jal.blog_id    = ?
  AND jal.event_type = 'status_transition'
  AND jal.created_at >= ?
  AND jal.created_at  < ?
ORDER BY jal.created_at ASC`

const sqlInsertMonitor = `
INSERT INTO jetpack_monitor_sites
    (blog_id, bucket_no, monitor_url, monitor_active, site_status, check_interval, redirect_policy)
VALUES (?, 0, ?, 1, 1, 1, 'follow')`

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
	err := row.Scan(&m.BlogID, &m.MonitorURL, &m.SiteStatus, &active, &m.Keyword, &m.RedirectPolicy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan monitor: %w", err)
	}
	m.MonitorActive = active != 0
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

func lookupEvents(ctx context.Context, db *sql.DB, blogID int64, since, until time.Time) ([]event, error) {
	rows, err := db.QueryContext(ctx, sqlEvents, blogID, since, until)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	events := make([]event, 0)
	for rows.Next() {
		var e event
		var httpCode, oldStatus, newStatus sql.NullInt32
		var detail sql.NullString
		var createdAt time.Time
		if err := rows.Scan(
			&e.ID, &e.BlogID, &e.EventType, &e.Source,
			&httpCode, &oldStatus, &newStatus, &detail, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if httpCode.Valid {
			v := int(httpCode.Int32)
			e.HTTPCode = &v
		}
		if oldStatus.Valid {
			v := int(oldStatus.Int32)
			e.OldStatus = &v
		}
		if newStatus.Valid {
			v := int(newStatus.Int32)
			e.NewStatus = &v
		}
		if detail.Valid {
			e.Detail = &detail.String
		}
		e.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return events, nil
}

// createMonitor upserts a monitor: inserts a new one or re-activates a deactivated one.
// Returns the monitor and true if it was newly created or reactivated; false if already active.
func createMonitor(ctx context.Context, db *sql.DB, monitorURL string) (*monitor, bool, error) {
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
		if _, err := tx.ExecContext(ctx, sqlInsertMonitor, blogID, monitorURL); err != nil {
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
