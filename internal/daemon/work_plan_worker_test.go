package daemon

import (
	"context"
	"fmt"
	"reflect"
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

func TestInvestigationBootstrapPersistsRankedEvidenceWithoutModel(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "internal/recovery/daemon.go", "package recovery\n\nfunc TestDaemonGracefulShutdownPreservesWaitingInteraction() {}\n// recovery recovery waiting interaction\n")
	writeProjectIntakeFile(t, root, "docs/recovery.md", "recovery overview\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add recovery fixture")
	project := createProjectInventoryFixture(t, ctx, db, root)
	if inventory, err := db.LatestProjectInventory(ctx, project.ID, true); err != nil || inventory != nil {
		t.Fatalf("initial inventory = %#v, %v", inventory, err)
	}

	interpreted, decision := planner.InvestigationBootstrap("Investigate TestDaemonGracefulShutdownPreservesWaitingInteraction recovery behavior", []string{"do not modify files"}, false)
	if !decision.Eligible {
		t.Fatalf("decision = %#v", decision)
	}
	spec := workPlanSpecFromPlanner(interpreted)
	spec.Constraints = []string{"do not modify files"}
	session, plan, err := db.CreateWorkPlanSession(ctx, "investigate", project.ID, spec)
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
	if loaded.Status != state.WorkPlanStatusBlocked || loaded.Tasks[0].Status != state.WorkTaskStatusCompleted || loaded.Tasks[1].Status != state.WorkTaskStatusBlocked {
		t.Fatalf("plan = %#v", loaded)
	}
	if !strings.Contains(loaded.Tasks[0].Result, "Deterministic discovery completed") || !strings.Contains(loaded.Tasks[0].Result, "no cached component analysis") {
		t.Fatalf("package = %q", loaded.Tasks[0].Result)
	}
	var candidates, excerpts []state.WorkEvidence
	for _, evidence := range loaded.Tasks[0].Evidence {
		switch evidence.Kind {
		case "candidate-file":
			candidates = append(candidates, evidence)
		case "source-excerpt":
			excerpts = append(excerpts, evidence)
		}
	}
	if len(candidates) < 2 || candidates[0].Path != "internal/recovery/daemon.go" {
		t.Fatalf("ranked candidates = %#v", candidates)
	}
	if len(excerpts) == 0 || excerpts[0].BlobHash == "" || excerpts[0].Line <= 0 || excerpts[0].EndLine < excerpts[0].Line {
		t.Fatalf("excerpt citations = %#v", excerpts)
	}
	if inventory, err := db.LatestProjectInventory(ctx, project.ID, true); err != nil || inventory == nil {
		t.Fatalf("generated inventory = %#v, %v", inventory, err)
	}
}

func TestInvestigationBootstrapIsBoundedAndRepeatable(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	for i := 0; i < maxInvestigationCandidates+5; i++ {
		writeProjectIntakeFile(t, root, fmt.Sprintf("candidate-%02d.txt", i), "BoundedNeedle\n")
	}
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add bounded fixture")
	project := createProjectInventoryFixture(t, ctx, db, root)
	interpreted, _ := planner.InvestigationBootstrap("Investigate BoundedNeedle", nil, false)
	session, plan, err := db.CreateWorkPlanSession(ctx, "bounded", project.ID, workPlanSpecFromPlanner(interpreted))
	if err != nil {
		t.Fatal(err)
	}
	firstResult, firstEvidence, blocked, err := executeWorkPlanTask(ctx, db, plan, plan.Tasks[0])
	if err != nil || blocked != "" {
		t.Fatalf("first execution: blocked=%q err=%v", blocked, err)
	}
	secondResult, secondEvidence, blocked, err := executeWorkPlanTask(ctx, db, plan, plan.Tasks[0])
	if err != nil || blocked != "" {
		t.Fatalf("second execution: blocked=%q err=%v", blocked, err)
	}
	if firstResult != secondResult || !reflect.DeepEqual(firstEvidence, secondEvidence) {
		t.Fatal("repeat execution produced different or duplicated evidence")
	}
	candidateCount := 0
	for _, evidence := range firstEvidence {
		if evidence.Kind == "candidate-file" {
			candidateCount++
		}
	}
	if candidateCount != maxInvestigationCandidates || len(firstEvidence) > maxInvestigationEvidence || !strings.Contains(firstResult, "bounds were reached") {
		t.Fatalf("candidate/evidence bounds = %d/%d; result=%q", candidateCount, len(firstEvidence), firstResult)
	}
	_ = session
}

