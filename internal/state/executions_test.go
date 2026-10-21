package state

import (
	"context"
	"sync"
	"testing"
)

func TestStartSessionExecutionTransitionsSessionAndCreatesExecution(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Fake worker test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	if execution.ID == 0 {
		t.Fatal("execution ID was not assigned")
	}

	if execution.SessionID != session.ID {
		t.Fatalf(
			"SessionID = %d, want %d",
			execution.SessionID,
			session.ID,
		)
	}

	if execution.Status != ExecutionStatusRunning {
		t.Fatalf(
			"Status = %q, want %q",
			execution.Status,
			ExecutionStatusRunning,
		)
	}

	if execution.CurrentStep != 0 {
		t.Fatalf("CurrentStep = %d, want 0", execution.CurrentStep)
	}

	if execution.TotalSteps != 5 {
		t.Fatalf("TotalSteps = %d, want 5", execution.TotalSteps)
	}

	sessions, err := db.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}

	if sessions[0].State != SessionStateRunning {
		t.Fatalf(
			"session state = %q, want %q",
			sessions[0].State,
			SessionStateRunning,
		)
	}
}

func TestStartSessionExecutionRejectsSecondStart(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Start once")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := db.StartSessionExecution(ctx, session.ID, 5); err != nil {
		t.Fatalf("first StartSessionExecution: %v", err)
	}

	if _, err := db.StartSessionExecution(ctx, session.ID, 5); err == nil {
		t.Fatal("second StartSessionExecution unexpectedly succeeded")
	}

	var count int
	if err := db.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM session_executions WHERE session_id = ?`,
		session.ID,
	).Scan(&count); err != nil {
		t.Fatalf("count executions: %v", err)
	}

	if count != 1 {
		t.Fatalf("execution count = %d, want 1", count)
	}
}

func TestStartSessionExecutionRejectsMissingSession(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	if _, err := db.StartSessionExecution(ctx, 999999, 5); err == nil {
		t.Fatal("StartSessionExecution accepted missing session")
	}
}

func TestStartSessionExecutionRejectsInvalidStepCount(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Invalid steps")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := db.StartSessionExecution(ctx, session.ID, 0); err == nil {
		t.Fatal("StartSessionExecution accepted zero steps")
	}
}

func TestExecutionProgressAndCompletion(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Complete fake work")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	execution, err := db.StartSessionExecution(ctx, session.ID, 3)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	for step := 1; step <= 3; step++ {
		if err := db.AdvanceSessionExecution(
			ctx,
			execution.ID,
			step,
		); err != nil {
			t.Fatalf("advance to step %d: %v", step, err)
		}
	}

	if err := db.CompleteSessionExecution(ctx, execution.ID); err != nil {
		t.Fatalf("CompleteSessionExecution: %v", err)
	}

	var (
		executionStatus string
		currentStep     int
		completedAt     *string
	)

	if err := db.db.QueryRowContext(ctx, `
SELECT status, current_step, completed_at
FROM session_executions
WHERE id = ?
`,
		execution.ID,
	).Scan(
		&executionStatus,
		&currentStep,
		&completedAt,
	); err != nil {
		t.Fatalf("read completed execution: %v", err)
	}

	if executionStatus != ExecutionStatusCompleted {
		t.Fatalf(
			"execution status = %q, want %q",
			executionStatus,
			ExecutionStatusCompleted,
		)
	}

	if currentStep != 3 {
		t.Fatalf("current step = %d, want 3", currentStep)
	}

	if completedAt == nil || *completedAt == "" {
		t.Fatal("completed_at was not recorded")
	}

	sessions, err := db.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if sessions[0].State != SessionStateCompleted {
		t.Fatalf(
			"session state = %q, want %q",
			sessions[0].State,
			SessionStateCompleted,
		)
	}
}

func TestExecutionRejectsSkippedProgress(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "No skipping")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	if err := db.AdvanceSessionExecution(ctx, execution.ID, 2); err == nil {
		t.Fatal("execution unexpectedly skipped from step 0 to step 2")
	}
}

func TestExecutionCannotCompleteEarly(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Too soon")
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

	if err := db.CompleteSessionExecution(ctx, execution.ID); err == nil {
		t.Fatal("execution completed before all steps finished")
	}
}

func TestInterruptRunningExecutions(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Interrupted work")
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

	if err := db.AdvanceSessionExecution(ctx, execution.ID, 2); err != nil {
		t.Fatalf("advance to step 2: %v", err)
	}

	count, err := db.InterruptRunningExecutions(ctx)
	if err != nil {
		t.Fatalf("InterruptRunningExecutions: %v", err)
	}

	if count != 1 {
		t.Fatalf("interrupted count = %d, want 1", count)
	}

	var (
		status      string
		currentStep int
		totalSteps  int
	)

	if err := db.db.QueryRowContext(ctx, `
