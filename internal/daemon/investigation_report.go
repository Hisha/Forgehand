package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Hisha/Forgehand/internal/repocontext"
	"github.com/Hisha/Forgehand/internal/state"
)

const (
	maxInvestigationReportItems = 32
	maxInvestigationStatement   = 4000
)

type investigationCitation struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

type investigationStatement struct {
	Statement string                  `json:"statement"`
	Citations []investigationCitation `json:"citations"`
}

type investigationReport struct {
	ObservedFacts       []investigationStatement `json:"observed_facts"`
	ReasonedConclusions []investigationStatement `json:"reasoned_conclusions"`
	Uncertainties       []string                 `json:"uncertainties"`
	SuggestedNextSteps  []string                 `json:"suggested_next_steps"`
}

func investigationResponseSchema() map[string]any {
	citation := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"path": map[string]any{"type": "string"}, "line": map[string]any{"type": "integer", "minimum": 1}},
		"required":   []string{"path", "line"},
	}
	statement := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"statement": map[string]any{"type": "string", "minLength": 1, "maxLength": maxInvestigationStatement},
			"citations": map[string]any{"type": "array", "minItems": 1, "maxItems": maxInvestigationReportItems, "items": citation},
		},
		"required": []string{"statement", "citations"},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"observed_facts":       map[string]any{"type": "array", "minItems": 1, "maxItems": maxInvestigationReportItems, "items": statement},
			"reasoned_conclusions": map[string]any{"type": "array", "minItems": 1, "maxItems": maxInvestigationReportItems, "items": statement},
			"uncertainties":        map[string]any{"type": "array", "minItems": 1, "maxItems": maxInvestigationReportItems, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": maxInvestigationStatement}},
			"suggested_next_steps": map[string]any{"type": "array", "minItems": 1, "maxItems": maxInvestigationReportItems, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": maxInvestigationStatement}},
		},
		"required": []string{"observed_facts", "reasoned_conclusions", "uncertainties", "suggested_next_steps"},
	}
}

func validateInvestigationReport(raw, commit string, files []repocontext.FileRef, evidence []state.InvestigationEvidenceFile) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```json") && strings.HasSuffix(raw, "```") {
		raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "```json"), "```"))
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var report investigationReport
	if err := decoder.Decode(&report); err != nil {
		return "", fmt.Errorf("decode required JSON object: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return "", err
	}
	if len(report.ObservedFacts) == 0 || len(report.ReasonedConclusions) == 0 || len(report.Uncertainties) == 0 || len(report.SuggestedNextSteps) == 0 {
		return "", fmt.Errorf("all four report sections must be non-empty")
	}
	report.Uncertainties = uniqueInvestigationText(report.Uncertainties)
	report.SuggestedNextSteps = uniqueInvestigationText(report.SuggestedNextSteps)
	lineCounts := make(map[string]int, len(files))
	for _, file := range files {
		lineCounts[file.Path] = strings.Count(file.Content, "\n") + 1
	}
	allowedExcerptLines := map[string][]state.InvestigationSourceLocation{}
	for _, file := range evidence {
		allowedExcerptLines[file.Path] = append(allowedExcerptLines[file.Path], file.Locations...)
	}
	validated, unsupported, err := validateInvestigationStatements("observed_facts", &report.ObservedFacts, lineCounts, allowedExcerptLines, &report.Uncertainties)
	if err != nil {
		return "", err
	}
	conclusionValidated, conclusionUnsupported, err := validateInvestigationStatements("reasoned_conclusions", &report.ReasonedConclusions, lineCounts, allowedExcerptLines, &report.Uncertainties)
	if err != nil {
		return "", err
	}
	validated += conclusionValidated
	unsupported += conclusionUnsupported
	if err := validateInvestigationTextList("uncertainties", report.Uncertainties); err != nil {
		return "", err
	}
	if err := validateInvestigationTextList("suggested_next_steps", report.SuggestedNextSteps); err != nil {
		return "", err
	}

	var rendered strings.Builder
	fmt.Fprintf(&rendered, "Investigation report\nRepository revision: %s\n\nObserved repository facts\n", commit)
	renderInvestigationStatements(&rendered, report.ObservedFacts)
	rendered.WriteString("\nReasoned conclusions\n")
	renderInvestigationStatements(&rendered, report.ReasonedConclusions)
	rendered.WriteString("\nUncertainty and missing evidence\n")
	renderInvestigationList(&rendered, report.Uncertainties)
	rendered.WriteString("\nSuggested next investigation steps\n")
	renderInvestigationList(&rendered, report.SuggestedNextSteps)
	fmt.Fprintf(&rendered, "\nEvidence validation\n- %d citation(s) validated against the pinned evidence package.\n", validated)
	if unsupported > 0 {
		fmt.Fprintf(&rendered, "- %d unsupported citation(s) were removed; claims left without support were moved to uncertainty.\n", unsupported)
	} else {
		rendered.WriteString("- No unsupported citations were present.\n")
	}
	return rendered.String(), nil
}

