package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultHistoryPollInterval = 15 * time.Second
	historyTimeLayout          = "2006-01-02T15:04:05.000000000Z"
	observedLookupBatchSize    = 500
)

const sqlHistoryActiveMonitors = `
SELECT blog_id, monitor_url, site_status, last_status_change
FROM   jetpack_monitor_sites
WHERE  monitor_active = 1`

type historyStore struct {
	db *sql.DB
}

type monitorSnapshot struct {
	blogID           int64
	monitorURL       string
	siteStatus       int
	lastStatusChange nullableTime
}

type observedMonitor struct {
	lastSiteStatus   int
	lastStatusChange nullableTime
}

type nullableTime struct {
	Time  time.Time
	Valid bool
}

func (nt *nullableTime) Scan(value any) error {
	if value == nil {
		nt.Time = time.Time{}
		nt.Valid = false
		return nil
	}

	var t time.Time
	var err error
	switch v := value.(type) {
	case time.Time:
		t = v
	case string:
		t, err = parseDBTime(v)
	case []byte:
		t, err = parseDBTime(string(v))
	default:
		return fmt.Errorf("unsupported time value %T", value)
	}
	if err != nil {
		return err
	}

	nt.Time = t.UTC()
	nt.Valid = true
	return nil
}

func openHistoryStore(path string) (*historyStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open history db: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set history busy timeout: %w", err)
	}
	if path != ":memory:" {
		if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
			db.Close()
			return nil, fmt.Errorf("set history journal mode: %w", err)
		}
	}

	if err := initHistorySchema(db); err != nil {
		db.Close()
		return nil, err
	}

	return &historyStore{db: db}, nil
}

