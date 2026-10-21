package topiceval

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/planner"
)

// Report is the durable artifact of one scored candidate.
type Report struct {
	Candidate  string        `json:"candidate"`
	Model      string        `json:"model,omitempty"`
	Aggregate  Aggregate     `json:"aggregate"`
	Results    []CaseResult  `json:"results"`
	Elapsed    time.Duration `json:"elapsed"`
	ReportPath string        `json:"-"`
}

// ScoreDeterministic scores the deterministic extractor over the whole corpus.
func ScoreDeterministic() Report {
	results := make([]CaseResult, 0, len(Cases))
	start := time.Now()
	for _, testCase := range Cases {
		begin := time.Now()
		topics := planner.ExtractInvestigationTopics(testCase.Request)
		result := Score(testCase, topics)
		result.LatencyMS = float64(time.Since(begin).Microseconds()) / 1000
		results = append(results, result)
	}
	return Report{Candidate: "deterministic", Aggregate: AggregateResults(results), Results: results, Elapsed: time.Since(start)}
}

func TestDeterministicBaselineReport(t *testing.T) {
	report := ScoreDeterministic()
	if report.Aggregate.Cases != len(Cases) {
		t.Fatalf("scored %d cases; corpus has %d", report.Aggregate.Cases, len(Cases))
	}
	fmt.Print(FormatReport("deterministic", report.Aggregate, report.Results))
	if path := os.Getenv("TOPICEVAL_REPORT"); path != "" {
		writeReport(t, path, report)
	}
}

func TestAggregateDoesNotCountInvalidResultAsPerfect(t *testing.T) {
	aggregate := AggregateResults([]CaseResult{{
		SchemaValid:      false,
		Recall:           1,
		PhraseRecall:     1,
		IdentifierRecall: 1,
	}})
	if aggregate.PerfectCases != 0 {
		t.Fatalf("perfect cases = %d; invalid output cannot be perfect", aggregate.PerfectCases)
	}
}

func writeReport(t *testing.T, path string, report Report) {
	t.Helper()
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}
}
