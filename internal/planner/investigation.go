package planner

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	// MaxInvestigationTerms bounds how many grounded concepts one
	// deterministic investigation may carry.
	MaxInvestigationTerms = 8

	// maxInvestigationTopicWords bounds how many constituent words a single
	// topic may contribute to concept-coverage accounting.
	maxInvestigationTopicWords = 12
)

// Topic kinds describe how a topic is matched against committed source.
//
//	identifier - one language-level identifier such as "WorldSession::IsHeadless",
//	             "session_status", "FORGEHAND_PROVIDER_MODEL", or "mod-native-hunts".
//	             Matched literally only.
//	phrase     - two adjacent domain words such as "session persistence". The
//	             phrase is matched literally and its words are also matched
//	             individually.
//	word       - one domain word such as "recovery".
const (
	InvestigationTopicIdentifier = "identifier"
	InvestigationTopicPhrase     = "phrase"
	InvestigationTopicWord       = "word"
)

// InvestigationTopic is one deterministic, grounded investigation concept.
//
// Term is the literal text that is matched against committed source. Words are
// the constituent identifiers of the concept and are used for relaxed matching
// (phrases and words only) and for concept-coverage accounting. Kind records
// which matching rule applies.
type InvestigationTopic struct {
	Term  string   `json:"term"`
	Words []string `json:"words,omitempty"`
	Kind  string   `json:"kind,omitempty"`
}

// UnmarshalJSON accepts both the object form and a bare string so a stored
// topic payload written by an older build remains readable.
func (t *InvestigationTopic) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*t = InvestigationTopic{Term: text, Words: []string{strings.ToLower(text)}, Kind: InvestigationTopicWord}
		return nil
	}
	type topicAlias InvestigationTopic
	var decoded topicAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*t = InvestigationTopic(decoded)
	return nil
}

