package state

import (
	"context"
	"sync"
	"testing"
)

func startPausedExecution(
	t *testing.T,
	ctx context.Context,
	db *Database,
	steps int,
	advanceTo int,
) (Session, SessionExecution, HumanInteraction) {
	t.Helper()

	session, err := db.CreateSession(ctx, "human interaction")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	execution, err := db.StartSessionExecution(ctx, session.ID, steps)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	for step := 1; step <= advanceTo; step++ {
		if err := db.AdvanceSessionExecution(ctx, execution.ID, step); err != nil {
			t.Fatalf("advance to step %d: %v", step, err)
		}
	}

	interaction, created, err := db.RequestHumanInput(
		ctx,
		execution.ID,
		"Continue after the checkpoint?",
		map[string]any{"checkpoint": advanceTo},
	)
	if err != nil {
		t.Fatalf("RequestHumanInput: %v", err)
	}
	if !created {
		t.Fatal("RequestHumanInput reported an existing interaction on first call")
	}

	return session, execution, interaction
}

func TestRequestHumanInputTransitionsSessionAndExecutionAtomically(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, execution, interaction := startPausedExecution(t, ctx, db, 5, 1)

	if interaction.Status != InteractionStatusPending {
		t.Fatalf("interaction status = %q, want %q", interaction.Status, InteractionStatusPending)
	}
	if interaction.ID == 0 {
		t.Fatal("interaction ID was not assigned")
	}
	if interaction.ExecutionID != execution.ID || interaction.SessionID != session.ID {
		t.Fatalf("interaction ownership = %d/%d, want %d/%d",
			interaction.SessionID, interaction.ExecutionID, session.ID, execution.ID)
	}
	if interaction.Answer != nil || interaction.AnsweredAt != nil {
		t.Fatal("pending interaction unexpectedly has an answer")
	}
	if interaction.Question == "" {
		t.Fatal("pending interaction has an empty question")
	}
	if interaction.Context["checkpoint"] != float64(1) {
		t.Fatalf("interaction context checkpoint = %#v", interaction.Context["checkpoint"])
	}

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if sessionState.State != SessionStateWaitingForUser {
		t.Fatalf("session state = %q, want %q", sessionState.State, SessionStateWaitingForUser)
	}
	if latest == nil || latest.Status != ExecutionStatusWaitingForUser {
		t.Fatalf("execution = %#v, want WAITING_FOR_USER", latest)
	}
	if latest.CurrentStep != 1 {
		t.Fatalf("checkpoint not preserved: current step = %d, want 1", latest.CurrentStep)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	if !hasEventType(events, EventTypeInteractionRequested) {
		t.Fatal("missing interaction.requested event")
	}
	if !hasEventType(events, EventTypeExecutionWaitingForUser) {
		t.Fatal("missing execution.waiting_for_user event")
	}
}

func TestRequestHumanInputIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	_, execution, first := startPausedExecution(t, ctx, db, 5, 1)

	second, created, err := db.RequestHumanInput(ctx, execution.ID, "Different question?", nil)
	if err != nil {
		t.Fatalf("second RequestHumanInput: %v", err)
	}
	if created {
		t.Fatal("second RequestHumanInput created a duplicate interaction")
	}
	if second.ID != first.ID {
		t.Fatalf("duplicate request returned interaction %d, want %d", second.ID, first.ID)
	}
	if second.Question != first.Question {
		t.Fatal("duplicate request silently replaced the existing question")
	}

	var count int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM human_interactions WHERE execution_id = ?`,
		execution.ID,
	).Scan(&count); err != nil {
		t.Fatalf("count interactions: %v", err)
	}
	if count != 1 {
		t.Fatalf("interaction count = %d, want 1", count)
	}

	events, _, err := db.ListSessionEvents(ctx, first.SessionID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	requested := 0
	for _, e := range events {
		if e.Type == EventTypeInteractionRequested {
			requested++
		}
	}
	if requested != 1 {
		t.Fatalf("interaction.requested events = %d, want 1", requested)
	}
}

func TestOnlyOnePendingInteractionPerExecution(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	_, execution, interaction := startPausedExecution(t, ctx, db, 5, 1)

	_, err := db.db.ExecContext(ctx, `
INSERT INTO human_interactions (
	session_id, execution_id, question, status, created_at
)
VALUES (?, ?, 'Second pending', ?, ?)
`,
		interaction.SessionID,
		execution.ID,
		InteractionStatusPending,
		"2026-10-06T00:00:00Z",
	)
	if err == nil {
		t.Fatal("second PENDING interaction for the same execution unexpectedly inserted")
	}

	var count int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM human_interactions WHERE execution_id = ? AND status = ?`,
		execution.ID,
		InteractionStatusPending,
	).Scan(&count); err != nil {
		t.Fatalf("count pending interactions: %v", err)
	}
	if count != 1 {
		t.Fatalf("pending interaction count = %d, want 1", count)
	}
}

