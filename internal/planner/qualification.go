package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type QualificationRecommendation string

const (
	RecommendationQualified    QualificationRecommendation = "QUALIFIED"
	RecommendationNotQualified QualificationRecommendation = "NOT_QUALIFIED"
	RecommendationInconclusive QualificationRecommendation = "INCONCLUSIVE"
)

type QualificationRequirement struct {
	Name  string   `json:"name"`
	AnyOf []string `json:"any_of"`
}

type QualificationDependency struct {
	BeforeMethod string `json:"before_method"`
	AfterMethod  string `json:"after_method"`
}

type QualificationCase struct {
	Name                 string                     `json:"name"`
	Objective            string                     `json:"objective"`
	Constraints          []string                   `json:"constraints,omitempty"`
	RequiredOperations   []string                   `json:"required_operations,omitempty"`
	RequiredDependencies []QualificationDependency  `json:"required_dependencies,omitempty"`
	Requirements         []QualificationRequirement `json:"requirements,omitempty"`
	ExpectUnsupported    bool                       `json:"expect_unsupported,omitempty"`
	ExpectClarification  bool                       `json:"expect_clarification,omitempty"`
}

type RequirementCoverage struct {
	Requirement string `json:"requirement"`
	Covered     bool   `json:"covered"`
	Matched     string `json:"matched,omitempty"`
}

type QualificationCriteria struct {
	SchemaValid             bool `json:"schema_valid"`
	RequirementCoverage     bool `json:"requirement_coverage"`
	ConstraintsPreserved    bool `json:"constraints_preserved"`
	DependenciesCorrect     bool `json:"dependencies_correct"`
	OperationsSupported     bool `json:"operations_supported"`
	EvidenceRequirements    bool `json:"evidence_requirements"`
	UnsupportedRecognized   bool `json:"unsupported_recognized"`
	ClarificationRecognized bool `json:"clarification_recognized"`
}

type QualificationRun struct {
	Case                  string                `json:"case"`
	Attempt               int                   `json:"attempt"`
	Passed                bool                  `json:"passed"`
	InfrastructureFailure bool                  `json:"infrastructure_failure"`
	Criteria              QualificationCriteria `json:"criteria"`
	RequirementResults    []RequirementCoverage `json:"requirement_results,omitempty"`
	MissingOperations     []string              `json:"missing_operations,omitempty"`
	ValidationErrors      []string              `json:"validation_errors,omitempty"`
	HumanReview           []string              `json:"human_review,omitempty"`
	TaskCount             int                   `json:"task_count,omitempty"`
}

type QualificationConsistency struct {
	Case            string  `json:"case"`
	PassingAttempts int     `json:"passing_attempts"`
	Attempts        int     `json:"attempts"`
	PassRate        float64 `json:"pass_rate"`
	Consistent      bool    `json:"consistent"`
}

type QualificationAggregate struct {
	ExpectedRuns              int     `json:"expected_runs"`
	CompletedRuns             int     `json:"completed_runs"`
	PassingRuns               int     `json:"passing_runs"`
	ModelFailures             int     `json:"model_failures"`
	InfrastructureFailures    int     `json:"infrastructure_failures"`
	HumanReviewRuns           int     `json:"human_review_runs"`
	PassRate                  float64 `json:"pass_rate"`
	SchemaValidRuns           int     `json:"schema_valid_runs"`
	RequirementCoverageRuns   int     `json:"requirement_coverage_runs"`
	ConstraintsPreservedRuns  int     `json:"constraints_preserved_runs"`
	DependenciesCorrectRuns   int     `json:"dependencies_correct_runs"`
	OperationsSupportedRuns   int     `json:"operations_supported_runs"`
	EvidenceRequirementsRuns  int     `json:"evidence_requirements_runs"`
	UnsupportedRecognizedRuns int     `json:"unsupported_recognized_runs"`
}

// QualificationReport is an evaluation artifact only. Nothing in the report
// changes provider configuration or grants a runtime capability.
type QualificationReport struct {
	Model               string                      `json:"model"`
	ProviderFingerprint string                      `json:"provider_configuration_fingerprint"`
	Capability          string                      `json:"capability"`
	EvaluatedAt         time.Time                   `json:"evaluated_at"`
	CaseCount           int                         `json:"case_count"`
	Repetitions         int                         `json:"repetitions"`
	Runs                []QualificationRun          `json:"runs"`
	Consistency         []QualificationConsistency  `json:"consistency"`
	Aggregate           QualificationAggregate      `json:"aggregate"`
	Recommendation      QualificationRecommendation `json:"recommendation"`
}