var (
	investigationIntent = regexp.MustCompile(`(?i)\b(?:investigat(?:e|ion)|diagnos(?:e|is)|trace|explain|determine|identify|locate|understand|analy[sz]e|why|how|where|what)\b`)

	// investigationScopeIntent marks the wording that makes a sentence carry
	// an inquiry. It is broader than investigationIntent, which governs
	// authorization, because a request may name the subject of an inquiry in
	// a follow-up sentence that repeats no request verb.
	investigationScopeIntent = regexp.MustCompile(`(?i)\b(?:investigat(?:e|ion)|diagnos(?:e|is)|trace|explain|determine|identify|locate|understand|analy[sz]e|why|how|where|what|check|checks|checking|verify|verifies|confirm|confirms|review|reviews|examine|examines|inspect|inspects|assess|compare|compares|validate|validates)\b`)
	unsafeIntent             = regexp.MustCompile(`(?i)\b(?:access|add|apply|browse|change|commit|create|delete|deploy|download|edit|execute|fetch|fix|implement|install|modify|push|remove|rename|rewrite|run|update|write)\b`)
	unsafeGerundIntent       = regexp.MustCompile(`(?i)\b(?:by|and)\s+(?:accessing\s+(?:external services?|the network)|deploying|downloading|executing\s+commands?|installing|modifying\s+files?|pushing|running\s+(?:commands?|tests?|builds?))\b`)
	negativeRestriction      = regexp.MustCompile(`(?i)\b(?:do not|don't|must not|never|without)\s+(?:automatically\s+)?(?:access(?:ing)?|brows(?:ing|e)|chang(?:ing|e)|commit(?:ting)?|deploy(?:ing)?|download(?:ing)?|edit(?:ing)?|execut(?:ing|e)|fetch(?:ing)?|install(?:ing)?|modif(?:ying|y)|push(?:ing)?|remov(?:ing|e)|run(?:ning)?|writ(?:ing|e))(?:\s+(?:files?|commands?|tests?|external services?|the network))?`)
	quotedTopic              = regexp.MustCompile("[\"'`]([^\"'`]{2,120})[\"'`]")

	// investigationToken matches one identifier-like span. Internal separators
	// are preserved so scoped (::), namespaced (.), hierarchical (/),
	// tag-like (#), and hyphenated identifiers survive extraction. Trailing
	// sentence punctuation is excluded because a separator must be followed by
	// an identifier character, so "sessions." yields "sessions" while
	// "config.json" is preserved intact.
	investigationToken = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:(?:::|[:./#-])[A-Za-z0-9_]+)*`)

	// investigationClauseBreakers separate independent clauses. Phrases are
	// never formed across a clause boundary.
	investigationClauseBreakers = ".,;:!?\n\r"

	// investigationSentenceBreakers separate sentences. Sentence boundaries
	// are tracked apart from clause boundaries because the subject of a
	// request is normally named by one whole sentence, which may span
	// several comma-separated clauses.
	investigationSentenceBreakers = ".!?\n\r"
)

// investigationGenericTerms holds closed-class words and request scaffolding
// that carry no repository-specific meaning. A candidate whose every
// constituent word appears here is discarded, which removes meta wording such
// as "source-code", "file paths", or "line numbers" while preserving real
// identifiers that merely contain a generic word.
var investigationGenericTerms = map[string]bool{
	// articles, determiners, and pronouns
	"a": true, "an": true, "the": true, "this": true, "that": true, "these": true,
	"those": true, "its": true, "their": true, "our": true, "your": true, "my": true,
	"mine": true, "it": true, "them": true, "they": true, "he": true, "she": true,
	"we": true, "you": true, "i": true, "us": true, "me": true, "his": true, "hers": true,
	"ours": true, "yours": true, "itself": true, "themselves": true, "each": true,
	"every": true, "either": true, "neither": true, "both": true, "all": true,
	"any": true, "some": true, "such": true, "no": true, "none": true, "other": true,
	"others": true, "another": true, "same": true, "own": true, "few": true,
	"many": true, "much": true, "more": true, "most": true, "less": true,
	"least": true, "several": true, "various": true,

	// prepositions, conjunctions, and subordinators
	"of": true, "in": true, "on": true, "at": true, "to": true, "for": true,
	"from": true, "with": true, "without": true, "by": true, "between": true,
	"among": true, "during": true, "after": true, "before": true, "into": true,
	"onto": true, "over": true, "under": true, "through": true, "across": true,
	"about": true, "above": true, "below": true, "against": true, "along": true,
	"around": true, "behind": true, "beyond": true, "within": true, "upon": true,
	"per": true, "via": true, "than": true, "as": true, "and": true, "or": true,
	"but": true, "nor": true, "so": true, "yet": true, "if": true, "then": true,
	"else": true, "when": true, "while": true, "because": true, "although": true,
	"though": true, "unless": true, "whereas": true, "since": true, "until": true,
	"whether": true, "plus": true, "versus": true, "vs": true,

	// interrogatives and auxiliaries
	"what": true, "which": true, "who": true, "whom": true, "whose": true,
	"where": true, "why": true, "how": true, "do": true, "does": true,
	"did": true, "is": true, "are": true, "was": true, "were": true, "be": true,
	"been": true, "being": true, "am": true, "can": true, "could": true,
	"should": true, "would": true, "may": true, "might": true, "must": true,
	"shall": true, "will": true, "has": true, "have": true, "had": true,

	// request verbs and investigation scaffolding
	"investigate": true, "investigates": true, "investigated": true,
	"investigation": true, "investigations": true, "diagnose": true,
	"diagnoses": true, "diagnosed": true, "diagnosis": true, "trace": true,
	"traces": true, "tracing": true, "explain": true, "explains": true,
	"explained": true, "explanation": true, "determine": true, "determines": true,
	"determined": true, "identify": true, "identifies": true, "identified": true,
	"identifying": true, "identification": true, "locate": true, "locates": true,
	"located": true, "locating": true, "understand": true, "understands": true,
	"understood": true, "understanding": true, "analyze": true, "analyzes": true,
	"analyzed": true, "analyzing": true, "analysis": true, "gather": true,
	"gathers": true, "gathering": true, "collect": true, "collects": true,
	"collecting": true, "provide": true, "provides": true, "providing": true,
	"show": true, "shows": true, "shown": true, "showing": true, "list": true,
	"lists": true, "listing": true, "describe": true, "describes": true,
	"describing": true, "summarize": true, "summarizes": true,
	"summarizing": true, "outline": true, "outlines": true, "review": true,
	"reviews": true, "examine": true, "examines": true, "inspect": true,
	"inspects": true, "assess": true, "assesses": true, "evaluates": true,
	"evaluate": true, "find": true, "finds": true, "finding": true,
	"search": true, "searches": true, "searching": true, "look": true,
	"looks": true, "looking": true, "tell": true, "tells": true, "report": true,
	"reports": true, "reporting": true, "support": true, "supports": true,
	"supporting": true, "covered": true, "covers": true, "covering": true,
	"include": true, "includes": true, "including": true, "use": true,
	"uses": true, "using": true, "used": true, "leverage": true, "obtain": true,
	"obtains": true, "retrieve": true, "retrieves": true, "fetch": true,
	"read": true, "reads": true, "reading": true, "mention": true, "mentions": true,
	"note": true, "notes": true, "focus": true, "focuses": true, "want": true,
	"wants": true, "need": true, "needs": true, "like": true, "please": true,
	"help": true, "helps": true, "ask": true, "asks": true, "asked": true,
	"thanks": true, "thank": true, "also": true,
	"answer": true, "answers": true, "answered": true, "given": true,
	"give": true, "gives": true, "take": true, "takes": true, "see": true,
	"able": true, "ensure": true, "ensures": true, "allow": true, "allows": true,
	"enable": true, "enables": true, "disable": true, "disables": true,
	"handle": true, "handles": true, "handled": true, "handling": true,
	"prevent": true, "prevents": true, "prevented": true, "prevention": true,
	"responsible": true, "consider": true, "considers": true,
	"based": true, "make": true,
	"makes": true, "made": true, "get": true, "gets": true, "set": true,
	"sets": true, "let": true, "cause": true, "causes": true, "result": true,
	"results": true, "resulting": true, "related": true,
	"relating": true, "regarding": true, "concerning": true, "respecting": true,

	// meta nouns used to ask for repository material
	"code": true, "codes": true, "source": true, "sources": true, "src": true,
	"file": true, "files": true, "filename": true, "filenames": true,
	"path": true, "paths": true, "line": true, "lines": true, "number": true,
	"numbers": true, "num": true, "evidence": true, "information": true,
	"info": true, "detail": true, "details": true, "data": true, "thing": true,
	"things": true, "stuff": true, "way": true, "ways": true, "part": true,
	"parts": true, "piece": true, "pieces": true, "kind": true, "kinds": true,
	"sort": true, "sorts": true, "type": true, "types": true, "example": true,
	"examples": true, "question": true,
	"questions": true, "problem": true, "problems": true, "issue": true,
	"issues": true, "change": true, "changes": true, "system": true,
	"systems": true, "work": true, "works": true, "working": true,
	"repository": true, "repo": true, "project": true, "projects": true,
	"component": true, "components": true, "documentation": true, "docs": true,
	"doc": true, "overview": true, "overviews": true, "summary": true,
	"purpose": true, "goal": true, "goals": true, "context": true,
	"concept": true, "concepts": true, "topic": true, "topics": true,
	"term": true, "terms": true, "word": true, "words": true, "text": true,
	"texts": true, "content": true, "contents": true, "page": true,
	"pages": true, "item": true, "items": true, "element": true,
	"elements": true, "area": true, "areas": true, "place": true,
	"places": true, "time": true, "times": true, "case": true, "cases": true,
	"scenario": true, "scenarios": true, "relation": true, "relationship": true,
	"relationships": true, "step": true, "steps": true, "behavior": true,
	"behaviour": true, "logic": true, "codebase": true, "everything": true,
	"fit": true, "overall": true, "shape": true, "together": true, "e.g": true,
	"i.e": true, "etc": true, "readme": true,

	// ordinary verbs and their inflections that describe the request rather
	// than the repository
	"e": true, "g": true,
	"leave": true, "leaves": true, "left": true, "leaving": true,
	"remain": true, "remains": true, "remaining": true,
	"stay": true, "stays": true, "staying": true,
	"keep": true, "keeps": true, "keeping": true,
	"become": true, "becomes": true, "becoming": true,
	"come": true, "comes": true, "coming": true,
	"go": true, "goes": true, "going": true, "gone": true,
	"put": true, "puts": true, "seem": true, "seems": true,
	"appear": true, "appears": true, "appeared": true,
	"exist": true, "exists": true, "existed": true,
	"occur": true, "occurs": true, "occurred": true,
	"happen": true, "happens": true, "happened": true,
	"mean": true, "means": true, "meant": true,
	"matter": true, "matters": true, "affect": true, "affects": true,
	"affected": true, "impact": true, "impacts": true,
	"reach": true, "reaches": true, "achieve": true, "achieves": true,
	"achieved": true, "fail": true, "fails": true, "failed": true,
	"failing": true, "failure": true, "failures": true,
	"error": true, "errors": true, "exception": true, "exceptions": true,
	"bug": true, "bugs": true, "crash": true, "crashes": true, "crashed": true,
	"hang": true, "hangs": true, "hung": true,
	"preventing": true,
	"run":        true, "runs": true, "running": true,
	"start": true, "starts": true, "started": true, "starting": true,
	"stop": true, "stops": true, "stopped": true, "stopping": true,
	"call": true, "calls": true, "called": true, "calling": true,
	"apply": true, "applies": true, "applied": true, "applying": true,
	"allowing": true,
	"worked":   true,
	"helped":   true, "helping": true,
	"try": true, "tries": true, "tried": true, "trying": true,
	"needed": true, "needing": true,
	"wanted": true, "wanting": true,
}

// investigationInstructionTerms holds words that only make sense as a
// directive to the assistant. A sentence that carries no inquiry and one of
// these words describes how the answer must be delivered rather than naming
// a repository concept, so it must not contribute search terms.
var investigationInstructionTerms = map[string]bool{
	"remember": true, "remembers": true, "remembered": true, "remembering": true,
	"update": true, "updates": true, "updated": true, "updating": true,
	"run": true, "runs": true, "ran": true, "running": true,
	"tell": true, "tells": true, "told": true, "telling": true,
	"notify": true, "notifies": true, "notified": true, "notifying": true,
	"inform": true, "informs": true, "informed": true, "informing": true,
	"follow": true, "follows": true, "followed": true, "following": true,
	"mind": true, "minds": true, "minded": true,
	"ping": true, "pings": true, "pinged": true, "pinging": true,
	"remind": true, "reminds": true, "reminded": true, "reminding": true,
}

// investigationTopicCandidate is one scored extraction candidate.
type investigationTopicCandidate struct {
	term     string
	words    []string
	kind     string
	score    int
	position int
	proper   bool
}

// investigationTokenRecord is one extracted identifier-like span.
type investigationTokenRecord struct {
	text          string
	lower         string
	words         []string
	codeLike      bool
	generic       bool
	properNoun    bool
	clause        int
	sentence      int
	position      int
	clauseInitial bool
	start         int
	end           int
}

// InvestigationDecision records whether a request authorizes a deterministic
// read-only investigation and, when it does, the grounded concept list that
// will drive the bounded discovery.
type InvestigationDecision struct {
	Eligible           bool
	NeedsClarification bool
	Reason             string
	Topics             []string
}

// AssessInvestigation intentionally recognizes only explicit read-only inquiry.
// Positive mutation, command execution, deployment, or network language makes
// authorization unclear and is returned to the operator for clarification.
func AssessInvestigation(request string, constraints []string) InvestigationDecision {
	text := strings.TrimSpace(strings.Join(append([]string{request}, constraints...), "\n"))
	if text == "" || !investigationIntent.MatchString(text) {
		return InvestigationDecision{Reason: "request does not explicitly authorize a read-only repository investigation"}
	}
	withoutRestrictions := negativeRestriction.ReplaceAllString(text, " ")
	if unsafeIntent.MatchString(withoutRestrictions) || unsafeGerundIntent.MatchString(withoutRestrictions) {
		return InvestigationDecision{NeedsClarification: true, Reason: "request combines investigation with modification, command execution, deployment, or external-access work; clarify the authorized read-only scope"}
	}
	// Concepts are taken from the request alone. Constraints describe the
	// authorized scope and must never contribute search terms.
	topics := ExtractInvestigationTopics(request)
	if len(topics) == 0 {
		return InvestigationDecision{NeedsClarification: true, Reason: "request does not identify a concrete symbol, subsystem, behavior, or phrase to investigate"}
	}
	return InvestigationDecision{Eligible: true, Topics: investigationTopicTerms(topics)}
}

// ExtractInvestigationTopics derives the bounded, grounded concept list for a
// request. The extractor is deterministic and conservative: it normalizes
// punctuation, discards generic request wording, preserves language-level
// identifiers, and forms multiword phrases only from adjacent domain words.
func ExtractInvestigationTopics(request string) []InvestigationTopic {
	text := strings.TrimSpace(request)
	if text == "" {
		return nil
	}
	// Negative restrictions describe the authorized scope, not the subject of
	// the investigation, and are removed before concepts are extracted.
	text = negativeRestriction.ReplaceAllString(text, " ")
	candidates := investigationTopicCandidates(text)
	selected := selectInvestigationTopics(candidates, MaxInvestigationTerms)
	topics := make([]InvestigationTopic, 0, len(selected))
	for _, candidate := range selected {
		topics = append(topics, InvestigationTopic{Term: candidate.term, Words: candidate.words, Kind: candidate.kind})
	}
	return topics
}

func investigationTopicTerms(topics []InvestigationTopic) []string {
	terms := make([]string, 0, len(topics))
	for _, topic := range topics {
		terms = append(terms, topic.Term)
	}
	return terms
}

// investigationTopicCandidates turns a request into scored concept candidates.
func investigationTopicCandidates(text string) []investigationTopicCandidate {
	tokens := tokenizeInvestigation(text)
	// A request mixes the subject of the investigation with instructions to
	// the assistant about how the answer must be delivered. Sentences that
	// only instruct carry no repository concept, so they are removed before
	// concepts are extracted. Sentences that carry an inquiry are always
	// kept, and a sentence that is left without an inquiry still keeps its
	// vocabulary unless it is purely an instruction.
	scoped, scope := scopeInvestigationTokensToSubjectSentences(tokens)
	candidates := investigationCandidatesFromTokens(text, scoped, scope, tokens)
	if len(candidates) == 0 && len(scoped) != len(tokens) {
		candidates = investigationCandidatesFromTokens(text, tokens, nil, tokens)
	}
	return candidates
}

// scopeInvestigationTokensToSubjectSentences removes the sentences that only
// instruct the assistant from concept extraction. A sentence that carries no
// inquiry and only directs the assistant describes how the answer must be
// delivered, so it names no repository concept.
//
// The returned scope maps sentence index to true for the sentences that stay
// in scope. A nil scope means every sentence is in scope. A sentence that
// carries an inquiry is never removed, so a request that names its subject in
// a follow-up sentence is still answered.
func scopeInvestigationTokensToSubjectSentences(tokens []investigationTokenRecord) ([]investigationTokenRecord, map[int]bool) {
	if len(tokens) == 0 {
		return tokens, nil
	}
	inquirySentences := map[int]bool{}
	instructionSentences := map[int]bool{}
	for _, token := range tokens {
		if investigationScopeIntent.MatchString(token.text) {
			inquirySentences[token.sentence] = true
			continue
		}
		if investigationInstructionTerms[token.lower] {
			instructionSentences[token.sentence] = true
		}
	}
	if len(instructionSentences) == 0 {
		return tokens, nil
	}
	scope := map[int]bool{}
	scoped := make([]investigationTokenRecord, 0, len(tokens))
	for _, token := range tokens {
		if inquirySentences[token.sentence] || !instructionSentences[token.sentence] {
			scoped = append(scoped, token)
			scope[token.sentence] = true
		}
	}
	if len(scoped) == 0 {
		return tokens, nil
	}
	return scoped, scope
}

// investigationSentenceAt reports the sentence that covers a byte offset in
// the request text.
func investigationSentenceAt(tokens []investigationTokenRecord, offset int) int {
	for _, token := range tokens {
		if token.end > offset {
			return token.sentence
		}
	}
	return -1
}

// investigationCandidatesFromTokens turns a token list into scored concept
// candidates. When a scope is supplied, only quoted subjects that fall inside
// a scoped sentence are considered.
func investigationCandidatesFromTokens(text string, tokens []investigationTokenRecord, scope map[int]bool, allTokens []investigationTokenRecord) []investigationTopicCandidate {
	var candidates []investigationTopicCandidate
	var properNouns []investigationTopicCandidate
	position := 0

	for _, match := range quotedTopic.FindAllStringSubmatchIndex(text, -1) {
		quoted := strings.TrimSpace(text[match[2]:match[3]])
		if quoted == "" {
			continue
		}
		if scope != nil && !scope[investigationSentenceAt(allTokens, match[2])] {
			continue
		}
		words := investigationTopicWords(quoted)
		if len(words) == 0 {
			continue
		}
		kind := InvestigationTopicPhrase
		if IsCodeLikeIdentifier(quoted) {
			kind = InvestigationTopicIdentifier
		}
		candidates = append(candidates, investigationTopicCandidate{
			term:     quoted,
			words:    words,
			kind:     kind,
			score:    investigationTopicSpecificity(kind, words, quoted),
			position: position,
		})
		position++
	}

	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if token.generic {
			continue
		}
		kind := tokenKind(token)
		if token.properNoun {
			// A capitalized name that is neither code-like nor clause-initial
			// is usually a product or organization name rather than a
			// repository concept. It is retained only as a fallback so a
			// request about a single named subject is still answered.
			properNouns = append(properNouns, investigationTopicCandidate{
				term:     token.text,
				words:    token.words,
				kind:     kind,
				score:    investigationTopicSpecificity(kind, token.words, token.text),
				position: position,
			})
			position++
			continue
		}
		candidates = append(candidates, investigationTopicCandidate{
			term:     token.text,
			words:    token.words,
			kind:     kind,
			score:    investigationTopicSpecificity(kind, token.words, token.text),
			position: position,
		})
		position++
		next := index + 1
		if next >= len(tokens) || tokens[next].clause != token.clause {
			continue
		}
		following := tokens[next]
		if following.generic || following.properNoun {
			continue
		}
		// Phrases are formed only from plain domain words. Mixing a language
		// identifier with an adjacent word produces a term that matches
		// almost nothing in committed source.
		if kind != InvestigationTopicWord || tokenKind(following) != InvestigationTopicWord {
			continue
		}
		phrase := token.text + " " + following.text
		words := mergeInvestigationWords(token.words, following.words)
		if len(words) < 2 {
			continue
		}
		candidates = append(candidates, investigationTopicCandidate{
			term:     phrase,
			words:    words,
			kind:     InvestigationTopicPhrase,
			score:    investigationTopicSpecificity(InvestigationTopicPhrase, words, phrase),
			position: position,
		})
		position++
	}
	if len(candidates) == 0 {
		return properNouns
	}
	return candidates
}

// selectInvestigationTopics greedily selects concepts that maximize distinct
// coverage of the request's domain vocabulary. Ties are resolved by
// specificity, then by first appearance, then lexicographically, so the result
// is stable for equivalent input.
func selectInvestigationTopics(candidates []investigationTopicCandidate, limit int) []investigationTopicCandidate {
	if len(candidates) == 0 || limit <= 0 {
		return nil
	}
	ordered := make([]investigationTopicCandidate, len(candidates))
	copy(ordered, candidates)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].score != ordered[j].score {
			return ordered[i].score > ordered[j].score
		}
		if ordered[i].position != ordered[j].position {
			return ordered[i].position < ordered[j].position
		}
		return ordered[i].term < ordered[j].term
	})

	selected := make([]investigationTopicCandidate, 0, limit)
	covered := map[string]bool{}
	used := make([]bool, len(ordered))
	for len(selected) < limit {
		best := -1
		bestScore := 0
		for index, candidate := range ordered {
			if used[index] {
				continue
			}
			// A candidate that repeats already covered vocabulary adds no
			// concept, so it is only worth a slot when it is a
			// language-level identifier that may name a distinct symbol.
			if candidate.kind != InvestigationTopicIdentifier && countNewInvestigationWords(candidate.words, covered) == 0 {
				continue
			}
			score := candidate.score + 25*countNewInvestigationWords(candidate.words, covered)
			if best < 0 || score > bestScore || (score == bestScore && candidate.position < ordered[best].position) {
				best, bestScore = index, score
			}
		}
		if best < 0 {
			break
		}
		used[best] = true
		selected = append(selected, ordered[best])
		for _, word := range ordered[best].words {
			covered[investigationConceptKey(word)] = true
		}
	}
	return selected
}

