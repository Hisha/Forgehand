package state

import (
	"context"
		"sync"
	"testing"
	)

func TestSessionCreationProducesEvent(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventTypeSessionCreated {
		t.Fatalf("event type = %q, want %q", events[0].Type, EventTypeSessionCreated)
	}
	if events[0].SessionID != session.ID {
		t.Fatalf("session id mismatch")
	}
}

func TestEventOrderingAndCursorPagination(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	exec, err := db.StartSessionExecution(ctx, session.ID, 3)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	if err := db.AdvanceSessionExecution(ctx, exec.ID, 1); err != nil {
		t.Fatalf("AdvanceSessionExecution: %v", err)
	}
	if err := db.AdvanceSessionExecution(ctx, exec.ID, 2); err != nil {
		t.Fatalf("AdvanceSessionExecution: %v", err)
	}
	if err := db.AdvanceSessionExecution(ctx, exec.ID, 3); err != nil {
		t.Fatalf("AdvanceSessionExecution: %v", err)
	}
	if err := db.CompleteSessionExecution(ctx, exec.ID); err != nil {
		t.Fatalf("CompleteSessionExecution: %v", err)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}

	// created + started + checkpoint(1) + checkpoint(2) + checkpoint(3) + completed
	if len(events) != 6 {
		t.Fatalf("got %d events, want 6", len(events))
	}

	types := []string{}
	for _, e := range events {
		types = append(types, e.Type)
	}
	if types[0] != EventTypeSessionCreated || types[1] != EventTypeExecutionStarted {
		t.Fatalf("unexpected first types: %v", types)
	}
	if types[5] != EventTypeExecutionCompleted {
		t.Fatalf("unexpected last type: %v", types[5])
	}

	// test pagination
	page1, next1, err := db.ListSessionEvents(ctx, session.ID, 0, 2)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 || next1 == 0 {
		t.Fatalf("unexpected page1: len=%d next=%d", len(page1), next1)
	}

	page2, next2, err := db.ListSessionEvents(ctx, session.ID, next1, 2)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 2 || next2 == 0 {
		t.Fatalf("unexpected page2: len=%d next=%d", len(page2), next2)
	}

	page3, next3, err := db.ListSessionEvents(ctx, session.ID, next2, 2)
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3) != 2 || next3 == 0 {
		t.Fatalf("unexpected page3: len=%d next=%d", len(page3), next3)
	}

	// empty page when beyond
	page4, _, err := db.ListSessionEvents(ctx, session.ID, next3, 10)
	if err != nil {
		t.Fatalf("page4: %v", err)
	}
	if len(page4) != 0 {
		t.Fatalf("expected empty page, got %d", len(page4))
	}
}

func TestEventAPIValidationAndPaginationLimits(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	_, _, err = db.ListSessionEvents(ctx, session.ID, 0, 0)
	if err != nil {
		t.Fatalf("ListSessionEvents with limit 0: %v", err)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 10000)
	if err != nil {
		t.Fatalf("ListSessionEvents with large limit: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("unexpected events: %d", len(events))
	}

	_, _, err = db.ListSessionEvents(ctx, 999, 0, 100)
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
}

func TestEventPersistenceAcrossCloseReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("FORGEHAND_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", dir)
	db, err := Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	session, err := db.CreateSession(ctx, "test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err = Open(context.Background())
	if err != nil {
		t.Fatalf("Open2: %v", err)
	}
	defer db.Close()

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	if len(events) != 1 || events[0].Type != EventTypeSessionCreated {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestCheckpointEvents(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	exec, err := db.StartSessionExecution(ctx, session.ID, 2)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	if err := db.AdvanceSessionExecution(ctx, exec.ID, 1); err != nil {
		t.Fatalf("advance 1: %v", err)
	}
	if err := db.AdvanceSessionExecution(ctx, exec.ID, 2); err != nil {
		t.Fatalf("advance 2: %v", err)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}

	checkpoints := 0
	for _, e := range events {
		if e.Type == EventTypeExecutionCheckpoint {
			checkpoints++
		}
	}
	if checkpoints != 2 {
		t.Fatalf("expected 2 checkpoints, got %d", checkpoints)
	}
}

func TestExecutionCompletionFailureInterruption(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	exec, err := db.StartSessionExecution(ctx, session.ID, 2)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	if err := db.AdvanceSessionExecution(ctx, exec.ID, 1); err != nil {
		t.Fatalf("advance1: %v", err)
	}
	if err := db.AdvanceSessionExecution(ctx, exec.ID, 2); err != nil {
		t.Fatalf("advance2: %v", err)
	}
	if err := db.CompleteSessionExecution(ctx, exec.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	if !hasEventType(events, EventTypeExecutionCompleted) {
		t.Fatal("missing execution.completed")
	}
}

func TestExecutionFailureProducesEvent(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	exec, err := db.StartSessionExecution(ctx, session.ID, 2)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	if err := db.FailSessionExecution(ctx, exec.ID); err != nil {
		t.Fatalf("MarkSessionExecutionFailed: %v", err)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	if !hasEventType(events, EventTypeExecutionFailed) {
		t.Fatal("missing execution.failed")
	}
}

func TestExecutionInterruptionProducesEvent(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	exec, err := db.StartSessionExecution(ctx, session.ID, 2)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	if err := db.InterruptSessionExecution(ctx, exec.ID); err != nil {
		t.Fatalf("InterruptSessionExecution: %v", err)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	if !hasEventType(events, EventTypeExecutionInterrupted) {
		t.Fatal("missing execution.interrupted")
	}
}

func TestExecutionResumeProducesEvent(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	exec, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	if err := db.AdvanceSessionExecution(ctx, exec.ID, 1); err != nil {
		t.Fatalf("advance1: %v", err)
	}
	if err := db.AdvanceSessionExecution(ctx, exec.ID, 2); err != nil {
		t.Fatalf("advance2: %v", err)
	}
	if err := db.InterruptSessionExecution(ctx, exec.ID); err != nil {
		t.Fatalf("interrupt: %v", err)
	}

	resumed, err := db.ResumeSessionExecution(ctx, session.ID)
	if err != nil {
		t.Fatalf("ResumeSessionExecution: %v", err)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	if !hasEventType(events, EventTypeExecutionResumed) {
		t.Fatal("missing execution.resumed")
	}
	_ = resumed
}

func hasEventType(events []SessionEvent, t string) bool {
	for _, e := range events {
		if e.Type == t {
			return true
		}
	}
	return false
}

func TestConcurrentExecutionEvents(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "concurrent")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	exec, err := db.StartSessionExecution(ctx, session.ID, 3)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
go func() {
		defer wg.Done()
		if err := db.AdvanceSessionExecution(ctx, exec.ID, 1); err != nil {
			t.Errorf("advance 1: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		// try to advance again from current state
		if err := db.AdvanceSessionExecution(ctx, exec.ID, 2); err != nil {
			// may fail if already at 1 or race; that's okay
		}
	}()
	wg.Wait()

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	checkpoints := 0
	for _, e := range events {
		if e.Type == EventTypeExecutionCheckpoint {
			checkpoints++
		}
	}
	if checkpoints < 1 {
		t.Fatalf("expected at least 1 checkpoint, got %d", checkpoints)
	}
}
