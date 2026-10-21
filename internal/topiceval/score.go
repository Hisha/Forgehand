package topiceval

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/Hisha/Forgehand/internal/planner"
)

// CaseResult is the score of one extraction against one corpus case.
type CaseResult struct {
	Name string `json:"name"`
	// SchemaValid records whether the candidate produced a schema-valid
	// topic list. The deterministic extractor is always schema valid.
	SchemaValid bool `json:"schema_valid"`
	// TopicCount is the number of topics the candidate returned.
	TopicCount int `json:"topic_count"`
	// Recall is the fraction of required concepts the candidate surfaced.
	// A concept is covered when every one of its words is present in the
	// extracted topic vocabulary, compared with light morphological folding.
	Recall float64 `json:"recall"`
	// PhraseRecall is the fraction of required concepts that appear as a
	// single topic term. It measures multiword concept quality.
	PhraseRecall float64 `json:"phrase_recall"`
	// IdentifierRecall is the fraction of required identifiers preserved
	// verbatim as a topic term.
	IdentifierRecall float64 `json:"identifier_recall"`
	// HasIdentifiers records whether the case carries required identifiers.
	HasIdentifiers bool `json:"has_identifiers"`
	// ForbiddenHits counts forbidden concepts that became topics.
	ForbiddenHits int `json:"forbidden_hits"`
	// Ungrounded lists extracted topic words that do not appear in the
	// request. They are unsupported inventions.
	Ungrounded []string `json:"ungrounded,omitempty"`
	// Missing lists required concepts that were not extracted.
	Missing []string `json:"missing,omitempty"`
	// Forbidden lists forbidden concepts that were extracted.
	Forbidden []string `json:"forbidden,omitempty"`
	// MissingIdentifiers lists identifiers that were not preserved.
	MissingIdentifiers []string `json:"missing_identifiers,omitempty"`
	// LatencyMS is the wall time the candidate needed for this request.
	LatencyMS float64 `json:"latency_ms"`
	// Terms lists the extracted topic terms for human review.
	Terms []string `json:"terms,omitempty"`
	// Error records a candidate failure for this request.
	Error string `json:"error,omitempty"`
}

// Aggregate summarizes a scored run.
type Aggregate struct {
	Cases             int     `json:"cases"`
	MeanRecall        float64 `json:"mean_recall"`
	MeanPhraseRecall  float64 `json:"mean_phrase_recall"`
	MeanIdentifierRec float64 `json:"mean_identifier_recall"`
	TotalForbidden    int     `json:"total_forbidden_hits"`
	TotalUngrounded   int     `json:"total_ungrounded"`
	SchemaFailures    int     `json:"schema_failures"`
	ErrorCases        int     `json:"error_cases"`
	MeanLatencyMS     float64 `json:"mean_latency_ms"`
	TotalLatencyMS    float64 `json:"total_latency_ms"`
	PerfectCases      int     `json:"perfect_cases"`
}

// Score compares one extraction against one corpus case. The comparison uses
// only the request text and the extracted topics; no model is consulted.
func Score(c Case, topics []planner.InvestigationTopic) CaseResult {
	result := CaseResult{Name: c.Name, SchemaValid: true, TopicCount: len(topics)}
	extracted := topicVocabulary(topics)
	terms := make([]string, 0, len(topics))
	for _, topic := range topics {
		terms = append(terms, topic.Term)
	}
	result.Terms = terms

	for _, forbidden := range c.ForbiddenConcepts {
		words := conceptWords(forbidden)
		if len(words) == 0 {
			continue
		}
		if vocabularyHasAll(extracted, words) {
			result.ForbiddenHits++
			result.Forbidden = append(result.Forbidden, forbidden)
		}
	}

	requestWords := requestVocabulary(c.Request)
	for _, word := range extracted.words {
		if vocabularyHas(requestWords, word) {
			continue
		}
		result.Ungrounded = append(result.Ungrounded, word)
	}
	sort.Strings(result.Ungrounded)
	result.Ungrounded = uniqueStrings(result.Ungrounded)

	if c.NoSubject {
		// A request that names no concrete subject should not produce a
		// meaningful topic list. It scores as a pass only when the candidate
		// stays inside the allowed topic budget and invents nothing.
		limit := c.MaxTopics
		if limit <= 0 {
			limit = 1
		}
		if len(topics) <= limit && result.ForbiddenHits == 0 && len(result.Ungrounded) == 0 {
			result.Recall = 1
			result.PhraseRecall = 1
			result.IdentifierRecall = 1
		}
		return result
	}

	for _, concept := range c.RequiredConcepts {
		words := conceptWords(concept)
		if len(words) == 0 {
			continue
		}
		if !vocabularyHasAll(extracted, words) {
			result.Missing = append(result.Missing, concept)
			continue
		}
		result.Recall++
		if termEquals(words, terms) {
			result.PhraseRecall++
		}
	}
	if len(c.RequiredConcepts) > 0 {
		result.Recall /= float64(len(c.RequiredConcepts))
		result.PhraseRecall /= float64(len(c.RequiredConcepts))
	}

	result.HasIdentifiers = len(c.RequiredIdentifiers) > 0
	for _, identifier := range c.RequiredIdentifiers {
		if !containsTerm(terms, identifier) {
			result.MissingIdentifiers = append(result.MissingIdentifiers, identifier)
			continue
		}
		result.IdentifierRecall++
	}
	if result.HasIdentifiers {
		result.IdentifierRecall /= float64(len(c.RequiredIdentifiers))
	}
	return result
}

