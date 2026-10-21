package topiceval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/config"
	"github.com/Hisha/Forgehand/internal/planner"
	"github.com/Hisha/Forgehand/internal/provider"
)

// ModelReport is the scored record of one model candidate.
type ModelReport struct {
	Candidate string        `json:"candidate"`
	Model     string        `json:"model,omitempty"`
	Aggregate Aggregate     `json:"aggregate"`
	Results   []CaseResult  `json:"results"`
	Elapsed   time.Duration `json:"elapsed"`
}

// ModelPass is one run of the corpus against one model.
type ModelPass struct {
	// Results holds the raw model answer for every case.
	Results []CaseResult `json:"results"`
	// Responses holds the raw model response text for human review.
	Responses map[string]string `json:"responses,omitempty"`
	// Fallbacks counts the cases where the production path fell back to the
	// deterministic extractor.
	Fallbacks int `json:"fallbacks"`
	// Elapsed is the wall time of the whole pass.
	Elapsed time.Duration `json:"elapsed"`
}

// EvaluationArtifact is the durable record of one evaluation run.
type EvaluationArtifact struct {
	GeneratedAt     time.Time         `json:"generated_at"`
	BaseURL         string            `json:"base_url,omitempty"`
	Model           string            `json:"model,omitempty"`
	PromptVariant   string            `json:"prompt_variant,omitempty"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	Deterministic   ModelReport       `json:"deterministic"`
	ModelRaw        *ModelReport      `json:"model_raw,omitempty"`
	ModelProduction *ModelReport      `json:"model_production,omitempty"`
	RepeatStability float64           `json:"repeat_stability,omitempty"`
	RepeatAgreement float64           `json:"repeat_agreement,omitempty"`
	RepeatRuns      int               `json:"repeat_runs,omitempty"`
	Responses       map[string]string `json:"responses,omitempty"`
	PassTerms       [][]string        `json:"pass_terms,omitempty"`
}

// TestModelExtractionEvaluation scores a live model against the corpus. It is
// skipped unless TOPICEVAL_MODEL names a model and FORGEHAND_PROVIDER_BASE_URL
// points at an OpenAI-compatible endpoint.
//
// The evaluation is model independent: no model judges another model. The
// corpus carries the human-reviewed expectations and the scorer compares the
// extracted terms against them.
func TestModelExtractionEvaluation(t *testing.T) {
	model := strings.TrimSpace(os.Getenv("TOPICEVAL_MODEL"))
	if model == "" {
		t.Skip("TOPICEVAL_MODEL is not set; skipping live model evaluation")
	}
	cfg, err := config.ProviderForModel(model)
	if err != nil {
		t.Fatalf("resolve provider: %v", err)
	}
	if !cfg.Configured() {
		t.Fatalf("%s is not set; the live evaluation needs an OpenAI-compatible endpoint", config.ProviderBaseURLEnvironment)
	}
	limits := TopicExtractionLimits{}
	if configured := strings.TrimSpace(os.Getenv("TOPICEVAL_MAX_TOKENS")); configured != "" {
		parsed, parseErr := strconv.Atoi(configured)
		if parseErr != nil || parsed < 1 {
			t.Fatalf("TOPICEVAL_MAX_TOKENS must be a positive integer")
		}
		limits.MaxTokens = parsed
	}
	client := provider.NewClient(cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.Timeout, cfg.MaxTokens)

	variant := strings.TrimSpace(os.Getenv("TOPICEVAL_PROMPT"))
	if variant == "" {
		variant = PromptProduction
	}
	prompt := topicExtractionPrompt(variant)

	repeat := 1
	if configured := strings.TrimSpace(os.Getenv("TOPICEVAL_REPEAT")); configured != "" {
		parsed, parseErr := strconv.Atoi(configured)
		if parseErr != nil || parsed < 1 {
			t.Fatalf("TOPICEVAL_REPEAT must be a positive integer")
		}
		repeat = parsed
	}

	passes := make([]ModelPass, 0, repeat)
	for round := 0; round < repeat; round++ {
		passes = append(passes, runModelPass(t, client, cfg.Model, limits, prompt))
	}

	raw := buildModelReport("model-raw", cfg.Model, passes[0])
	production := buildProductionReport(cfg.Model, passes[0])

	stability := 1.0
	agreement := 1.0
	if len(passes) > 1 {
		stability = repeatStability(passes)
		agreement = repeatAgreement(passes)
	}

	artifact := EvaluationArtifact{
		GeneratedAt:     time.Now().UTC(),
		BaseURL:         cfg.DisplayEndpoint(),
		Model:           cfg.Model,
		PromptVariant:   variant,
		MaxTokens:       limits.withDefaults().MaxTokens,
		Deterministic:   toModelReport(ScoreDeterministic()),
		ModelRaw:        &raw,
		ModelProduction: &production,
		RepeatStability: stability,
		RepeatAgreement: agreement,
		RepeatRuns:      len(passes),
		Responses:       passes[0].Responses,
		PassTerms:       passTerms(passes),
	}

	fmt.Print(FormatReport("model-raw", raw.Aggregate, raw.Results))
	fmt.Print(FormatReport("model-production", production.Aggregate, production.Results))
	fmt.Printf("== repeatability ==\n runs=%d stability=%.3f agreement=%.3f\n", len(passes), stability, agreement)

	if path := strings.TrimSpace(os.Getenv("TOPICEVAL_REPORT")); path != "" {
		writeArtifact(t, path, artifact)
	}
}

// runModelPass runs the whole corpus against one model and records the raw
// answer for every case.
func runModelPass(t *testing.T, client provider.Provider, model string, limits TopicExtractionLimits, prompt string) ModelPass {
	t.Helper()
	pass := ModelPass{Responses: map[string]string{}}
	start := time.Now()
	for _, testCase := range Cases {
		begin := time.Now()
		result := CaseResult{Name: testCase.Name, SchemaValid: true}
		response, err := client.Complete(context.Background(), provider.ChatRequest{
			System:    prompt,
			User:      testCase.Request,
			Model:     model,
			MaxTokens: limits.MaxTokens,
		})
		result.LatencyMS = float64(time.Since(begin).Microseconds()) / 1000
		if err != nil {
			result.SchemaValid = false
			result.Error = err.Error()
			pass.Results = append(pass.Results, result)
			continue
		}
		pass.Responses[testCase.Name] = response.Content
		topics, parseErr := ParseTopicExtraction(response.Content, limits)
		if parseErr != nil {
			result.SchemaValid = false
			result.Error = parseErr.Error()
			pass.Results = append(pass.Results, result)
			continue
		}
		for _, topic := range topics {
			result.Terms = append(result.Terms, topic.Term)
		}
		result.TopicCount = len(topics)
		pass.Results = append(pass.Results, result)
	}
	pass.Elapsed = time.Since(start)
	return pass
}

// buildModelReport scores the raw model answer of one pass.
func buildModelReport(name, model string, pass ModelPass) ModelReport {
	results := make([]CaseResult, 0, len(pass.Results))
	for index, testCase := range Cases {
		raw := pass.Results[index]
		scored := Score(testCase, nil)
		scored.SchemaValid = raw.SchemaValid
		scored.Error = raw.Error
		scored.LatencyMS = raw.LatencyMS
		if len(raw.Terms) > 0 {
			topics := make([]planner.InvestigationTopic, 0, len(raw.Terms))
			for _, term := range raw.Terms {
				topics = append(topics, planner.InvestigationTopic{Term: term})
			}
			scored = Score(testCase, topics)
			scored.SchemaValid = raw.SchemaValid
			scored.Error = raw.Error
			scored.LatencyMS = raw.LatencyMS
		}
		results = append(results, scored)
	}
	return ModelReport{Candidate: name, Model: model, Aggregate: AggregateResults(results), Results: results, Elapsed: pass.Elapsed}
}

// buildProductionReport scores the production path: the grounded model answer
// when the model answered usably, and the deterministic extractor otherwise.
func buildProductionReport(model string, pass ModelPass) ModelReport {
	results := make([]CaseResult, 0, len(pass.Results))
	fallbacks := 0
	for index, testCase := range Cases {
		raw := pass.Results[index]
		topics, err := parseGroundedTopics(testCase.Request, raw, pass.Responses[testCase.Name])
		if err != nil {
			topics = planner.ExtractInvestigationTopics(testCase.Request)
			fallbacks++
		}
		scored := Score(testCase, topics)
		scored.LatencyMS = raw.LatencyMS
		if err != nil {
			scored.Error = "deterministic fallback: " + err.Error()
		}
		results = append(results, scored)
	}
	report := ModelReport{Candidate: "model-production", Model: model, Aggregate: AggregateResults(results), Results: results, Elapsed: pass.Elapsed}
	return report
}

// parseGroundedTopics returns the grounded model answer for one case.
func parseGroundedTopics(request string, raw CaseResult, response string) ([]planner.InvestigationTopic, error) {
	if !raw.SchemaValid {
		if raw.Error == "" {
			return nil, fmt.Errorf("model output was not schema valid")
		}
		return nil, fmt.Errorf("%s", raw.Error)
	}
	topics, err := ParseTopicExtraction(response, TopicExtractionLimits{})
	if err != nil {
		return nil, err
	}
	if len(topics) == 0 {
		return topics, nil
	}
	grounded := GroundInvestigationTopics(request, topics)
	if len(grounded) == 0 {
		return nil, fmt.Errorf("no topic was grounded in the request")
	}
	return grounded, nil
}

// repeatStability measures how much two runs of the same model agree. It is
// the fraction of term positions that carry the same term in every run.
func repeatStability(passes []ModelPass) float64 {
	if len(passes) < 2 {
		return 1
	}
	total, stable := 0, 0
	for index := range Cases {
		terms := make([][]string, 0, len(passes))
		for _, pass := range passes {
			terms = append(terms, pass.Results[index].Terms)
		}
		widest := 0
		for _, set := range terms {
			if len(set) > widest {
				widest = len(set)
			}
		}
		for position := 0; position < widest; position++ {
			total++
			if position >= len(terms[0]) {
				continue
			}
			first := terms[0][position]
			same := true
			for _, set := range terms[1:] {
				if position >= len(set) || set[position] != first {
					same = false
					break
				}
			}
			if same {
				stable++
			}
		}
	}
	if total == 0 {
		return 1
	}
	return float64(stable) / float64(total)
}

func toModelReport(report Report) ModelReport {
	return ModelReport{Candidate: report.Candidate, Aggregate: report.Aggregate, Results: report.Results, Elapsed: report.Elapsed}
}

// repeatAgreement measures how much two runs of the same model agree on the
// set of extracted terms, independent of their order. It is the mean Jaccard
// similarity of the term sets across the corpus, so a model that returns the
// same concepts in a different order is not penalized.
func repeatAgreement(passes []ModelPass) float64 {
	if len(passes) < 2 {
		return 1
	}
	total := 0.0
	for index := range Cases {
		sets := make([]map[string]bool, 0, len(passes))
		union := map[string]bool{}
		for _, pass := range passes {
			set := map[string]bool{}
			for _, term := range pass.Results[index].Terms {
				set[strings.ToLower(strings.TrimSpace(term))] = true
			}
			sets = append(sets, set)
			for term := range set {
				union[term] = true
			}
		}
		if len(union) == 0 {
			total++
			continue
		}
		intersection := 0
		for term := range sets[0] {
			shared := true
			for _, set := range sets[1:] {
				if !set[term] {
					shared = false
					break
				}
			}
			if shared {
				intersection++
			}
		}
		total += float64(intersection) / float64(len(union))
	}
	return total / float64(len(Cases))
}

// passTerms records the terms every run produced so the repeatability numbers
// can be audited from the artifact alone.
func passTerms(passes []ModelPass) [][]string {
	recorded := make([][]string, 0, len(passes))
	for _, pass := range passes {
		terms := make([]string, 0, len(pass.Results))
		for _, result := range pass.Results {
			terms = append(terms, result.Name+"="+strings.Join(result.Terms, "|"))
		}
		recorded = append(recorded, terms)
	}
	return recorded
}

func writeArtifact(t *testing.T, path string, artifact EvaluationArtifact) {
	t.Helper()
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
}
