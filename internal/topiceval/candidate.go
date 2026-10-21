package topiceval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Hisha/Forgehand/internal/planner"
	"github.com/Hisha/Forgehand/internal/provider"
)

// This file holds the model-assisted candidate that the evaluation compares
// against the deterministic extractor. It lives with the evaluation because the
// evaluation rejected it: the deterministic extractor scored higher on every
// measured metric, so the candidate was never wired into the daemon, the
// provider capability list, or the work-plan method table.
//
// The candidate is deliberately shaped like production code. It has its own
// bounded capability contract, a strict response schema, grounding against the
// request text, and a deterministic fallback, so the measured numbers describe
// what integration would actually have produced.

const (
	// DefaultMaxTopicExtractionRequestChars bounds the request text handed to
	// a topic extraction model.
	DefaultMaxTopicExtractionRequestChars = 4000
	// DefaultMaxTopicExtractionResponseChars bounds the model response that is
	// parsed.
	DefaultMaxTopicExtractionResponseChars = 4096
	// DefaultMaxTopicExtractionTokens bounds the generated token count.
	DefaultMaxTopicExtractionTokens = 512
	// DefaultMaxTopicExtractionTermChars bounds one extracted term.
	DefaultMaxTopicExtractionTermChars = 120
	// DefaultMaxTopicExtractionWords bounds the words one term may declare.
	DefaultMaxTopicExtractionWords = 12
)

// TopicExtractionLimits bounds one model-assisted topic extraction. The zero
// value of every field selects the documented default.
type TopicExtractionLimits struct {
	MaxRequestChars  int
	MaxResponseChars int
	MaxTokens        int
	MaxTopics        int
	MaxTermChars     int
	MaxWordsPerTopic int
}

func (l TopicExtractionLimits) withDefaults() TopicExtractionLimits {
	if l.MaxRequestChars <= 0 {
		l.MaxRequestChars = DefaultMaxTopicExtractionRequestChars
	}
	if l.MaxResponseChars <= 0 {
		l.MaxResponseChars = DefaultMaxTopicExtractionResponseChars
	}
	if l.MaxTokens <= 0 {
		l.MaxTokens = DefaultMaxTopicExtractionTokens
	}
	if l.MaxTopics <= 0 || l.MaxTopics > planner.MaxInvestigationTerms {
		l.MaxTopics = planner.MaxInvestigationTerms
	}
	if l.MaxTermChars <= 0 {
		l.MaxTermChars = DefaultMaxTopicExtractionTermChars
	}
	if l.MaxWordsPerTopic <= 0 {
		l.MaxWordsPerTopic = DefaultMaxTopicExtractionWords
	}
	return l
}

// TopicExtractionFailureKind classifies why a model-assisted extraction failed.
type TopicExtractionFailureKind string

const (
	// TopicExtractionFailureInfrastructure marks a failure that has nothing to
	// do with the model answer, such as a missing provider or an oversized
	// request.
	TopicExtractionFailureInfrastructure TopicExtractionFailureKind = "infrastructure"
	// TopicExtractionFailureModelOutput marks a model answer that cannot be
	// used, such as invalid JSON, an out-of-bounds topic list, or a topic that
	// is not grounded in the request.
	TopicExtractionFailureModelOutput TopicExtractionFailureKind = "model_output"
)

// TopicExtractionError reports a failed model-assisted extraction.
type TopicExtractionError struct {
	Kind TopicExtractionFailureKind
	Err  error
}

func (e *TopicExtractionError) Error() string { return e.Err.Error() }
func (e *TopicExtractionError) Unwrap() error { return e.Err }

// ModelTopicExtractor proposes the bounded investigation concept list with a
// model. It is the model-assisted counterpart of the deterministic extractor
// and shares its contract: the result is a grounded, bounded list of terms
// that only ever name things the request itself states.
type ModelTopicExtractor struct {
	Provider provider.Provider
	Model    string
	Limits   TopicExtractionLimits
}