func TestAnswerHumanInputPersistsAnswer(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, _, interaction := startPausedExecution(t, ctx, db, 5, 1)

	answered, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "yes, proceed")
	if err != nil {
		t.Fatalf("AnswerHumanInput: %v", err)
	}
	if !applied {
		t.Fatal("first answer was not applied")
	}
	if answered.Status != InteractionStatusAnswered {
		t.Fatalf("status = %q, want %q", answered.Status, InteractionStatusAnswered)
	}
	if answered.Answer == nil || *answered.Answer != "yes, proceed" {
		t.Fatalf("answer = %#v", answered.Answer)
	}
	if answered.AnsweredAt == nil {
		t.Fatal("answered_at was not recorded")
	}

	reloaded, err := db.GetHumanInteraction(ctx, interaction.ID)
	if err != nil {
		t.Fatalf("GetHumanInteraction: %v", err)
	}
	if reloaded.Status != InteractionStatusAnswered || reloaded.Answer == nil || *reloaded.Answer != "yes, proceed" {
		t.Fatalf("reloaded interaction = %#v", reloaded)
	}

	// The execution stays WAITING_FOR_USER until an explicit resume.
	_, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if latest == nil || latest.Status != ExecutionStatusWaitingForUser {
		t.Fatalf("execution after answer = %#v, want WAITING_FOR_USER", latest)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	if !hasEventType(events, EventTypeInteractionAnswered) {
		t.Fatal("missing interaction.answered event")
	}
}

func TestAnswerHumanInputDuplicateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, _, interaction := startPausedExecution(t, ctx, db, 5, 1)

	first, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "first answer")
	if err != nil || !applied {
		t.Fatalf("first answer: applied=%v err=%v", applied, err)
	}

	second, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "second answer")
	if err != nil {
		t.Fatalf("duplicate answer: %v", err)
	}
	if applied {
		t.Fatal("duplicate answer was applied")
	}
	if second.Answer == nil || *second.Answer != "first answer" {
		t.Fatalf("answer was replaced: %#v", second.Answer)
	}
	if first.Answer == nil || *first.Answer != *second.Answer {
		t.Fatal("answers differ after duplicate submission")
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	answered := 0
	for _, e := range events {
		if e.Type == EventTypeInteractionAnswered {
			answered++
		}
	}
	if answered != 1 {
		t.Fatalf("interaction.answered events = %d, want 1", answered)
	}
}

func TestResumeAfterHumanInputRejectsUnanswered(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, _, _ := startPausedExecution(t, ctx, db, 5, 1)

	if _, err := db.ResumeAfterHumanInput(ctx, session.ID); err == nil {
		t.Fatal("resume succeeded before the interaction was answered")
	}

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if sessionState.State != SessionStateWaitingForUser {
		t.Fatalf("session state = %q, want %q", sessionState.State, SessionStateWaitingForUser)
	}
	if latest == nil || latest.Status != ExecutionStatusWaitingForUser || latest.CurrentStep != 1 {
		t.Fatalf("execution changed unexpectedly: %#v", latest)
	}
}

