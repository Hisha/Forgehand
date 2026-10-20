package state

import (
	"context"
	"testing"
)

func newTaskRunTarget(t *testing.T, db *Database) (Session, SessionExecution) {
	t.Helper()

	ctx := context.Background()
	session, err := db.CreateSession(ctx, "task session")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 1)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	return session, execution
}

func latestExecution(t *testing.T, db *Database, sessionID int64) *SessionExecution {
	t.Helper()

	_, execution, err := db.SessionExecutionStatus(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if execution == nil {
		t.Fatal("no execution for session")
	}
	return execution
}

func countEvents(t *testing.T, db *Database, sessionID int64, eventType string) int {
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

func TestPrepareTaskRunIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	session, execution := newTaskRunTarget(t, db)

	run := TaskRun{
		SessionID:     session.ID,
		ExecutionID:   execution.ID,
		RepoRoot:      "/repo",
		WorkspacePath: "/ws",
		BaseCommit:    "abc123",
		Phase:         TaskPhasePrepared,
	}

	first, err := db.PrepareTaskRun(ctx, run)
	if err != nil {
		t.Fatalf("PrepareTaskRun: %v", err)
	}
	if first.Phase != TaskPhasePrepared {
		t.Fatalf("phase = %q, want %q", first.Phase, TaskPhasePrepared)
	}

	// A restart re-prepares the same execution with a new workspace path and
	// base commit: the row is updated without duplicating the event.
	run.WorkspacePath = "/ws2"
	run.BaseCommit = "def456"
	second, err := db.PrepareTaskRun(ctx, run)
	if err != nil {
		t.Fatalf("second PrepareTaskRun: %v", err)
	}
	if second.WorkspacePath != "/ws2" || second.BaseCommit != "def456" {
		t.Fatalf("re-prepare did not update the run: %+v", second)
	}

	if got := countEvents(t, db, session.ID, EventTypeTaskPrepared); got != 1 {
		t.Fatalf("task.prepared events = %d, want 1", got)
	}

	bySession, err := db.GetTaskRunBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetTaskRunBySession: %v", err)
	}
	if bySession == nil || bySession.ExecutionID != execution.ID {
		t.Fatalf("GetTaskRunBySession = %+v", bySession)
	}
}

func TestPrepareTaskRunRequiresSessionAndExecution(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	if _, err := db.PrepareTaskRun(ctx, TaskRun{ExecutionID: 1}); err == nil {
		t.Fatal("PrepareTaskRun accepted a run without a session")
	}
	if _, err := db.PrepareTaskRun(ctx, TaskRun{SessionID: 1}); err == nil {
		t.Fatal("PrepareTaskRun accepted a run without an execution")
	}
}

func TestRequestTaskApprovalIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	session, execution := newTaskRunTarget(t, db)

	if _, err := db.PrepareTaskRun(ctx, TaskRun{
		SessionID:     session.ID,
		ExecutionID:   execution.ID,
		RepoRoot:      "/repo",
		WorkspacePath: "/ws",
		BaseCommit:    "abc123",
		Phase:         TaskPhasePrepared,
	}); err != nil {
		t.Fatalf("PrepareTaskRun: %v", err)
	}

	proposal := TaskRun{
		ProposalJSON:         `[{"op":"write","path":"a.txt","content":"a"}]`,
		ProposalHash:         "hash-a",
		Summary:              "rewrite a.txt",
		WorkspaceFingerprint: "fp-1",
	}

	interaction, created, err := db.RequestTaskApproval(ctx, execution.ID, proposal, "approve?", nil)
	if err != nil {
		t.Fatalf("RequestTaskApproval: %v", err)
	}
	if !created {
		t.Fatal("first RequestTaskApproval did not create an interaction")
	}

	run, err := db.GetTaskRunByExecution(ctx, execution.ID)
	if err != nil {
		t.Fatalf("GetTaskRunByExecution: %v", err)
	}
	if run.Phase != TaskPhaseProposed {
		t.Fatalf("phase = %q, want %q", run.Phase, TaskPhaseProposed)
	}
	if run.ProposalHash != "hash-a" || run.Applied {
		t.Fatalf("proposal not persisted correctly: %+v", run)
	}

	exec := latestExecution(t, db, session.ID)
	if exec.Status != ExecutionStatusWaitingForUser {
		t.Fatalf("execution status = %q, want %q", exec.Status, ExecutionStatusWaitingForUser)
	}

	// Retrying must not create a second pending interaction or overwrite the
	// persisted proposal.
	retry, createdAgain, err := db.RequestTaskApproval(ctx, execution.ID, TaskRun{
		ProposalJSON: `[{"op":"write","path":"b.txt","content":"b"}]`,
		ProposalHash: "hash-b",
		Summary:      "rewrite b.txt",
	}, "approve?", nil)
	if err != nil {
		t.Fatalf("retry RequestTaskApproval: %v", err)
	}
	if createdAgain {
		t.Fatal("retry created a duplicate interaction")
	}
	if retry.ID != interaction.ID {
		t.Fatalf("retry returned a different interaction: %d vs %d", retry.ID, interaction.ID)
	}

	run, _ = db.GetTaskRunByExecution(ctx, execution.ID)
	if run.ProposalHash != "hash-a" {
		t.Fatalf("retry overwrote the persisted proposal hash: %q", run.ProposalHash)
	}
}