// topicExtractionPayload is the strict schema a topic extraction model must
// return.
type topicExtractionPayload struct {
	Topics *[]planner.InvestigationTopic `json:"topics"`
}

// Extract asks the model for the investigation concept list and returns the
// grounded subset of its answer. Topics whose words are not present in the
// request are dropped, because a topic the request never states cannot be
// searched for safely.
func (e *ModelTopicExtractor) Extract(ctx context.Context, request string) ([]planner.InvestigationTopic, error) {
	text := strings.TrimSpace(request)
	topics, err := e.ExtractRaw(ctx, text)
	if err != nil {
		return nil, err
	}
	if len(topics) == 0 {
		return topics, nil
	}
	grounded := GroundInvestigationTopics(text, topics)
	if len(grounded) == 0 {
		return nil, &TopicExtractionError{Kind: TopicExtractionFailureModelOutput, Err: fmt.Errorf("topic extraction returned no topic grounded in the request")}
	}
	return grounded, nil
}

// ExtractRaw asks the model for the investigation concept list and returns the
// answer exactly as parsed, before any grounding rule is applied. It exists so
// a candidate model can be measured on what it actually proposed.
func (e *ModelTopicExtractor) ExtractRaw(ctx context.Context, request string) ([]planner.InvestigationTopic, error) {
	limits := e.Limits.withDefaults()
	text := strings.TrimSpace(request)
	if text == "" {
		return nil, &TopicExtractionError{Kind: TopicExtractionFailureInfrastructure, Err: fmt.Errorf("topic extraction request is empty")}
	}
	if len(text) > limits.MaxRequestChars {
		return nil, &TopicExtractionError{Kind: TopicExtractionFailureInfrastructure, Err: fmt.Errorf("topic extraction request is %d characters; limit is %d", len(text), limits.MaxRequestChars)}
	}
	if e.Provider == nil {
		return nil, &TopicExtractionError{Kind: TopicExtractionFailureInfrastructure, Err: fmt.Errorf("topic extraction provider is not configured")}
	}
	response, err := e.Provider.Complete(ctx, provider.ChatRequest{
		System:    TopicExtractionSystemPrompt(),
		User:      text,
		Model:     e.Model,
		MaxTokens: limits.MaxTokens,
	})
	if err != nil {
		return nil, &TopicExtractionError{Kind: TopicExtractionFailureInfrastructure, Err: fmt.Errorf("topic extraction provider: %w", err)}
	}
	if len(response.Content) > limits.MaxResponseChars {
		return nil, &TopicExtractionError{Kind: TopicExtractionFailureModelOutput, Err: fmt.Errorf("topic extraction output is %d characters; limit is %d", len(response.Content), limits.MaxResponseChars)}
	}
	topics, err := ParseTopicExtraction(response.Content, limits)
	if err != nil {
		return nil, &TopicExtractionError{Kind: TopicExtractionFailureModelOutput, Err: err}
	}
	return topics, nil
}

