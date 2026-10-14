package state

import (
	"context"
	"testing"
)

func TestEventInsertionFailureRollsBackStateTransition(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("FORGEHAND_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", dir)
	db, err := Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()

	session, err := db.CreateSession(ctx, "test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	exec, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	eventsBefore, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("list before: %v", err)
	}

	if _, err := db.db.ExecContext(ctx, `
CREATE TRIGGER fail_session_event_insert
BEFORE INSERT ON session_events
BEGIN
    SELECT RAISE(ABORT, 'injected event insertion failure');
END;
`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	defer func() {
		_, _ = db.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS fail_session_event_insert`)
	}()

	err = db.AdvanceSessionExecution(ctx, exec.ID, 1)
	if err == nil {
		t.Fatal("expected error caused by event insertion failure")
	}

	eventsAfter, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("event count changed: %d before, %d after", len(eventsBefore), len(eventsAfter))
	}

	_, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if latest == nil || latest.CurrentStep != 0 || latest.Status != ExecutionStatusRunning {
		t.Fatalf("state changed unexpectedly: %+v", latest)
	}

	if _, err := db.db.ExecContext(ctx, `DROP TRIGGER fail_session_event_insert`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	if err := db.AdvanceSessionExecution(ctx, exec.ID, 1); err != nil {
		t.Fatalf("advance after trigger removed: %v", err)
	}

	eventsFinal, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("list final: %v", err)
	}
	checkpointEvents := 0
	for _, ev := range eventsFinal {
		if ev.Type == EventTypeExecutionCheckpoint {
			checkpointEvents++
		}
	}
	if checkpointEvents != 1 {
		t.Fatalf("expected exactly 1 checkpoint event, got %d", checkpointEvents)
	}

	_, latestFinal, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("status final: %v", err)
	}
	if latestFinal == nil || latestFinal.CurrentStep != 1 {
		t.Fatalf("checkpoint did not advance: %+v", latestFinal)
	}
}