// Qualify repeatedly evaluates semantic planning competence. Consistency is
// based on satisfying the same requirements on every attempt, not on emitting
// byte-identical task graphs.
func Qualify(ctx context.Context, d Decomposer, base Request, model, providerFingerprint, capability string, cases []QualificationCase, repetitions int) QualificationReport {
	if repetitions < 1 {
		repetitions = 1
	}
	report := QualificationReport{Model: model, ProviderFingerprint: providerFingerprint, Capability: capability, EvaluatedAt: time.Now().UTC(), CaseCount: len(cases), Repetitions: repetitions, Recommendation: RecommendationInconclusive}
	report.Aggregate.ExpectedRuns = len(cases) * repetitions

abort:
	for _, testCase := range cases {
		for attempt := 1; attempt <= repetitions; attempt++ {
			if err := ctx.Err(); err != nil {
				report.Runs = append(report.Runs, QualificationRun{Case: testCase.Name, Attempt: attempt, InfrastructureFailure: true, ValidationErrors: []string{err.Error()}})
				break abort
			}
			request := base
			request.Objective = testCase.Objective
			request.Constraints = append([]string(nil), testCase.Constraints...)
			proposal, err := d.Propose(ctx, request)
			run := QualificationRun{Case: testCase.Name, Attempt: attempt}
			if err != nil {
				var decomposition *DecompositionError
				if errors.As(err, &decomposition) && decomposition.Kind == FailureInfrastructure {
					run.InfrastructureFailure = true
				}
				run.ValidationErrors = qualificationErrors(err)
				report.Runs = append(report.Runs, run)
				if run.InfrastructureFailure {
					break abort
				}
				continue
			}
			if err := ValidateProposal(request, proposal); err != nil {
				run.ValidationErrors = qualificationErrors(err)
				report.Runs = append(report.Runs, run)
				continue
			}

			run.TaskCount = len(proposal.Tasks)
			run.Criteria.SchemaValid = true
			run.Criteria.DependenciesCorrect = true
			run.Criteria.OperationsSupported = true
			run.Criteria.ConstraintsPreserved = preservesConstraints(testCase.Constraints, proposal.Constraints)
			operations := map[string]bool{}
			unsupported, clarification := false, false
			searchable := ""
			for _, task := range proposal.Tasks {
				operations[task.Method] = true
				unsupported = unsupported || task.Method == MethodUnsupported
				clarification = clarification || task.Method == MethodHumanClarification
				searchable += " " + strings.ToLower(task.Objective+" "+task.RequiredEvidence+" "+task.CompletionCriteria)
			}
			for _, operation := range testCase.RequiredOperations {
				if !operations[operation] {
					run.MissingOperations = append(run.MissingOperations, operation)
				}
			}
			for _, requirement := range testCase.Requirements {
				result := RequirementCoverage{Requirement: requirement.Name}
				for _, phrase := range requirement.AnyOf {
					if strings.Contains(searchable, strings.ToLower(phrase)) {
						result.Covered, result.Matched = true, phrase
						break
					}
				}
				run.RequirementResults = append(run.RequirementResults, result)
			}
			run.Criteria.RequirementCoverage = len(run.MissingOperations) == 0 && allRequirementsCovered(run.RequirementResults)
			run.Criteria.DependenciesCorrect = requiredDependenciesPresent(proposal, testCase.RequiredDependencies)
			run.Criteria.EvidenceRequirements = proposalHasSpecificEvidence(proposal)
			run.Criteria.UnsupportedRecognized = unsupported == testCase.ExpectUnsupported
			run.Criteria.ClarificationRecognized = clarification == testCase.ExpectClarification
			run.Passed = criteriaPass(run.Criteria) && len(run.HumanReview) == 0
			report.Runs = append(report.Runs, run)
		}
	}

	return FinalizeQualificationReport(report, cases)
}