// AggregateResults summarizes scored cases.
func AggregateResults(results []CaseResult) Aggregate {
	aggregate := Aggregate{Cases: len(results)}
	if len(results) == 0 {
		return aggregate
	}
	identifierCases := 0
	for _, result := range results {
		aggregate.MeanRecall += result.Recall
		aggregate.MeanPhraseRecall += result.PhraseRecall
		if result.HasIdentifiers {
			aggregate.MeanIdentifierRec += result.IdentifierRecall
			identifierCases++
		}
		aggregate.TotalForbidden += result.ForbiddenHits
		aggregate.TotalUngrounded += len(result.Ungrounded)
		aggregate.TotalLatencyMS += result.LatencyMS
		if !result.SchemaValid {
			aggregate.SchemaFailures++
		}
		if result.Error != "" {
			aggregate.ErrorCases++
		}
		if result.SchemaValid && result.Recall == 1 && result.ForbiddenHits == 0 && len(result.Ungrounded) == 0 &&
			result.IdentifierRecall == 1 && result.PhraseRecall == 1 {
			aggregate.PerfectCases++
		}
	}
	aggregate.MeanRecall /= float64(len(results))
	aggregate.MeanPhraseRecall /= float64(len(results))
	if identifierCases > 0 {
		aggregate.MeanIdentifierRec /= float64(identifierCases)
	}
	aggregate.MeanLatencyMS = aggregate.TotalLatencyMS / float64(len(results))
	return aggregate
}

// vocabulary is the set of words a candidate extraction exposes.
type vocabulary struct {
	words []string
	forms map[string]bool
}

func newVocabulary(words []string) vocabulary {
	forms := map[string]bool{}
	for _, word := range words {
		for _, form := range wordForms(word) {
			forms[form] = true
		}
	}
	return vocabulary{words: words, forms: forms}
}

// vocabularyHas reports whether a word is present, allowing for light
// morphological variation such as "persistence" and "persisted".
func vocabularyHas(set vocabulary, word string) bool {
	for _, form := range wordForms(word) {
		if set.forms[form] {
			return true
		}
	}
	return false
}

// vocabularyHasAll reports whether every word is present.
func vocabularyHasAll(set vocabulary, words []string) bool {
	for _, word := range words {
		if !vocabularyHas(set, word) {
			return false
		}
	}
	return true
}

// topicVocabulary derives the comparable words of an extraction from the topic
// terms alone. The words a candidate declares are ignored so that two
// candidates are compared on the same footing: the term is the artifact under
// test, not the metadata around it.
func topicVocabulary(topics []planner.InvestigationTopic) vocabulary {
	words := make([]string, 0, len(topics)*2)
	for _, topic := range topics {
		topicWords := planner.SplitIdentifierWords(topic.Term)
		if len(topicWords) == 0 {
			topicWords = topic.Words
		}
		for _, word := range topicWords {
			words = append(words, strings.ToLower(word))
		}
	}
	return newVocabulary(uniqueStrings(words))
}

// requestVocabulary splits the request into comparable words. It does not use
// the planner's splitter because that splitter truncates long requests, which
// would hide grounding failures.
func requestVocabulary(request string) vocabulary {
	return newVocabulary(uniqueStrings(splitRequestWords(request)))
}

func splitRequestWords(value string) []string {
	replaced := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, value)
	fields := strings.Fields(replaced)
	words := make([]string, 0, len(fields))
	for _, field := range fields {
		words = append(words, splitCamel(field)...)
	}
	return words
}

