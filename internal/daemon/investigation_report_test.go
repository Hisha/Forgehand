package daemon

import (
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/repocontext"
	"github.com/Hisha/Forgehand/internal/state"
)

func TestValidateInvestigationReportRendersStructuredSections(t *testing.T) {
	files := []repocontext.FileRef{{Path: "internal/state/queue.go", Content: "package state\nfunc claim() {}\n"}}
	rendered, err := validateInvestigationReport(investigationJSON("The queue claim is implemented here.", "internal/state/queue.go", 2), "abc123", files, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Repository revision: abc123", "Observed repository facts", "Reasoned conclusions", "Uncertainty and missing evidence", "Suggested next investigation steps", "2 citation(s) validated", "[internal/state/queue.go:2]"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered report missing %q:\n%s", want, rendered)
		}
	}
}

func TestValidateInvestigationReportRejectsSectionWithOnlyUnsupportedClaim(t *testing.T) {
	_, err := validateInvestigationReport(investigationJSON("Invented.", "internal/state/missing.go", 99), "abc123", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "no claim with a supported citation") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateInvestigationReportRejectsUnstructuredOutput(t *testing.T) {
	_, err := validateInvestigationReport("The queue probably handles it.", "abc123", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "decode required JSON") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateInvestigationReportAcceptsFencedJSONAndExcerptLine(t *testing.T) {
	raw := "```json\n" + investigationJSON("Claimed atomically.", "internal/state/queue.go", 748) + "\n```"
	evidence := []state.InvestigationEvidenceFile{{Path: "internal/state/queue.go", Locations: []state.InvestigationSourceLocation{{Line: 748, EndLine: 752}}}}
	if _, err := validateInvestigationReport(raw, "abc123", nil, evidence); err != nil {
		t.Fatal(err)
	}
}