// ParseTopicExtraction decodes and validates a topic extraction answer. The
// schema is strict: unknown fields, a missing topics field, more than one JSON
// value, an oversized list, or an unknown topic kind are all rejected. An
// empty list is valid because the prompt explicitly requires it when the
// request names no concrete subject.
func ParseTopicExtraction(content string, limits TopicExtractionLimits) ([]planner.InvestigationTopic, error) {
	limits = limits.withDefaults()
	body := stripTopicExtractionFences(content)
	var payload topicExtractionPayload
	decoder := json.NewDecoder(bytes.NewBufferString(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("topic extraction returned invalid JSON schema: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("topic extraction returned more than one JSON value")
	}
	if payload.Topics == nil {
		return nil, fmt.Errorf("topic extraction omitted the topics field")
	}
	if len(*payload.Topics) > limits.MaxTopics {
		return nil, fmt.Errorf("topic extraction returned %d topics; limit is %d", len(*payload.Topics), limits.MaxTopics)
	}
	topics := make([]planner.InvestigationTopic, 0, len(*payload.Topics))
	for index, topic := range *payload.Topics {
		normalized, err := normalizeExtractedTopic(topic, limits)
		if err != nil {
			return nil, fmt.Errorf("topic extraction topic %d: %w", index, err)
		}
		topics = append(topics, normalized)
	}
	return topics, nil
}

// normalizeExtractedTopic validates one topic and derives its words and kind
// when the model omitted them.
func normalizeExtractedTopic(topic planner.InvestigationTopic, limits TopicExtractionLimits) (planner.InvestigationTopic, error) {
	term := strings.TrimSpace(topic.Term)
	if term == "" {
		return planner.InvestigationTopic{}, fmt.Errorf("term must not be empty")
	}
	if len(term) > limits.MaxTermChars {
		return planner.InvestigationTopic{}, fmt.Errorf("term is %d characters; limit is %d", len(term), limits.MaxTermChars)
	}
	words := make([]string, 0, len(topic.Words))
	for _, word := range topic.Words {
		word = strings.ToLower(strings.TrimSpace(word))
		if word == "" {
			continue
		}
		words = append(words, word)
	}
	if len(words) == 0 {
		words = planner.SplitIdentifierWords(term)
	}
	if len(words) == 0 {
		return planner.InvestigationTopic{}, fmt.Errorf("term %q declares no words", term)
	}
	if len(words) > limits.MaxWordsPerTopic {
		return planner.InvestigationTopic{}, fmt.Errorf("term %q declares %d words; limit is %d", term, len(words), limits.MaxWordsPerTopic)
	}
	kind := strings.TrimSpace(topic.Kind)
	switch kind {
	case planner.InvestigationTopicIdentifier, planner.InvestigationTopicPhrase, planner.InvestigationTopicWord:
	case "":
		kind = inferInvestigationTopicKind(term, words)
	default:
		return planner.InvestigationTopic{}, fmt.Errorf("term %q declares unknown kind %q", term, topic.Kind)
	}
	return planner.InvestigationTopic{Term: term, Words: words, Kind: kind}, nil
}

// inferInvestigationTopicKind derives the topic kind from the term shape when
// a model omitted it.
func inferInvestigationTopicKind(term string, words []string) string {
	if planner.IsCodeLikeIdentifier(term) {
		return planner.InvestigationTopicIdentifier
	}
	if len(words) > 1 {
		return planner.InvestigationTopicPhrase
	}
	return planner.InvestigationTopicWord
}

// GroundInvestigationTopics keeps the topics whose words all appear in the
// request. An identifier must additionally appear verbatim, because language
// identifiers are case-sensitive and a reformatted identifier cannot be
// searched for.
func GroundInvestigationTopics(request string, topics []planner.InvestigationTopic) []planner.InvestigationTopic {
	lower := strings.ToLower(request)
	grounded := make([]planner.InvestigationTopic, 0, len(topics))
	for _, topic := range topics {
		if !topicGrounded(request, lower, topic) {
			continue
		}
		grounded = append(grounded, topic)
	}
	return grounded
}

func topicGrounded(request, lowerRequest string, topic planner.InvestigationTopic) bool {
	if strings.TrimSpace(topic.Term) == "" || len(topic.Words) == 0 {
		return false
	}
	// Word grounding alone is insufficient because model-declared metadata
	// could otherwise make an invented or reordered term appear grounded.
	// Ordinary phrases may differ only in case; code-like identifiers may not.
	if !strings.Contains(lowerRequest, strings.ToLower(topic.Term)) {
		return false
	}
	if topic.Kind == planner.InvestigationTopicIdentifier && !strings.Contains(request, topic.Term) {
		return false
	}
	if !strings.Contains(request, topic.Term) && requestHasCodeLikeFold(request, topic.Term) {
		return false
	}
	for _, word := range topic.Words {
		if !strings.Contains(lowerRequest, strings.ToLower(word)) {
			return false
		}
	}
	return true
}

func requestHasCodeLikeFold(request, term string) bool {
	for _, field := range strings.Fields(request) {
		candidate := strings.Trim(field, "\"'`()[]{}<>,;:!?")
		if strings.EqualFold(candidate, term) && planner.IsCodeLikeIdentifier(candidate) {
			return true
		}
	}
	return false
}

// stripTopicExtractionFences removes the reasoning block and markdown fence a
// chat model may wrap its answer in, so the strict JSON schema can still be
// applied to the answer itself.
func stripTopicExtractionFences(content string) string {
	body := content
	if start := strings.Index(body, "<think>"); start >= 0 {
		if end := strings.Index(body[start:], "</think>"); end >= 0 {
			body = body[:start] + body[start+end+len("</think>"):]
		}
	}
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "{") {
		if start := strings.Index(body, "{"); start >= 0 {
			body = body[start:]
		}
	}
	if end := strings.LastIndex(body, "}"); end >= 0 {
		body = body[:end+1]
	}
	return strings.TrimSpace(body)
}

// TopicExtractionOutcome records which extraction produced the concept list.
type TopicExtractionOutcome struct {
	// Topics is the grounded concept list.
	Topics []planner.InvestigationTopic
	// Source names the producer, either "model" or "deterministic".
	Source string
	// Reason explains a deterministic fallback. It is empty when the model
	// produced the topics.
	Reason string
}

// ExtractInvestigationTopicsWithModel prefers a model-assisted extraction and
// falls back to the deterministic extractor whenever the model is unavailable,
// times out, or returns output that cannot be grounded. The fallback keeps
// investigation available without a qualified model and keeps every result
// deterministic in the degraded case.
func ExtractInvestigationTopicsWithModel(ctx context.Context, request string, extractor *ModelTopicExtractor) TopicExtractionOutcome {
	if extractor == nil || extractor.Provider == nil {
		return TopicExtractionOutcome{Topics: planner.ExtractInvestigationTopics(request), Source: "deterministic", Reason: "no topic extraction model is configured"}
	}
	topics, err := extractor.Extract(ctx, request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return TopicExtractionOutcome{Topics: planner.ExtractInvestigationTopics(request), Source: "deterministic", Reason: "topic extraction was cancelled: " + ctxErr.Error()}
		}
		return TopicExtractionOutcome{Topics: planner.ExtractInvestigationTopics(request), Source: "deterministic", Reason: "topic extraction fell back to the deterministic extractor: " + err.Error()}
	}
	return TopicExtractionOutcome{Topics: topics, Source: "model"}
}

