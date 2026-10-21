// Package planner translates a deliberately small request language into
// bounded, deterministic repository work. It is intentionally conservative:
// requests outside the documented forms are represented as blocked semantic
// work instead of being guessed from loose keyword matches.
package planner

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	MethodInventoryLookup        = "repository.inventory.lookup"
	MethodCommittedSearch        = "repository.committed_text.search"
	MethodSourceExcerpt          = "repository.source_excerpt.read"
	MethodComponentLookup        = "repository.component.lookup"
	MethodDependencyLookup       = "repository.dependency.lookup"
	MethodInvestigationBootstrap = "repository.investigation.bootstrap"
	MethodSemanticPlanning       = "planning.semantic_interpretation"
)

type Task struct {
	Key                string            `json:"key"`
	Objective          string            `json:"objective"`
	Method             string            `json:"method"`
	DependsOn          []string          `json:"depends_on,omitempty"`
	Inputs             map[string]string `json:"inputs,omitempty"`
	RequiredEvidence   string            `json:"required_evidence"`
	CompletionCriteria string            `json:"completion_criteria"`
	RequiresModel      bool              `json:"requires_model,omitempty"`
	BlockedReason      string            `json:"blocked_reason,omitempty"`
	FactRefs           []string          `json:"fact_refs,omitempty"`
}

type Plan struct {
	Objective     string
	Original      string
	ReadOnly      bool
	Clarification string
	Tasks         []Task
}

var (
	quotedSearch       = regexp.MustCompile(`(?is)^\s*(?:find|search(?:\s+committed\s+source)?\s+for|where\s+(?:does|is))\s+["']([^"']+)["'](?:\s+(?:in|under)\s+["']?([^"']+?)["']?)?\s*(?:occur|used|defined|located)?\s*[?.!]*\s*$`)
	inventoryRequest   = regexp.MustCompile(`(?is)^\s*(?:list|show|inventory)(?:\s+(?:the\s+)?)?(?:committed\s+)?(?:repository\s+)?files(?:\s+(?:in|under)\s+["']?([^"']+?)["']?)?\s*[?.!]*\s*$`)
	dependencyRequest  = regexp.MustCompile(`(?is)^\s*(?:(?:what|which)\s+(dependencies|dependents)\s+(?:does|do|of)\s*|show\s+(dependencies|dependents)\s+(?:of|for)\s+)(?:component\s+)?["']([^"']+)["']\s*[?.!]*\s*$`)
	modificationIntent = regexp.MustCompile(`(?i)\b(?:add|apply|change|create|delete|edit|fix|implement|modify|remove|rename|rewrite|update|write)\b`)
)