// splitCamel splits a camel-case or Pascal-case word into its parts.
func splitCamel(field string) []string {
	runes := []rune(field)
	if len(runes) == 0 {
		return nil
	}
	var words []string
	current := strings.Builder{}
	for index, r := range runes {
		if index > 0 && unicode.IsUpper(r) {
			previous := runes[index-1]
			nextIsLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
			if !unicode.IsUpper(previous) || nextIsLower {
				if current.Len() > 0 {
					words = append(words, current.String())
					current.Reset()
				}
			}
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

func conceptWords(concept string) []string {
	words := splitRequestWords(concept)
	out := make([]string, 0, len(words))
	for _, word := range words {
		lower := strings.ToLower(word)
		if lower == "" {
			continue
		}
		out = append(out, lower)
	}
	return out
}

// termEquals reports whether one extracted term names the whole concept.
func termEquals(words []string, terms []string) bool {
	concept := strings.Join(words, " ")
	for _, term := range terms {
		if strings.Join(conceptWords(term), " ") == concept {
			return true
		}
	}
	return false
}

// containsTerm reports whether a term is present verbatim, compared
// case-sensitively because language-level identifiers are case-sensitive.
func containsTerm(terms []string, identifier string) bool {
	for _, term := range terms {
		if term == identifier {
			return true
		}
	}
	return false
}

// wordForms returns the comparable forms of one word. The forms fold the
// regular English inflections that appear in engineering vocabulary so that
// "persistence" and "persisted" are treated as one concept. The rule is
// intentionally small and deterministic.
func wordForms(word string) []string {
	base := strings.ToLower(strings.TrimSpace(word))
	if base == "" {
		return nil
	}
	forms := []string{base}
	add := func(value string) {
		if value == "" || value == base {
			return
		}
		forms = append(forms, value)
	}
	if strings.HasSuffix(base, "ies") && len(base) > 4 {
		add(base[:len(base)-3] + "y")
	}
	if strings.HasSuffix(base, "es") && len(base) > 4 && !strings.HasSuffix(base, "sses") {
		add(base[:len(base)-2])
	}
	if strings.HasSuffix(base, "s") && len(base) > 3 &&
		!strings.HasSuffix(base, "ss") && !strings.HasSuffix(base, "us") && !strings.HasSuffix(base, "is") {
		add(base[:len(base)-1])
	}
	if strings.HasSuffix(base, "ing") && len(base) > 5 {
		add(base[:len(base)-3])
	}
	if strings.HasSuffix(base, "ed") && len(base) > 4 {
		add(base[:len(base)-2])
	}
	if strings.HasSuffix(base, "e") && len(base) > 4 {
		add(base[:len(base)-1])
	}
	if strings.HasSuffix(base, "y") && len(base) > 4 {
		add(base[:len(base)-1])
	}
	if strings.HasSuffix(base, "ation") && len(base) > 6 {
		add(base[:len(base)-4])
	}
	if strings.HasSuffix(base, "ion") && len(base) > 5 {
		add(base[:len(base)-3])
	}
	if (strings.HasSuffix(base, "ance") || strings.HasSuffix(base, "ence")) && len(base) > 5 {
		add(base[:len(base)-4])
	}
	if strings.HasSuffix(base, "ness") && len(base) > 5 {
		add(base[:len(base)-4])
	}
	if strings.HasSuffix(base, "c") && len(base) > 4 {
		add(base[:len(base)-1])
	}
	return forms
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// FormatReport renders a human-reviewable summary of a scored run.
func FormatReport(name string, aggregate Aggregate, results []CaseResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "== %s ==\n", name)
	fmt.Fprintf(&b, "cases=%d mean_recall=%.3f mean_phrase_recall=%.3f mean_identifier_recall=%.3f forbidden_hits=%d ungrounded=%d schema_failures=%d error_cases=%d perfect_cases=%d mean_latency_ms=%.1f\n",
		aggregate.Cases, aggregate.MeanRecall, aggregate.MeanPhraseRecall, aggregate.MeanIdentifierRec,
		aggregate.TotalForbidden, aggregate.TotalUngrounded, aggregate.SchemaFailures, aggregate.ErrorCases,
		aggregate.PerfectCases, aggregate.MeanLatencyMS)
	for _, result := range results {
		fmt.Fprintf(&b, "- %-32s recall=%.2f phrase=%.2f id=%.2f forbidden=%d ungrounded=%d topics=%d %.0fms\n",
			result.Name, result.Recall, result.PhraseRecall, result.IdentifierRecall, result.ForbiddenHits,
			len(result.Ungrounded), result.TopicCount, result.LatencyMS)
		if len(result.Missing) > 0 {
			fmt.Fprintf(&b, "    missing: %s\n", strings.Join(result.Missing, ", "))
		}
		if len(result.MissingIdentifiers) > 0 {
			fmt.Fprintf(&b, "    missing identifiers: %s\n", strings.Join(result.MissingIdentifiers, ", "))
		}
		if len(result.Forbidden) > 0 {
			fmt.Fprintf(&b, "    forbidden: %s\n", strings.Join(result.Forbidden, ", "))
		}
		if len(result.Ungrounded) > 0 {
			fmt.Fprintf(&b, "    ungrounded: %s\n", strings.Join(result.Ungrounded, ", "))
		}
		if result.Error != "" {
			fmt.Fprintf(&b, "    error: %s\n", result.Error)
		}
		fmt.Fprintf(&b, "    terms: %s\n", strings.Join(result.Terms, " | "))
	}
	return b.String()
}
