package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/Hisha/Forgehand/internal/provider"
)

const (
	MethodIncrementalPlanning = "planning.incremental_revision"
	MethodHumanClarification  = "human.clarification"
	MethodUnsupported         = "capability.unsupported"
	CapabilityPlanning        = "planning"
	CapabilityInterpretation  = "interpretation"
	CapabilityCoding          = "coding"
	CapabilityDeterministic   = "deterministic"
)

const (
	DefaultMaxTasks          = 24
	DefaultMaxPlanningRounds = 3
	DefaultMaxPromptChars    = 48000
	DefaultMaxOutputChars    = 32000
)

// Decomposer is independent of any model provider. Deterministic and model-
// assisted planners implement the same bounded contract.
type Decomposer interface {
	Propose(context.Context, Request) (Proposal, error)
}

type Request struct {
	Objective         string            `json:"objective"`
	Constraints       []string          `json:"constraints,omitempty"`
	Project           Project           `json:"project"`
	Repository        []RepositoryFact  `json:"repository_knowledge,omitempty"`
	Operations        []Operation       `json:"supported_operations"`
	ModelCapabilities []ModelCapability `json:"qualified_model_capabilities,omitempty"`
	Completed         []CompletedTask   `json:"completed_tasks,omitempty"`
	PlanningRound     int               `json:"planning_round"`
}

type Project struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type RepositoryFact struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Summary  string `json:"summary"`
	Evidence string `json:"evidence,omitempty"`
}

type Operation struct {
	Name        string   `json:"name"`
	Capability  string   `json:"capability"`
	Description string   `json:"description"`
	Inputs      []string `json:"inputs,omitempty"`
}

type ModelCapability struct {
	Name      string `json:"name"`
	Model     string `json:"model"`
	Qualified bool   `json:"qualified"`
	Config    string `json:"configuration,omitempty"`
}

type CompletedTask struct {
	Key      string   `json:"key"`
	Result   string   `json:"result"`
	Evidence []string `json:"evidence,omitempty"`
}

type Proposal struct {
	Objective           string   `json:"objective"`
	Constraints         []string `json:"constraints,omitempty"`
	ReadOnly            bool     `json:"read_only"`
	Tasks               []Task   `json:"tasks"`
	UnresolvedQuestions []string `json:"unresolved_questions,omitempty"`
}

type Diagnostic struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type ValidationError struct{ Diagnostics []Diagnostic }

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Diagnostics))
	for _, d := range e.Diagnostics {
		parts = append(parts, d.Path+": "+d.Message)
	}
	return "invalid semantic plan: " + strings.Join(parts, "; ")
}

// SupportedOperations is the allow-list exposed to a planning model. It is
// also the execution allow-list used by ValidateProposal.
func SupportedOperations() []Operation {
	return []Operation{
		{Name: MethodInventoryLookup, Capability: CapabilityDeterministic, Description: "load commit-pinned repository paths", Inputs: []string{"path_prefix"}},
		{Name: MethodCommittedSearch, Capability: CapabilityDeterministic, Description: "literal search of commit-pinned text", Inputs: []string{"term", "path_prefix"}},
		{Name: MethodSourceExcerpt, Capability: CapabilityDeterministic, Description: "read bounded excerpts from evidence produced by another task", Inputs: []string{"source_task"}},
		{Name: MethodComponentLookup, Capability: CapabilityDeterministic, Description: "resolve a component from stored analysis", Inputs: []string{"identity"}},
		{Name: MethodDependencyLookup, Capability: CapabilityDeterministic, Description: "read direct dependencies or dependents from stored analysis", Inputs: []string{"identity", "relation"}},
		{Name: MethodIncrementalPlanning, Capability: CapabilityPlanning, Description: "revise the plan after dependencies establish new evidence"},
		{Name: MethodHumanClarification, Capability: "human", Description: "block on a specific unresolved user decision", Inputs: []string{"question"}},
		{Name: MethodUnsupported, Capability: "unsupported", Description: "identify work Forgehand cannot currently execute", Inputs: []string{"capability"}},
	}
}

var taskKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func ValidateRequest(request Request) error {
	var diagnostics []Diagnostic
	if strings.TrimSpace(request.Objective) == "" {
		diagnostics = append(diagnostics, Diagnostic{"objective", "must not be empty"})
	}
	if request.Project.ID <= 0 || strings.TrimSpace(request.Project.Name) == "" {
		diagnostics = append(diagnostics, Diagnostic{"project", "must identify a selected project"})
	}
	if request.PlanningRound < 0 || request.PlanningRound > DefaultMaxPlanningRounds {
		diagnostics = append(diagnostics, Diagnostic{"planning_round", fmt.Sprintf("must be between 0 and %d", DefaultMaxPlanningRounds)})
	}
	seenConstraints := map[string]bool{}
	for i, constraint := range request.Constraints {
		if strings.TrimSpace(constraint) == "" {
			diagnostics = append(diagnostics, Diagnostic{fmt.Sprintf("constraints[%d]", i), "must not be empty"})
		}
		if seenConstraints[constraint] {
			diagnostics = append(diagnostics, Diagnostic{fmt.Sprintf("constraints[%d]", i), "is duplicated"})
		}
		seenConstraints[constraint] = true
	}
	seenOperations := map[string]bool{}
	for i, operation := range request.Operations {
		if strings.TrimSpace(operation.Name) == "" {
			diagnostics = append(diagnostics, Diagnostic{fmt.Sprintf("supported_operations[%d].name", i), "must not be empty"})
		}
		if seenOperations[operation.Name] {
			diagnostics = append(diagnostics, Diagnostic{fmt.Sprintf("supported_operations[%d].name", i), "is duplicated"})
		}
		seenOperations[operation.Name] = true
	}
	if len(request.Operations) == 0 {
		diagnostics = append(diagnostics, Diagnostic{"supported_operations", "must not be empty"})
	}
	seenFacts := map[string]bool{}
	for i, fact := range request.Repository {
		if strings.TrimSpace(fact.ID) == "" {
			diagnostics = append(diagnostics, Diagnostic{fmt.Sprintf("repository_knowledge[%d].id", i), "must not be empty"})
		}
		if seenFacts[fact.ID] {
			diagnostics = append(diagnostics, Diagnostic{fmt.Sprintf("repository_knowledge[%d].id", i), "is duplicated"})
		}
		seenFacts[fact.ID] = true
	}
	if len(diagnostics) > 0 {
		return &ValidationError{Diagnostics: diagnostics}
	}
	return nil
}

