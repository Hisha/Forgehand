package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

func seedWaitingSession(
	t *testing.T,
	db *state.Database,
	advanceTo int,
) (state.Session, state.SessionExecution, state.HumanInteraction) {
	t.Helper()

	ctx := context.Background()

	session, err := db.CreateSession(ctx, "waiting session")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, fakeWorkerSteps)
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
		"Continue after checkpoint?",
		map[string]any{"step": advanceTo},
	)
	if err != nil {
		t.Fatalf("RequestHumanInput: %v", err)
	}
	if !created {
		t.Fatal("RequestHumanInput did not create an interaction")
	}

	return session, execution, interaction
}

func waitForExecutionStatus(
	t *testing.T,
	db *state.Database,
	sessionID int64,
	want string,
	timeout time.Duration,
) state.SessionExecution {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		_, latest, err := db.SessionExecutionStatus(context.Background(), sessionID)
		if err != nil {
			t.Fatalf("SessionExecutionStatus: %v", err)
		}
		if latest != nil && latest.Status == want {
			return *latest
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; latest = %#v", want, latest)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func checkpointSteps(t *testing.T, db *state.Database, sessionID int64) map[int]int {
	t.Helper()

	events, _, err := db.ListSessionEvents(context.Background(), sessionID, 0, 1000)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	steps := map[int]int{}
	for _, event := range events {
		if event.Type != state.EventTypeExecutionCheckpoint {
			continue
		}
		step, ok := event.Details["step"].(float64)
		if !ok {
			t.Fatalf("checkpoint event without step: %#v", event.Details)
		}
		steps[int(step)]++
	}
	return steps
}

// TestFakeWorkerPauseReleasesCapacityAndResumesWithoutDuplicateCheckpoints
// runs a paused worker, proves it consummated no active worker capacity while
// waiting, then resumes it and confirms no checkpoint is repeated.
func TestFakeWorkerPauseReleasesCapacityAndResumesWithoutDuplicateCheckpoints(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	session, err := db.CreateSession(ctx, "paused")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, fakeWorkerSteps)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	var workers workerGroup
	if !workers.start() {
		t.Fatal("worker group refused to start")
	}
	go func() {
		defer workers.done()
		runFakeWorkerWithPause(ctx, db, execution, 0, 2)
	}()

	latest := waitForExecutionStatus(t, db, session.ID, state.ExecutionStatusWaitingForUser, 3*time.Second)
	if latest.CurrentStep != 2 {
		t.Fatalf("waiting current step = %d, want 2", latest.CurrentStep)
	}

	// The worker goroutine must have exited, releasing its capacity.
	released := make(chan struct{})
	go func() {
		workers.stopAndWait()
		close(released)
	}()
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("waiting worker did not release worker capacity")
	}

	pending, err := db.GetPendingHumanInput(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetPendingHumanInput: %v", err)
	}
	if pending == nil || pending.Status != state.InteractionStatusPending {
		t.Fatalf("pending interaction = %#v", pending)
	}

	if _, applied, err := db.AnswerHumanInput(ctx, pending.ID, "continue"); err != nil || !applied {
		t.Fatalf("AnswerHumanInput: applied=%v err=%v", applied, err)
	}
	resumed, err := db.ResumeAfterHumanInput(ctx, session.ID)
	if err != nil {
		t.Fatalf("ResumeAfterHumanInput: %v", err)
	}
	if resumed.CurrentStep != 2 {
		t.Fatalf("resumed current step = %d, want 2", resumed.CurrentStep)
	}

	runFakeWorkerWithPause(ctx, db, resumed, 0, 0)

	_, final, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if final == nil || final.Status != state.ExecutionStatusCompleted || final.CurrentStep != fakeWorkerSteps {
		t.Fatalf("final execution = %#v", final)
	}

	steps := checkpointSteps(t, db, session.ID)
	if len(steps) != fakeWorkerSteps {
		t.Fatalf("distinct checkpoints = %d, want %d (%v)", len(steps), fakeWorkerSteps, steps)
	}
	for step := 1; step <= fakeWorkerSteps; step++ {
		if steps[step] != 1 {
			t.Fatalf("checkpoint step %d emitted %d times, want 1", step, steps[step])
		}
	}
}

