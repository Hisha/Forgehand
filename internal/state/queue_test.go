package state

import (
	"context"
	"testing"
)

func countEventsOfType(t *testing.T, db *Database, sessionID int64, eventType string) int {
	t.Helper()

	events, _, err := db.ListSessionEvents(context.Background(), sessionID, 0, 1000)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func TestQueueSessionExecutionAssignsOrderedQueueSequence(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	sessions := make([]Session, 0, 3)
	for i := 0; i < 3; i++ {
		session, err := db.CreateSession(ctx, "queued session")
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		sessions = append(sessions, session)
	}

	for i, session := range sessions {
		execution, err := db.QueueSessionExecution(ctx, session.ID, 5)
		if err != nil {
			t.Fatalf("QueueSessionExecution: %v", err)
		}
		if execution.Status != ExecutionStatusQueued {
			t.Fatalf("status = %q, want %q", execution.Status, ExecutionStatusQueued)
		}
		if execution.QueueSeq == nil {
			t.Fatal("queue sequence was not assigned")
		}
		if *execution.QueueSeq != int64(i+1) {
			t.Fatalf("queue seq = %d, want %d", *execution.QueueSeq, i+1)
		}
		if execution.QueuePosition != i+1 {
			t.Fatalf("queue position = %d, want %d", execution.QueuePosition, i+1)
		}

		stored, latest, err := db.SessionExecutionStatus(ctx, session.ID)
		if err != nil {
			t.Fatalf("SessionExecutionStatus: %v", err)
		}
		if stored.State != SessionStateQueued {
			t.Fatalf("session state = %q, want %q", stored.State, SessionStateQueued)
		}
		if latest == nil || latest.Status != ExecutionStatusQueued {
			t.Fatalf("stored execution = %#v, want QUEUED", latest)
		}
		if got := countEventsOfType(t, db, session.ID, EventTypeExecutionQueued); got != 1 {
			t.Fatalf("execution.queued events = %d, want 1", got)
		}
	}

	count, err := db.CountQueuedExecutions(ctx)
	if err != nil {
		t.Fatalf("CountQueuedExecutions: %v", err)
	}
	if count != 3 {
		t.Fatalf("queued count = %d, want 3", count)
	}
}

func TestQueueSessionExecutionRejectsNonReadySession(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "double queue")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := db.QueueSessionExecution(ctx, session.ID, 5); err != nil {
		t.Fatalf("first QueueSessionExecution: %v", err)
	}
	if _, err := db.QueueSessionExecution(ctx, session.ID, 5); err == nil {
		t.Fatal("second QueueSessionExecution unexpectedly succeeded")
	}
}

func TestQueueClaimPreservesFIFOOrder(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	var sessionIDs []int64
	for i := 0; i < 3; i++ {
		session, err := db.CreateSession(ctx, "fifo session")
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if _, err := db.QueueSessionExecution(ctx, session.ID, 5); err != nil {
			t.Fatalf("QueueSessionExecution: %v", err)
		}
		sessionIDs = append(sessionIDs, session.ID)
	}

	first, ok, err := db.ClaimNextQueuedExecution(ctx)
	if err != nil {
		t.Fatalf("ClaimNextQueuedExecution: %v", err)
	}
	if !ok {
		t.Fatal("claimed no execution while queue was non-empty")
	}
	if first.SessionID != sessionIDs[0] {
		t.Fatalf("first claim session = %d, want %d", first.SessionID, sessionIDs[0])
	}
	if first.Status != ExecutionStatusRunning {
		t.Fatalf("claimed status = %q, want %q", first.Status, ExecutionStatusRunning)
	}
	if got := countEventsOfType(t, db, first.SessionID, EventTypeExecutionDispatched); got != 1 {
		t.Fatalf("execution.dispatched events = %d, want 1", got)
	}

	stored, _, err := db.SessionExecutionStatus(ctx, first.SessionID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if stored.State != SessionStateRunning {
		t.Fatalf("dispatched session state = %q, want %q", stored.State, SessionStateRunning)
	}

	second, ok, err := db.ClaimNextQueuedExecution(ctx)
	if err != nil {
		t.Fatalf("second ClaimNextQueuedExecution: %v", err)
	}
	if !ok || second.SessionID != sessionIDs[1] {
		t.Fatalf("second claim = %#v ok=%v, want session %d", second, ok, sessionIDs[1])
	}

	remaining, err := db.CountQueuedExecutions(ctx)
	if err != nil {
		t.Fatalf("CountQueuedExecutions: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining queued = %d, want 1", remaining)
	}
}

func TestClaimNextQueuedExecutionSkipsCanceled(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	first, err := db.CreateSession(ctx, "canceled while queued")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := db.QueueSessionExecution(ctx, first.ID, 5); err != nil {
		t.Fatalf("QueueSessionExecution: %v", err)
	}
	second, err := db.CreateSession(ctx, "runnable")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := db.QueueSessionExecution(ctx, second.ID, 5); err != nil {
		t.Fatalf("QueueSessionExecution: %v", err)
	}

	if _, err := db.CancelSessionExecution(ctx, first.ID, "no longer needed"); err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}

	claimed, ok, err := db.ClaimNextQueuedExecution(ctx)
	if err != nil {
		t.Fatalf("ClaimNextQueuedExecution: %v", err)
	}
	if !ok || claimed.SessionID != second.ID {
		t.Fatalf("claimed = %#v ok=%v, want session %d", claimed, ok, second.ID)
	}
}

