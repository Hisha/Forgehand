package state

import (
	"context"
	"errors"
	"testing"
)

func startCodingExecution(t *testing.T, ctx context.Context, db *Database) SessionExecution {
	t.Helper()

	session, err := db.CreateCodingSession(ctx, "coding task", 1, "do the thing", "openai-compatible", "test-model")
	if err != nil {
		t.Fatalf("CreateCodingSession: %v", err)
	}
	if session.Kind != "coding" {
		t.Fatalf("session kind = %q, want coding", session.Kind)
	}

	if _, err := db.QueueSessionExecution(ctx, session.ID, 1); err != nil {
		t.Fatalf("QueueSessionExecution: %v", err)
	}

	execution, ok, err := db.ClaimNextQueuedExecution(ctx, nil)
	if err != nil {
		t.Fatalf("ClaimNextQueuedExecution: %v", err)
	}
	if !ok {
		t.Fatal("ClaimNextQueuedExecution found no queued execution")
	}
	if execution.SessionID != session.ID {
		t.Fatalf("claimed session = %d, want %d", execution.SessionID, session.ID)
	}
	if execution.Kind != "coding" {
		t.Fatalf("execution kind = %q, want coding", execution.Kind)
	}
	if execution.Status != ExecutionStatusRunning {
		t.Fatalf("execution status = %q, want RUNNING", execution.Status)
	}
	return execution
}

func TestCompleteModelExecutionPersistsAtomically(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	execution := startCodingExecution(t, ctx, db)

	if err := db.CompleteModelExecution(ctx, execution.ID, "openai-compatible", "test-model", "model output"); err != nil {
		t.Fatalf("CompleteModelExecution: %v", err)
	}

	session, latest, err := db.SessionExecutionStatus(ctx, execution.SessionID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if latest == nil || latest.Status != ExecutionStatusCompleted {
		t.Fatalf("execution status = %#v, want COMPLETED", latest)
	}
	if session.State != SessionStateCompleted {
		t.Fatalf("session state = %q, want %q", session.State, SessionStateCompleted)
	}

	result, err := db.LatestModelResult(ctx, execution.SessionID)
	if err != nil {
		t.Fatalf("LatestModelResult: %v", err)
	}
	if result.Content != "model output" || result.Provider != "openai-compatible" || result.Model != "test-model" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.ExecutionID != execution.ID {
		t.Fatalf("result execution = %d, want %d", result.ExecutionID, execution.ID)
	}

	events, _, err := db.ListSessionEvents(ctx, execution.SessionID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type == EventTypeModelResponseCompleted {
			found = true
		}
	}
	if !found {
		t.Fatalf("model.response.completed event not appended: %#v", events)
	}
}

func TestCompleteModelExecutionRejectsNonRunning(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	execution := startCodingExecution(t, ctx, db)

	// Cancelling transitions the execution out of RUNNING.
	if _, err := db.CancelSessionExecution(ctx, execution.SessionID, "test cancel"); err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}

	if err := db.CompleteModelExecution(ctx, execution.ID, "openai-compatible", "m", "late"); err == nil {
		t.Fatal("expected CompleteModelExecution to reject a canceled execution")
	}

	if _, err := db.LatestModelResult(ctx, execution.SessionID); !errors.Is(err, ErrNoModelResult) {
		t.Fatalf("LatestModelResult error = %v, want ErrNoModelResult", err)
	}
}

func TestCompleteModelExecutionRejectsDuplicate(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	execution := startCodingExecution(t, ctx, db)

	if err := db.CompleteModelExecution(ctx, execution.ID, "openai-compatible", "m", "first"); err != nil {
		t.Fatalf("first CompleteModelExecution: %v", err)
	}
	if err := db.CompleteModelExecution(ctx, execution.ID, "openai-compatible", "m", "second"); err == nil {
		t.Fatal("expected duplicate CompleteModelExecution to fail")
	}

	var count int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_results WHERE execution_id = ?`, execution.ID).Scan(&count); err != nil {
		t.Fatalf("count model_results: %v", err)
	}
	if count != 1 {
		t.Fatalf("model_results rows = %d, want 1", count)
	}

	result, err := db.LatestModelResult(ctx, execution.SessionID)
	if err != nil {
		t.Fatalf("LatestModelResult: %v", err)
	}
	if result.Content != "first" {
		t.Fatalf("result content = %q, want first", result.Content)
	}
}

func TestLatestModelResultMissingReturnsSentinel(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateCodingSession(ctx, "no result", 1, "task", "openai-compatible", "m")
	if err != nil {
		t.Fatalf("CreateCodingSession: %v", err)
	}

	if _, err := db.LatestModelResult(ctx, session.ID); !errors.Is(err, ErrNoModelResult) {
		t.Fatalf("LatestModelResult error = %v, want ErrNoModelResult", err)
	}
}

func TestModelResultSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	db, err := OpenAt(ctx, dir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}

	execution := startCodingExecution(t, ctx, db)
	if err := db.CompleteModelExecution(ctx, execution.ID, "openai-compatible", "test-model", "durable output"); err != nil {
		t.Fatalf("CompleteModelExecution: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenAt(ctx, dir)
	if err != nil {
		t.Fatalf("reopen OpenAt: %v", err)
	}
	defer reopened.Close()

	result, err := reopened.LatestModelResult(ctx, execution.SessionID)
	if err != nil {
		t.Fatalf("LatestModelResult after reopen: %v", err)
	}
	if result.Content != "durable output" {
		t.Fatalf("result content after reopen = %q, want durable output", result.Content)
	}
}
