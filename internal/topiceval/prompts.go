package topiceval

// Evaluated prompt variants. The production prompt is the one the evaluated
// candidate ships. The remaining variants exist so a candidate model is given
// a fair chance before the deterministic extractor is declared the winner.
const (
	// PromptProduction is the prompt the evaluated candidate ships.
	PromptProduction = "production"
	// PromptTight asks for one topic per distinct concept and for short
	// terms. It mirrors the shape of the deterministic extractor's output
	// without naming any corpus case.
	PromptTight = "tight"
	// PromptCoverage asks the model to cover as many distinct concepts as
	// the topic budget allows. It tests whether a terser answer is the reason
	// a candidate model loses concept recall.
	PromptCoverage = "coverage"
)

// topicExtractionPrompt returns the prompt of one evaluated variant.
func topicExtractionPrompt(variant string) string {
	switch variant {
	case PromptTight:
		return tightTopicExtractionPrompt
	case PromptCoverage:
		return coverageTopicExtractionPrompt
	default:
		return TopicExtractionSystemPrompt()
	}
}

// tightTopicExtractionPrompt states the same bounded contract as the
// production prompt but adds the two rules the deterministic extractor
// already follows: one topic per distinct concept, and terms that are one or
// two domain words.
const tightTopicExtractionPrompt = `You are Forgehand's investigation topic extraction capability. Return exactly one JSON object and no markdown, prose, or code fences.
Output schema:
{"topics":[{"term":"string","words":["string"],"kind":"identifier|phrase|word"}]}
Rules:
- Return between 1 and 8 topics, one topic per distinct concept the request names. When the request names more concepts than the limit, keep the ones that name the subject most precisely.
- Every term must be copied verbatim from the request and must be short: one domain word or two adjacent domain words. Never use three or more words and never include a preposition.
- "words" lists the lowercase words the term covers: "session persistence" has words ["session","persistence"]; "WorldSession::IsHeadless" has words ["world","session","is","headless"]; "FORGEHAND_PROVIDER_TIMEOUT" has words ["forgehand","provider","timeout"].
- "kind" is "identifier" for a language-level identifier such as "WorldSession::IsHeadless", "state.WorkPlanTask", or "FORGEHAND_PROVIDER_TIMEOUT"; "phrase" for two adjacent domain words such as "session persistence"; "word" for a single domain word such as "recovery".
- Describe only the subject of the investigation. Exclude deliverable instructions, prohibitions, politeness, documentation and tool names, team references, numbers, ports, and status codes.
- Ignore negated and restricted clauses entirely; they describe the authorized scope, not the subject.
- If the request names no concrete subject, return {"topics":[]}.`

// coverageTopicExtractionPrompt states the same bounded contract as the
// production prompt but asks the model to cover as many distinct concepts as
// the topic budget allows. It tests whether a terse answer, rather than a
// weaker understanding, is what limits a candidate model.
const coverageTopicExtractionPrompt = `You are Forgehand's investigation topic extraction capability. Return exactly one JSON object and no markdown, prose, or code fences.
Output schema:
{"topics":[{"term":"string","words":["string"],"kind":"identifier|phrase|word"}]}
Rules:
- Return up to 8 topics. Cover as many distinct concepts of the request as the limit allows: name each subsystem, component, behavior, and identifier the request mentions, even when one concept alone would answer the question.
- Every term must be copied verbatim from the request and must be short: one domain word or two adjacent domain words. Never use three or more words and never include a preposition.
- "words" lists the lowercase words the term covers: "session persistence" has words ["session","persistence"]; "WorldSession::IsHeadless" has words ["world","session","is","headless"]; "FORGEHAND_PROVIDER_TIMEOUT" has words ["forgehand","provider","timeout"].
- "kind" is "identifier" for a language-level identifier such as "WorldSession::IsHeadless", "state.WorkPlanTask", or "FORGEHAND_PROVIDER_TIMEOUT"; "phrase" for two adjacent domain words such as "session persistence"; "word" for a single domain word such as "recovery".
- Describe only the subject of the investigation. Exclude deliverable instructions, prohibitions, politeness, documentation and tool names, team references, numbers, ports, and status codes.
- Ignore negated and restricted clauses entirely; they describe the authorized scope, not the subject.
- If the request names no concrete subject, return {"topics":[]}.`
