package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestLookupEventsSyntheticPreservedWhenHistoryDisabled(t *testing.T) {
	ctx := context.Background()
	sourceDB := newTestJetmonDB(t)
	defer sourceDB.Close()

	changedAt := time.Date(2026, 4, 22, 14, 0, 10, 0, time.UTC)
	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 2, changedAt)

	events, err := lookupEvents(ctx, sourceDB, 1001, changedAt.Add(-time.Minute), changedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("lookupEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1: %#v", len(events), events)
	}
	assertEvent(t, events[0], 1001, 1, 2, "veriflier", changedAt)
	if events[0].ID != 0 {
		t.Fatalf("synthetic event ID = %d, want 0", events[0].ID)
	}
}

func TestHistoryFirstPollBaselinesWithoutEvent(t *testing.T) {
	ctx := context.Background()
	sourceDB := newTestJetmonDB(t)
	defer sourceDB.Close()
	history := newTestHistoryStore(t, ":memory:")
	defer history.Close()

	baselineAt := time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC)
	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 1, baselineAt)

	if err := history.poll(ctx, sourceDB, baselineAt.Add(time.Second)); err != nil {
		t.Fatalf("poll baseline: %v", err)
	}

	events, err := history.lookupEvents(ctx, 1001, baselineAt.Add(-time.Minute), baselineAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("lookupEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("len(events) = %d, want 0: %#v", len(events), events)
	}
}

func TestHistoryDownRecoverySequenceReturnsBothEvents(t *testing.T) {
	ctx := context.Background()
	sourceDB := newTestJetmonDB(t)
	defer sourceDB.Close()
	history := newTestHistoryStore(t, ":memory:")
	defer history.Close()

	baselineAt := time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC)
	downAt := baselineAt.Add(2 * time.Minute)
	recoveredAt := baselineAt.Add(4 * time.Minute)
	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 1, baselineAt)
	if err := history.poll(ctx, sourceDB, baselineAt.Add(time.Second)); err != nil {
		t.Fatalf("poll baseline: %v", err)
	}

	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 2, downAt)
	if err := history.poll(ctx, sourceDB, downAt.Add(time.Second)); err != nil {
		t.Fatalf("poll down: %v", err)
	}
	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 1, recoveredAt)
	if err := history.poll(ctx, sourceDB, recoveredAt.Add(time.Second)); err != nil {
		t.Fatalf("poll recovery: %v", err)
	}

	events, err := history.lookupEvents(ctx, 1001, baselineAt, recoveredAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("lookupEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2: %#v", len(events), events)
	}
	assertEvent(t, events[0], 1001, 1, 2, "veriflier", downAt)
	assertEvent(t, events[1], 1001, 2, 1, "jetmon", recoveredAt)
}

