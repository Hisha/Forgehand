package state

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestCancelRunningSessionExecution(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Cancel running")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	for step := 1; step <= 2; step++ {
		if err := db.AdvanceSessionExecution(ctx, execution.ID, step); err != nil {
			t.Fatalf("advance to step %d: %v", step, err)
		}
	}

	result, err := db.CancelSessionExecution(ctx, session.ID, "user changed plans")
	if err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}

	if result.Outcome != CancellationApplied {
		t.Fatalf("outcome = %q, want %q", result.Outcome, CancellationApplied)
	}
	if result.Session.State != SessionStateCanceled {
		t.Fatalf("session state = %q, want %q", result.Session.State, SessionStateCanceled)
	}
	if result.Execution.Status != ExecutionStatusCanceled {
		t.Fatalf("execution status = %q, want %q", result.Execution.Status, ExecutionStatusCanceled)
	}
	if result.Execution.CurrentStep != 2 {
		t.Fatalf("current step = %d, want 2 (checkpoint preserved)", result.Execution.CurrentStep)
	}
	if result.Execution.CanceledAt == nil {
		t.Fatal("canceled_at was not recorded")
	}
	if result.Execution.CancelReason == nil || *result.Execution.CancelReason != "user changed plans" {
		t.Fatalf("cancel reason = %v, want %q", result.Execution.CancelReason, "user changed plans")
	}

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if sessionState.State != SessionStateCanceled || latest.Status != ExecutionStatusCanceled {
		t.Fatalf("persisted state unexpected: session=%q execution=%q", sessionState.State, latest.Status)
	}
	if latest.CanceledAt == nil {
		t.Fatal("persisted canceled_at missing")
	}

	if err := db.AdvanceSessionExecution(ctx, execution.ID, 3); err == nil {
		t.Fatal("canceled execution unexpectedly advanced")
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	if !hasEventType(events, EventTypeExecutionCanceled) {
		t.Fatal("missing execution.canceled event")
	}
	for _, event := range events {
		if event.Type != EventTypeExecutionCanceled {
			continue
		}
		if event.Details["status"] != ExecutionStatusCanceled {
			t.Fatalf("event status = %v, want %q", event.Details["status"], ExecutionStatusCanceled)
		}
		if event.Details["session_state"] != SessionStateCanceled {
			t.Fatalf("event session_state = %v, want %q", event.Details["session_state"], SessionStateCanceled)
		}
		if event.Details["reason"] != "user changed plans" {
			t.Fatalf("event reason = %v, want %q", event.Details["reason"], "user changed plans")
		}
		if event.ExecutionID == nil || *event.ExecutionID != execution.ID {
			t.Fatalf("event execution id = %v, want %d", event.ExecutionID, execution.ID)
		}
	}
}

func TestCancelWaitingForUserSessionExecution(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Cancel waiting")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	if err := db.AdvanceSessionExecution(ctx, execution.ID, 1); err != nil {
		t.Fatalf("advance to step 1: %v", err)
	}
	if _, created, err := db.RequestHumanInput(ctx, execution.ID, "Continue?", nil); err != nil || !created {
		t.Fatalf("RequestHumanInput: created=%v err=%v", created, err)
	}

	result, err := db.CancelSessionExecution(ctx, session.ID, "")
	if err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}
	if result.Outcome != CancellationApplied {
		t.Fatalf("outcome = %q, want %q", result.Outcome, CancellationApplied)
	}
	if result.Execution.CancelReason != nil {
		t.Fatalf("cancel reason = %v, want nil for empty reason", result.Execution.CancelReason)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	for _, event := range events {
		if event.Type == EventTypeExecutionCanceled {
			if _, ok := event.Details["reason"]; ok {
				t.Fatalf("event unexpectedly included reason: %#v", event.Details)
			}
		}
	}

	if _, err := db.ResumeAfterHumanInput(ctx, session.ID); err == nil {
		t.Fatal("canceled waiting execution unexpectedly resumed")
	}
}

func TestCancelInterruptedSessionExecution(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Cancel interrupted")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	if err := db.AdvanceSessionExecution(ctx, execution.ID, 1); err != nil {
		t.Fatalf("advance to step 1: %v", err)
	}
	if err := db.InterruptSessionExecution(ctx, execution.ID); err != nil {
		t.Fatalf("InterruptSessionExecution: %v", err)
	}

	result, err := db.CancelSessionExecution(ctx, session.ID, "abandoning")
	if err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}
	if result.Outcome != CancellationApplied {
		t.Fatalf("outcome = %q, want %q", result.Outcome, CancellationApplied)
	}
	if result.Execution.CurrentStep != 1 {
		t.Fatalf("current step = %d, want 1 (checkpoint preserved)", result.Execution.CurrentStep)
	}

	if _, err := db.ResumeSessionExecution(ctx, session.ID); err == nil {
		t.Fatal("canceled interrupted execution unexpectedly resumed")
	}
}

func TestCancelSessionExecutionIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Cancel twice")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	first, err := db.CancelSessionExecution(ctx, session.ID, "first reason")
	if err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	if first.Outcome != CancellationApplied {
		t.Fatalf("first outcome = %q, want %q", first.Outcome, CancellationApplied)
	}

	second, err := db.CancelSessionExecution(ctx, session.ID, "second reason")
	if err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	if second.Outcome != CancellationAlreadyCanceled {
		t.Fatalf("second outcome = %q, want %q", second.Outcome, CancellationAlreadyCanceled)
	}
	if second.Execution.CancelReason == nil || *second.Execution.CancelReason != "first reason" {
		t.Fatalf("reason changed on repeat: %v", second.Execution.CancelReason)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	canceledEvents := 0
	for _, event := range events {
		if event.Type == EventTypeExecutionCanceled {
			canceledEvents++
		}
	}
	if canceledEvents != 1 {
		t.Fatalf("canceled event count = %d, want 1", canceledEvents)
	}
	_ = execution
}

func TestCancelSessionExecutionRejectsTerminalExecutions(t *testing.T) {
	ctx := context.Background()

	for _, testCase := range []struct {
		name       string
		transition func(*Database, int64) error
	}{
		{
			name: "completed",
			transition: func(db *Database, executionID int64) error {
				for step := 1; step <= 2; step++ {
					if err := db.AdvanceSessionExecution(ctx, executionID, step); err != nil {
						return err
					}
				}
				return db.CompleteSessionExecution(ctx, executionID)
			},
		},
		{
			name: "failed",
			transition: func(db *Database, executionID int64) error {
				if err := db.AdvanceSessionExecution(ctx, executionID, 1); err != nil {
					return err
				}
				return db.FailSessionExecution(ctx, executionID)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openTestDatabase(t)
			session, err := db.CreateSession(ctx, "Terminal")
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			execution, err := db.StartSessionExecution(ctx, session.ID, 2)
			if err != nil {
				t.Fatalf("StartSessionExecution: %v", err)
			}
			if err := testCase.transition(db, execution.ID); err != nil {
				t.Fatalf("transition: %v", err)
			}

			eventsBefore, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
			if err != nil {
				t.Fatalf("ListSessionEvents: %v", err)
			}

			result, err := db.CancelSessionExecution(ctx, session.ID, "too late")
			if err != nil {
				t.Fatalf("CancelSessionExecution: %v", err)
			}
			if result.Outcome != CancellationRejectedTerminal {
				t.Fatalf("outcome = %q, want %q", result.Outcome, CancellationRejectedTerminal)
			}

			eventsAfter, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
			if err != nil {
				t.Fatalf("ListSessionEvents after: %v", err)
			}
			if len(eventsAfter) != len(eventsBefore) {
				t.Fatalf("event count changed on rejected cancel: %d -> %d", len(eventsBefore), len(eventsAfter))
			}
			if hasEventType(eventsAfter, EventTypeExecutionCanceled) {
				t.Fatal("rejected cancel appended an execution.canceled event")
			}
		})
	}
}

func TestCancelSessionExecutionMissingExecution(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "No execution")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := db.CancelSessionExecution(ctx, session.ID, ""); err == nil {
		t.Fatal("cancel accepted a session with no execution")
	}
}

func TestCancelSessionExecutionMissingSession(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	if _, err := db.CancelSessionExecution(ctx, 999999, ""); err == nil {
		t.Fatal("cancel accepted a missing session")
	}
}

func TestCancelSessionExecutionTruncatesReason(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Long reason")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := db.StartSessionExecution(ctx, session.ID, 5); err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	longReason := strings.Repeat("x", MaxCancelReasonLength+50)
	result, err := db.CancelSessionExecution(ctx, session.ID, longReason)
	if err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}
	if result.Execution.CancelReason == nil {
		t.Fatal("cancel reason missing")
	}
	if len(*result.Execution.CancelReason) != MaxCancelReasonLength {
		t.Fatalf("reason length = %d, want %d", len(*result.Execution.CancelReason), MaxCancelReasonLength)
	}
}

func TestCancelSessionExecutionPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	db, err := OpenAt(ctx, dir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	session, err := db.CreateSession(ctx, "Restart cancel")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := db.StartSessionExecution(ctx, session.ID, 5); err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	if _, err := db.CancelSessionExecution(ctx, session.ID, "before restart"); err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenAt(ctx, dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	sessionState, latest, err := reopened.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if sessionState.State != SessionStateCanceled {
		t.Fatalf("session state = %q, want %q", sessionState.State, SessionStateCanceled)
	}
	if latest == nil || latest.Status != ExecutionStatusCanceled {
		t.Fatalf("execution status = %#v, want %q", latest, ExecutionStatusCanceled)
	}
	if latest.CancelReason == nil || *latest.CancelReason != "before restart" {
		t.Fatalf("cancel reason = %v, want %q", latest.CancelReason, "before restart")
	}
	if latest.CanceledAt == nil {
		t.Fatal("canceled_at lost across reopen")
	}
}

func TestCancelSessionExecutionEventFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Rollback cancel")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	if err := db.AdvanceSessionExecution(ctx, execution.ID, 1); err != nil {
		t.Fatalf("advance to step 1: %v", err)
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

	if _, err := db.CancelSessionExecution(ctx, session.ID, "should roll back"); err == nil {
		t.Fatal("expected error caused by event insertion failure")
	}

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if sessionState.State != SessionStateRunning {
		t.Fatalf("session state = %q, want %q after rollback", sessionState.State, SessionStateRunning)
	}
	if latest.Status != ExecutionStatusRunning {
		t.Fatalf("execution status = %q, want %q after rollback", latest.Status, ExecutionStatusRunning)
	}
	if latest.CanceledAt != nil || latest.CancelReason != nil {
		t.Fatalf("cancellation fields set despite rollback: %#v", latest)
	}

	if _, err := db.db.ExecContext(ctx, `DROP TRIGGER fail_session_event_insert`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	result, err := db.CancelSessionExecution(ctx, session.ID, "retry")
	if err != nil {
		t.Fatalf("CancelSessionExecution after trigger removed: %v", err)
	}
	if result.Outcome != CancellationApplied {
		t.Fatalf("outcome = %q, want %q", result.Outcome, CancellationApplied)
	}
}

func TestCancelSessionExecutionTrimsReasonWhitespace(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Whitespace reason")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := db.StartSessionExecution(ctx, session.ID, 5); err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	result, err := db.CancelSessionExecution(ctx, session.ID, "   ")
	if err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}
	if result.Execution.CancelReason != nil {
		t.Fatalf("whitespace-only reason was recorded: %q", *result.Execution.CancelReason)
	}
}

func TestAnswerAfterCancellationRejected(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, _, interaction := startPausedExecution(t, ctx, db, 5, 1)

	if _, err := db.CancelSessionExecution(ctx, session.ID, "stop before answer"); err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}

	_, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "too late")
	if err == nil {
		t.Fatal("AnswerHumanInput accepted an answer for a canceled execution")
	}
	if applied {
		t.Fatal("rejected answer reported applied")
	}
	if !strings.Contains(err.Error(), ExecutionStatusCanceled) {
		t.Fatalf("error = %q, want mention of %s", err, ExecutionStatusCanceled)
	}

	reloaded, err := db.GetHumanInteraction(ctx, interaction.ID)
	if err != nil {
		t.Fatalf("GetHumanInteraction: %v", err)
	}
	if reloaded.Status != InteractionStatusPending {
		t.Fatalf(
			"interaction status = %q, want %q preserved for inspection",
			reloaded.Status,
			InteractionStatusPending,
		)
	}
	if reloaded.Answer != nil || reloaded.AnsweredAt != nil {
		t.Fatalf("rejected answer was recorded: %#v", reloaded)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	if hasEventType(events, EventTypeInteractionAnswered) {
		t.Fatal("rejected answer appended interaction.answered")
	}
	if !hasEventType(events, EventTypeExecutionCanceled) {
		t.Fatal("missing execution.canceled event")
	}
}