// TestFakeWorkerPausesAtFinalCheckpointAndCompletesOnResume covers the boundary
// where the pause step is the last step.
func TestFakeWorkerPausesAtFinalCheckpointAndCompletesOnResume(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	session, err := db.CreateSession(ctx, "pause at end")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, fakeWorkerSteps)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	runFakeWorkerWithPause(ctx, db, execution, 0, fakeWorkerSteps)

	latest := waitForExecutionStatus(t, db, session.ID, state.ExecutionStatusWaitingForUser, time.Second)
	if latest.CurrentStep != fakeWorkerSteps {
		t.Fatalf("waiting current step = %d, want %d", latest.CurrentStep, fakeWorkerSteps)
	}

	pending, err := db.GetPendingHumanInput(ctx, session.ID)
	if err != nil || pending == nil {
		t.Fatalf("GetPendingHumanInput: interaction=%#v err=%v", pending, err)
	}
	if _, applied, err := db.AnswerHumanInput(ctx, pending.ID, "go"); err != nil || !applied {
		t.Fatalf("AnswerHumanInput: applied=%v err=%v", applied, err)
	}
	resumed, err := db.ResumeAfterHumanInput(ctx, session.ID)
	if err != nil {
		t.Fatalf("ResumeAfterHumanInput: %v", err)
	}

	runFakeWorkerWithPause(ctx, db, resumed, 0, 0)

	_, final, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if final == nil || final.Status != state.ExecutionStatusCompleted || final.CurrentStep != fakeWorkerSteps {
		t.Fatalf("final execution = %#v", final)
	}

	steps := checkpointSteps(t, db, session.ID)
	if len(steps) != fakeWorkerSteps {
		t.Fatalf("distinct checkpoints = %d, want %d", len(steps), fakeWorkerSteps)
	}
	for step := 1; step <= fakeWorkerSteps; step++ {
		if steps[step] != 1 {
			t.Fatalf("checkpoint step %d emitted %d times, want 1", step, steps[step])
		}
	}
}

// TestFakeWorkerPauseDisabledByDefault confirms the default worker still runs
// straight through to completion without requesting input.
func TestFakeWorkerPauseDisabledByDefault(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	session, err := db.CreateSession(ctx, "no pause")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, fakeWorkerSteps)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	runFakeWorkerWithPause(ctx, db, execution, 0, 0)

	_, final, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if final == nil || final.Status != state.ExecutionStatusCompleted {
		t.Fatalf("final execution = %#v, want COMPLETED", final)
	}
	pending, err := db.GetPendingHumanInput(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetPendingHumanInput: %v", err)
	}
	if pending != nil {
		t.Fatalf("unexpected pending interaction: %#v", pending)
	}
}

func TestSessionStatusIPCReportsPendingInteraction(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	workers := &workerGroup{}
	defer workers.stopAndWait()

	_, _, interaction := seedWaitingSession(t, db, 1)

	status := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-status",
		SessionID: interaction.SessionID,
	})
	if !status.OK || status.Session == nil {
		t.Fatalf("session-status response = %#v", status)
	}
	if status.Session.State != state.SessionStateWaitingForUser {
		t.Fatalf("session state = %q, want %q", status.Session.State, state.SessionStateWaitingForUser)
	}
	if status.Execution == nil || status.Execution.Status != state.ExecutionStatusWaitingForUser {
		t.Fatalf("execution = %#v, want WAITING_FOR_USER", status.Execution)
	}
	if status.Interaction == nil || status.Interaction.ID != interaction.ID {
		t.Fatalf("interaction = %#v, want ID %d", status.Interaction, interaction.ID)
	}
	if status.Interaction.Question == "" {
		t.Fatal("pending interaction carries no question")
	}

	answered := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:       "interaction-answer",
		InteractionID: interaction.ID,
		Answer:        "yes",
	})
	if !answered.OK || !answered.AnswerApplied {
		t.Fatalf("interaction-answer response = %#v", answered)
	}

	after := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-status",
		SessionID: interaction.SessionID,
	})
	if !after.OK {
		t.Fatalf("session-status after answer = %#v", after)
	}
	if after.Interaction != nil {
		t.Fatalf("answered interaction still reported as pending: %#v", after.Interaction)
	}
}

