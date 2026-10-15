package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

func TestWorkerGroupClaimReleaseAndCancel(t *testing.T) {
	var workers workerGroup

	canceled := false
	claimed := workers.claim(1, func() { canceled = true })
	if !claimed {
		t.Fatal("first claim was rejected")
	}
	if workers.ownerCount() != 1 {
		t.Fatalf("owner count = %d, want 1", workers.ownerCount())
	}

	if workers.claim(1, func() {}) {
		t.Fatal("duplicate claim for the same execution was accepted")
	}

	if !workers.cancel(1) {
		t.Fatal("cancel did not find the owner")
	}
	if !canceled {
		t.Fatal("cancel did not invoke the registered cancel function")
	}
	if workers.cancel(2) {
		t.Fatal("cancel reported an owner for an unclaimed execution")
	}

	workers.release(1)
	if workers.ownerCount() != 0 {
		t.Fatalf("owner count = %d, want 0 after release", workers.ownerCount())
	}
}

func TestWorkerGroupStopAndWaitCancelsOwnersAndRejectsNewWork(t *testing.T) {
	var workers workerGroup

	canceled := false
	if !workers.claim(7, func() { canceled = true }) {
		t.Fatal("claim was rejected")
	}

	workers.stopAndWait()

	if !canceled {
		t.Fatal("stopAndWait did not cancel the owned worker")
	}
	if workers.start() {
		t.Fatal("start succeeded after stopAndWait")
	}
	if workers.claim(8, func() {}) {
		t.Fatal("claim succeeded after stopAndWait")
	}
}

func TestHandleWorkerStopLeavesCanceledExecutionUntouched(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	session, err := db.CreateSession(ctx, "canceled stop")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	if _, err := db.CancelSessionExecution(ctx, session.ID, "stop"); err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}

	// The worker exits with a canceled context after the execution was already
	// canceled: it must not overwrite CANCELED with INTERRUPTED.
	stopCtx, cancel := context.WithCancel(ctx)
	cancel()
	handleWorkerStop(db, execution, stopCtx)

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if latest == nil || latest.Status != state.ExecutionStatusCanceled {
		t.Fatalf("execution status = %#v, want CANCELED", latest)
	}
	if sessionState.State != state.SessionStateCanceled {
		t.Fatalf("session state = %q, want %q", sessionState.State, state.SessionStateCanceled)
	}
}

func TestHandleWorkerStopInterruptsWhenContextCanceled(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	session, err := db.CreateSession(ctx, "interrupt stop")
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

	stopCtx, cancel := context.WithCancel(ctx)
	cancel()
	handleWorkerStop(db, execution, stopCtx)

	_, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if latest == nil || latest.Status != state.ExecutionStatusInterrupted {
		t.Fatalf("execution status = %#v, want INTERRUPTED", latest)
	}
	if latest.CurrentStep != 1 {
		t.Fatalf("current step = %d, want 1", latest.CurrentStep)
	}
}

func TestHandleWorkerStopFailsWhenWorkerErrored(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	session, err := db.CreateSession(ctx, "fail stop")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	// A live (non-canceled) context means the worker stopped due to an error.
	handleWorkerStop(db, execution, ctx)

	_, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if latest == nil || latest.Status != state.ExecutionStatusFailed {
		t.Fatalf("execution status = %#v, want FAILED", latest)
	}
}

func TestSessionCancelIPCAppliesAndIsIdempotent(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	workers := &workerGroup{}

	session, err := db.CreateSession(ctx, "ipc cancel")
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

	applied := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-cancel",
		SessionID: session.ID,
		Reason:    "not needed",
	})
	if !applied.OK {
		t.Fatalf("session-cancel = %#v", applied)
	}
	if !applied.Canceled {
		t.Fatal("canceled flag not set")
	}
	if applied.CancelOutcome != string(state.CancellationApplied) {
		t.Fatalf("outcome = %q, want %q", applied.CancelOutcome, state.CancellationApplied)
	}
	if applied.Session == nil || applied.Session.State != state.SessionStateCanceled {
		t.Fatalf("session = %#v, want CANCELED", applied.Session)
	}
	if applied.Execution == nil || applied.Execution.Status != state.ExecutionStatusCanceled {
		t.Fatalf("execution = %#v, want CANCELED", applied.Execution)
	}
	if applied.Execution.CancelReason == nil || *applied.Execution.CancelReason != "not needed" {
		t.Fatalf("cancel reason = %v, want %q", applied.Execution.CancelReason, "not needed")
	}

	repeat := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-cancel",
		SessionID: session.ID,
	})
	if !repeat.OK {
		t.Fatalf("second session-cancel = %#v", repeat)
	}
	if repeat.CancelOutcome != string(state.CancellationAlreadyCanceled) {
		t.Fatalf("second outcome = %q, want %q", repeat.CancelOutcome, state.CancellationAlreadyCanceled)
	}

	workers.stopAndWait()
}

