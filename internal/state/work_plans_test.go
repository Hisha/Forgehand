package state

import (
	"context"
	"testing"
)

func TestWorkPlanPersistsTaskGraphAndRecoversRunningTask(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	db, err := OpenAt(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateProject(ctx, "fixture", "/tmp/fixture")
	if err != nil {
		t.Fatal(err)
	}
	session, created, err := db.CreateWorkPlanSession(ctx, "search", project.ID, WorkPlanSpec{
		Objective: "find needle", OriginalRequest: `find "needle"`, ReadOnly: true,
		Tasks: []WorkPlanTaskSpec{
			{Key: "inventory", Objective: "inventory", ExecutionMethod: "inventory", RequiredEvidence: "files", CompletionCriteria: "loaded"},
			{Key: "search", Objective: "search", ExecutionMethod: "search", Dependencies: []string{"inventory"}, RequiredEvidence: "lines", CompletionCriteria: "searched"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Tasks[0].Status != WorkTaskStatusReady || created.Tasks[1].Status != WorkTaskStatusPending {
		t.Fatalf("tasks = %#v", created.Tasks)
	}
	if err := db.StartWorkPlanTask(ctx, created.Tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenAt(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n, err := db.RecoverRunningWorkPlanTasks(ctx); err != nil || n != 1 {
		t.Fatalf("recover = %d, %v", n, err)
	}
	loaded, err := db.GetWorkPlanBySession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.OriginalRequest != `find "needle"` || !loaded.ReadOnly || loaded.Tasks[0].Status != WorkTaskStatusReady {
		t.Fatalf("loaded = %#v", loaded)
	}
}

func TestBlockedPrerequisiteBlocksDependentTask(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	project, _ := db.CreateProject(ctx, "fixture", "/tmp/fixture-blocked")
	_, plan, err := db.CreateWorkPlanSession(ctx, "dependencies", project.ID, WorkPlanSpec{Objective: "deps", OriginalRequest: "deps", ReadOnly: true, Tasks: []WorkPlanTaskSpec{
		{Key: "component", Objective: "component", ExecutionMethod: "lookup", RequiredEvidence: "component", CompletionCriteria: "resolved"},
		{Key: "deps", Objective: "deps", ExecutionMethod: "deps", Dependencies: []string{"component"}, RequiredEvidence: "deps", CompletionCriteria: "listed"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.StartWorkPlanTask(ctx, plan.Tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishWorkPlanTask(ctx, plan.Tasks[0].ID, WorkTaskStatusBlocked, "", "analysis missing", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshWorkPlanReadiness(ctx, plan.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _ := db.GetWorkPlanBySession(ctx, plan.SessionID)
	if loaded.Tasks[1].Status != WorkTaskStatusBlocked || loaded.Tasks[1].BlockedReason == "" {
		t.Fatalf("dependent = %#v", loaded.Tasks[1])
	}
}

func TestExpandWorkPlanPreservesCompletedTaskEvidence(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	project, _ := db.CreateProject(ctx, "fixture", "/tmp/fixture-expand")
	_, plan, err := db.CreateWorkPlanSession(ctx, "semantic", project.ID, WorkPlanSpec{Objective: "request", OriginalRequest: "request", ReadOnly: true, Tasks: []WorkPlanTaskSpec{{Key: "semantic-plan", Objective: "plan", ExecutionMethod: "planning", RequiredEvidence: "proposal", CompletionCriteria: "accepted"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.StartWorkPlanTask(ctx, plan.Tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	evidence := []WorkEvidence{{Kind: "semantic-proposal", Detail: "validated"}}
	if err := db.FinishWorkPlanTask(ctx, plan.Tasks[0].ID, WorkTaskStatusCompleted, "accepted", "", evidence); err != nil {
		t.Fatal(err)
	}
	err = db.ExpandWorkPlan(ctx, plan.ID, "semantic-plan", WorkPlanSpec{Objective: "bounded request", ReadOnly: true, Constraints: []string{"read only"}, Tasks: []WorkPlanTaskSpec{{Key: "inventory", Objective: "inventory", ExecutionMethod: "repository.inventory.lookup", RequiredEvidence: "paths", CompletionCriteria: "recorded"}}}, `{}`, 24, 3)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := db.GetWorkPlanBySession(ctx, plan.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || len(loaded.Tasks) != 2 || loaded.Tasks[0].Result != "accepted" || len(loaded.Tasks[0].Evidence) != 1 {
		t.Fatalf("expanded plan = %#v", loaded)
	}
	if len(loaded.Tasks[1].Dependencies) != 1 || loaded.Tasks[1].Dependencies[0] != "semantic-plan" {
		t.Fatalf("dependencies = %#v", loaded.Tasks[1].Dependencies)
	}
}