// TopicExtractionSystemPrompt states the bounded contract of the capability.
// The model may only name things the request states, so every proposed term
// can be checked against the request text.
func TopicExtractionSystemPrompt() string {
	return `You are Forgehand's investigation topic extraction capability. Return exactly one JSON object and no markdown, prose, or code fences.
Output schema:
{"topics":[{"term":"string","words":["string"],"kind":"identifier|phrase|word"}]}
Rules:
- Return between 1 and 8 topics.
- Every term must be copied verbatim from the request. Never paraphrase, translate, expand, or invent a term that the request does not contain.
- "words" lists the lowercase words the term covers. "session persistence" has words ["session","persistence"]. "WorldSession::IsHeadless" has words ["world","session","is","headless"]. "FORGEHAND_PROVIDER_TIMEOUT" has words ["forgehand","provider","timeout"].
- "kind" is "identifier" for a language-level identifier such as "WorldSession::IsHeadless", "state.WorkPlanTask", or "FORGEHAND_PROVIDER_TIMEOUT"; "phrase" for two adjacent domain words such as "session persistence"; "word" for a single domain word such as "recovery".
- Describe only the subject of the investigation. Do not include request wording, deliverable instructions, prohibitions, or politeness such as "README", "linter", "team", "file paths", "line numbers", "do not modify files", or "Thanks".
- Ignore negated and restricted clauses; they describe the authorized scope, not the subject.
- Prefer the multiword phrases and identifiers that name the subject over single generic words.
- If the request names no concrete subject, return {"topics":[]}.`
}