SELECT status, current_step, total_steps
FROM session_executions
WHERE id = ?
`,
		execution.ID,
	).Scan(
		&status,
		&currentStep,
		&totalSteps,
	); err != nil {
		t.Fatalf("read interrupted execution: %v", err)
	}

	if status != ExecutionStatusInterrupted {
		t.Fatalf(
			"execution status = %q, want %q",
			status,
			ExecutionStatusInterrupted,
		)
	}

	if currentStep != 2 {
		t.Fatalf("current step = %d, want 2", currentStep)
	}

	if totalSteps != 5 {
		t.Fatalf("total steps = %d, want 5", totalSteps)
	}

	sessions, err := db.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if sessions[0].State != SessionStateInterrupted {
		t.Fatalf(
			"session state = %q, want %q",
			sessions[0].State,
			SessionStateInterrupted,
		)
	}
}

func TestInterruptRunningExecutionsDoesNothingWithoutRunningWork(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	if _, err := db.CreateSession(ctx, "Still ready"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	count, err := db.InterruptRunningExecutions(ctx)
	if err != nil {
		t.Fatalf("InterruptRunningExecutions: %v", err)
	}

	if count != 0 {
		t.Fatalf("interrupted count = %d, want 0", count)
	}
}

func TestResumeSessionExecutionPreservesCheckpoint(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Resume work")
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

	if _, err := db.InterruptRunningExecutions(ctx); err != nil {
		t.Fatalf("InterruptRunningExecutions: %v", err)
	}

	resumed, err := db.ResumeSessionExecution(ctx, session.ID)
	if err != nil {
		t.Fatalf("ResumeSessionExecution: %v", err)
	}

	if resumed.ID != execution.ID {
		t.Fatalf(
			"resumed execution ID = %d, want existing ID %d",
			resumed.ID,
			execution.ID,
		)
	}

	if resumed.Status != ExecutionStatusRunning {
		t.Fatalf(
			"resumed status = %q, want %q",
			resumed.Status,
			ExecutionStatusRunning,
		)
	}

	if resumed.CurrentStep != 2 {
		t.Fatalf(
			"resumed current step = %d, want 2",
			resumed.CurrentStep,
		)
	}

	if resumed.TotalSteps != 5 {
		t.Fatalf(
			"resumed total steps = %d, want 5",
			resumed.TotalSteps,
		)
	}

	sessions, err := db.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if sessions[0].State != SessionStateRunning {
		t.Fatalf(
			"session state = %q, want %q",
			sessions[0].State,
			SessionStateRunning,
		)
	}
}

func TestResumeSessionExecutionRejectsNonInterruptedExecution(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Already running")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := db.StartSessionExecution(ctx, session.ID, 5); err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	if _, err := db.ResumeSessionExecution(ctx, session.ID); err == nil {
		t.Fatal("running execution unexpectedly resumed")
	}
}

func TestSessionExecutionForeignKeyIsEnforced(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	_, err := db.db.ExecContext(ctx, `
INSERT INTO session_executions (
	session_id,
	status,
	current_step,
	total_steps,
	started_at
)
VALUES (?, ?, ?, ?, ?)
`,
		999999,
		ExecutionStatusRunning,
		0,
		5,
		"2026-10-06T00:00:00Z",
	)

	if err == nil {
		t.Fatal("execution for nonexistent session unexpectedly succeeded")
	}
}

func TestInterruptSessionExecutionPreservesCheckpointAndSessionIsResumable(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Interrupt one execution")
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

	if err := db.InterruptSessionExecution(ctx, execution.ID); err != nil {
		t.Fatalf("InterruptSessionExecution: %v", err)
	}

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}

	if sessionState.State != SessionStateInterrupted {
		t.Fatalf("session state = %q, want %q", sessionState.State, SessionStateInterrupted)
	}
	if latest == nil {
		t.Fatal("interrupted execution missing")
	}
	if latest.Status != ExecutionStatusInterrupted {
		t.Fatalf("execution status = %q, want %q", latest.Status, ExecutionStatusInterrupted)
	}
	if latest.CurrentStep != 2 {
		t.Fatalf("current step = %d, want 2", latest.CurrentStep)
	}

	resumed, err := db.ResumeSessionExecution(ctx, session.ID)
	if err != nil {
		t.Fatalf("ResumeSessionExecution: %v", err)
	}
	if resumed.CurrentStep != 2 {
		t.Fatalf("resumed current step = %d, want 2", resumed.CurrentStep)
	}
	if resumed.Status != ExecutionStatusRunning {
		t.Fatalf("resumed status = %q, want %q", resumed.Status, ExecutionStatusRunning)
	}
}

func TestFailSessionExecutionPreservesCheckpointAndIsTerminal(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Fail one execution")
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

	if err := db.FailSessionExecution(ctx, execution.ID); err != nil {
		t.Fatalf("FailSessionExecution: %v", err)
	}

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}

	if sessionState.State != SessionStateFailed {
		t.Fatalf("session state = %q, want %q", sessionState.State, SessionStateFailed)
	}
	if latest == nil {
		t.Fatal("failed execution missing")
	}
	if latest.Status != ExecutionStatusFailed {
		t.Fatalf("execution status = %q, want %q", latest.Status, ExecutionStatusFailed)
	}
	if latest.CurrentStep != 1 {
		t.Fatalf("current step = %d, want 1 (checkpoint preserved)", latest.CurrentStep)
	}
	if latest.CompletedAt != nil {
		t.Fatal("failed execution unexpectedly has a completion time")
	}

	if _, err := db.ResumeSessionExecution(ctx, session.ID); err == nil {
		t.Fatal("failed execution unexpectedly resumed")
	}
}

func TestSessionExecutionStatusWithoutExecution(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "No execution yet")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if sessionState.State != SessionStateReady {
		t.Fatalf("session state = %q, want %q", sessionState.State, SessionStateReady)
	}
	if latest != nil {
		t.Fatalf("expected no execution, got %#v", latest)
	}

	if _, _, err := db.SessionExecutionStatus(ctx, 999999); err == nil {
		t.Fatal("SessionExecutionStatus accepted missing session")
	}
}

func TestSessionExecutionStatusIncludesTypedSessionMetadata(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	project, err := db.CreateProject(ctx, "fixture", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := db.CreateCodingSession(ctx, "typed", project.ID, "bounded task", "test-provider", "test-model")
	if err != nil {
		t.Fatal(err)
	}
	loaded, execution, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if execution != nil {
		t.Fatalf("execution = %#v, want nil", execution)
	}
	if loaded.ProjectID == nil || *loaded.ProjectID != project.ID || loaded.Task != "bounded task" || loaded.Kind != "coding" || loaded.Provider != "test-provider" || loaded.Model != "test-model" {
		t.Fatalf("session metadata = %#v", loaded)
	}
}

// TestConcurrentExecutionTransactionsComplete runs several full execution
// lifecycles in parallel against one database. It reproduces the checkpoint
// contention that previously produced SQLITE_BUSY and verifies every execution
// reaches COMPLETED without leaving a session RUNNING.
func TestConcurrentExecutionTransactionsComplete(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	const workers = 4
	const steps = 5

	executions := make([]SessionExecution, 0, workers)
	for i := 0; i < workers; i++ {
		session, err := db.CreateSession(ctx, "concurrent")
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		execution, err := db.StartSessionExecution(ctx, session.ID, steps)
		if err != nil {
			t.Fatalf("StartSessionExecution: %v", err)
		}
		executions = append(executions, execution)
	}

	start := make(chan struct{})
	var wait sync.WaitGroup

	for _, execution := range executions {
		wait.Add(1)
		go func(execution SessionExecution) {
			defer wait.Done()
			<-start
			for step := 1; step <= steps; step++ {
				if err := db.AdvanceSessionExecution(ctx, execution.ID, step); err != nil {
					t.Errorf("execution %d advance to step %d: %v", execution.ID, step, err)
					return
				}
			}
			if err := db.CompleteSessionExecution(ctx, execution.ID); err != nil {
				t.Errorf("execution %d complete: %v", execution.ID, err)
			}
		}(execution)
	}

	close(start)
	wait.Wait()

	for _, execution := range executions {
		sessionState, latest, err := db.SessionExecutionStatus(ctx, execution.SessionID)
		if err != nil {
			t.Fatalf("SessionExecutionStatus: %v", err)
		}
		if latest == nil || latest.Status != ExecutionStatusCompleted {
			t.Fatalf("execution %d status = %#v, want COMPLETED", execution.ID, latest)
		}
		if latest.CurrentStep != steps {
			t.Fatalf("execution %d current step = %d, want %d", execution.ID, latest.CurrentStep, steps)
		}
		if sessionState.State != SessionStateCompleted {
			t.Fatalf("session %d state = %q, want %q", execution.SessionID, sessionState.State, SessionStateCompleted)
		}
	}
}