func TestHistoryEventsWindowIsSinceInclusiveUntilExclusive(t *testing.T) {
	ctx := context.Background()
	sourceDB := newTestJetmonDB(t)
	defer sourceDB.Close()
	history := newTestHistoryStore(t, ":memory:")
	defer history.Close()

	baselineAt := time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC)
	downAt := baselineAt.Add(2 * time.Minute)
	recoveredAt := baselineAt.Add(4 * time.Minute)
	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 1, baselineAt)
	if err := history.poll(ctx, sourceDB, baselineAt.Add(time.Second)); err != nil {
		t.Fatalf("poll baseline: %v", err)
	}
	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 2, downAt)
	if err := history.poll(ctx, sourceDB, downAt.Add(time.Second)); err != nil {
		t.Fatalf("poll down: %v", err)
	}
	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 1, recoveredAt)
	if err := history.poll(ctx, sourceDB, recoveredAt.Add(time.Second)); err != nil {
		t.Fatalf("poll recovery: %v", err)
	}

	events, err := history.lookupEvents(ctx, 1001, baselineAt, downAt)
	if err != nil {
		t.Fatalf("lookup first window: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("[baseline, down) len(events) = %d, want 0: %#v", len(events), events)
	}

	events, err = history.lookupEvents(ctx, 1001, downAt, recoveredAt)
	if err != nil {
		t.Fatalf("lookup second window: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("[down, recovery) len(events) = %d, want 1: %#v", len(events), events)
	}
	assertEvent(t, events[0], 1001, 1, 2, "veriflier", downAt)

	events, err = history.lookupEvents(ctx, 1001, recoveredAt, recoveredAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("lookup third window: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("[recovery, after) len(events) = %d, want 1: %#v", len(events), events)
	}
	assertEvent(t, events[0], 1001, 2, 1, "jetmon", recoveredAt)
}

func TestHistoryDuplicatePollsDoNotDuplicateEvents(t *testing.T) {
	ctx := context.Background()
	sourceDB := newTestJetmonDB(t)
	defer sourceDB.Close()
	history := newTestHistoryStore(t, ":memory:")
	defer history.Close()

	baselineAt := time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC)
	downAt := baselineAt.Add(2 * time.Minute)
	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 1, baselineAt)
	if err := history.poll(ctx, sourceDB, baselineAt.Add(time.Second)); err != nil {
		t.Fatalf("poll baseline: %v", err)
	}

	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 2, downAt)
	if err := history.poll(ctx, sourceDB, downAt.Add(time.Second)); err != nil {
		t.Fatalf("poll down: %v", err)
	}
	if err := history.poll(ctx, sourceDB, downAt.Add(2*time.Second)); err != nil {
		t.Fatalf("duplicate poll: %v", err)
	}

	events, err := history.lookupEvents(ctx, 1001, baselineAt, downAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("lookupEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1: %#v", len(events), events)
	}
	assertEvent(t, events[0], 1001, 1, 2, "veriflier", downAt)
}

func TestHistoryLastStatusChangeUpdateCreatesEvent(t *testing.T) {
	ctx := context.Background()
	sourceDB := newTestJetmonDB(t)
	defer sourceDB.Close()
	history := newTestHistoryStore(t, ":memory:")
	defer history.Close()

	firstChangeAt := time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC)
	secondChangeAt := firstChangeAt.Add(2 * time.Minute)
	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 2, firstChangeAt)
	if err := history.poll(ctx, sourceDB, firstChangeAt.Add(time.Second)); err != nil {
		t.Fatalf("poll baseline: %v", err)
	}

	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 2, secondChangeAt)
	if err := history.poll(ctx, sourceDB, secondChangeAt.Add(time.Second)); err != nil {
		t.Fatalf("poll timestamp update: %v", err)
	}

	events, err := history.lookupEvents(ctx, 1001, firstChangeAt, secondChangeAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("lookupEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1: %#v", len(events), events)
	}
	assertEvent(t, events[0], 1001, 2, 2, "veriflier", secondChangeAt)
}

func TestHistoryRestartReopenKeepsEvents(t *testing.T) {
	ctx := context.Background()
	sourceDB := newTestJetmonDB(t)
	defer sourceDB.Close()
	historyPath := filepath.Join(t.TempDir(), "history.db")

	baselineAt := time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC)
	downAt := baselineAt.Add(2 * time.Minute)
	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 1, baselineAt)

	history := newTestHistoryStore(t, historyPath)
	if err := history.poll(ctx, sourceDB, baselineAt.Add(time.Second)); err != nil {
		t.Fatalf("poll baseline: %v", err)
	}
	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 2, downAt)
	if err := history.poll(ctx, sourceDB, downAt.Add(time.Second)); err != nil {
		t.Fatalf("poll down: %v", err)
	}
	if err := history.Close(); err != nil {
		t.Fatalf("close history: %v", err)
	}

	reopened := newTestHistoryStore(t, historyPath)
	defer reopened.Close()
	events, err := reopened.lookupEvents(ctx, 1001, baselineAt, downAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("lookupEvents after reopen: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1: %#v", len(events), events)
	}
	assertEvent(t, events[0], 1001, 1, 2, "veriflier", downAt)
}

