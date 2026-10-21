package planner

import (
	"reflect"
	"strings"
	"testing"
)

func investigationTopicTermsOf(topics []InvestigationTopic) []string {
	terms := make([]string, 0, len(topics))
	for _, topic := range topics {
		terms = append(terms, topic.Term)
	}
	return terms
}

func TestExtractInvestigationTopicsKeepsLanguageIdentifiers(t *testing.T) {
	request := "Investigate WorldSession::IsHeadless, session_status, FORGEHAND_PROVIDER_MODEL, mod-native-hunts, and config.json"
	topics := ExtractInvestigationTopics(request)
	terms := investigationTopicTermsOf(topics)
	for _, want := range []string{"WorldSession::IsHeadless", "session_status", "FORGEHAND_PROVIDER_MODEL", "mod-native-hunts", "config.json"} {
		if !containsString(terms, want) {
			t.Fatalf("terms = %#v; missing %q", terms, want)
		}
	}
	for _, topic := range topics {
		switch topic.Term {
		case "WorldSession::IsHeadless", "session_status", "FORGEHAND_PROVIDER_MODEL", "mod-native-hunts", "config.json":
			if topic.Kind != InvestigationTopicIdentifier {
				t.Fatalf("topic %q kind = %q", topic.Term, topic.Kind)
			}
			if len(topic.Words) == 0 {
				t.Fatalf("topic %q has no words", topic.Term)
			}
		}
	}
}

func TestExtractInvestigationTopicsDropsSentencePunctuation(t *testing.T) {
	topics := ExtractInvestigationTopics("Investigate daemon shutdown. Recovery matters. Numbers matter.")
	terms := investigationTopicTermsOf(topics)
	for _, term := range terms {
		if strings.HasSuffix(term, ".") {
			t.Fatalf("term %q retains trailing punctuation", term)
		}
	}
	for _, unwanted := range []string{"shutdown.", "numbers.", "files."} {
		if containsString(terms, unwanted) {
			t.Fatalf("terms = %#v; retained %q", terms, unwanted)
		}
	}
	words := map[string]bool{}
	for _, topic := range topics {
		for _, word := range topic.Words {
			words[word] = true
		}
	}
	if !words["shutdown"] {
		t.Fatalf("terms = %#v; missing the shutdown concept", terms)
	}
	if !words["recovery"] {
		t.Fatalf("terms = %#v; missing the recovery concept", terms)
	}
	if words["numbers"] || words["matters"] {
		t.Fatalf("terms = %#v; retained generic wording", terms)
	}
}

func TestExtractInvestigationTopicsRemovesGenericRequestWording(t *testing.T) {
	topics := ExtractInvestigationTopics("Gather supporting source-code evidence with file paths and line numbers.")
	terms := investigationTopicTermsOf(topics)
	for _, unwanted := range []string{"source-code", "file", "files", "path", "paths", "line", "lines", "number", "numbers", "evidence", "source", "code"} {
		if containsString(terms, unwanted) {
			t.Fatalf("terms = %#v; retained generic term %q", terms, unwanted)
		}
	}
	if len(terms) != 0 {
		t.Fatalf("terms = %#v; expected no repository concept", terms)
	}
}

func TestExtractInvestigationTopicsKeepsDomainVocabulary(t *testing.T) {
	topics := ExtractInvestigationTopics("Investigate how daemon shutdown and recovery of interrupted sessions prevents duplicate execution")
	terms := investigationTopicTermsOf(topics)
	words := map[string]bool{}
	for _, topic := range topics {
		for _, word := range topic.Words {
			words[word] = true
		}
	}
	for _, want := range []string{"daemon", "shutdown", "recovery", "interrupted", "duplicate", "execution"} {
		if !words[want] {
			t.Fatalf("topic words = %#v; missing %q", words, want)
		}
	}
	if !words["session"] && !words["sessions"] {
		t.Fatalf("topic words = %#v; missing the session concept", words)
	}
	if !containsString(terms, "daemon shutdown") {
		t.Fatalf("terms = %#v; missing adjacent domain phrase", terms)
	}
}

