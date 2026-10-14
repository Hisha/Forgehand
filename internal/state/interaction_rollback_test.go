package state

import (
	"context"
	"testing"
)

func interactionEventCount(t *testing.T, db *Database, sessionID int64) int {
	t.Helper()

	events, _, err := db.ListSessionEvents(context.Background(), sessionID, 0, 1000)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	count := 0
	for _, event := range events {
		switch event.Type {
		case EventTypeInteractionRequested, EventTypeInteractionAnswered,
			EventTypeExecutionWaitingForUser, EventTypeExecutionResumedAfterInput:
			count++
		}
	}
	return count
}

// TestInteractionInsertionFailureRollsBackRequest injects a failure on the
// human_interactions insert and verifies the whole request transition rolls
// back: the execution and session stay RUNNING and no interaction or event is
// left behind.
func TestInteractionInsertionFailureRollsBackRequest(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "rollback request")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	if err := db.AdvanceSessionExecution(ctx, execution.ID, 1); err != nil {
		t.Fatalf("advance: %v", err)
	}

	eventsBefore := interactionEventCount(t, db, session.ID)

	if _, err := db.db.ExecContext(ctx, `
CREATE TRIGGER fail_interaction_insert
BEFORE INSERT ON human_interactions
BEGIN
    SELECT RAISE(ABORT, 'injected interaction insertion failure');
END;
`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, _, err := db.RequestHumanInput(ctx, execution.ID, "question", nil); err == nil {
		t.Fatal("RequestHumanInput unexpectedly succeeded with a failing insert")
	}

	if _, err := db.db.ExecContext(ctx, `DROP TRIGGER fail_interaction_insert`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	var interactions int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM human_interactions WHERE execution_id = ?`,
		execution.ID,
	).Scan(&interactions); err != nil {
		t.Fatalf("count interactions: %v", err)
	}
	if interactions != 0 {
		t.Fatalf("interaction row persisted after rollback: %d", interactions)
	}

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if sessionState.State != SessionStateRunning {
		t.Fatalf("session state = %q, want %q", sessionState.State, SessionStateRunning)
	}
	if latest == nil || latest.Status != ExecutionStatusRunning || latest.CurrentStep != 1 {
		t.Fatalf("execution changed after rollback: %#v", latest)
	}

	if after := interactionEventCount(t, db, session.ID); after != eventsBefore {
		t.Fatalf("interaction event count changed: %d before, %d after", eventsBefore, after)
	}

	// After the fault is removed the request succeeds.
	if _, created, err := db.RequestHumanInput(ctx, execution.ID, "question", nil); err != nil || !created {
		t.Fatalf("RequestHumanInput after rollback: created=%v err=%v", created, err)
	}
}

// TestEventInsertionFailureRollsBackRequest injects a failure on the session
// event insert and verifies the human_interactions row and both state
// transitions roll back together.
func TestEventInsertionFailureRollsBackRequest(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "rollback request event")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	if _, err := db.db.ExecContext(ctx, `
CREATE TRIGGER fail_request_event_insert
BEFORE INSERT ON session_events
BEGIN
    SELECT RAISE(ABORT, 'injected event insertion failure');
END;
`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, _, err := db.RequestHumanInput(ctx, execution.ID, "question", nil); err == nil {
		t.Fatal("RequestHumanInput unexpectedly succeeded with a failing event insert")
	}

	if _, err := db.db.ExecContext(ctx, `DROP TRIGGER fail_request_event_insert`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	var interactions int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM human_interactions WHERE execution_id = ?`,
		execution.ID,
	).Scan(&interactions); err != nil {
		t.Fatalf("count interactions: %v", err)
	}
	if interactions != 0 {
		t.Fatalf("interaction row persisted after event rollback: %d", interactions)
	}

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if sessionState.State != SessionStateRunning {
		t.Fatalf("session state = %q, want %q", sessionState.State, SessionStateRunning)
	}
	if latest == nil || latest.Status != ExecutionStatusRunning {
		t.Fatalf("execution changed after event rollback: %#v", latest)
	}
}

// TestEventInsertionFailureRollsBackAnswer injects a failure on the session
// event insert during an answer and verifies the interaction remains PENDING
// with no answer recorded.
func TestEventInsertionFailureRollsBackAnswer(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	_, _, interaction := startPausedExecution(t, ctx, db, 5, 1)

	if _, err := db.db.ExecContext(ctx, `
CREATE TRIGGER fail_answer_event_insert
BEFORE INSERT ON session_events
BEGIN
    SELECT RAISE(ABORT, 'injected event insertion failure');
END;
`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, _, err := db.AnswerHumanInput(ctx, interaction.ID, "answer"); err == nil {
		t.Fatal("AnswerHumanInput unexpectedly succeeded with a failing event insert")
	}

	if _, err := db.db.ExecContext(ctx, `DROP TRIGGER fail_answer_event_insert`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	reloaded, err := db.GetHumanInteraction(ctx, interaction.ID)
	if err != nil {
		t.Fatalf("GetHumanInteraction: %v", err)
	}
	if reloaded.Status != InteractionStatusPending {
		t.Fatalf("interaction status = %q, want %q", reloaded.Status, InteractionStatusPending)
	}
	if reloaded.Answer != nil || reloaded.AnsweredAt != nil {
		t.Fatal("answer persisted despite rollback")
	}

	// The rollback leaves the interaction answerable.
	if _, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "answer"); err != nil || !applied {
		t.Fatalf("AnswerHumanInput after rollback: applied=%v err=%v", applied, err)
	}
}

// TestEventInsertionFailureRollsBackResume injects a failure on the session
// event insert during a resume and verifies the execution stays
// WAITING_FOR_USER at the preserved checkpoint.
func TestEventInsertionFailureRollsBackResume(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, execution, interaction := startPausedExecution(t, ctx, db, 5, 1)
	if _, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "go"); err != nil || !applied {
		t.Fatalf("AnswerHumanInput: applied=%v err=%v", applied, err)
	}

	if _, err := db.db.ExecContext(ctx, `
CREATE TRIGGER fail_resume_event_insert
BEFORE INSERT ON session_events
BEGIN
    SELECT RAISE(ABORT, 'injected event insertion failure');
END;
`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, err := db.ResumeAfterHumanInput(ctx, session.ID); err == nil {
		t.Fatal("ResumeAfterHumanInput unexpectedly succeeded with a failing event insert")
	}

	if _, err := db.db.ExecContext(ctx, `DROP TRIGGER fail_resume_event_insert`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if sessionState.State != SessionStateWaitingForUser {
		t.Fatalf("session state = %q, want %q", sessionState.State, SessionStateWaitingForUser)
	}
	if latest == nil || latest.Status != ExecutionStatusWaitingForUser || latest.CurrentStep != 1 {
		t.Fatalf("execution changed after resume rollback: %#v", latest)
	}

	if resumed, err := db.ResumeAfterHumanInput(ctx, session.ID); err != nil || resumed.ID != execution.ID {
		t.Fatalf("ResumeAfterHumanInput after rollback: id=%d err=%v", resumed.ID, err)
	}
}