func TestCompleteTaskExecutionAppliedEmitsTaskApplied(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	session, execution := newTaskRunTarget(t, db)

	if _, err := db.PrepareTaskRun(ctx, TaskRun{
		SessionID: session.ID, ExecutionID: execution.ID, Phase: TaskPhasePrepared,
	}); err != nil {
		t.Fatalf("PrepareTaskRun: %v", err)
	}
	interaction, _, err := db.RequestTaskApproval(ctx, execution.ID, TaskRun{
		ProposalJSON: `[{"op":"write","path":"a.txt","content":"a"}]`,
		ProposalHash: "hash-a",
	}, "approve?", nil)
	if err != nil {
		t.Fatalf("RequestTaskApproval: %v", err)
	}

	// The worker applies only after the human answers and the execution is
	// resumed back to RUNNING.
	if _, _, err := db.AnswerHumanInput(ctx, interaction.ID, "approve"); err != nil {
		t.Fatalf("AnswerHumanInput: %v", err)
	}
	if _, err := db.ResumeAfterHumanInput(ctx, session.ID); err != nil {
		t.Fatalf("ResumeAfterHumanInput: %v", err)
	}

	run := TaskRun{
		Phase:         TaskPhaseApplied,
		Applied:       true,
		Summary:       "applied",
		ProposalJSON:  `[{"op":"write","path":"a.txt","content":"a"}]`,
		ProposalHash:  "hash-a",
		WorkspaceDiff: "diff --git a/a.txt b/a.txt",
	}
	if err := db.CompleteTaskExecution(ctx, execution.ID, run); err != nil {
		t.Fatalf("CompleteTaskExecution: %v", err)
	}

	saved, _ := db.GetTaskRunByExecution(ctx, execution.ID)
	if !saved.Applied || saved.Phase != TaskPhaseApplied {
		t.Fatalf("run not finalized as applied: %+v", saved)
	}
	if saved.WorkspaceDiff == "" {
		t.Fatal("applied diff was not persisted")
	}

	exec := latestExecution(t, db, session.ID)
	if exec.Status != ExecutionStatusCompleted {
		t.Fatalf("execution status = %q, want %q", exec.Status, ExecutionStatusCompleted)
	}
	sessionState, _ := db.GetSession(ctx, session.ID)
	if sessionState.State != SessionStateCompleted {
		t.Fatalf("session state = %q, want %q", sessionState.State, SessionStateCompleted)
	}
	if countEvents(t, db, session.ID, EventTypeTaskApplied) != 1 {
		t.Fatal("task.applied event not emitted")
	}
}