func TestQueueResumeExecutionPreservesCheckpointAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "resumable")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	if err := db.AdvanceSessionExecution(ctx, execution.ID, 1); err != nil {
		t.Fatalf("AdvanceSessionExecution: %v", err)
	}
	if err := db.InterruptSessionExecution(ctx, execution.ID); err != nil {
		t.Fatalf("InterruptSessionExecution: %v", err)
	}

	queued, err := db.QueueResumeExecution(ctx, session.ID)
	if err != nil {
		t.Fatalf("QueueResumeExecution: %v", err)
	}
	if queued.ID != execution.ID {
		t.Fatalf("resumed execution = %d, want %d", queued.ID, execution.ID)
	}
	if queued.Status != ExecutionStatusQueued {
		t.Fatalf("status = %q, want %q", queued.Status, ExecutionStatusQueued)
	}
	if queued.CurrentStep != 1 {
		t.Fatalf("current step = %d, want 1", queued.CurrentStep)
	}

	again, err := db.QueueResumeExecution(ctx, session.ID)
	if err != nil {
		t.Fatalf("idempotent QueueResumeExecution: %v", err)
	}
	if again.ID != execution.ID || again.Status != ExecutionStatusQueued {
		t.Fatalf("idempotent resume = %#v", again)
	}

	if got := countEventsOfType(t, db, session.ID, EventTypeExecutionQueued); got != 1 {
		t.Fatalf("execution.queued events = %d, want 1 (idempotent resume must not duplicate)", got)
	}
}

func TestQueueResumeAfterHumanInputRequiresAnswer(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "waiting")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	if err := db.AdvanceSessionExecution(ctx, execution.ID, 1); err != nil {
		t.Fatalf("AdvanceSessionExecution: %v", err)
	}
	interaction, created, err := db.RequestHumanInput(ctx, execution.ID, "continue?", nil)
	if err != nil {
		t.Fatalf("RequestHumanInput: %v", err)
	}
	if !created {
		t.Fatal("RequestHumanInput did not create an interaction")
	}

	if _, err := db.QueueResumeAfterHumanInput(ctx, session.ID); err == nil {
		t.Fatal("resume after unanswered input unexpectedly succeeded")
	}

	if _, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "go"); err != nil || !applied {
		t.Fatalf("AnswerHumanInput: applied=%v err=%v", applied, err)
	}

	queued, err := db.QueueResumeAfterHumanInput(ctx, session.ID)
	if err != nil {
		t.Fatalf("QueueResumeAfterHumanInput: %v", err)
	}
	if queued.ID != execution.ID || queued.Status != ExecutionStatusQueued {
		t.Fatalf("queued resume = %#v", queued)
	}
	if queued.CurrentStep != 1 {
		t.Fatalf("current step = %d, want 1", queued.CurrentStep)
	}
}

func TestQueuePositionRecomputesAfterDispatch(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	var sessionIDs []int64
	for i := 0; i < 3; i++ {
		session, err := db.CreateSession(ctx, "position")
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if _, err := db.QueueSessionExecution(ctx, session.ID, 5); err != nil {
			t.Fatalf("QueueSessionExecution: %v", err)
		}
		sessionIDs = append(sessionIDs, session.ID)
	}

	if _, ok, err := db.ClaimNextQueuedExecution(ctx); err != nil || !ok {
		t.Fatalf("claim head: ok=%v err=%v", ok, err)
	}

	// The second session was position 2; after the head is dispatched it must
	// report position 1.
	_, latest, err := db.SessionExecutionStatus(ctx, sessionIDs[1])
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if latest == nil || latest.Status != ExecutionStatusQueued {
		t.Fatalf("second execution = %#v, want QUEUED", latest)
	}
	if latest.QueuePosition != 1 {
		t.Fatalf("second queue position = %d, want 1", latest.QueuePosition)
	}
}