func TestResumeAfterHumanInputPreservesCheckpointAndAvoidsDuplicateCheckpoints(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, execution, interaction := startPausedExecution(t, ctx, db, 5, 1)

	if _, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "go"); err != nil || !applied {
		t.Fatalf("AnswerHumanInput: applied=%v err=%v", applied, err)
	}

	resumed, err := db.ResumeAfterHumanInput(ctx, session.ID)
	if err != nil {
		t.Fatalf("ResumeAfterHumanInput: %v", err)
	}
	if resumed.ID != execution.ID {
		t.Fatalf("resumed execution %d, want %d", resumed.ID, execution.ID)
	}
	if resumed.Status != ExecutionStatusRunning {
		t.Fatalf("resumed status = %q, want %q", resumed.Status, ExecutionStatusRunning)
	}
	if resumed.CurrentStep != 1 {
		t.Fatalf("resumed current step = %d, want 1 (checkpoint preserved)", resumed.CurrentStep)
	}

	for step := 2; step <= 5; step++ {
		if err := db.AdvanceSessionExecution(ctx, execution.ID, step); err != nil {
			t.Fatalf("advance to step %d: %v", step, err)
		}
	}
	if err := db.CompleteSessionExecution(ctx, execution.ID); err != nil {
		t.Fatalf("CompleteSessionExecution: %v", err)
	}

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if sessionState.State != SessionStateCompleted {
		t.Fatalf("session state = %q, want %q", sessionState.State, SessionStateCompleted)
	}
	if latest == nil || latest.Status != ExecutionStatusCompleted || latest.CurrentStep != 5 {
		t.Fatalf("final execution = %#v", latest)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	checkpoints := map[int]int{}
	for _, e := range events {
		if e.Type == EventTypeExecutionCheckpoint {
			step, ok := e.Details["step"].(float64)
			if !ok {
				t.Fatalf("checkpoint event missing step: %#v", e.Details)
			}
			checkpoints[int(step)]++
		}
	}
	if len(checkpoints) != 5 {
		t.Fatalf("distinct checkpoint steps = %d, want 5 (%v)", len(checkpoints), checkpoints)
	}
	for step := 1; step <= 5; step++ {
		if checkpoints[step] != 1 {
			t.Fatalf("checkpoint step %d emitted %d times, want 1", step, checkpoints[step])
		}
	}
	if !hasEventType(events, EventTypeExecutionResumedAfterInput) {
		t.Fatal("missing execution.resumed_after_input event")
	}
}

func TestInvalidInteractionAndSessionIDs(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	if _, _, err := db.RequestHumanInput(ctx, 999999, "question", nil); err == nil {
		t.Fatal("RequestHumanInput accepted a missing execution")
	}
	if _, _, err := db.RequestHumanInput(ctx, 1, "   ", nil); err == nil {
		t.Fatal("RequestHumanInput accepted an empty question")
	}
	if _, _, err := db.AnswerHumanInput(ctx, 999999, "answer"); err == nil {
		t.Fatal("AnswerHumanInput accepted a missing interaction")
	}
	if _, err := db.GetHumanInteraction(ctx, 999999); err == nil {
		t.Fatal("GetHumanInteraction accepted a missing interaction")
	}
	if _, err := db.ResumeAfterHumanInput(ctx, 999999); err == nil {
		t.Fatal("ResumeAfterHumanInput accepted a missing session")
	}
	if _, err := db.GetPendingHumanInput(ctx, 999999); err == nil {
		t.Fatal("GetPendingHumanInput accepted a missing session")
	}
	if _, err := db.ListPendingHumanInteractions(ctx, 999999); err == nil {
		t.Fatal("ListPendingHumanInteractions accepted a missing session")
	}
}

func TestGetPendingHumanInputReturnsNilWhenNone(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "no questions")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	pending, err := db.GetPendingHumanInput(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetPendingHumanInput: %v", err)
	}
	if pending != nil {
		t.Fatalf("expected no pending interaction, got %#v", pending)
	}
}

