package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	URL            string `json:"url"`
	Status         string `json:"status"`
	Keyword        string `json:"keyword"`
	RedirectPolicy string `json:"redirect_policy"`
}

// event is a single entry in the /events response.
type event struct {
	BlogID     int64     `json:"blog_id"`
	EventType  string    `json:"event_type"`
	OldStatus  string    `json:"old_status"`
	NewStatus  string    `json:"new_status"`
	ErrorCode  int       `json:"error_code"`
	HTTPCode   int       `json:"http_code"`
	Source     string    `json:"source"`
	RTTMS      int       `json:"rtt_ms"`
	DNSMS      *int      `json:"dns_ms"`
	TCPMS      *int      `json:"tcp_ms"`
	TLSMS      *int      `json:"tls_ms"`
	TTFBMS     *int      `json:"ttfb_ms"`
	OccurredAt time.Time `json:"occurred_at"`
}

const sqlMonitor = `
SELECT blog_id, monitor_url, site_status,
       COALESCE(check_keyword, '')      AS check_keyword,
       COALESCE(redirect_policy, 'follow') AS redirect_policy
FROM   jetpack_monitor_sites
WHERE  monitor_url = ?
LIMIT  1`

// GROUP BY jal.id collapses duplicate rows when more than one check_history
// record falls within the ±1 s match window for the same audit event.
const sqlEvents = `
SELECT
    jal.blog_id,
    jal.event_type,
    COALESCE(jal.old_status, 0)  AS old_status,
    COALESCE(jal.new_status, 0)  AS new_status,
    COALESCE(jal.error_code, 0)  AS error_code,
    COALESCE(jal.http_code, 0)   AS http_code,
    jal.source,
    COALESCE(jal.rtt_ms, 0)      AS rtt_ms,
    jal.created_at,
    MAX(jch.dns_ms)  AS dns_ms,
    MAX(jch.tcp_ms)  AS tcp_ms,
    MAX(jch.tls_ms)  AS tls_ms,
    MAX(jch.ttfb_ms) AS ttfb_ms
FROM jetmon_audit_log jal
LEFT JOIN jetmon_check_history jch
    ON  jch.blog_id    = jal.blog_id
    AND jch.checked_at >= DATE_SUB(jal.created_at, INTERVAL 1 SECOND)
    AND jch.checked_at <= DATE_ADD(jal.created_at, INTERVAL 1 SECOND)
WHERE jal.blog_id    = ?
  AND jal.event_type = 'status_transition'
  AND jal.created_at >= ?
  AND jal.created_at  < ?
GROUP BY jal.id
ORDER BY jal.created_at ASC`

func lookupMonitor(ctx context.Context, db *sql.DB, url string) (*monitor, error) {
	var m monitor
	var siteStatus int
	err := db.QueryRowContext(ctx, sqlMonitor, url).Scan(
		&m.BlogID, &m.URL, &siteStatus, &m.Keyword, &m.RedirectPolicy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query monitor: %w", err)
	}
	m.Status = siteStatusString(siteStatus)
	return &m, nil
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
		var oldStatus, newStatus int
		var dnsMS, tcpMS, tlsMS, ttfbMS sql.NullInt32
		if err := rows.Scan(
			&e.BlogID, &e.EventType, &oldStatus, &newStatus,
			&e.ErrorCode, &e.HTTPCode, &e.Source, &e.RTTMS, &e.OccurredAt,
			&dnsMS, &tcpMS, &tlsMS, &ttfbMS,
		); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.OldStatus = siteStatusString(oldStatus)
		e.NewStatus = siteStatusString(newStatus)
		if dnsMS.Valid {
			v := int(dnsMS.Int32)
			e.DNSMS = &v
		}
		if tcpMS.Valid {
			v := int(tcpMS.Int32)
			e.TCPMS = &v
		}
		if tlsMS.Valid {
			v := int(tlsMS.Int32)
			e.TLSMS = &v
		}
		if ttfbMS.Valid {
			v := int(ttfbMS.Int32)
			e.TTFBMS = &v
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return events, nil
}

func siteStatusString(s int) string {
	switch s {
	case 1:
		return "running"
	case 2:
		return "confirmed_down"
	default:
		return "unknown"
	}
}