func TestAnswerCommittedBeforeCancellationIsPreserved(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, _, interaction := startPausedExecution(t, ctx, db, 5, 1)

	if _, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "yes continue"); err != nil || !applied {
		t.Fatalf("AnswerHumanInput: applied=%v err=%v", applied, err)
	}

	if _, err := db.CancelSessionExecution(ctx, session.ID, "after answer"); err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}

	reloaded, err := db.GetHumanInteraction(ctx, interaction.ID)
	if err != nil {
		t.Fatalf("GetHumanInteraction: %v", err)
	}
	if reloaded.Status != InteractionStatusAnswered {
		t.Fatalf("interaction status = %q, want %q", reloaded.Status, InteractionStatusAnswered)
	}
	if reloaded.Answer == nil || *reloaded.Answer != "yes continue" {
		t.Fatalf("committed answer was altered: %#v", reloaded.Answer)
	}

	// A duplicate answer after cancellation stays idempotent and must not
	// replace the committed answer.
	again, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "replacement")
	if err != nil {
		t.Fatalf("duplicate AnswerHumanInput: %v", err)
	}
	if applied {
		t.Fatal("duplicate answer after cancellation was applied")
	}
	if again.Answer == nil || *again.Answer != "yes continue" {
		t.Fatalf("committed answer replaced: %#v", again.Answer)
	}

	_, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if latest == nil || latest.Status != ExecutionStatusCanceled {
		t.Fatalf("execution = %#v, want CANCELED", latest)
	}
}

// TestConcurrentAnswerAndCancelResolveDeterministically races AnswerHumanInput
// against CancelSessionExecution using the real production methods. Whichever
// transaction commits first wins: an answer committed first is preserved and
// cancellation still succeeds, while a cancellation committed first makes the
// answer fail with the interaction left PENDING. No partial state is allowed.
func TestConcurrentAnswerAndCancelResolveDeterministically(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	const rounds = 10

	for round := 0; round < rounds; round++ {
		session, _, interaction := startPausedExecution(t, ctx, db, 5, 1)

		var (
			wait      sync.WaitGroup
			answerErr error
			applied   bool
			cancelErr error
			cancelOut CancellationOutcome
		)

		start := make(chan struct{})
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			_, applied, answerErr = db.AnswerHumanInput(ctx, interaction.ID, "race answer")
		}()
		go func() {
			defer wait.Done()
			<-start
			result, err := db.CancelSessionExecution(ctx, session.ID, "race cancel")
			cancelErr = err
			cancelOut = result.Outcome
		}()
		close(start)
		wait.Wait()

		if cancelErr != nil {
			t.Fatalf("round %d cancel error: %v", round, cancelErr)
		}
		if cancelOut != CancellationApplied {
			t.Fatalf("round %d cancel outcome = %q, want %q", round, cancelOut, CancellationApplied)
		}

		reloaded, err := db.GetHumanInteraction(ctx, interaction.ID)
		if err != nil {
			t.Fatalf("round %d GetHumanInteraction: %v", round, err)
		}

		wantAnsweredEvents := 0
		switch {
		case answerErr == nil && applied:
			// Answer committed first.
			if reloaded.Status != InteractionStatusAnswered {
				t.Fatalf("round %d answer-first status = %q", round, reloaded.Status)
			}
			if reloaded.Answer == nil || *reloaded.Answer != "race answer" {
				t.Fatalf("round %d answer-first answer = %#v", round, reloaded.Answer)
			}
			wantAnsweredEvents = 1
		case answerErr != nil && !applied:
			// Cancellation committed first.
			if reloaded.Status != InteractionStatusPending {
				t.Fatalf("round %d cancel-first status = %q, want PENDING", round, reloaded.Status)
			}
			if reloaded.Answer != nil || reloaded.AnsweredAt != nil {
				t.Fatalf("round %d cancel-first answer recorded: %#v", round, reloaded)
			}
		default:
			t.Fatalf("round %d inconsistent answer result: applied=%v err=%v", round, applied, answerErr)
		}

		_, latest, err := db.SessionExecutionStatus(ctx, session.ID)
		if err != nil {
			t.Fatalf("round %d SessionExecutionStatus: %v", round, err)
		}
		if latest == nil || latest.Status != ExecutionStatusCanceled {
			t.Fatalf("round %d execution = %#v, want CANCELED", round, latest)
		}

		events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
		if err != nil {
			t.Fatalf("round %d ListSessionEvents: %v", round, err)
		}
		answeredEvents := 0
		for _, event := range events {
			if event.Type == EventTypeInteractionAnswered {
				answeredEvents++
			}
		}
		if answeredEvents != wantAnsweredEvents {
			t.Fatalf(
				"round %d interaction.answered events = %d, want %d",
				round,
				answeredEvents,
				wantAnsweredEvents,
			)
		}
	}
}