// FinalizeQualificationReport derives consistency, aggregate scores, and the
// recommendation from persisted individual runs. Daemon jobs use it after
// reconstructing a report without rerunning completed evaluations.
func FinalizeQualificationReport(report QualificationReport, cases []QualificationCase) QualificationReport {
	report.Aggregate = QualificationAggregate{ExpectedRuns: len(cases) * report.Repetitions}
	report.Consistency = nil
	report.Aggregate.CompletedRuns = len(report.Runs)
	for _, run := range report.Runs {
		if run.Criteria.SchemaValid {
			report.Aggregate.SchemaValidRuns++
		}
		if run.Criteria.RequirementCoverage {
			report.Aggregate.RequirementCoverageRuns++
		}
		if run.Criteria.ConstraintsPreserved {
			report.Aggregate.ConstraintsPreservedRuns++
		}
		if run.Criteria.DependenciesCorrect {
			report.Aggregate.DependenciesCorrectRuns++
		}
		if run.Criteria.OperationsSupported {
			report.Aggregate.OperationsSupportedRuns++
		}
		if run.Criteria.EvidenceRequirements {
			report.Aggregate.EvidenceRequirementsRuns++
		}
		if run.Criteria.UnsupportedRecognized {
			report.Aggregate.UnsupportedRecognizedRuns++
		}
		if run.Passed {
			report.Aggregate.PassingRuns++
		}
		if run.InfrastructureFailure {
			report.Aggregate.InfrastructureFailures++
		} else if !run.Passed {
			report.Aggregate.ModelFailures++
		}
		if len(run.HumanReview) > 0 {
			report.Aggregate.HumanReviewRuns++
		}
	}
	if report.Aggregate.CompletedRuns > 0 {
		report.Aggregate.PassRate = float64(report.Aggregate.PassingRuns) / float64(report.Aggregate.CompletedRuns)
	}
	for _, testCase := range cases {
		item := QualificationConsistency{Case: testCase.Name}
		for _, run := range report.Runs {
			if run.Case == testCase.Name {
				item.Attempts++
				if run.Passed {
					item.PassingAttempts++
				}
			}
		}
		if item.Attempts > 0 {
			item.PassRate = float64(item.PassingAttempts) / float64(item.Attempts)
		}
		item.Consistent = item.Attempts == report.Repetitions && item.PassingAttempts == report.Repetitions
		report.Consistency = append(report.Consistency, item)
	}
	switch {
	case report.Aggregate.InfrastructureFailures > 0 || report.Aggregate.HumanReviewRuns > 0 || report.Aggregate.CompletedRuns != report.Aggregate.ExpectedRuns:
		report.Recommendation = RecommendationInconclusive
	case report.Aggregate.PassingRuns == report.Aggregate.ExpectedRuns:
		report.Recommendation = RecommendationQualified
	default:
		report.Recommendation = RecommendationNotQualified
	}
	return report
}

// QualificationRequest is the bounded synthetic repository context shared by
// standalone and daemon-backed qualification entry points.
func QualificationRequest(model, providerFingerprint, capability string) Request {
	return Request{
		Project:           Project{ID: 1, Name: "qualification-fixture"},
		Repository:        []RepositoryFact{{ID: "qualification-project", Kind: "project", Summary: "representative qualification project; repository facts must be established by supported deterministic operations"}},
		Operations:        SupportedOperations(),
		ModelCapabilities: []ModelCapability{{Name: capability, Model: model, Qualified: true, Config: providerFingerprint}},
	}
}

func qualificationErrors(err error) []string {
	var validation *ValidationError
	if errors.As(err, &validation) {
		out := make([]string, 0, len(validation.Diagnostics))
		for _, diagnostic := range validation.Diagnostics {
			out = append(out, diagnostic.Path+": "+diagnostic.Message)
		}
		return out
	}
	return []string{err.Error()}
}

func preservesConstraints(want, got []string) bool {
	set := map[string]bool{}
	for _, value := range got {
		set[value] = true
	}
	for _, value := range want {
		if !set[value] {
			return false
		}
	}
	return true
}

func allRequirementsCovered(results []RequirementCoverage) bool {
	for _, result := range results {
		if !result.Covered {
			return false
		}
	}
	return true
}

