package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestSchemaV11DataPreservedWhenMigratingToV12 constructs a schema-v11
// database with an existing session, execution, and event history, then
// verifies the v12 migration adds the queue_seq column and its indexes without
// altering pre-existing rows or retroactively queueing anything.
func TestSchemaV11DataPreservedWhenMigratingToV12(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "forgehand.db")

	raw, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}

	if _, err := raw.Exec(`CREATE TABLE schema_version (version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_version(version) VALUES (0)`); err != nil {
		t.Fatalf("initialize schema version: %v", err)
	}
	for _, migration := range migrations {
		if migration.version > 11 {
			break
		}
		if _, err := raw.Exec(migration.sql); err != nil {
			t.Fatalf("apply migration %d: %v", migration.version, err)
		}
		if _, err := raw.Exec(`UPDATE schema_version SET version = ?`, migration.version); err != nil {
			t.Fatalf("record migration %d: %v", migration.version, err)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := raw.Exec(`
INSERT INTO sessions (title, state, created_at, updated_at)
VALUES (?, ?, ?, ?)
`, "Legacy Session", SessionStateCompleted, now, now); err != nil {
		t.Fatalf("insert legacy session: %v", err)
	}
	if _, err := raw.Exec(`
INSERT INTO session_executions (
	session_id, status, current_step, total_steps, started_at, completed_at
)
VALUES (1, ?, 5, 5, ?, ?)
`, ExecutionStatusCompleted, now, now); err != nil {
		t.Fatalf("insert legacy execution: %v", err)
	}

	if err := raw.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	db, err := OpenAt(ctx, dir)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.db.QueryRowContext(ctx,
		`SELECT version FROM schema_version LIMIT 1`,
	).Scan(&version); err != nil {
		t.Fatalf("read migrated schema version: %v", err)
	}
	if version != 15 {
		t.Fatalf("migrated schema version = %d, want 15", version)
	}

	var queueSeq sql.NullInt64
	if err := db.db.QueryRowContext(ctx,
		`SELECT queue_seq FROM session_executions WHERE id = 1`,
	).Scan(&queueSeq); err != nil {
		t.Fatalf("read queue_seq column: %v", err)
	}
	if queueSeq.Valid {
		t.Fatalf("pre-existing execution unexpectedly has queue_seq = %d", queueSeq.Int64)
	}

	for _, indexName := range []string{
		"idx_session_executions_queue_seq",
		"idx_session_executions_status_queue_seq",
	} {
		var name string
		if err := db.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`,
			indexName,
		).Scan(&name); err != nil {
			t.Fatalf("index %s not found after migration: %v", indexName, err)
		}
	}

	session, latest, err := db.SessionExecutionStatus(ctx, 1)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if session.Title != "Legacy Session" || session.State != SessionStateCompleted {
		t.Fatalf("preserved session unexpected: %+v", session)
	}
	if latest == nil || latest.CurrentStep != 5 || latest.Status != ExecutionStatusCompleted {
		t.Fatalf("preserved execution unexpected: %+v", latest)
	}
	if latest.QueueSeq != nil {
		t.Fatalf("preserved execution has queue_seq %d, want nil", *latest.QueueSeq)
	}

	count, err := db.CountQueuedExecutions(ctx)
	if err != nil {
		t.Fatalf("CountQueuedExecutions: %v", err)
	}
	if count != 0 {
		t.Fatalf("queued count = %d, want 0", count)
	}
}
