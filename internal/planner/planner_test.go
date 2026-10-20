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