func TestExtractInvestigationTopicsFormsPhrasesOnlyFromAdjacentDomainWords(t *testing.T) {
	topics := ExtractInvestigationTopics("Investigate session persistence across the repository")
	terms := investigationTopicTermsOf(topics)
	if !containsString(terms, "session persistence") {
		t.Fatalf("terms = %#v", terms)
	}
	for _, term := range terms {
		if strings.Contains(term, " across ") || strings.Contains(term, "the ") {
			t.Fatalf("term %q spans a generic word", term)
		}
	}
}

func TestExtractInvestigationTopicsDoesNotRepeatCoveredConcepts(t *testing.T) {
	topics := ExtractInvestigationTopics("Investigate session persistence and session recovery and session handling")
	seen := map[string]bool{}
	for _, topic := range topics {
		if topic.Kind == InvestigationTopicIdentifier {
			continue
		}
		for _, word := range topic.Words {
			if seen[word] && word != "session" {
				t.Fatalf("topic %q repeats covered word %q", topic.Term, word)
			}
		}
		for _, word := range topic.Words {
			seen[word] = true
		}
	}
}

func TestExtractInvestigationTopicsIsDeterministic(t *testing.T) {
	request := "Investigate daemon shutdown, session persistence, and duplicate execution after a restart"
	first := ExtractInvestigationTopics(request)
	for i := 0; i < 25; i++ {
		if !reflect.DeepEqual(first, ExtractInvestigationTopics(request)) {
			t.Fatalf("extraction is not deterministic: %#v", ExtractInvestigationTopics(request))
		}
	}
}

func TestExtractInvestigationTopicsRespectsTermLimit(t *testing.T) {
	request := "Investigate alpha bravo charlie delta echo foxtrot golf hotel india juliet"
	topics := ExtractInvestigationTopics(request)
	if len(topics) > MaxInvestigationTerms {
		t.Fatalf("topic count = %d; limit = %d", len(topics), MaxInvestigationTerms)
	}
	if len(topics) == 0 {
		t.Fatal("expected grounded topics")
	}
}

func TestExtractInvestigationTopicsIgnoresConstraints(t *testing.T) {
	decision := AssessInvestigation("Investigate daemon shutdown", []string{"read-only", "do not modify files", "do not execute commands"})
	if !decision.Eligible {
		t.Fatalf("decision = %#v", decision)
	}
	for _, term := range decision.Topics {
		if term == "read-only" || term == "files" || term == "commands" {
			t.Fatalf("topics = %#v; constraint wording leaked into topics", decision.Topics)
		}
	}
}

func TestExtractInvestigationTopicsKeepsNamedSubjectWhenItIsTheOnlyConcept(t *testing.T) {
	topics := ExtractInvestigationTopics("Investigate PostgreSQL")
	if len(topics) != 1 || topics[0].Term != "PostgreSQL" {
		t.Fatalf("topics = %#v", topics)
	}
}

func TestExtractInvestigationTopicsIgnoresNonLatinAndPunctuationNoise(t *testing.T) {
	topics := ExtractInvestigationTopics("Investigate 日本語 shutdown --- ??? recovery")
	terms := investigationTopicTermsOf(topics)
	if !containsString(terms, "shutdown") || !containsString(terms, "recovery") {
		t.Fatalf("terms = %#v", terms)
	}
	for _, term := range terms {
		if strings.Contains(term, "?") || strings.Contains(term, "-") {
			t.Fatalf("term %q retains punctuation noise", term)
		}
	}
}

func TestExtractInvestigationTopicsPrefersQuotedSubject(t *testing.T) {
	topics := ExtractInvestigationTopics("Investigate \"session persistence\" behavior")
	if len(topics) == 0 || topics[0].Term != "session persistence" {
		t.Fatalf("topics = %#v", topics)
	}
}