// Interpret recognizes only explicit, falsifiable request forms. A future
// qualified semantic planner can implement the same Plan contract without
// changing persistence or execution.
func Interpret(request string) Plan {
	original := strings.TrimSpace(request)
	plan := Plan{Original: original, Objective: original, ReadOnly: true}
	if original == "" {
		return blocked(plan, "the request is empty; provide a repository question")
	}
	if modificationIntent.MatchString(original) {
		return blocked(plan, "this planner is read-only and does not plan or perform repository modifications")
	}

	if match := inventoryRequest.FindStringSubmatch(original); match != nil {
		prefix := strings.Trim(strings.TrimSpace(match[1]), `"'`)
		plan.Objective = "Inventory committed repository files"
		if prefix != "" {
			plan.Objective += " under " + prefix
		}
		plan.Tasks = []Task{{
			Key: "inventory", Objective: plan.Objective, Method: MethodInventoryLookup,
			Inputs:             map[string]string{"path_prefix": prefix},
			RequiredEvidence:   "commit-pinned inventory file records",
			CompletionCriteria: "return matching committed paths with blob identity",
		}}
		return plan
	}

	if match := quotedSearch.FindStringSubmatch(original); match != nil {
		term := strings.TrimSpace(match[1])
		prefix := strings.Trim(strings.TrimSpace(match[2]), `"'`)
		if term == "" {
			return blocked(plan, "the literal search term is empty")
		}
		plan.Objective = fmt.Sprintf("Find the literal text %q in committed source", term)
		plan.Tasks = []Task{
			{Key: "inventory", Objective: "Load the commit-pinned repository inventory", Method: MethodInventoryLookup, Inputs: map[string]string{"path_prefix": prefix}, RequiredEvidence: "inventory commit and file/blob records", CompletionCriteria: "a usable committed-tree inventory is available"},
			{Key: "search", Objective: fmt.Sprintf("Search committed files for literal text %q", term), Method: MethodCommittedSearch, DependsOn: []string{"inventory"}, Inputs: map[string]string{"term": term, "path_prefix": prefix}, RequiredEvidence: "path, blob hash, and exact matching line", CompletionCriteria: "record every match up to the documented bound, including an empty result"},
			{Key: "excerpts", Objective: "Retrieve exact committed source excerpts around each match", Method: MethodSourceExcerpt, DependsOn: []string{"search"}, Inputs: map[string]string{"source_task": "search"}, RequiredEvidence: "path, line range, blob hash, and verbatim excerpt", CompletionCriteria: "record a bounded excerpt for every captured search match"},
		}
		return plan
	}

	if match := dependencyRequest.FindStringSubmatch(original); match != nil {
		relation := match[1]
		if relation == "" {
			relation = match[2]
		}
		identity := strings.TrimSpace(match[3])
		plan.Objective = fmt.Sprintf("Find %s for component %q", strings.ToLower(relation), identity)
		plan.Tasks = []Task{
			{Key: "component", Objective: fmt.Sprintf("Resolve component %q from stored analysis", identity), Method: MethodComponentLookup, Inputs: map[string]string{"identity": identity}, RequiredEvidence: "component declaration path and line", CompletionCriteria: "resolve the exact component identity from a commit-pinned analysis"},
			{Key: "relationships", Objective: plan.Objective, Method: MethodDependencyLookup, DependsOn: []string{"component"}, Inputs: map[string]string{"identity": identity, "relation": strings.ToLower(relation)}, RequiredEvidence: "relationship declarations with path and line", CompletionCriteria: "return the direct relationships, including an empty result"},
		}
		return plan
	}

	return blocked(plan, "request needs semantic interpretation; supported forms are: list files, search for a quoted literal, or show dependencies/dependents of a quoted component")
}

func blocked(plan Plan, reason string) Plan {
	plan.Clarification = reason
	plan.Tasks = []Task{{
		Key: "clarify", Objective: "Clarify and semantically decompose the request",
		Method: MethodSemanticPlanning, RequiredEvidence: "an unambiguous supported outcome and constraints",
		CompletionCriteria: "produce a bounded plan without assuming repository facts",
		RequiresModel:      true, BlockedReason: reason,
	}}
	return plan
}

// SemanticBootstrap creates the bounded first planning step used for ordinary
// natural-language requests. It remains in the same durable work plan as the
// tasks it later proposes.
func SemanticBootstrap(request string, modelAvailable bool) Plan {
	plan := Plan{Original: strings.TrimSpace(request), Objective: strings.TrimSpace(request), ReadOnly: true}
	reason := ""
	if !modelAvailable {
		reason = "semantic planning requires a configured and qualified planning model"
		plan.Clarification = reason
	}
	plan.Tasks = []Task{{
		Key: "semantic-plan", Objective: "Decompose the request into bounded repository work",
		Method: MethodSemanticPlanning, Inputs: map[string]string{"mode": "initial"},
		RequiredEvidence:   "a schema-valid proposal using only supported operations",
		CompletionCriteria: "persist a validated bounded task graph or actionable diagnostics",
		RequiresModel:      true, BlockedReason: reason,
	}}
	return plan
}