// countNewInvestigationWords counts the concept words a candidate adds that
// no earlier candidate covered. Singular and plural forms of the same word
// count as one concept so a request does not spend two topic slots on one
// idea.
func countNewInvestigationWords(words []string, covered map[string]bool) int {
	count := 0
	for _, word := range words {
		if !covered[investigationConceptKey(word)] {
			count++
		}
	}
	return count
}

// investigationConceptKey folds a word to the concept it names. Only regular
// English plural "-s" endings are folded, and words that end in "ss", "us",
// or "is" are left alone so "status" and "analysis" keep their own meaning.
func investigationConceptKey(word string) string {
	if len(word) > 3 && strings.HasSuffix(word, "s") &&
		!strings.HasSuffix(word, "ss") && !strings.HasSuffix(word, "us") && !strings.HasSuffix(word, "is") {
		return word[:len(word)-1]
	}
	return word
}

// investigationTopicSpecificity scores how distinctive a concept is. Structured
// identifiers outrank phrases, phrases outrank single words, and longer
// identifiers outrank shorter ones.
func investigationTopicSpecificity(kind string, words []string, text string) int {
	score := 0
	switch kind {
	case InvestigationTopicIdentifier:
		score = 90
	case InvestigationTopicPhrase:
		score = 70
	default:
		score = 20
	}
	score += 8 * len(words)
	if kind != InvestigationTopicIdentifier {
		return score
	}
	if strings.Contains(text, "_") {
		score += 12
	}
	if strings.Contains(text, "::") {
		score += 12
	}
	if strings.Contains(text, "/") {
		score += 12
	}
	if strings.Contains(text, ".") {
		score += 12
	}
	if strings.Contains(text, "-") {
		score += 8
	}
	if strings.Contains(text, "#") {
		score += 8
	}
	if len(words) > 1 {
		score += 10
	}
	return score
}