func TestInvestigationBootstrapEvidenceReachesQualifiedPlanner(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "internal/bootstrap.go", "package internal\n// EvidenceNeedle is committed evidence.\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add evidence fixture")
	project := createProjectInventoryFixture(t, ctx, db, root)
	interpreted, _ := planner.InvestigationBootstrap("Investigate EvidenceNeedle", nil, true)
	session, plan, err := db.CreateWorkPlanSession(ctx, "qualified", project.ID, workPlanSpecFromPlanner(interpreted))
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
	sawEvidence := false
	p := &stubProvider{fn: func(_ context.Context, request provider.ChatRequest) (provider.ChatResponse, error) {
		sawEvidence = strings.Contains(request.User, "internal/bootstrap.go") && strings.Contains(request.User, "EvidenceNeedle is committed evidence")
		return provider.ChatResponse{Content: `{"objective":"Inspect the committed inventory","read_only":true,"tasks":[{"key":"inventory-after-discovery","objective":"Inventory committed paths","method":"repository.inventory.lookup","required_evidence":"commit-pinned file records","completion_criteria":"bounded paths are recorded"}]}`, Model: "test-model"}, nil
	}}
	runtime := modelRuntimeForTest(t, db, p)
	runWorkPlanWorkerWithModel(ctx, db, runtime, execution)
	loaded, err := db.GetWorkPlanBySession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sawEvidence || loaded.Revision != 1 || loaded.Status != state.WorkPlanStatusCompleted || len(loaded.Tasks) != 3 {
		t.Fatalf("qualified plan = %#v; saw evidence=%t", loaded, sawEvidence)
	}
}

func TestInvestigationBootstrapRecoversInterruptedTask(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project := createProjectInventoryFixture(t, ctx, db, root)
	interpreted, _ := planner.InvestigationBootstrap("Investigate tracked recovery", nil, false)
	session, plan, err := db.CreateWorkPlanSession(ctx, "recovery", project.ID, workPlanSpecFromPlanner(interpreted))
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
	if err := db.StartWorkPlan(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.StartWorkPlanTask(ctx, plan.Tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	if count, err := db.RecoverRunningWorkPlanTasks(ctx); err != nil || count != 1 {
		t.Fatalf("recover count=%d err=%v", count, err)
	}
	runWorkPlanWorker(ctx, db, execution)
	loaded, err := db.GetWorkPlanBySession(ctx, session.ID)
	if err != nil || loaded.Tasks[0].Status != state.WorkTaskStatusCompleted {
		t.Fatalf("recovered plan = %#v, %v", loaded, err)
	}
}

func TestInvestigationBootstrapUsesMatchingCachedAnalysis(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)
	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatal(err)
	}
	interpreted, _ := planner.InvestigationBootstrap("Investigate example.com/app/internal/greeting dependencies", nil, false)
	session, plan, err := db.CreateWorkPlanSession(ctx, "analysis", project.ID, workPlanSpecFromPlanner(interpreted))
	if err != nil {
		t.Fatal(err)
	}
	result, evidence, blocked, err := executeWorkPlanTask(ctx, db, plan, plan.Tasks[0])
	if err != nil || blocked != "" {
		t.Fatalf("execution: blocked=%q err=%v", blocked, err)
	}
	foundComponent, foundDependency := false, false
	for _, item := range evidence {
		foundComponent = foundComponent || item.Kind == "relevant-component"
		foundDependency = foundDependency || item.Kind == "relevant-dependency"
	}
	if !foundComponent || !foundDependency || !strings.Contains(result, "example.com/app/internal/greeting") {
		t.Fatalf("analysis package=%q evidence=%#v", result, evidence)
	}
	_ = session
}

func TestInvestigationBootstrapRecordsNoRelevantMatches(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project := createProjectInventoryFixture(t, ctx, db, root)
	interpreted, _ := planner.InvestigationBootstrap("Investigate ZephyrQuuxSymbol", nil, false)
	_, plan, err := db.CreateWorkPlanSession(ctx, "empty", project.ID, workPlanSpecFromPlanner(interpreted))
	if err != nil {
		t.Fatal(err)
	}
	result, evidence, blocked, err := executeWorkPlanTask(ctx, db, plan, plan.Tasks[0])
	if err != nil || blocked != "" {
		t.Fatalf("execution: blocked=%q err=%v", blocked, err)
	}
	for _, item := range evidence {
		if item.Kind == "candidate-file" || item.Kind == "source-excerpt" {
			t.Fatalf("unexpected matching evidence: %#v", item)
		}
	}
	if !strings.Contains(result, "no committed paths or source lines matched") {
		t.Fatalf("package = %q", result)
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