func initHistorySchema(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS observed_monitors (
			blog_id            BIGINT PRIMARY KEY,
			monitor_url        TEXT NOT NULL,
			last_site_status   INTEGER NOT NULL,
			last_status_change DATETIME NULL,
			last_polled_at     DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS bridge_events (
			id                 INTEGER PRIMARY KEY AUTOINCREMENT,
			blog_id            BIGINT NOT NULL,
			monitor_url        TEXT NOT NULL,
			event_type         TEXT NOT NULL DEFAULT 'status_transition',
			old_status         INTEGER NULL,
			new_status         INTEGER NOT NULL,
			source             TEXT NOT NULL,
			created_at         DATETIME NOT NULL,
			observed_at        DATETIME NOT NULL,
			UNIQUE (blog_id, new_status, created_at)
		)`,
		`CREATE INDEX IF NOT EXISTS bridge_events_lookup
			ON bridge_events (blog_id, created_at)`,
	}

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("init history schema: %w", err)
		}
	}
	return nil
}

func (h *historyStore) Close() error {
	return h.db.Close()
}

func (h *historyStore) PingContext(ctx context.Context) error {
	return h.db.PingContext(ctx)
}

func (h *historyStore) poll(ctx context.Context, jetmonDB *sql.DB, observedAt time.Time) error {
	rows, err := jetmonDB.QueryContext(ctx, sqlHistoryActiveMonitors)
	if err != nil {
		return fmt.Errorf("query active monitors: %w", err)
	}
	defer rows.Close()

	var snapshots []monitorSnapshot
	for rows.Next() {
		var snapshot monitorSnapshot
		if err := rows.Scan(
			&snapshot.blogID,
			&snapshot.monitorURL,
			&snapshot.siteStatus,
			&snapshot.lastStatusChange,
		); err != nil {
			return fmt.Errorf("scan active monitor: %w", err)
		}

		if _, err := sourceForStatus(snapshot.siteStatus); err != nil {
			log.Printf("history poll blog_id=%d url=%q: %v, skipping row", snapshot.blogID, snapshot.monitorURL, err)
			continue
		}

		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate active monitors: %w", err)
	}
	if len(snapshots) == 0 {
		return nil
	}

	observed, err := h.lookupObservedMonitors(ctx, snapshots)
	if err != nil {
		return err
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin history tx: %w", err)
	}
	defer tx.Rollback()

	for _, snapshot := range snapshots {
		prior, found := observed[snapshot.blogID]
		if err := recordMonitorSnapshot(ctx, tx, snapshot, prior, found, observedAt.UTC()); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit history tx: %w", err)
	}
	return nil
}

func recordMonitorSnapshot(ctx context.Context, tx *sql.Tx, snapshot monitorSnapshot, observed observedMonitor, found bool, observedAt time.Time) error {
	if found && snapshotChanged(observed, snapshot) {
		createdAt := observedAt
		if snapshot.lastStatusChange.Valid {
			createdAt = snapshot.lastStatusChange.Time
		}
		source, err := sourceForStatus(snapshot.siteStatus)
		if err != nil {
			return err
		}
		if err := insertBridgeEvent(ctx, tx, snapshot, observed.lastSiteStatus, source, createdAt, observedAt); err != nil {
			return err
		}
	}

	if !found || snapshotChanged(observed, snapshot) {
		if err := upsertObservedMonitor(ctx, tx, snapshot, observedAt); err != nil {
			return err
		}
	}
	return nil
}

func (h *historyStore) lookupObservedMonitors(ctx context.Context, snapshots []monitorSnapshot) (map[int64]observedMonitor, error) {
	observed := make(map[int64]observedMonitor, len(snapshots))
	for start := 0; start < len(snapshots); start += observedLookupBatchSize {
		end := start + observedLookupBatchSize
		if end > len(snapshots) {
			end = len(snapshots)
		}
		placeholders := make([]string, 0, end-start)
		args := make([]any, 0, end-start)
		for _, snapshot := range snapshots[start:end] {
			placeholders = append(placeholders, "?")
			args = append(args, snapshot.blogID)
		}
		q := `
SELECT blog_id, last_site_status, last_status_change
FROM   observed_monitors
WHERE  blog_id IN (` + strings.Join(placeholders, ",") + `)`

		rows, err := h.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("lookup observed monitors: %w", err)
		}
		for rows.Next() {
			var blogID int64
			var prior observedMonitor
			if err := rows.Scan(&blogID, &prior.lastSiteStatus, &prior.lastStatusChange); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan observed monitor: %w", err)
			}
			observed[blogID] = prior
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate observed monitors: %w", err)
		}
		rows.Close()
	}
	return observed, nil
}

func snapshotChanged(observed observedMonitor, snapshot monitorSnapshot) bool {
	return observed.lastSiteStatus != snapshot.siteStatus ||
		!sameNullableTime(observed.lastStatusChange, snapshot.lastStatusChange)
}

func insertBridgeEvent(ctx context.Context, tx *sql.Tx, snapshot monitorSnapshot, oldStatus int, source string, createdAt, observedAt time.Time) error {
	const q = `
INSERT OR IGNORE INTO bridge_events
    (blog_id, monitor_url, event_type, old_status, new_status, source, created_at, observed_at)
VALUES (?, ?, 'status_transition', ?, ?, ?, ?, ?)`

	if _, err := tx.ExecContext(
		ctx,
		q,
		snapshot.blogID,
		snapshot.monitorURL,
		oldStatus,
		snapshot.siteStatus,
		source,
		formatHistoryTime(createdAt),
		formatHistoryTime(observedAt),
	); err != nil {
		return fmt.Errorf("insert bridge event: %w", err)
	}
	return nil
}

func upsertObservedMonitor(ctx context.Context, tx *sql.Tx, snapshot monitorSnapshot, observedAt time.Time) error {
	const q = `
INSERT INTO observed_monitors
    (blog_id, monitor_url, last_site_status, last_status_change, last_polled_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(blog_id) DO UPDATE SET
    monitor_url        = excluded.monitor_url,
    last_site_status   = excluded.last_site_status,
    last_status_change = excluded.last_status_change,
    last_polled_at     = excluded.last_polled_at`

	if _, err := tx.ExecContext(
		ctx,
		q,
		snapshot.blogID,
		snapshot.monitorURL,
		snapshot.siteStatus,
		nullableHistoryTimeValue(snapshot.lastStatusChange),
		formatHistoryTime(observedAt),
	); err != nil {
		return fmt.Errorf("upsert observed monitor: %w", err)
	}
	return nil
}

func (h *historyStore) lookupEvents(ctx context.Context, blogID int64, since, until time.Time) ([]event, error) {
	const q = `
SELECT id, blog_id, event_type, source, old_status, new_status, created_at
FROM   bridge_events
WHERE  blog_id     = ?
  AND  created_at >= ?
  AND  created_at  < ?
ORDER BY created_at ASC, id ASC`

	rows, err := h.db.QueryContext(ctx, q, blogID, formatHistoryTime(since), formatHistoryTime(until))
	if err != nil {
		return nil, fmt.Errorf("query history events: %w", err)
	}
	defer rows.Close()

	events := []event{}
	for rows.Next() {
		var e event
		var oldStatus sql.NullInt64
		var newStatus int
		var createdAtRaw string
		if err := rows.Scan(
			&e.ID,
			&e.BlogID,
			&e.EventType,
			&e.Source,
			&oldStatus,
			&newStatus,
			&createdAtRaw,
		); err != nil {
			return nil, fmt.Errorf("scan history event: %w", err)
		}

		if oldStatus.Valid {
			old := int(oldStatus.Int64)
			e.OldStatus = &old
		}
		e.NewStatus = &newStatus

		createdAt, err := parseDBTime(createdAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse history event created_at: %w", err)
		}
		e.CreatedAt = formatAPITime(createdAt)

		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history events: %w", err)
	}
	return events, nil
}

func runHistoryPoller(ctx context.Context, jetmonDB *sql.DB, history *historyStore, interval, timeout time.Duration) {
	if interval <= 0 {
		interval = defaultHistoryPollInterval
	}
	if timeout <= 0 {
		timeout = interval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollCtx, cancel := context.WithTimeout(ctx, timeout)
			err := history.poll(pollCtx, jetmonDB, time.Now().UTC())
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("history poll: %v", err)
			}
		}
	}
}

func nullableHistoryTimeValue(nt nullableTime) any {
	if !nt.Valid {
		return nil
	}
	return formatHistoryTime(nt.Time)
}

func sameNullableTime(a, b nullableTime) bool {
	if a.Valid != b.Valid {
		return false
	}
	if !a.Valid {
		return true
	}
	return a.Time.UTC().Equal(b.Time.UTC())
}

func formatHistoryTime(t time.Time) string {
	return t.UTC().Format(historyTimeLayout)
}

func formatAPITime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseDBTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}

	layouts := []string{
		historyTimeLayout,
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("unsupported time format %q", s)
}