func tokenKind(token investigationTokenRecord) string {
	if token.codeLike {
		return InvestigationTopicIdentifier
	}
	return InvestigationTopicWord
}

// tokenizeInvestigation splits a request into clause-aware identifier-like
// tokens. Clause boundaries are recorded so phrases never span a sentence or
// list boundary, and sentence boundaries are recorded so the extractor can
// tell the subject of an inquiry from request wording.
func tokenizeInvestigation(text string) []investigationTokenRecord {
	tokens := make([]investigationTokenRecord, 0, 32)
	clause := 0
	sentence := 0
	clauseInitial := true
	cursor := 0
	for _, location := range investigationToken.FindAllStringIndex(text, -1) {
		gap := text[cursor:location[0]]
		if strings.ContainsAny(gap, investigationClauseBreakers) {
			clause++
			clauseInitial = true
		}
		if strings.ContainsAny(gap, investigationSentenceBreakers) {
			sentence++
		}
		raw := text[location[0]:location[1]]
		record := investigationTokenRecord{
			text:          raw,
			lower:         strings.ToLower(raw),
			words:         investigationTopicWords(raw),
			codeLike:      IsCodeLikeIdentifier(raw),
			clause:        clause,
			sentence:      sentence,
			position:      len(tokens),
			clauseInitial: clauseInitial,
			start:         location[0],
			end:           location[1],
		}
		record.properNoun = !record.codeLike && !record.clauseInitial && startsWithUpper(raw)
		record.generic = record.genericTerm()
		tokens = append(tokens, record)
		clauseInitial = false
		cursor = location[1]
	}
	return tokens
}