func uniqueInvestigationText(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, strings.TrimSpace(value))
	}
	return result
}

func validateInvestigationStatements(section string, statements *[]investigationStatement, lineCounts map[string]int, excerpts map[string][]state.InvestigationSourceLocation, uncertainties *[]string) (int, int, error) {
	if len(*statements) > maxInvestigationReportItems {
		return 0, 0, fmt.Errorf("%s exceeds %d items", section, maxInvestigationReportItems)
	}
	kept := make([]investigationStatement, 0, len(*statements))
	validated, unsupported := 0, 0
	for index, statement := range *statements {
		if strings.TrimSpace(statement.Statement) == "" || len(statement.Statement) > maxInvestigationStatement {
			return 0, 0, fmt.Errorf("%s[%d] has an empty or oversized statement", section, index)
		}
		if len(statement.Citations) == 0 || len(statement.Citations) > maxInvestigationReportItems {
			return 0, 0, fmt.Errorf("%s[%d] must have 1-%d citations", section, index, maxInvestigationReportItems)
		}
		valid := make([]investigationCitation, 0, len(statement.Citations))
		for _, citation := range statement.Citations {
			count, wholeFileSupplied := lineCounts[citation.Path]
			supplied := wholeFileSupplied && citation.Line > 0 && citation.Line <= count
			if !supplied {
				for _, location := range excerpts[citation.Path] {
					end := max(location.Line, location.EndLine)
					if citation.Line >= location.Line && citation.Line <= end {
						supplied = true
						break
					}
				}
			}
			if supplied {
				valid = append(valid, citation)
				validated++
			} else {
				unsupported++
				*uncertainties = append(*uncertainties, fmt.Sprintf("Unsupported citation removed from model claim (%s:%d): %s", citation.Path, citation.Line, strings.TrimSpace(statement.Statement)))
			}
		}
		if len(valid) == 0 {
			*uncertainties = append(*uncertainties, "UNSUPPORTED model claim: "+strings.TrimSpace(statement.Statement))
			continue
		}
		statement.Citations = valid
		kept = append(kept, statement)
	}
	if len(kept) == 0 {
		return 0, unsupported, fmt.Errorf("%s contains no claim with a supported citation", section)
	}
	*statements = kept
	return validated, unsupported, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing response data: %w", err)
	}
	return fmt.Errorf("response contains more than one JSON value")
}

func validateInvestigationTextList(name string, values []string) error {
	if len(values) > maxInvestigationReportItems {
		return fmt.Errorf("%s exceeds %d items", name, maxInvestigationReportItems)
	}
	for index, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > maxInvestigationStatement {
			return fmt.Errorf("%s[%d] is empty or oversized", name, index)
		}
	}
	return nil
}

func renderInvestigationStatements(rendered *strings.Builder, statements []investigationStatement) {
	for _, statement := range statements {
		fmt.Fprintf(rendered, "- %s", strings.TrimSpace(statement.Statement))
		for _, citation := range statement.Citations {
			fmt.Fprintf(rendered, " [%s:%d]", citation.Path, citation.Line)
		}
		rendered.WriteByte('\n')
	}
}

func renderInvestigationList(rendered *strings.Builder, values []string) {
	for _, value := range values {
		fmt.Fprintf(rendered, "- %s\n", strings.TrimSpace(value))
	}
}