func requiredDependenciesPresent(proposal Proposal, requirements []QualificationDependency) bool {
	tasks := make(map[string]Task, len(proposal.Tasks))
	for _, task := range proposal.Tasks {
		tasks[task.Key] = task
	}
	for _, requirement := range requirements {
		found := false
		for _, task := range proposal.Tasks {
			if task.Method != requirement.AfterMethod {
				continue
			}
			for _, dependency := range task.DependsOn {
				if tasks[dependency].Method == requirement.BeforeMethod {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func criteriaPass(c QualificationCriteria) bool {
	return c.SchemaValid && c.RequirementCoverage && c.ConstraintsPreserved && c.DependenciesCorrect && c.OperationsSupported && c.EvidenceRequirements && c.UnsupportedRecognized && c.ClarificationRecognized
}

func proposalHasSpecificEvidence(proposal Proposal) bool {
	tokens := map[string][]string{
		MethodInventoryLookup: {"path", "blob", "commit", "file"}, MethodCommittedSearch: {"path", "line", "match", "blob"},
		MethodSourceExcerpt: {"path", "line", "excerpt", "blob"}, MethodComponentLookup: {"identity", "path", "declaration", "component"},
		MethodDependencyLookup: {"relationship", "dependency", "path", "line"}, MethodIncrementalPlanning: {"evidence", "proposal", "plan"},
		MethodHumanClarification: {"answer", "decision", "clarification"}, MethodUnsupported: {"capability", "reason", "unsupported"},
	}
	for _, task := range proposal.Tasks {
		text := strings.ToLower(task.RequiredEvidence)
		if len(strings.TrimSpace(text)) < 8 {
			return false
		}
		matched := false
		for _, token := range tokens[task.Method] {
			if strings.Contains(text, token) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func RepresentativeQualificationCases() []QualificationCase {
	return []QualificationCase{
		{Name: "repository-information", Objective: "List the committed files under docs without modifying the repository", Constraints: []string{"do not modify the repository"}, RequiredOperations: []string{MethodInventoryLookup}, Requirements: []QualificationRequirement{{Name: "committed paths", AnyOf: []string{"committed path", "file path", "inventory"}}}},
		{Name: "config-to-implementation", Objective: "Locate the declaration of setting MAX_RETRIES, then identify the implementation that consumes MAX_RETRIES without changing files", Constraints: []string{"do not modify files"}, RequiredOperations: []string{MethodCommittedSearch, MethodIncrementalPlanning}, RequiredDependencies: []QualificationDependency{{BeforeMethod: MethodCommittedSearch, AfterMethod: MethodIncrementalPlanning}}, Requirements: []QualificationRequirement{{Name: "declaration", AnyOf: []string{"declaration", "define"}}, {Name: "consumer", AnyOf: []string{"consumer", "consumes", "implementation"}}}},
		{Name: "multi-part-investigation", Objective: "Find where feature flag ENABLE_CACHE is declared, capture its committed source context, and determine which component uses it", RequiredOperations: []string{MethodCommittedSearch, MethodSourceExcerpt, MethodIncrementalPlanning}, RequiredDependencies: []QualificationDependency{{BeforeMethod: MethodCommittedSearch, AfterMethod: MethodSourceExcerpt}, {BeforeMethod: MethodSourceExcerpt, AfterMethod: MethodIncrementalPlanning}}, Requirements: []QualificationRequirement{{Name: "declaration", AnyOf: []string{"declaration", "declared"}}, {Name: "source context", AnyOf: []string{"excerpt", "context"}}, {Name: "usage", AnyOf: []string{"uses", "consumer", "component"}}}},
		{Name: "dependency-tracing", Objective: "Show the direct dependencies of component example.invalid/service", RequiredOperations: []string{MethodComponentLookup, MethodDependencyLookup}, RequiredDependencies: []QualificationDependency{{BeforeMethod: MethodComponentLookup, AfterMethod: MethodDependencyLookup}}, Requirements: []QualificationRequirement{{Name: "direct dependencies", AnyOf: []string{"direct dependencies", "relationship"}}}},
		{Name: "huntmaster-geography", Objective: "Investigate which geography zones are eligible for Huntmaster by locating the eligibility declaration and then tracing the implementation that consumes it; read only", Constraints: []string{"read only"}, RequiredOperations: []string{MethodCommittedSearch, MethodIncrementalPlanning}, RequiredDependencies: []QualificationDependency{{BeforeMethod: MethodCommittedSearch, AfterMethod: MethodIncrementalPlanning}}, Requirements: []QualificationRequirement{{Name: "geography eligibility", AnyOf: []string{"geography", "zone", "eligibility"}}, {Name: "declaration", AnyOf: []string{"declaration", "declared"}}, {Name: "consumer", AnyOf: []string{"consumer", "consumes", "implementation"}}}},
		{Name: "unsupported-deployment", Objective: "Deploy the current branch to production", Constraints: []string{"do not deploy"}, RequiredOperations: []string{MethodUnsupported}, Requirements: []QualificationRequirement{{Name: "deployment unsupported", AnyOf: []string{"deploy", "deployment", "unsupported"}}}, ExpectUnsupported: true},
		{Name: "ambiguous-change", Objective: "Make the service better without changing anything until I clarify what better means", Constraints: []string{"do not change anything until I clarify"}, RequiredOperations: []string{MethodHumanClarification}, Requirements: []QualificationRequirement{{Name: "definition of better", AnyOf: []string{"better", "desired behavior", "clarification"}}}, ExpectClarification: true},
	}
}

func WriteQualificationReport(path string, report QualificationReport) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("qualification report path is required")
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create qualification report directory: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write qualification report: %w", err)
	}
	return os.Chmod(path, 0o600)
}