func TestListPendingHumanInteractionsExcludesAnswered(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, _, first := startPausedExecution(t, ctx, db, 5, 1)
	if _, applied, err := db.AnswerHumanInput(ctx, first.ID, "done"); err != nil || !applied {
		t.Fatalf("AnswerHumanInput: applied=%v err=%v", applied, err)
	}

	pending, err := db.ListPendingHumanInteractions(ctx, session.ID)
	if err != nil {
		t.Fatalf("ListPendingHumanInteractions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("answered interaction still listed as pending: %#v", pending)
	}
}

func TestConcurrentAnswerSubmissionsApplyExactlyOnce(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, _, interaction := startPausedExecution(t, ctx, db, 5, 1)

	const submissions = 8

	start := make(chan struct{})
	var wait sync.WaitGroup
	var (
		mu      sync.Mutex
		applied int
	)

	for i := 0; i < submissions; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			<-start
			_, ok, err := db.AnswerHumanInput(ctx, interaction.ID, "answer")
			if err != nil {
				t.Errorf("AnswerHumanInput: %v", err)
				return
			}
			if ok {
				mu.Lock()
				applied++
				mu.Unlock()
			}
		}(i)
	}

	close(start)
	wait.Wait()

	if applied != 1 {
		t.Fatalf("applied answer count = %d, want 1", applied)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	answered := 0
	for _, e := range events {
		if e.Type == EventTypeInteractionAnswered {
			answered++
		}
	}
	if answered != 1 {
		t.Fatalf("interaction.answered events = %d, want 1", answered)
	}
}

func TestConcurrentResumeAttemptsSucceedExactlyOnce(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, _, interaction := startPausedExecution(t, ctx, db, 5, 1)
	if _, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "go"); err != nil || !applied {
		t.Fatalf("AnswerHumanInput: applied=%v err=%v", applied, err)
	}

	const attempts = 8

	start := make(chan struct{})
	var wait sync.WaitGroup
	var (
		mu        sync.Mutex
		succeeded int
	)

	for i := 0; i < attempts; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			if _, err := db.ResumeAfterHumanInput(ctx, session.ID); err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}()
	}

	close(start)
	wait.Wait()

	if succeeded != 1 {
		t.Fatalf("successful resumes = %d, want 1", succeeded)
	}

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	resumed := 0
	for _, e := range events {
		if e.Type == EventTypeExecutionResumedAfterInput {
			resumed++
		}
	}
	if resumed != 1 {
		t.Fatalf("execution.resumed_after_input events = %d, want 1", resumed)
	}
}

func TestInteractionPersistenceAcrossCloseReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("FORGEHAND_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", dir)

	db, err := Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	session, _, interaction := startPausedExecution(t, ctx, db, 5, 1)
	if _, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "persisted answer"); err != nil || !applied {
		t.Fatalf("AnswerHumanInput: applied=%v err=%v", applied, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err = Open(context.Background())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	reloaded, err := db.GetHumanInteraction(ctx, interaction.ID)
	if err != nil {
		t.Fatalf("GetHumanInteraction after reopen: %v", err)
	}
	if reloaded.Status != InteractionStatusAnswered || reloaded.Answer == nil || *reloaded.Answer != "persisted answer" {
		t.Fatalf("reloaded interaction = %#v", reloaded)
	}

	// A waiting execution survives reopen and remains resumable.
	resumed, err := db.ResumeAfterHumanInput(ctx, session.ID)
	if err != nil {
		t.Fatalf("ResumeAfterHumanInput after reopen: %v", err)
	}
	if resumed.CurrentStep != 1 {
		t.Fatalf("resumed current step = %d, want 1", resumed.CurrentStep)
	}
}

func TestInteractionForeignKeyIsEnforced(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	_, err := db.db.ExecContext(ctx, `
INSERT INTO human_interactions (
	session_id, execution_id, question, status, created_at
)
VALUES (?, ?, 'orphan', ?, ?)
`, 999999, 999999, InteractionStatusPending, "2026-10-06T00:00:00Z")
	if err == nil {
		t.Fatal("interaction with missing session/execution unexpectedly inserted")
	}
}
