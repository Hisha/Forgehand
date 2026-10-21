package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/planner"
	"github.com/Hisha/Forgehand/internal/provider"
	"github.com/Hisha/Forgehand/internal/state"
)

func TestWorkPlanWorkerSearchesCommittedSourceWithoutModel(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "src/message.txt", "before\ncommitted needle here\nafter\n")
	runProjectIntakeGit(t, root, "add", "src/message.txt")
	runProjectIntakeGit(t, root, "commit", "-m", "add searchable source")
	project := createProjectInventoryFixture(t, ctx, db, root)
	// The worker must use the committed blob, not this mutable working-tree text.
	writeProjectIntakeFile(t, root, "src/message.txt", "working tree needle must not appear\n")

	interpreted := planner.Interpret(`Find "needle" in src`)
	spec := workPlanSpecFromPlanner(interpreted)
	session, plan, err := db.CreateWorkPlanSession(ctx, "search", project.ID, spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueSessionExecution(ctx, session.ID, len(plan.Tasks)); err != nil {
		t.Fatal(err)
	}
	execution, ok, err := db.ClaimNextQueuedExecution(ctx, nil)
	if err != nil || !ok {
		t.Fatalf("claim = %#v, %t, %v", execution, ok, err)
	}

	runWorkPlanWorker(ctx, db, execution)
	loaded, err := db.GetWorkPlanBySession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != state.WorkPlanStatusCompleted {
		t.Fatalf("plan status = %s; tasks=%#v", loaded.Status, loaded.Tasks)
	}
	if len(loaded.Tasks[1].Evidence) != 1 || loaded.Tasks[1].Evidence[0].Line != 2 {
		t.Fatalf("search evidence = %#v", loaded.Tasks[1].Evidence)
	}
	if len(loaded.Tasks[2].Evidence) != 1 || !strings.Contains(loaded.Tasks[2].Evidence[0].Excerpt, "committed needle here") || strings.Contains(loaded.Tasks[2].Evidence[0].Excerpt, "working tree") {
		t.Fatalf("excerpt evidence = %#v", loaded.Tasks[2].Evidence)
	}
	finalSession, finalExecution, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil || finalSession.State != state.SessionStateCompleted || finalExecution.Status != state.ExecutionStatusCompleted {
		t.Fatalf("final state = %#v %#v %v", finalSession, finalExecution, err)
	}
}

func TestSemanticWorkPlanExpandsInsideExistingExecution(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project := createProjectInventoryFixture(t, ctx, db, root)
	interpreted := planner.SemanticBootstrap("Explain the repository shape", true)
	session, plan, err := db.CreateWorkPlanSession(ctx, "semantic", project.ID, workPlanSpecFromPlanner(interpreted))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueSessionExecution(ctx, session.ID, len(plan.Tasks)); err != nil {
		t.Fatal(err)
	}
	execution, ok, err := db.ClaimNextQueuedExecution(ctx, nil)
	if err != nil || !ok {
		t.Fatalf("claim = %#v, %t, %v", execution, ok, err)
	}
	p := &stubProvider{fn: func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Content: `{"objective":"Inventory the committed repository","read_only":true,"tasks":[{"key":"inventory","objective":"Inventory committed paths","method":"repository.inventory.lookup","required_evidence":"commit-pinned file records","completion_criteria":"bounded paths are recorded"}]}`, Model: "test-model"}, nil
	}}
	runtime := modelRuntimeForTest(t, db, p)
	runWorkPlanWorkerWithModel(ctx, db, runtime, execution)
	loaded, err := db.GetWorkPlanBySession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != state.WorkPlanStatusCompleted || loaded.Revision != 1 || len(loaded.Tasks) != 2 {
		t.Fatalf("expanded plan = %#v", loaded)
	}
	if loaded.Tasks[0].Status != state.WorkTaskStatusCompleted || loaded.Tasks[1].Status != state.WorkTaskStatusCompleted {
		t.Fatalf("tasks = %#v", loaded.Tasks)
	}
	_, finalExecution, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil || finalExecution.TotalSteps != 2 || finalExecution.CurrentStep != 2 {
		t.Fatalf("execution = %#v, %v", finalExecution, err)
	}
}

func workPlanSpecFromPlanner(plan planner.Plan) state.WorkPlanSpec {
	spec := state.WorkPlanSpec{Objective: plan.Objective, OriginalRequest: plan.Original, ReadOnly: plan.ReadOnly, Clarification: plan.Clarification}
	for _, task := range plan.Tasks {
		spec.Tasks = append(spec.Tasks, state.WorkPlanTaskSpec{Key: task.Key, Objective: task.Objective, ExecutionMethod: task.Method, Dependencies: task.DependsOn, Inputs: task.Inputs, RequiredEvidence: task.RequiredEvidence, CompletionCriteria: task.CompletionCriteria, RequiresModel: task.RequiresModel, BlockedReason: task.BlockedReason})
	}
	return spec
}