func TestSessionStatusIPCReportsNoInteractionWhenIdle(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	workers := &workerGroup{}
	defer workers.stopAndWait()

	start := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command: "session-start",
		Title:   "idle",
	})
	if !start.OK || start.Session == nil {
		t.Fatalf("session-start response = %#v", start)
	}

	status := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-status",
		SessionID: start.Session.ID,
	})
	if !status.OK {
		t.Fatalf("session-status response = %#v", status)
	}
	if status.Interaction != nil {
		t.Fatalf("idle session reported an interaction: %#v", status.Interaction)
	}
}

func TestSessionQuestionsIPCListsOnlyPending(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	workers := &workerGroup{}
	defer workers.stopAndWait()

	_, _, interaction := seedWaitingSession(t, db, 1)

	questions := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-questions",
		SessionID: interaction.SessionID,
	})
	if !questions.OK {
		t.Fatalf("session-questions response = %#v", questions)
	}
	if len(questions.Interactions) != 1 {
		t.Fatalf("pending interactions = %d, want 1", len(questions.Interactions))
	}
	if questions.Interactions[0].ID != interaction.ID {
		t.Fatalf("pending interaction ID = %d, want %d", questions.Interactions[0].ID, interaction.ID)
	}
	if questions.Interactions[0].Context["step"] != float64(1) {
		t.Fatalf("pending interaction context = %#v", questions.Interactions[0].Context)
	}

	if _, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "done"); err != nil || !applied {
		t.Fatalf("AnswerHumanInput: applied=%v err=%v", applied, err)
	}

	empty := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-questions",
		SessionID: interaction.SessionID,
	})
	if !empty.OK || len(empty.Interactions) != 0 {
		t.Fatalf("session-questions after answer = %#v", empty)
	}
}

func TestInteractionGetIPC(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	workers := &workerGroup{}
	defer workers.stopAndWait()

	_, _, interaction := seedWaitingSession(t, db, 1)

	found := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:       "interaction-get",
		InteractionID: interaction.ID,
	})
	if !found.OK || found.Interaction == nil {
		t.Fatalf("interaction-get response = %#v", found)
	}
	if found.Interaction.ID != interaction.ID || found.Interaction.Status != state.InteractionStatusPending {
		t.Fatalf("interaction-get returned %#v", found.Interaction)
	}

	missing := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:       "interaction-get",
		InteractionID: 999999,
	})
	if missing.OK {
		t.Fatalf("interaction-get for missing ID unexpectedly succeeded: %#v", missing)
	}
}

func TestInteractionAnswerIPCIsIdempotent(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	workers := &workerGroup{}
	defer workers.stopAndWait()

	_, _, interaction := seedWaitingSession(t, db, 1)

	first := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:       "interaction-answer",
		InteractionID: interaction.ID,
		Answer:        "first",
	})
	if !first.OK || !first.AnswerApplied {
		t.Fatalf("first interaction-answer = %#v", first)
	}
	if first.Interaction == nil || first.Interaction.Answer == nil || *first.Interaction.Answer != "first" {
		t.Fatalf("first answer = %#v", first.Interaction)
	}

	second := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:       "interaction-answer",
		InteractionID: interaction.ID,
		Answer:        "second",
	})
	if !second.OK {
		t.Fatalf("second interaction-answer = %#v", second)
	}
	if second.AnswerApplied {
		t.Fatal("duplicate answer reported as applied")
	}
	if second.Interaction == nil || second.Interaction.Answer == nil || *second.Interaction.Answer != "first" {
		t.Fatalf("duplicate answer replaced the original: %#v", second.Interaction)
	}

	events := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-events",
		SessionID: interaction.SessionID,
	})
	if !events.OK {
		t.Fatalf("session-events response = %#v", events)
	}
	answered := 0
	for _, event := range events.Events {
		if event.Type == state.EventTypeInteractionAnswered {
			answered++
		}
	}
	if answered != 1 {
		t.Fatalf("interaction.answered events = %d, want 1", answered)
	}
}