// genericTerm reports whether a token carries no repository-specific meaning.
// A structured token is generic only when every constituent word is generic,
// which removes meta wording such as "source-code" while keeping identifiers
// that merely contain a common word.
func (t investigationTokenRecord) genericTerm() bool {
	if len(t.words) == 0 {
		return true
	}
	for _, word := range t.words {
		if !investigationGenericTerms[word] {
			return false
		}
	}
	return true
}

// investigationTopicWords splits an identifier-like span into its constituent
// words. Separators and case transitions are both honored so Go, C++, C#, and
// Python naming conventions are handled the same way.
func investigationTopicWords(value string) []string {
	replaced := strings.Map(func(r rune) rune {
		switch r {
		case '_', '.', '/', '#', '-', ':':
			return ' '
		}
		return r
	}, value)
	fields := strings.Fields(replaced)
	var words []string
	for _, field := range fields {
		words = append(words, splitCamelCase(field)...)
	}
	return investigationUniqueLower(words)
}

// SplitIdentifierWords exposes the identifier word split so repository
// analysis can classify files and match identifiers with the same rules the
// topic extractor used.
func SplitIdentifierWords(value string) []string {
	return investigationTopicWords(value)
}

// SplitTextWords splits arbitrary committed text into comparable words. It is
// the matching counterpart of SplitIdentifierWords.
func SplitTextWords(value string) []string {
	replaced := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, value)
	fields := strings.Fields(replaced)
	var words []string
	for _, field := range fields {
		words = append(words, splitCamelCase(field)...)
	}
	return investigationUniqueLower(words)
}

