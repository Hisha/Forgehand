package topiceval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/planner"
	"github.com/Hisha/Forgehand/internal/provider"
)

// stubProvider answers with a fixed payload and records the request it saw.
type stubProvider struct {
	content   string
	err       error
	requests  []provider.ChatRequest
	cancelled bool
}

func (p *stubProvider) Complete(ctx context.Context, request provider.ChatRequest) (provider.ChatResponse, error) {
	p.requests = append(p.requests, request)
	if p.err != nil {
		return provider.ChatResponse{}, p.err
	}
	if err := ctx.Err(); err != nil {
		return provider.ChatResponse{}, err
	}
	return provider.ChatResponse{Content: p.content, Model: request.Model}, nil
}

func TestParseTopicExtractionAcceptsStrictPayload(t *testing.T) {
	payload := `{"topics":[{"term":"session persistence","words":["session","persistence"],"kind":"phrase"},{"term":"FORGEHAND_PROVIDER_TIMEOUT","words":["forgehand","provider","timeout"],"kind":"identifier"}]}`
	topics, err := ParseTopicExtraction(payload, TopicExtractionLimits{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(topics) != 2 {
		t.Fatalf("topics = %#v", topics)
	}
	if topics[0].Term != "session persistence" || topics[0].Kind != planner.InvestigationTopicPhrase {
		t.Fatalf("topic = %#v", topics[0])
	}
	if topics[1].Kind != planner.InvestigationTopicIdentifier {
		t.Fatalf("topic = %#v", topics[1])
	}
}

func TestParseTopicExtractionAcceptsFencedPayload(t *testing.T) {
	payload := "```json\n{\"topics\":[{\"term\":\"recovery\"}]}\n```"
	topics, err := ParseTopicExtraction(payload, TopicExtractionLimits{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(topics) != 1 || topics[0].Term != "recovery" {
		t.Fatalf("topics = %#v", topics)
	}
	if topics[0].Kind != planner.InvestigationTopicWord {
		t.Fatalf("topic = %#v; kind was not inferred", topics[0])
	}
}

func TestParseTopicExtractionAcceptsEmptyListForRequestWithoutSubject(t *testing.T) {
	topics, err := ParseTopicExtraction(`{"topics":[]}`, TopicExtractionLimits{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(topics) != 0 {
		t.Fatalf("topics = %#v; expected an empty list", topics)
	}
}

func TestParseTopicExtractionRejectsInvalidAnswers(t *testing.T) {
	cases := map[string]string{
		"unknown field":   `{"topics":[{"term":"recovery"}],"extra":true}`,
		"two json values": `{"topics":[{"term":"recovery"}]} {"topics":[{"term":"restart"}]}`,
		"missing topics":  `{}`,
		"null topics":     `{"topics":null}`,
		"malformed json":  `{"topics":[{"term":"recovery"}`,
		"unknown kind":    `{"topics":[{"term":"recovery","kind":"function"}]}`,
		"empty term":      `{"topics":[{"term":"   "}]}`,
		"not an object":   `["recovery"]`,
	}
	for name, payload := range cases {
		if _, err := ParseTopicExtraction(payload, TopicExtractionLimits{}); err == nil {
			t.Fatalf("%s: expected the answer to be rejected", name)
		}
	}
}

func TestParseTopicExtractionRejectsOversizedAnswer(t *testing.T) {
	var builder strings.Builder
	builder.WriteString(`{"topics":[`)
	for index := 0; index < 9; index++ {
		if index > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(`{"term":"topic"}`)
	}
	builder.WriteString(`]}`)
	if _, err := ParseTopicExtraction(builder.String(), TopicExtractionLimits{}); err == nil {
		t.Fatal("expected an oversized topic list to be rejected")
	}
	// The topic budget is clamped to the deterministic limit, so a candidate
	// can never answer with more topics than the deterministic extractor.
	if _, err := ParseTopicExtraction(builder.String(), TopicExtractionLimits{MaxTopics: 16}); err == nil {
		t.Fatal("expected the topic budget to be clamped to the deterministic limit")
	}
}

func TestGroundInvestigationTopicsDropsUngroundedTopics(t *testing.T) {
	request := "Investigate how session persistence protects the daemon"
	topics := []planner.InvestigationTopic{
		{Term: "session persistence", Words: []string{"session", "persistence"}, Kind: planner.InvestigationTopicPhrase},
		{Term: "provider capability allow-list", Words: []string{"provider", "capability"}, Kind: planner.InvestigationTopicPhrase},
	}
	grounded := GroundInvestigationTopics(request, topics)
	if len(grounded) != 1 || grounded[0].Term != "session persistence" {
		t.Fatalf("grounded = %#v", grounded)
	}
}

func TestGroundInvestigationTopicsRequiresVerbatimIdentifier(t *testing.T) {
	request := "Investigate WorldSession::IsHeadless"
	topics := []planner.InvestigationTopic{
		{Term: "worldsession::isheadless", Words: []string{"worldsession", "isheadless"}, Kind: planner.InvestigationTopicIdentifier},
	}
	if grounded := GroundInvestigationTopics(request, topics); len(grounded) != 0 {
		t.Fatalf("grounded = %#v; a reformatted identifier must not be grounded", grounded)
	}
}

func TestGroundInvestigationTopicsRequiresVerbatimTerm(t *testing.T) {
	request := "Investigate how CancellationToken is observed"
	topics := []planner.InvestigationTopic{
		{Term: "cancellationtoken", Words: []string{"cancellation", "token"}, Kind: planner.InvestigationTopicPhrase},
	}
	if grounded := GroundInvestigationTopics(request, topics); len(grounded) != 0 {
		t.Fatalf("grounded = %#v; a reformatted term must not be grounded", grounded)
	}
}

func TestExtractInvestigationTopicsWithModelFallsBackOnInvalidAnswer(t *testing.T) {
	client := &stubProvider{content: "not json at all"}
	extractor := &ModelTopicExtractor{Provider: client, Model: "candidate"}
	outcome := ExtractInvestigationTopicsWithModel(context.Background(), "Investigate daemon shutdown", extractor)
	if outcome.Source != "deterministic" {
		t.Fatalf("outcome = %#v; expected the deterministic fallback", outcome)
	}
	if outcome.Reason == "" {
		t.Fatal("expected the fallback to record a reason")
	}
	expected := planner.ExtractInvestigationTopics("Investigate daemon shutdown")
	if len(outcome.Topics) != len(expected) {
		t.Fatalf("topics = %#v; expected the deterministic topics %#v", outcome.Topics, expected)
	}
}

func TestExtractInvestigationTopicsWithModelFallsBackWithoutProvider(t *testing.T) {
	outcome := ExtractInvestigationTopicsWithModel(context.Background(), "Investigate daemon shutdown", nil)
	if outcome.Source != "deterministic" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(outcome.Topics) == 0 {
		t.Fatal("expected the deterministic extractor to produce topics")
	}
}

func TestExtractInvestigationTopicsWithModelFallsBackOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &stubProvider{content: `{"topics":[{"term":"daemon"}]}`}
	extractor := &ModelTopicExtractor{Provider: client, Model: "candidate"}
	outcome := ExtractInvestigationTopicsWithModel(ctx, "Investigate daemon shutdown", extractor)
	if outcome.Source != "deterministic" {
		t.Fatalf("outcome = %#v; expected the deterministic fallback", outcome)
	}
	if !strings.Contains(outcome.Reason, "cancelled") {
		t.Fatalf("reason = %q; expected the cancellation to be recorded", outcome.Reason)
	}
}

func TestExtractInvestigationTopicsWithModelUsesGroundedModelAnswer(t *testing.T) {
	client := &stubProvider{content: `{"topics":[{"term":"daemon shutdown","words":["daemon","shutdown"],"kind":"phrase"}]}`}
	extractor := &ModelTopicExtractor{Provider: client, Model: "candidate"}
	outcome := ExtractInvestigationTopicsWithModel(context.Background(), "Investigate daemon shutdown", extractor)
	if outcome.Source != "model" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(outcome.Topics) != 1 || outcome.Topics[0].Term != "daemon shutdown" {
		t.Fatalf("topics = %#v", outcome.Topics)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %#v; expected exactly one provider call", client.requests)
	}
	if client.requests[0].System != TopicExtractionSystemPrompt() {
		t.Fatal("expected the candidate to use the bounded capability prompt")
	}
	if client.requests[0].MaxTokens != DefaultMaxTopicExtractionTokens {
		t.Fatalf("max tokens = %d; expected the documented bound", client.requests[0].MaxTokens)
	}
}

func TestExtractInvestigationTopicsWithModelAcceptsEmptyNoSubjectAnswer(t *testing.T) {
	client := &stubProvider{content: `{"topics":[]}`}
	extractor := &ModelTopicExtractor{Provider: client, Model: "candidate"}
	outcome := ExtractInvestigationTopicsWithModel(context.Background(), "Could you please help me understand how this works?", extractor)
	if outcome.Source != "model" || outcome.Reason != "" || len(outcome.Topics) != 0 {
		t.Fatalf("outcome = %#v; expected a valid empty model answer", outcome)
	}
}

func TestExtractInvestigationTopicsWithModelRejectsOversizedRequest(t *testing.T) {
	client := &stubProvider{content: `{"topics":[{"term":"daemon"}]}`}
	extractor := &ModelTopicExtractor{Provider: client, Model: "candidate", Limits: TopicExtractionLimits{MaxRequestChars: 10}}
	outcome := ExtractInvestigationTopicsWithModel(context.Background(), "Investigate daemon shutdown", extractor)
	if outcome.Source != "deterministic" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(client.requests) != 0 {
		t.Fatalf("requests = %#v; expected no provider call", client.requests)
	}
}

func TestExtractInvestigationTopicsWithModelIsDeterministicOnFallback(t *testing.T) {
	client := &stubProvider{err: errors.New("provider unavailable")}
	extractor := &ModelTopicExtractor{Provider: client, Model: "candidate"}
	request := "Investigate how daemon shutdown and recovery of interrupted sessions works"
	first := ExtractInvestigationTopicsWithModel(context.Background(), request, extractor)
	for index := 0; index < 10; index++ {
		next := ExtractInvestigationTopicsWithModel(context.Background(), request, extractor)
		if next.Source != first.Source || len(next.Topics) != len(first.Topics) {
			t.Fatalf("outcome = %#v; first outcome = %#v", next, first)
		}
		for position := range next.Topics {
			if next.Topics[position].Term != first.Topics[position].Term {
				t.Fatalf("topic = %#v; first topic = %#v", next.Topics[position], first.Topics[position])
			}
		}
	}
}