func TestSessionResumeIPCDispatchesWaitingSession(t *testing.T) {
	db := openWorkerTestDatabase(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workers := &workerGroup{}

	_, _, interaction := seedWaitingSession(t, db, 1)
	if _, applied, err := db.AnswerHumanInput(ctx, interaction.ID, "go"); err != nil || !applied {
		t.Fatalf("AnswerHumanInput: applied=%v err=%v", applied, err)
	}

	resume := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-resume",
		SessionID: interaction.SessionID,
	})
	if !resume.OK || resume.Execution == nil {
		t.Fatalf("session-resume response = %#v", resume)
	}
	if resume.Execution.Status != state.ExecutionStatusQueued {
		t.Fatalf("resumed status = %q, want %q", resume.Execution.Status, state.ExecutionStatusQueued)
	}
	if resume.Execution.CurrentStep != 1 {
		t.Fatalf("resumed current step = %d, want 1", resume.Execution.CurrentStep)
	}
	waitForExecutionStatus(t, db, interaction.SessionID, state.ExecutionStatusRunning, 6*time.Second)

	cancel()
	workers.stopAndWait()
}

func TestSessionResumeIPCRejectsUnanswered(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	workers := &workerGroup{}
	defer workers.stopAndWait()

	_, _, interaction := seedWaitingSession(t, db, 1)

	resume := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-resume",
		SessionID: interaction.SessionID,
	})
	if resume.OK {
		t.Fatalf("session-resume unexpectedly succeeded before answer: %#v", resume)
	}

	sessionState, latest, err := db.SessionExecutionStatus(ctx, interaction.SessionID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if sessionState.State != state.SessionStateWaitingForUser {
		t.Fatalf("session state = %q, want %q", sessionState.State, state.SessionStateWaitingForUser)
	}
	if latest == nil || latest.Status != state.ExecutionStatusWaitingForUser || latest.CurrentStep != 1 {
		t.Fatalf("execution changed after rejected resume: %#v", latest)
	}
}

// TestSessionRunIPCPausesAndResumesEndToEnd wires the env-configured pause
// through the real IPC handlers: session-run pauses, the question is answered
// over IPC, and session-resume dispatches the waiting session.
func TestSessionRunIPCPausesAndResumesEndToEnd(t *testing.T) {
	t.Setenv("FORGEHAND_FAKE_WORKER_PAUSE_STEP", "1")

	db := openWorkerTestDatabase(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workers := &workerGroup{}

	start := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command: "session-start",
		Title:   "ipc pause",
	})
	if !start.OK || start.Session == nil {
		t.Fatalf("session-start response = %#v", start)
	}
	sessionID := start.Session.ID

	run := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-run",
		SessionID: sessionID,
	})
	if !run.OK || run.Execution == nil {
		t.Fatalf("session-run response = %#v", run)
	}

	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusWaitingForUser, 6*time.Second)

	questions := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-questions",
		SessionID: sessionID,
	})
	if !questions.OK || len(questions.Interactions) != 1 {
		t.Fatalf("session-questions response = %#v", questions)
	}
	interaction := questions.Interactions[0]
	if interaction.Status != state.InteractionStatusPending {
		t.Fatalf("interaction status = %q", interaction.Status)
	}

	answered := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:       "interaction-answer",
		InteractionID: interaction.ID,
		Answer:        "continue",
	})
	if !answered.OK || !answered.AnswerApplied {
		t.Fatalf("interaction-answer response = %#v", answered)
	}

	resume := roundTripRequest(t, ctx, db, workers, ipc.Request{
		Command:   "session-resume",
		SessionID: sessionID,
	})
	if !resume.OK || resume.Execution == nil {
		t.Fatalf("session-resume response = %#v", resume)
	}
	if resume.Execution.CurrentStep != 1 {
		t.Fatalf("resumed current step = %d, want 1", resume.Execution.CurrentStep)
	}

	cancel()
	workers.stopAndWait()
}