func TestCompleteTaskExecutionNotAppliedEmitsTaskCompleted(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	session, execution := newTaskRunTarget(t, db)

	if _, err := db.PrepareTaskRun(ctx, TaskRun{
		SessionID: session.ID, ExecutionID: execution.ID, Phase: TaskPhasePrepared,
	}); err != nil {
		t.Fatalf("PrepareTaskRun: %v", err)
	}

	if err := db.CompleteTaskExecution(ctx, execution.ID, TaskRun{
		Phase:   TaskPhaseCompleted,
		Applied: false,
		Summary: "nothing to do",
	}); err != nil {
		t.Fatalf("CompleteTaskExecution: %v", err)
	}

	if countEvents(t, db, session.ID, EventTypeTaskCompleted) != 1 {
		t.Fatal("task.completed event not emitted for a no-change run")
	}
	if countEvents(t, db, session.ID, EventTypeTaskApplied) != 0 {
		t.Fatal("task.applied emitted for a no-change run")
	}
}

func TestRejectTaskExecutionLeavesNoAppliedChange(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	session, execution := newTaskRunTarget(t, db)

	if _, err := db.PrepareTaskRun(ctx, TaskRun{
		SessionID: session.ID, ExecutionID: execution.ID, Phase: TaskPhasePrepared,
	}); err != nil {
		t.Fatalf("PrepareTaskRun: %v", err)
	}
	interaction, _, err := db.RequestTaskApproval(ctx, execution.ID, TaskRun{
		ProposalJSON: `[{"op":"write","path":"a.txt","content":"a"}]`,
		ProposalHash: "hash-a",
	}, "approve?", nil)
	if err != nil {
		t.Fatalf("RequestTaskApproval: %v", err)
	}
	if _, _, err := db.AnswerHumanInput(ctx, interaction.ID, "reject"); err != nil {
		t.Fatalf("AnswerHumanInput: %v", err)
	}
	if _, err := db.ResumeAfterHumanInput(ctx, session.ID); err != nil {
		t.Fatalf("ResumeAfterHumanInput: %v", err)
	}

	if err := db.RejectTaskExecution(ctx, execution.ID, TaskRun{
		ProposalHash:  "hash-a",
		WorkspaceDiff: "should be cleared",
	}, "user declined"); err != nil {
		t.Fatalf("RejectTaskExecution: %v", err)
	}

	saved, _ := db.GetTaskRunByExecution(ctx, execution.ID)
	if saved.Applied {
		t.Fatal("rejected run marked applied")
	}
	if saved.Phase != TaskPhaseRejected {
		t.Fatalf("phase = %q, want %q", saved.Phase, TaskPhaseRejected)
	}
	if saved.WorkspaceDiff != "" {
		t.Fatalf("rejected run kept a workspace diff: %q", saved.WorkspaceDiff)
	}
	if saved.Error != "user declined" {
		t.Fatalf("rejection reason = %q", saved.Error)
	}

	exec := latestExecution(t, db, session.ID)
	if exec.Status != ExecutionStatusCompleted {
		t.Fatalf("execution status = %q, want %q", exec.Status, ExecutionStatusCompleted)
	}
	if countEvents(t, db, session.ID, EventTypeTaskRejected) != 1 {
		t.Fatal("task.rejected event not emitted")
	}
}

func TestFailTaskExecutionRecordsCause(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	session, execution := newTaskRunTarget(t, db)

	if _, err := db.PrepareTaskRun(ctx, TaskRun{
		SessionID: session.ID, ExecutionID: execution.ID, Phase: TaskPhasePrepared,
	}); err != nil {
		t.Fatalf("PrepareTaskRun: %v", err)
	}

	if err := db.FailTaskExecution(ctx, execution.ID, TaskRun{}, "provider exploded"); err != nil {
		t.Fatalf("FailTaskExecution: %v", err)
	}

	saved, _ := db.GetTaskRunByExecution(ctx, execution.ID)
	if saved.Phase != TaskPhaseFailed || saved.Error != "provider exploded" {
		t.Fatalf("failed run not recorded: %+v", saved)
	}

	exec := latestExecution(t, db, session.ID)
	if exec.Status != ExecutionStatusFailed {
		t.Fatalf("execution status = %q, want %q", exec.Status, ExecutionStatusFailed)
	}
	sessionState, _ := db.GetSession(ctx, session.ID)
	if sessionState.State != SessionStateFailed {
		t.Fatalf("session state = %q, want %q", sessionState.State, SessionStateFailed)
	}
	if countEvents(t, db, session.ID, EventTypeTaskFailed) != 1 {
		t.Fatal("task.failed event not emitted")
	}
}

