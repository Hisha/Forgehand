package planner

import "testing"

func TestInterpretLiteralSearchOrdersBoundedTasks(t *testing.T) {
	plan := Interpret(`Find "Needle" in internal/`)
	if !plan.ReadOnly || len(plan.Tasks) != 3 {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.Tasks[1].Method != MethodCommittedSearch || len(plan.Tasks[1].DependsOn) != 1 || plan.Tasks[1].DependsOn[0] != "inventory" {
		t.Fatalf("search task = %#v", plan.Tasks[1])
	}
	if plan.Tasks[2].DependsOn[0] != "search" {
		t.Fatalf("excerpt dependency = %#v", plan.Tasks[2].DependsOn)
	}
	if plan.Tasks[1].Inputs["term"] != "Needle" || plan.Tasks[1].Inputs["path_prefix"] != "internal/" {
		t.Fatalf("search inputs = %#v", plan.Tasks[1].Inputs)
	}
}

func TestInterpretComponentRelationships(t *testing.T) {
	plan := Interpret(`Show dependents of "example.invalid/project/internal/state"`)
	if len(plan.Tasks) != 2 || plan.Tasks[1].Method != MethodDependencyLookup || plan.Tasks[1].Inputs["relation"] != "dependents" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestInterpretModificationStaysReadOnlyAndBlocked(t *testing.T) {
	plan := Interpret("Fix the parser and update its tests")
	if !plan.ReadOnly || len(plan.Tasks) != 1 || plan.Tasks[0].BlockedReason == "" || !plan.Tasks[0].RequiresModel {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestInterpretAmbiguousRequestDoesNotGuess(t *testing.T) {
	plan := Interpret("How does everything fit together?")
	if plan.Clarification == "" || plan.Tasks[0].Method != MethodSemanticPlanning {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestAssessInvestigationConservativelyExtractsTopics(t *testing.T) {
	decision := AssessInvestigation("Investigate why TestDaemonGracefulShutdownPreservesWaitingInteraction leaves recovery queued", []string{"read-only", "do not modify files", "do not execute commands"})
	if !decision.Eligible || decision.NeedsClarification {
		t.Fatalf("decision = %#v", decision)
	}
	if len(decision.Topics) == 0 || decision.Topics[0] != "TestDaemonGracefulShutdownPreservesWaitingInteraction" {
		t.Fatalf("topics = %#v", decision.Topics)
	}
	if len(decision.Topics) > MaxInvestigationTerms {
		t.Fatalf("topic count = %d", len(decision.Topics))
	}
}

func TestAssessInvestigationRejectsMixedAuthority(t *testing.T) {
	for _, request := range []string{"Investigate the parser and fix it", "Diagnose the failure by running tests"} {
		decision := AssessInvestigation(request, nil)
		if decision.Eligible || !decision.NeedsClarification || decision.Reason == "" {
			t.Fatalf("request %q decision = %#v", request, decision)
		}
	}
}

func TestAssessInvestigationAllowsRunningAsObservedState(t *testing.T) {
	decision := AssessInvestigation("Investigate why the execution remains running", nil)
	if !decision.Eligible || decision.NeedsClarification {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestAssessInvestigationDoesNotClaimArbitraryRequest(t *testing.T) {
	decision := AssessInvestigation("parser timeout", nil)
	if decision.Eligible || decision.NeedsClarification {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestAssessInvestigationRequestsClarificationForBroadQuestion(t *testing.T) {
	decision := AssessInvestigation("How does everything fit together?", nil)
	if decision.Eligible || !decision.NeedsClarification {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestInvestigationBootstrapPrecedesCapabilityBoundary(t *testing.T) {
	plan, decision := InvestigationBootstrap("Trace the daemon recovery timeout", []string{"do not deploy"}, false)
	if !decision.Eligible || len(plan.Tasks) != 2 {
		t.Fatalf("plan = %#v; decision = %#v", plan, decision)
	}
	if plan.Tasks[0].Method != MethodInvestigationBootstrap || plan.Tasks[1].Method != MethodSemanticPlanning {
		t.Fatalf("tasks = %#v", plan.Tasks)
	}
	if plan.Tasks[1].BlockedReason == "" || len(plan.Tasks[1].DependsOn) != 1 || plan.Tasks[1].DependsOn[0] != plan.Tasks[0].Key {
		t.Fatalf("semantic task = %#v", plan.Tasks[1])
	}
}