func splitCamelCase(field string) []string {
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

func investigationUniqueLower(words []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(words))
	for _, word := range words {
		lower := strings.ToLower(word)
		if lower == "" || seen[lower] {
			continue
		}
		seen[lower] = true
		result = append(result, lower)
	}
	if len(result) > maxInvestigationTopicWords {
		result = result[:maxInvestigationTopicWords]
	}
	return result
}

func mergeInvestigationWords(left, right []string) []string {
	return investigationUniqueLower(append(append([]string(nil), left...), right...))
}

// IsCodeLikeIdentifier reports whether a span uses a structured naming
// convention: a separator, an internal case transition, or a constant-style
// uppercase name. It is exported so repository analysis and the evaluated
// topic extraction candidate classify identifiers with the same rules.
func IsCodeLikeIdentifier(value string) bool {
	if strings.ContainsAny(value, "_./#-") || strings.Contains(value, "::") {
		return true
	}
	if len(splitCamelCase(value)) > 1 {
		return true
	}
	if len(value) >= 3 && value == strings.ToUpper(value) && value != strings.ToLower(value) {
		return true
	}
	return false
}

func startsWithUpper(value string) bool {
	for _, r := range value {
		return unicode.IsUpper(r)
	}
	return false
}

// InvestigationBootstrap creates one bounded deterministic discovery task and
// then preserves the existing semantic-planning capability boundary.
func InvestigationBootstrap(request string, constraints []string, modelAvailable bool) (Plan, InvestigationDecision) {
	decision := AssessInvestigation(request, constraints)
	plan := Plan{Original: strings.TrimSpace(request), Objective: strings.TrimSpace(request), ReadOnly: true}
	if !decision.Eligible {
		plan.Clarification = decision.Reason
		plan.Tasks = []Task{{
			Key: "clarify", Objective: "Clarify the authorized investigation scope", Method: MethodHumanClarification,
			Inputs: map[string]string{"question": decision.Reason}, RequiredEvidence: "an explicit read-only scope and concrete investigation subject",
			CompletionCriteria: "the operator resolves the ambiguous or unsupported authorization", BlockedReason: decision.Reason,
		}}
		return plan, decision
	}
	topics := ExtractInvestigationTopics(request)
	encoded, _ := json.Marshal(topics)
	plan.Tasks = []Task{{
		Key: "deterministic-discovery", Objective: "Gather bounded committed-repository evidence", Method: MethodInvestigationBootstrap,
		Inputs: map[string]string{"topics": string(encoded)}, RequiredEvidence: "ranked committed paths, blob identities, exact line ranges, excerpts, and cached component relationships when available",
		CompletionCriteria: "persist a bounded investigation package, including missing information and next steps",
	}}
	semantic := Task{
		Key: "semantic-interpretation", Objective: "Interpret the deterministic investigation package", Method: MethodSemanticPlanning,
		DependsOn: []string{"deterministic-discovery"}, Inputs: map[string]string{"mode": "after-deterministic-investigation"},
		RequiredEvidence:   "a schema-valid proposal grounded in the completed deterministic evidence",
		CompletionCriteria: "persist a validated bounded task graph or identify remaining capability needs", RequiresModel: true,
	}
	if !modelAvailable {
		semantic.BlockedReason = "deterministic discovery can complete, but semantic interpretation awaits a configured and qualified planning capability or operator direction"
	}
	plan.Tasks = append(plan.Tasks, semantic)
	return plan, decision
}