func TestFinishTaskExecutionRefusesCanceledExecution(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	session, execution := newTaskRunTarget(t, db)

	if _, err := db.PrepareTaskRun(ctx, TaskRun{
		SessionID: session.ID, ExecutionID: execution.ID, Phase: TaskPhasePrepared,
	}); err != nil {
		t.Fatalf("PrepareTaskRun: %v", err)
	}

	if _, err := db.CancelSessionExecution(ctx, session.ID, "test cancellation"); err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}

	// A canceled execution is terminal: a late task result must not commit.
	err := db.CompleteTaskExecution(ctx, execution.ID, TaskRun{
		Phase:   TaskPhaseCompleted,
		Applied: false,
	})
	if err == nil {
		t.Fatal("CompleteTaskExecution committed a result for a canceled execution")
	}

	exec := latestExecution(t, db, session.ID)
	if exec.Status != ExecutionStatusCanceled {
		t.Fatalf("execution status = %q, want %q", exec.Status, ExecutionStatusCanceled)
	}
}

func TestRecordAndListTaskOperations(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	session, execution := newTaskRunTarget(t, db)

	run, err := db.PrepareTaskRun(ctx, TaskRun{
		SessionID: session.ID, ExecutionID: execution.ID, Phase: TaskPhasePrepared,
	})
	if err != nil {
		t.Fatalf("PrepareTaskRun: %v", err)
	}

	for _, op := range []TaskOperation{
		{Kind: "read", Path: "a.txt", Outcome: "ok", Detail: "10 bytes"},
		{Kind: "search", Detail: "needle (2 hits)", Outcome: "ok"},
		{Kind: "read", Path: "missing", Outcome: "error", Error: "not found"},
	} {
		if err := db.RecordTaskOperation(ctx, run.ID, op); err != nil {
			t.Fatalf("RecordTaskOperation: %v", err)
		}
	}

	operations, err := db.ListTaskOperations(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListTaskOperations: %v", err)
	}
	if len(operations) != 3 {
		t.Fatalf("operations = %d, want 3", len(operations))
	}
	for i, op := range operations {
		if op.Sequence != i+1 {
			t.Fatalf("operation %d sequence = %d, want %d", i, op.Sequence, i+1)
		}
	}
	if operations[2].Outcome != "error" || operations[2].Error != "not found" {
		t.Fatalf("operation error trail lost: %+v", operations[2])
	}

	if err := db.RecordTaskOperation(ctx, 0, TaskOperation{Kind: "read"}); err == nil {
		t.Fatal("RecordTaskOperation accepted a zero run id")
	}
}

func TestMarkTaskRunCleanedPreservesDiff(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	session, execution := newTaskRunTarget(t, db)

	run, err := db.PrepareTaskRun(ctx, TaskRun{
		SessionID: session.ID, ExecutionID: execution.ID,
		WorkspacePath: "/ws", Phase: TaskPhasePrepared,
	})
	if err != nil {
		t.Fatalf("PrepareTaskRun: %v", err)
	}
	if err := db.CompleteTaskExecution(ctx, execution.ID, TaskRun{
		Phase: TaskPhaseApplied, Applied: true,
		WorkspaceDiff: "diff --git a/a.txt b/a.txt",
	}); err != nil {
		t.Fatalf("CompleteTaskExecution: %v", err)
	}

	if err := db.MarkTaskRunCleaned(ctx, run.ID); err != nil {
		t.Fatalf("MarkTaskRunCleaned: %v", err)
	}

	saved, _ := db.GetTaskRunByExecution(ctx, execution.ID)
	if saved.CleanedAt == nil {
		t.Fatal("cleaned_at was not recorded")
	}
	if saved.WorkspacePath != "" {
		t.Fatalf("workspace path not cleared: %q", saved.WorkspacePath)
	}
	if saved.WorkspaceDiff == "" {
		t.Fatal("cleanup discarded the durable diff")
	}

	if err := db.MarkTaskRunCleaned(ctx, 999999); err == nil {
		t.Fatal("MarkTaskRunCleaned accepted an unknown run id")
	}
}