// ValidateProposal rejects invented operations, repository-fact references,
// malformed dependencies, cycles, unbounded expansion, and capability use
// that has not been explicitly qualified.
func ValidateProposal(request Request, proposal Proposal) error {
	var diagnostics []Diagnostic
	knownText := strings.ToLower(request.Objective)
	for _, fact := range request.Repository {
		knownText += " " + strings.ToLower(fact.Summary+" "+fact.Evidence)
	}
	for _, completed := range request.Completed {
		knownText += " " + strings.ToLower(completed.Result+" "+strings.Join(completed.Evidence, " "))
	}
	if strings.TrimSpace(proposal.Objective) == "" {
		diagnostics = append(diagnostics, Diagnostic{"objective", "must not be empty"})
	}
	if !proposal.ReadOnly {
		diagnostics = append(diagnostics, Diagnostic{"read_only", "must be true; semantic work plans cannot authorize code modification"})
	}
	if len(proposal.Tasks) == 0 {
		diagnostics = append(diagnostics, Diagnostic{"tasks", "must contain at least one task"})
	}
	if len(proposal.Tasks) > DefaultMaxTasks {
		diagnostics = append(diagnostics, Diagnostic{"tasks", fmt.Sprintf("contains %d tasks; maximum is %d", len(proposal.Tasks), DefaultMaxTasks)})
	}
	ops := make(map[string]Operation)
	for _, op := range request.Operations {
		ops[op.Name] = op
	}
	facts := make(map[string]bool)
	for _, fact := range request.Repository {
		facts[fact.ID] = true
	}
	capabilities := make(map[string]bool)
	for _, capability := range request.ModelCapabilities {
		if capability.Qualified {
			capabilities[capability.Name] = true
		}
	}
	keys := make(map[string]int)
	planningTasks := 0
	for i, task := range proposal.Tasks {
		path := fmt.Sprintf("tasks[%d]", i)
		if !taskKeyPattern.MatchString(task.Key) {
			diagnostics = append(diagnostics, Diagnostic{path + ".key", "must match ^[a-z][a-z0-9-]{0,62}$"})
		} else if _, exists := keys[task.Key]; exists {
			diagnostics = append(diagnostics, Diagnostic{path + ".key", "is duplicated"})
		}
		keys[task.Key] = i
		op, ok := ops[task.Method]
		if !ok {
			diagnostics = append(diagnostics, Diagnostic{path + ".method", fmt.Sprintf("%q is not a supported operation", task.Method)})
		} else if (op.Capability == CapabilityPlanning || op.Capability == CapabilityInterpretation || op.Capability == CapabilityCoding) && !capabilities[op.Capability] {
			diagnostics = append(diagnostics, Diagnostic{path + ".method", fmt.Sprintf("requires unqualified model capability %q", op.Capability)})
		}
		if ok {
			allowedInputs := map[string]bool{}
			for _, name := range op.Inputs {
				allowedInputs[name] = true
			}
			for name, value := range task.Inputs {
				if !allowedInputs[name] {
					diagnostics = append(diagnostics, Diagnostic{path + ".inputs." + name, "is not an input supported by this operation"})
				}
				if len(value) > 4096 {
					diagnostics = append(diagnostics, Diagnostic{path + ".inputs." + name, "exceeds the 4096 character limit"})
				}
			}
		}
		if task.Method == MethodIncrementalPlanning {
			planningTasks++
		}
		if strings.TrimSpace(task.Objective) == "" {
			diagnostics = append(diagnostics, Diagnostic{path + ".objective", "must not be empty"})
		}
		if strings.TrimSpace(task.RequiredEvidence) == "" {
			diagnostics = append(diagnostics, Diagnostic{path + ".required_evidence", "must not be empty"})
		}
		if strings.TrimSpace(task.CompletionCriteria) == "" {
			diagnostics = append(diagnostics, Diagnostic{path + ".completion_criteria", "must not be empty"})
		}
		for _, ref := range task.FactRefs {
			if !facts[ref] {
				diagnostics = append(diagnostics, Diagnostic{path + ".fact_refs", fmt.Sprintf("references unknown repository fact %q", ref)})
			}
		}
		if task.Method == MethodHumanClarification && strings.TrimSpace(task.Inputs["question"]) == "" {
			diagnostics = append(diagnostics, Diagnostic{path + ".inputs.question", "is required for human clarification"})
		}
		switch task.Method {
		case MethodCommittedSearch:
			if strings.TrimSpace(task.Inputs["term"]) == "" {
				diagnostics = append(diagnostics, Diagnostic{path + ".inputs.term", "is required"})
			} else if !strings.Contains(knownText, strings.ToLower(task.Inputs["term"])) {
				diagnostics = append(diagnostics, Diagnostic{path + ".inputs.term", "is not grounded in the user objective, repository knowledge, or completed evidence"})
			}
			if prefix := strings.TrimSpace(task.Inputs["path_prefix"]); prefix != "" && !strings.Contains(knownText, strings.ToLower(strings.TrimSuffix(prefix, "/"))) {
				diagnostics = append(diagnostics, Diagnostic{path + ".inputs.path_prefix", "is not grounded in supplied knowledge"})
			}
		case MethodSourceExcerpt:
			if strings.TrimSpace(task.Inputs["source_task"]) == "" {
				diagnostics = append(diagnostics, Diagnostic{path + ".inputs.source_task", "is required"})
			}
		case MethodComponentLookup:
			if strings.TrimSpace(task.Inputs["identity"]) == "" {
				diagnostics = append(diagnostics, Diagnostic{path + ".inputs.identity", "is required"})
			} else if !strings.Contains(knownText, strings.ToLower(task.Inputs["identity"])) {
				diagnostics = append(diagnostics, Diagnostic{path + ".inputs.identity", "is not grounded in supplied knowledge"})
			}
		case MethodDependencyLookup:
			if strings.TrimSpace(task.Inputs["identity"]) == "" {
				diagnostics = append(diagnostics, Diagnostic{path + ".inputs.identity", "is required"})
			} else if !strings.Contains(knownText, strings.ToLower(task.Inputs["identity"])) {
				diagnostics = append(diagnostics, Diagnostic{path + ".inputs.identity", "is not grounded in supplied knowledge"})
			}
			relation := task.Inputs["relation"]
			if relation != "dependencies" && relation != "dependents" {
				diagnostics = append(diagnostics, Diagnostic{path + ".inputs.relation", "must be dependencies or dependents"})
			}
		case MethodIncrementalPlanning:
			if len(task.DependsOn) == 0 {
				diagnostics = append(diagnostics, Diagnostic{path + ".depends_on", "incremental planning must depend on deterministic evidence"})
			}
		}
		if task.Method == MethodUnsupported && strings.TrimSpace(task.BlockedReason) == "" {
			diagnostics = append(diagnostics, Diagnostic{path + ".blocked_reason", "is required for unsupported work"})
		}
	}
	if planningTasks+request.PlanningRound > DefaultMaxPlanningRounds {
		diagnostics = append(diagnostics, Diagnostic{"tasks", fmt.Sprintf("would exceed %d planning rounds", DefaultMaxPlanningRounds)})
	}
	if planningTasks > 1 {
		diagnostics = append(diagnostics, Diagnostic{"tasks", "a proposal may contain at most one incremental planning task"})
	}
	for i, task := range proposal.Tasks {
		for _, dep := range task.DependsOn {
			depIndex, ok := keys[dep]
			if !ok {
				diagnostics = append(diagnostics, Diagnostic{fmt.Sprintf("tasks[%d].depends_on", i), fmt.Sprintf("references unknown task %q", dep)})
			} else if depIndex >= i {
				diagnostics = append(diagnostics, Diagnostic{fmt.Sprintf("tasks[%d].depends_on", i), fmt.Sprintf("dependency %q must appear before the task", dep)})
			}
			if dep == task.Key {
				diagnostics = append(diagnostics, Diagnostic{fmt.Sprintf("tasks[%d].depends_on", i), "task cannot depend on itself"})
			}
		}
		if task.Method == MethodSourceExcerpt {
			source := task.Inputs["source_task"]
			found := false
			for _, dep := range task.DependsOn {
				if dep == source {
					found = true
					break
				}
			}
			if source != "" && !found {
				diagnostics = append(diagnostics, Diagnostic{fmt.Sprintf("tasks[%d].inputs.source_task", i), "must also be listed in depends_on"})
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(key string) bool {
		if visiting[key] {
			return true
		}
		if visited[key] {
			return false
		}
		visiting[key] = true
		for _, dep := range proposal.Tasks[keys[key]].DependsOn {
			if _, ok := keys[dep]; ok && visit(dep) {
				return true
			}
		}
		visiting[key], visited[key] = false, true
		return false
	}
	for key := range keys {
		if visit(key) {
			diagnostics = append(diagnostics, Diagnostic{"tasks", "dependency graph contains a cycle"})
			break
		}
	}
	for _, constraint := range request.Constraints {
		found := false
		for _, preserved := range proposal.Constraints {
			if preserved == constraint {
				found = true
				break
			}
		}
		if !found {
			diagnostics = append(diagnostics, Diagnostic{"constraints", fmt.Sprintf("does not preserve user constraint %q", constraint)})
		}
	}
	if len(diagnostics) > 0 {
		return &ValidationError{Diagnostics: diagnostics}
	}
	return nil
}

type ModelLimits struct {
	MaxPromptChars int
	MaxOutputChars int
	MaxTokens      int
}

type ModelDecomposer struct {
	Provider provider.Provider
	Model    string
	Limits   ModelLimits
}

type DecompositionFailureKind string

const (
	FailureInfrastructure DecompositionFailureKind = "infrastructure"
	FailureModelOutput    DecompositionFailureKind = "model_output"
)

type DecompositionError struct {
	Kind DecompositionFailureKind
	Err  error
}

func (e *DecompositionError) Error() string { return e.Err.Error() }
func (e *DecompositionError) Unwrap() error { return e.Err }

func (p *ModelDecomposer) Propose(ctx context.Context, request Request) (Proposal, error) {
	if p.Provider == nil {
		return Proposal{}, &DecompositionError{Kind: FailureInfrastructure, Err: fmt.Errorf("semantic planner provider is not configured")}
	}
	if err := ValidateRequest(request); err != nil {
		return Proposal{}, &DecompositionError{Kind: FailureInfrastructure, Err: err}
	}
	limits := p.Limits
	if limits.MaxPromptChars <= 0 {
		limits.MaxPromptChars = DefaultMaxPromptChars
	}
	if limits.MaxOutputChars <= 0 {
		limits.MaxOutputChars = DefaultMaxOutputChars
	}
	if limits.MaxTokens <= 0 {
		limits.MaxTokens = 4096
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return Proposal{}, &DecompositionError{Kind: FailureInfrastructure, Err: fmt.Errorf("marshal semantic planner input: %w", err)}
	}
	if len(payload) > limits.MaxPromptChars {
		return Proposal{}, &DecompositionError{Kind: FailureInfrastructure, Err: fmt.Errorf("semantic planner input is %d characters; limit is %d", len(payload), limits.MaxPromptChars)}
	}
	response, err := p.Provider.Complete(ctx, provider.ChatRequest{System: semanticSystemPrompt(), User: string(payload), Model: p.Model, MaxTokens: limits.MaxTokens})
	if err != nil {
		return Proposal{}, &DecompositionError{Kind: FailureInfrastructure, Err: fmt.Errorf("semantic planner provider: %w", err)}
	}
	if len(response.Content) > limits.MaxOutputChars {
		return Proposal{}, &DecompositionError{Kind: FailureModelOutput, Err: fmt.Errorf("semantic planner output is %d characters; limit is %d", len(response.Content), limits.MaxOutputChars)}
	}
	var proposal Proposal
	decoder := json.NewDecoder(bytes.NewBufferString(response.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proposal); err != nil {
		return Proposal{}, &DecompositionError{Kind: FailureModelOutput, Err: fmt.Errorf("semantic planner returned invalid JSON schema: %w", err)}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Proposal{}, &DecompositionError{Kind: FailureModelOutput, Err: fmt.Errorf("semantic planner returned more than one JSON value")}
	}
	if err := ValidateProposal(request, proposal); err != nil {
		return Proposal{}, &DecompositionError{Kind: FailureModelOutput, Err: err}
	}
	return proposal, nil
}

func semanticSystemPrompt() string {
	return `You are Forgehand's planning capability. Return exactly one JSON object and no markdown or prose.
Output schema:
{"objective":"string","constraints":["string"],"read_only":true,"tasks":[{"key":"lowercase-hyphenated","objective":"string","method":"one supported_operations name","depends_on":["earlier-task-key"],"inputs":{"supported_input_name":"string"},"required_evidence":"string","completion_criteria":"string","requires_model":false,"blocked_reason":"string when blocked","fact_refs":["repository_knowledge id"]}],"unresolved_questions":["string"]}
All fields other than depends_on, inputs, requires_model, blocked_reason, fact_refs, constraints, and unresolved_questions are required. Use only supported_operations. Repository knowledge is an allow-list of facts: do not invent paths, symbols, dependencies, commands, or tools. Operation inputs must be grounded in the objective, repository_knowledge, or completed_tasks. Reference known facts by fact_refs. Unknowns must be established by deterministic operations, asked through human.clarification, or marked capability.unsupported. Preserve every supplied constraint verbatim in constraints. Keep tasks in dependency order and the graph small and acyclic. A later planning.incremental_revision may be used only when a deterministic dependency is expected to reveal evidence needed to decide subsequent work. Never propose shell commands or autonomous code modification.`
}
