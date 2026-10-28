package state

import (
	"context"
	"testing"
)

func TestInspectRestartWorkStateCountsAuthoritativeDurableState(t *testing.T) {
	db := openTestDatabase(t)
	ctx := context.Background()

	runningSession, err := db.CreateSession(ctx, "running")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueSessionExecution(ctx, runningSession.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := db.ClaimNextQueuedExecution(ctx, nil); err != nil || !ok {
		t.Fatalf("claim running execution ok=%t err=%v", ok, err)
	}

	qualification, err := db.CreateQualificationJob(ctx, "worker", "fp", "planning", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkQualificationRunning(ctx, qualification.ID); err != nil {
		t.Fatal(err)
	}

	project, err := db.CreateProject(ctx, "plan", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, plan, err := db.CreateWorkPlanSession(ctx, "plan", project.ID, WorkPlanSpec{
		Objective: "objective", OriginalRequest: "request",
		Tasks: []WorkPlanTaskSpec{{Key: "one", Objective: "one", ExecutionMethod: "test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.StartWorkPlan(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}

	got, err := db.InspectRestartWorkState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunningExecutions != 1 || got.QueuedExecutions != 1 || got.RunningWorkPlans != 1 || got.RunningQualificationJobs != 1 {
		t.Fatalf("restart work state = %#v", got)
	}
}

func TestInspectRestartWorkStateReportsUnknownExecutionStatus(t *testing.T) {
	db := openTestDatabase(t)
	ctx := context.Background()
	session, err := db.CreateSession(ctx, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	execution, err := db.QueueSessionExecution(ctx, session.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE session_executions SET status = 'UNKNOWN_TEST_STATE' WHERE id = ?`, execution.ID); err != nil {
		t.Fatal(err)
	}
	got, err := db.InspectRestartWorkState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.UnknownExecutions != 1 || got.QueuedExecutions != 0 {
		t.Fatalf("restart work state = %#v", got)
	}
}