func TestSessionCancelIPCRejectsTerminalExecution(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	workers := &workerGroup{}

	session, err := db.CreateSession(ctx, "ipc terminal")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 2)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	for step := 1; step <= 2; step++ {
		if err := db.AdvanceSessionExecution(ctx, execution.ID, step); err != nil {
			t.Fatalf("advance: %v", err)
		}
	}
	if err := db.CompleteSessionExecution(ctx, execution.ID); err != nil {
		t.Fatalf("CompleteSessionExecution: %v", err)
	}

	response := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-cancel",
		SessionID: session.ID,
	})
	if response.OK {
		t.Fatalf("terminal session-cancel unexpectedly succeeded: %#v", response)
	}
	if response.CancelOutcome != string(state.CancellationRejectedTerminal) {
		t.Fatalf("outcome = %q, want %q", response.CancelOutcome, state.CancellationRejectedTerminal)
	}

	workers.stopAndWait()
}

func TestSessionCancelIPCMissingExecution(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	workers := &workerGroup{}

	session, err := db.CreateSession(ctx, "ipc no execution")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	response := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-cancel",
		SessionID: session.ID,
	})
	if response.OK {
		t.Fatalf("session-cancel without execution unexpectedly succeeded: %#v", response)
	}

	workers.stopAndWait()
}

// TestSessionCancelStopsRunningWorker verifies the end-to-end daemon path: a
// running worker is signaled by session-cancel, and the durable state remains
// CANCELED rather than being overwritten by the departing worker.
func TestSessionCancelStopsRunningWorker(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workers := &workerGroup{}

	start := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command: "session-start",
		Title:   "cancel running worker",
	})
	if !start.OK || start.Session == nil {
		t.Fatalf("session-start = %#v", start)
	}
	sessionID := start.Session.ID

	run := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-run",
		SessionID: sessionID,
	})
	if !run.OK || run.Execution == nil {
		t.Fatalf("session-run = %#v", run)
	}
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusRunning, 6*time.Second)
	if workers.ownerCount() != 1 {
		t.Fatalf("owner count = %d, want 1", workers.ownerCount())
	}

	response := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-cancel",
		SessionID: sessionID,
		Reason:    "stop now",
	})
	if !response.OK || response.CancelOutcome != string(state.CancellationApplied) {
		t.Fatalf("session-cancel = %#v", response)
	}

	workers.stopAndWait()

	sessionState, latest, err := db.SessionExecutionStatus(ctx, sessionID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if latest == nil || latest.Status != state.ExecutionStatusCanceled {
		t.Fatalf("execution status = %#v, want CANCELED", latest)
	}
	if sessionState.State != state.SessionStateCanceled {
		t.Fatalf("session state = %q, want %q", sessionState.State, state.SessionStateCanceled)
	}
	if workers.ownerCount() != 0 {
		t.Fatalf("owner count = %d, want 0 after worker exit", workers.ownerCount())
	}

	// A canceled session must not be resumable or restartable.
	_, err = db.ResumeSessionExecution(ctx, sessionID)
	if err == nil {
		t.Fatal("canceled session unexpectedly resumed")
	}
}

// TestInteractionAnswerIPCRejectedAfterCancellation verifies that answering a
// PENDING interaction through IPC is refused once its execution is canceled,
// while the interaction and its PENDING status are preserved and no
// interaction.answered event is appended.
func TestInteractionAnswerIPCRejectedAfterCancellation(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	workers := &workerGroup{}

	session, _, interaction := seedWaitingSession(t, db, 1)

	cancel := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-cancel",
		SessionID: session.ID,
		Reason:    "stop",
	})
	if !cancel.OK || cancel.CancelOutcome != string(state.CancellationApplied) {
		t.Fatalf("session-cancel response = %#v", cancel)
	}

	answer := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:       "interaction-answer",
		InteractionID: interaction.ID,
		Answer:        "too late",
	})
	if answer.OK {
		t.Fatalf("interaction-answer on canceled execution unexpectedly succeeded: %#v", answer)
	}
	if !strings.Contains(answer.Message, state.ExecutionStatusCanceled) {
		t.Fatalf(
			"answer rejection message = %q, want mention of %s",
			answer.Message,
			state.ExecutionStatusCanceled,
		)
	}

	found := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:       "interaction-get",
		InteractionID: interaction.ID,
	})
	if !found.OK || found.Interaction == nil {
		t.Fatalf("interaction-get response = %#v", found)
	}
	if found.Interaction.Status != state.InteractionStatusPending {
		t.Fatalf(
			"interaction status = %q, want %q preserved",
			found.Interaction.Status,
			state.InteractionStatusPending,
		)
	}
	if found.Interaction.Answer != nil {
		t.Fatalf("rejected answer was recorded: %#v", found.Interaction.Answer)
	}

	events := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-events",
		SessionID: session.ID,
	})
	if !events.OK {
		t.Fatalf("session-events response = %#v", events)
	}
	answered := 0
	canceled := 0
	for _, event := range events.Events {
		switch event.Type {
		case state.EventTypeInteractionAnswered:
			answered++
		case state.EventTypeExecutionCanceled:
			canceled++
		}
	}
	if answered != 0 {
		t.Fatalf("interaction.answered events = %d, want 0", answered)
	}
	if canceled != 1 {
		t.Fatalf("execution.canceled events = %d, want 1", canceled)
	}

	workers.stopAndWait()
}
