package planner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type qualificationDecomposerFunc func(context.Context, Request) (Proposal, error)

func (f qualificationDecomposerFunc) Propose(ctx context.Context, request Request) (Proposal, error) {
	return f(ctx, request)
}

func qualificationBaseRequest() Request {
	return Request{Project: Project{ID: 1, Name: "fixture"}, Operations: SupportedOperations(), ModelCapabilities: []ModelCapability{{Name: CapabilityPlanning, Model: "m", Qualified: true}}}
}

func TestQualifyAcceptsDifferentGraphsWhenEveryRunMeetsRequirements(t *testing.T) {
	calls := 0
	d := qualificationDecomposerFunc(func(_ context.Context, request Request) (Proposal, error) {
		calls++
		key := "find-token"
		if calls%2 == 0 {
			key = "locate-token"
		}
		return Proposal{Objective: "Locate TOKEN", Constraints: request.Constraints, ReadOnly: true, Tasks: []Task{{Key: key, Objective: "Locate the requested token", Method: MethodCommittedSearch, Inputs: map[string]string{"term": "TOKEN"}, RequiredEvidence: "committed path, blob, and matching line", CompletionCriteria: "all bounded matches are recorded"}}}, nil
	})
	cases := []QualificationCase{{Name: "different-valid-graphs", Objective: "Find TOKEN without modifications", Constraints: []string{"read only"}, RequiredOperations: []string{MethodCommittedSearch}, Requirements: []QualificationRequirement{{Name: "locate", AnyOf: []string{"locate", "find"}}}}}
	report := Qualify(context.Background(), d, qualificationBaseRequest(), "m", "fingerprint", CapabilityPlanning, cases, 2)
	if report.Recommendation != RecommendationQualified || report.Aggregate.PassingRuns != 2 || !report.Consistency[0].Consistent {
		t.Fatalf("report = %#v", report)
	}
}

func TestQualifyRejectsConsistentlyIncorrectAndVaguePlans(t *testing.T) {
	d := qualificationDecomposerFunc(func(_ context.Context, request Request) (Proposal, error) {
		return Proposal{Objective: "Inventory", Constraints: request.Constraints, ReadOnly: true, Tasks: []Task{{Key: "inventory", Objective: "Inventory files", Method: MethodInventoryLookup, RequiredEvidence: "evidence", CompletionCriteria: "inventory completes"}}}, nil
	})
	cases := []QualificationCase{{Name: "needs-search", Objective: "Find TOKEN", RequiredOperations: []string{MethodCommittedSearch}, Requirements: []QualificationRequirement{{Name: "token", AnyOf: []string{"token"}}}}}
	report := Qualify(context.Background(), d, qualificationBaseRequest(), "m", "fingerprint", CapabilityPlanning, cases, 2)
	if report.Recommendation != RecommendationNotQualified || report.Aggregate.ModelFailures != 2 {
		t.Fatalf("report = %#v", report)
	}
	if report.Runs[0].Criteria.RequirementCoverage || report.Runs[0].Criteria.EvidenceRequirements {
		t.Fatalf("criteria = %#v", report.Runs[0].Criteria)
	}
}

func TestQualifyDoesNotCountCopiedObjectiveAsTaskCoverage(t *testing.T) {
	d := qualificationDecomposerFunc(func(_ context.Context, request Request) (Proposal, error) {
		return Proposal{Objective: request.Objective, ReadOnly: true, Tasks: []Task{{Key: "inventory", Objective: "Inventory repository files", Method: MethodInventoryLookup, RequiredEvidence: "committed file path and blob records", CompletionCriteria: "the bounded inventory is recorded"}}}, nil
	})
	cases := []QualificationCase{{Name: "missing-consumer", Objective: "Find TOKEN and identify its consumer", RequiredOperations: []string{MethodInventoryLookup}, Requirements: []QualificationRequirement{{Name: "consumer", AnyOf: []string{"consumer"}}}}}
	report := Qualify(context.Background(), d, qualificationBaseRequest(), "m", "fingerprint", CapabilityPlanning, cases, 1)
	if report.Recommendation != RecommendationNotQualified || report.Runs[0].Criteria.RequirementCoverage {
		t.Fatalf("report = %#v", report)
	}
}

func TestQualifyRequiresExpectedDependencyEdges(t *testing.T) {
	d := qualificationDecomposerFunc(func(_ context.Context, request Request) (Proposal, error) {
		return Proposal{Objective: request.Objective, ReadOnly: true, Tasks: []Task{
			{Key: "component", Objective: "Resolve the component", Method: MethodComponentLookup, Inputs: map[string]string{"identity": "example.invalid/service"}, RequiredEvidence: "component identity and declaration path", CompletionCriteria: "component is resolved"},
			{Key: "relationships", Objective: "List direct dependency relationships", Method: MethodDependencyLookup, Inputs: map[string]string{"identity": "example.invalid/service", "relation": "dependencies"}, RequiredEvidence: "dependency relationship path and line", CompletionCriteria: "direct dependencies are recorded"},
		}}, nil
	})
	cases := []QualificationCase{{Name: "unordered-dependencies", Objective: "Show direct dependencies of example.invalid/service", RequiredOperations: []string{MethodComponentLookup, MethodDependencyLookup}, RequiredDependencies: []QualificationDependency{{BeforeMethod: MethodComponentLookup, AfterMethod: MethodDependencyLookup}}, Requirements: []QualificationRequirement{{Name: "dependencies", AnyOf: []string{"dependencies"}}}}}
	report := Qualify(context.Background(), d, qualificationBaseRequest(), "m", "fingerprint", CapabilityPlanning, cases, 1)
	if report.Recommendation != RecommendationNotQualified || report.Runs[0].Criteria.DependenciesCorrect {
		t.Fatalf("report = %#v", report)
	}
}

func TestQualifySeparatesInfrastructureFailure(t *testing.T) {
	d := qualificationDecomposerFunc(func(context.Context, Request) (Proposal, error) {
		return Proposal{}, &DecompositionError{Kind: FailureInfrastructure, Err: errors.New("provider unavailable")}
	})
	report := Qualify(context.Background(), d, qualificationBaseRequest(), "m", "fingerprint", CapabilityPlanning, []QualificationCase{{Name: "case", Objective: "List files"}}, 3)
	if report.Recommendation != RecommendationInconclusive || report.Aggregate.InfrastructureFailures != 1 || report.Aggregate.CompletedRuns != 1 {
		t.Fatalf("report = %#v", report)
	}
}

func TestWriteQualificationReportSerializesAndSecuresFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "report.json")
	report := QualificationReport{Model: "worker", ProviderFingerprint: "sha256:test", Capability: CapabilityPlanning, Recommendation: RecommendationNotQualified}
	if err := WriteQualificationReport(path, report); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded QualificationReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Model != "worker" || decoded.Recommendation != RecommendationNotQualified {
		t.Fatalf("decoded = %#v", decoded)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %#o", info.Mode().Perm())
	}
}
