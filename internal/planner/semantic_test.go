package planner

import (
	"context"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/provider"
)

type semanticStubProvider struct{ content string }

func (s semanticStubProvider) Complete(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{Content: s.content, Model: "qualified-planner"}, nil
}

func semanticRequest() Request {
	return Request{
		Objective:         "Find where the setting is consumed",
		Constraints:       []string{"do not modify files"},
		Project:           Project{ID: 1, Name: "fixture"},
		Operations:        SupportedOperations(),
		ModelCapabilities: []ModelCapability{{Name: CapabilityPlanning, Model: "qualified-planner", Qualified: true}},
	}
}

func TestModelDecomposerAcceptsStrictSupportedProposal(t *testing.T) {
	content := `{"objective":"Locate the setting consumer","constraints":["do not modify files"],"read_only":true,"tasks":[{"key":"find-setting","objective":"Find the literal setting declaration","method":"repository.committed_text.search","inputs":{"term":"SETTING"},"required_evidence":"exact committed match","completion_criteria":"all bounded matches are recorded"},{"key":"replan","objective":"Interpret the discovered declarations","method":"planning.incremental_revision","depends_on":["find-setting"],"required_evidence":"search evidence","completion_criteria":"a validated follow-up graph is persisted","requires_model":true}]}`
	d := ModelDecomposer{Provider: semanticStubProvider{content: content}, Model: "qualified-planner"}
	proposal, err := d.Propose(context.Background(), semanticRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Tasks) != 2 || proposal.Tasks[1].Method != MethodIncrementalPlanning {
		t.Fatalf("proposal = %#v", proposal)
	}
}

func TestValidateProposalReturnsActionableDiagnostics(t *testing.T) {
	req := semanticRequest()
	proposal := Proposal{Objective: "bad", ReadOnly: false, Tasks: []Task{
		{Key: "one", Objective: "one", Method: "shell.exec", DependsOn: []string{"two"}, RequiredEvidence: "e", CompletionCriteria: "c"},
		{Key: "two", Objective: "two", Method: MethodInventoryLookup, DependsOn: []string{"one"}, RequiredEvidence: "e", CompletionCriteria: "c", FactRefs: []string{"invented"}},
	}}
	err := ValidateProposal(req, proposal)
	if err == nil {
		t.Fatal("invalid proposal accepted")
	}
	for _, want := range []string{"read_only", "shell.exec", "unknown repository fact", "cycle", "does not preserve user constraint"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("diagnostics %q do not contain %q", err, want)
		}
	}
}

func TestModelDecomposerRejectsUnknownJSONField(t *testing.T) {
	content := `{"objective":"x","read_only":true,"tasks":[],"command":"rm -rf ."}`
	d := ModelDecomposer{Provider: semanticStubProvider{content: content}}
	if _, err := d.Propose(context.Background(), semanticRequest()); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateProposalRejectsInvestigationTopicWithInventedTerm(t *testing.T) {
	req := semanticRequest()
	proposal := Proposal{Objective: "Investigate the setting", ReadOnly: true, Constraints: req.Constraints, Tasks: []Task{{
		Key: "investigate-setting", Objective: "Gather setting evidence", Method: MethodInvestigationBootstrap,
		Inputs:           map[string]string{"topics": `[{"term":"invented setting","words":["setting"],"kind":"phrase"}]`},
		RequiredEvidence: "committed evidence", CompletionCriteria: "bounded evidence is recorded",
	}}}
	if err := ValidateProposal(req, proposal); err == nil || !strings.Contains(err.Error(), "not grounded verbatim") {
		t.Fatalf("ValidateProposal error = %v", err)
	}
}

func TestValidateProposalRejectsRepeatedInvestigationAfterAnyCompletedKey(t *testing.T) {
	req := semanticRequest()
	req.Completed = []CompletedTask{{Key: "custom-evidence-key", Method: MethodInvestigationBootstrap, Result: "setting evidence"}}
	proposal := Proposal{Objective: "Investigate the setting", ReadOnly: true, Constraints: req.Constraints, Tasks: []Task{{
		Key: "investigate-setting", Objective: "Gather setting evidence", Method: MethodInvestigationBootstrap,
		Inputs:           map[string]string{"topics": `[{"term":"setting","words":["setting"],"kind":"word"}]`},
		RequiredEvidence: "committed evidence", CompletionCriteria: "bounded evidence is recorded",
	}}}
	if err := ValidateProposal(req, proposal); err == nil || !strings.Contains(err.Error(), "already completed") {
		t.Fatalf("ValidateProposal error = %v", err)
	}
}
