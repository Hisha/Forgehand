package state

import (
	"context"
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