func TestExtractInvestigationTopicsRemovesNegativeRestrictionFragments(t *testing.T) {
	topics := ExtractInvestigationTopics("Investigate how the daemon prevents duplicate execution without modifying any files")
	terms := investigationTopicTermsOf(topics)
	for _, unwanted := range []string{"ing", "modifying", "files", "the config", "output"} {
		if containsString(terms, unwanted) {
			t.Fatalf("terms = %#v; retained restriction fragment %q", terms, unwanted)
		}
	}
	for _, topic := range topics {
		for _, word := range topic.Words {
			if word == "ing" || word == "modifying" {
				t.Fatalf("topic %q retained restriction fragment %q", topic.Term, word)
			}
		}
	}
}

func TestExtractInvestigationTopicsReturnsNothingForRequestWithoutSubject(t *testing.T) {
	topics := ExtractInvestigationTopics("Could you please help me understand how this works? Thanks!")
	if len(topics) != 0 {
		t.Fatalf("topics = %#v; expected no concept for a request without a subject", topics)
	}
}

func TestExtractInvestigationTopicsExcludesInstructionSentences(t *testing.T) {
	topics := ExtractInvestigationTopics("Investigate the scheduler. Also remember to update the README, run the linter, and tell the team.")
	terms := investigationTopicTermsOf(topics)
	for _, unwanted := range []string{"README", "linter", "team", "remember", "update", "tell"} {
		if containsString(terms, unwanted) {
			t.Fatalf("terms = %#v; retained instruction wording %q", terms, unwanted)
		}
	}
	if !containsString(terms, "scheduler") {
		t.Fatalf("terms = %#v; missing the investigation subject", terms)
	}
}

func TestExtractInvestigationTopicsKeepsFollowUpStatementSubject(t *testing.T) {
	topics := ExtractInvestigationTopics("Investigate daemon shutdown. Recovery matters.")
	words := map[string]bool{}
	for _, topic := range topics {
		for _, word := range topic.Words {
			words[word] = true
		}
	}
	if !words["shutdown"] || !words["recovery"] {
		t.Fatalf("topic words = %#v; expected both the inquiry and the follow-up subject", words)
	}
}

func TestExtractInvestigationTopicsHandlesOriginalDaemonRecoveryRequest(t *testing.T) {
	// This is the request the daemon received before the extractor was
	// corrected. The stored topics were "source-code", "sessions.",
	// "recovery.", "restart.", "numbers.", and "files.".
	request := "Investigate how Forgehand handles daemon shutdown and recovery of interrupted sessions. " +
		"Identify the components responsible for session persistence, execution scheduling, and recovery. " +
		"Determine how the system prevents duplicate execution after a restart. " +
		"Gather supporting source-code evidence with file paths and line numbers. Do not modify any files."
	topics := ExtractInvestigationTopics(request)
	terms := investigationTopicTermsOf(topics)
	for _, want := range []string{"daemon shutdown", "interrupted sessions", "session persistence", "execution scheduling", "duplicate execution", "recovery", "restart"} {
		if !containsString(terms, want) {
			t.Fatalf("terms = %#v; missing %q", terms, want)
		}
	}
	for _, unwanted := range []string{"source-code", "sessions.", "numbers.", "files.", "evidence"} {
		if containsString(terms, unwanted) {
			t.Fatalf("terms = %#v; retained %q", terms, unwanted)
		}
	}
}

func TestExtractInvestigationTopicsIgnoresDeliverableEvidenceSentence(t *testing.T) {
	topics := ExtractInvestigationTopics("Investigate daemon shutdown. Gather supporting source-code evidence with file paths and line numbers.")
	terms := investigationTopicTermsOf(topics)
	for _, unwanted := range []string{"source-code", "file", "paths", "line", "numbers", "evidence"} {
		if containsString(terms, unwanted) {
			t.Fatalf("terms = %#v; retained deliverable wording %q", terms, unwanted)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsStringFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