func TestHistoryUnexpectedSiteStatusIsSkipped(t *testing.T) {
	ctx := context.Background()
	sourceDB := newTestJetmonDB(t)
	defer sourceDB.Close()
	history := newTestHistoryStore(t, ":memory:")
	defer history.Close()

	baselineAt := time.Date(2026, 4, 22, 14, 0, 0, 0, time.UTC)
	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 9, baselineAt)
	if err := history.poll(ctx, sourceDB, baselineAt.Add(time.Second)); err != nil {
		t.Fatalf("poll invalid status: %v", err)
	}

	events, err := history.lookupEvents(ctx, 1001, baselineAt.Add(-time.Minute), baselineAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("lookupEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("len(events) = %d, want 0: %#v", len(events), events)
	}

	setTestMonitor(t, sourceDB, 1001, "https://bench-target-01.example.com", 1, baselineAt.Add(time.Minute))
	if err := history.poll(ctx, sourceDB, baselineAt.Add(time.Minute+time.Second)); err != nil {
		t.Fatalf("poll valid status after invalid: %v", err)
	}
	events, err = history.lookupEvents(ctx, 1001, baselineAt, baselineAt.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("lookupEvents after valid baseline: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("len(events) = %d, want 0 after valid baseline: %#v", len(events), events)
	}
}

func newTestJetmonDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open source db: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	const schema = `
CREATE TABLE jetpack_monitor_sites (
    blog_id            BIGINT PRIMARY KEY,
    monitor_url        TEXT NOT NULL,
    monitor_active     INTEGER NOT NULL,
    site_status        INTEGER NOT NULL,
    last_status_change DATETIME NULL
)`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatalf("create source schema: %v", err)
	}

	return db
}

func newTestHistoryStore(t *testing.T, path string) *historyStore {
	t.Helper()

	history, err := openHistoryStore(path)
	if err != nil {
		t.Fatalf("open history store: %v", err)
	}
	return history
}

func setTestMonitor(t *testing.T, db *sql.DB, blogID int64, monitorURL string, siteStatus int, lastStatusChange time.Time) {
	t.Helper()

	const q = `
INSERT INTO jetpack_monitor_sites
    (blog_id, monitor_url, monitor_active, site_status, last_status_change)
VALUES (?, ?, 1, ?, ?)
ON CONFLICT(blog_id) DO UPDATE SET
    monitor_url        = excluded.monitor_url,
    monitor_active     = excluded.monitor_active,
    site_status        = excluded.site_status,
    last_status_change = excluded.last_status_change`
	if _, err := db.Exec(q, blogID, monitorURL, siteStatus, lastStatusChange.UTC()); err != nil {
		t.Fatalf("set test monitor: %v", err)
	}
}

func assertEvent(t *testing.T, got event, blogID int64, oldStatus, newStatus int, source string, createdAt time.Time) {
	t.Helper()

	if got.BlogID != blogID {
		t.Fatalf("BlogID = %d, want %d", got.BlogID, blogID)
	}
	if got.EventType != "status_transition" {
		t.Fatalf("EventType = %q, want status_transition", got.EventType)
	}
	if got.Source != source {
		t.Fatalf("Source = %q, want %q", got.Source, source)
	}
	if got.OldStatus == nil || *got.OldStatus != oldStatus {
		t.Fatalf("OldStatus = %v, want %d", got.OldStatus, oldStatus)
	}
	if got.NewStatus == nil || *got.NewStatus != newStatus {
		t.Fatalf("NewStatus = %v, want %d", got.NewStatus, newStatus)
	}
	if got.CreatedAt != formatAPITime(createdAt) {
		t.Fatalf("CreatedAt = %q, want %q", got.CreatedAt, formatAPITime(createdAt))
	}
}
