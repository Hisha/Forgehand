package state

import (
	"context"
	"testing"
)

func openTestDatabase(t *testing.T) *Database {
	t.Helper()

	t.Setenv("XDG_STATE_HOME", t.TempDir())

	db, err := Open(context.Background())
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() returned error: %v", err)
		}
	})

	return db
}

func TestDaemonRunStartsUnclean(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	id, err := db.StartDaemonRun(ctx, 12345, "test-version")
	if err != nil {
		t.Fatalf("StartDaemonRun() returned error: %v", err)
	}

	run, err := db.LastDaemonRun(ctx)
	if err != nil {
		t.Fatalf("LastDaemonRun() returned error: %v", err)
	}

	if run == nil {
		t.Fatal("LastDaemonRun() returned nil")
	}

	if run.ID != id {
		t.Fatalf("run ID = %d, want %d", run.ID, id)
	}

	if run.PID != 12345 {
		t.Fatalf("PID = %d, want 12345", run.PID)
	}

	if run.Version != "test-version" {
		t.Fatalf("version = %q, want %q", run.Version, "test-version")
	}

	if run.ShutdownClean {
		t.Fatal("new daemon run was incorrectly marked clean")
	}

	if run.StoppedAt != nil {
		t.Fatal("new daemon run unexpectedly has stopped_at")
	}
}

func TestFinishDaemonRunMarksRunClean(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	id, err := db.StartDaemonRun(ctx, 12345, "test-version")
	if err != nil {
		t.Fatalf("StartDaemonRun() returned error: %v", err)
	}

	if err := db.FinishDaemonRun(ctx, id); err != nil {
		t.Fatalf("FinishDaemonRun() returned error: %v", err)
	}

	run, err := db.LastDaemonRun(ctx)
	if err != nil {
		t.Fatalf("LastDaemonRun() returned error: %v", err)
	}

	if run == nil {
		t.Fatal("LastDaemonRun() returned nil")
	}

	if !run.ShutdownClean {
		t.Fatal("finished daemon run was not marked clean")
	}

	if run.StoppedAt == nil {
		t.Fatal("finished daemon run has no stopped_at")
	}
}

func TestUncleanRunRemainsDetectable(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	firstID, err := db.StartDaemonRun(ctx, 11111, "first")
	if err != nil {
		t.Fatalf("start first run: %v", err)
	}

	if err := db.FinishDaemonRun(ctx, firstID); err != nil {
		t.Fatalf("finish first run: %v", err)
	}

	secondID, err := db.StartDaemonRun(ctx, 22222, "second")
	if err != nil {
		t.Fatalf("start second run: %v", err)
	}

	run, err := db.LastDaemonRun(ctx)
	if err != nil {
		t.Fatalf("LastDaemonRun() returned error: %v", err)
	}

	if run == nil {
		t.Fatal("LastDaemonRun() returned nil")
	}

	if run.ID != secondID {
		t.Fatalf("last run ID = %d, want %d", run.ID, secondID)
	}

	if run.PID != 22222 {
		t.Fatalf("last run PID = %d, want 22222", run.PID)
	}

	if run.ShutdownClean {
		t.Fatal("interrupted-style run was incorrectly marked clean")
	}

	if run.StoppedAt != nil {
		t.Fatal("interrupted-style run unexpectedly has stopped_at")
	}
}

func TestForeignKeysAreEnabled(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	var enabled int

	if err := db.db.QueryRowContext(
		ctx,
		`PRAGMA foreign_keys`,
	).Scan(&enabled); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}

	if enabled != 1 {
		t.Fatalf("foreign_keys = %d, want 1", enabled)
	}
}
