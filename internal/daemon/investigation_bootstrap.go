package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/Hisha/Forgehand/internal/inventory"
	"github.com/Hisha/Forgehand/internal/planner"
	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
)

const (
	// maxInvestigationFiles bounds how many commit-pinned inventory records
	// are considered at all.
	maxInvestigationFiles = 5000
	// maxInvestigationScannedFiles bounds how many committed blobs are read
	// for content matching. Scanning is ordered so that documentation,
	// configuration, and test files always receive a reserved share.
	maxInvestigationScannedFiles = 300
	// maxInvestigationCandidates bounds how many ranked candidate files are
	// reported.
	maxInvestigationCandidates = 20
	// maxInvestigationMatches bounds how many literal matches are recorded
	// across the whole scan.
	maxInvestigationMatches = 300
	// maxInvestigationMatchesPerFile bounds how many match records one file
	// may contribute so a single verbose file cannot consume the budget.
	maxInvestigationMatchesPerFile = 3
	// maxInvestigationExcerpts bounds how many cited excerpts are stored.
	maxInvestigationExcerpts = 30
	// maxInvestigationExcerptsPerFile bounds how many excerpts one file may
	// contribute.
	maxInvestigationExcerptsPerFile = 3
	// maxInvestigationExcerptsPerTopic bounds how many excerpts one topic may
	// contribute so evidence stays distributed across the topic list.
	maxInvestigationExcerptsPerTopic = 8
	// maxInvestigationExcerptLines bounds the line span of one excerpt.
	maxInvestigationExcerptLines = 5
	// maxInvestigationEvidence bounds the persisted evidence count.
	maxInvestigationEvidence = 64
	// maxInvestigationExcerptChars bounds the stored excerpt characters.
	maxInvestigationExcerptChars = 24000
	// maxInvestigationComponents bounds how many cached components are cited.
	maxInvestigationComponents = 10
	// maxInvestigationDependencies bounds how many cached relationships are
	// cited.
	maxInvestigationDependencies = 20
	// maxInvestigationComponentEvidence bounds how many component and
	// relationship citations are stored.
	maxInvestigationComponentEvidence = 12
	// maxInvestigationContentScore caps the content contribution of one
	// file, maxInvestigationCoverageBonus caps the multi-topic credit, and
	// maxInvestigationComponentBonus caps the cached-analysis credit.
	maxInvestigationPathScore      = 40
	maxInvestigationContentScore   = 90
	maxInvestigationCoverageBonus  = 30
	maxInvestigationComponentBonus = 10
)

// investigationRole is the coarse role a committed file plays in the
// repository. Roles are derived from the existing inventory classification
// plus generic filename and path-segment markers, so no repository layout is
// assumed.
type investigationRole string

const (
	roleImplementation investigationRole = "implementation"
	roleTest           investigationRole = "test"
	roleConfiguration  investigationRole = "configuration"
	roleDocumentation  investigationRole = "documentation"
	roleBuild          investigationRole = "build"
	roleData           investigationRole = "data"
	roleScript         investigationRole = "script"
	roleOther          investigationRole = "other"
)

// investigationIntent is the kind of question the request asks. It decides
// which file roles are expected to answer the question.
type investigationIntent string

const (
	intentBehavior      investigationIntent = "behavior"
	intentDocumentation investigationIntent = "documentation"
	intentConfiguration investigationIntent = "configuration"
	intentTest          investigationIntent = "test"
)

// investigationRoleWeights scores each file role for each intent on a
// 0-300 scale. A role weight is only ever added to a file that matched at
// least one topic, so the weight expresses relevance rather than presence.
//
// A behavior question is answered by implementation, and documentation is
// deliberately the weakest role for it; a documentation question reverses
// that ordering. Documentation is never excluded from the candidate list in
// any intent.
var investigationRoleWeights = map[investigationIntent]map[investigationRole]int{
	intentBehavior: {
		roleImplementation: 300,
		roleTest:           260,
		roleScript:         240,
		roleConfiguration:  200,
		roleBuild:          180,
		roleOther:          180,
		roleData:           170,
		roleDocumentation:  120,
	},
	intentDocumentation: {
		roleDocumentation:  300,
		roleConfiguration:  150,
		roleImplementation: 120,
		roleBuild:          120,
		roleOther:          120,
		roleData:           110,
		roleTest:           110,
		roleScript:         100,
	},
	intentConfiguration: {
		roleConfiguration:  300,
		roleBuild:          220,
		roleImplementation: 200,
		roleData:           200,
		roleScript:         180,
		roleTest:           170,
		roleOther:          160,
		roleDocumentation:  140,
	},
	intentTest: {
		roleTest:           300,
		roleImplementation: 260,
		roleConfiguration:  190,
		roleScript:         180,
		roleBuild:          160,
		roleOther:          160,
		roleData:           150,
		roleDocumentation:  120,
	},
}

// investigationIntentKeywords are the deterministic signals used to detect
// the kind of question. The intent with the most matches wins; a tie keeps
// the default behavior intent.
var investigationIntentKeywords = map[investigationIntent][]string{
	intentDocumentation: {
		"documentation", "docs", "readme", "guide", "tutorial", "architecture",
		"overview", "changelog", "handbook", "manual", "wiki", "design document",
		"adr", "rfc",
	},
	intentConfiguration: {
		"configuration", "config", "settings", "manifest", "environment variable",
		"env var", "defaults", "flags", "options", "profile",
	},
	intentTest: {
		"tests", "testing", "spec", "specs", "coverage", "unit test",
		"integration test", "regression test", "assertion",
	},
	intentBehavior: {
		"behavior", "behaviour", "runtime", "lifecycle", "flow", "execution",
		"crash", "hang", "deadlock", "race condition", "timeout", "why", "how does",
	},
}

// investigationScriptExtensions are extensions that mark an executable
// script rather than a compiled or interpreted implementation.
var investigationScriptExtensions = map[string]bool{
	".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".ksh": true,
	".ps1": true, ".bat": true, ".cmd": true,
}

// investigationMatch is one recorded literal or word-coverage match.
type investigationMatch struct {
	topic      string
	lineNumber int
	literal    bool
}

// investigationCandidate is one ranked committed file.
type investigationCandidate struct {
	path         string
	blob         string
	role         investigationRole
	score        int
	pathScore    int
	contentScore int
	coverage     int
	component    int
	topics       map[string]bool
	matches      []investigationMatch
	lines        []string
}

// investigationTopicMatcher matches one grounded topic against committed
// paths and lines.
type investigationTopicMatcher struct {
	topic planner.InvestigationTopic
	term  string
	words []string
	kind  string
}

// investigationCoverage records how much committed evidence one topic
// produced.
type investigationCoverage struct {
	lines int
	files int
}

// investigationComponentReport is the cached component-analysis diagnostic.
type investigationComponentReport struct {
	names        []string
	dependencies int
	status       string
	message      string
	paths        map[string]bool
	evidence     []state.WorkEvidence
}

func executeInvestigationBootstrap(ctx context.Context, db *state.Database, plan state.WorkPlan, task state.WorkPlanTask) (string, []state.WorkEvidence, string, error) {
	var topics []planner.InvestigationTopic
	if err := json.Unmarshal([]byte(task.Inputs["topics"]), &topics); err != nil {
		return "", nil, "", fmt.Errorf("decode investigation topics: %w", err)
	}
	if len(topics) == 0 || len(topics) > planner.MaxInvestigationTerms {
		return "", nil, "", fmt.Errorf("investigation requires between 1 and %d topics", planner.MaxInvestigationTerms)
	}
	matchers := newInvestigationTopicMatchers(topics)
	inv, project, err := currentPlanInventory(ctx, db, plan)
	if err != nil {
		return "", nil, "repository inventory unavailable: " + err.Error(), nil
	}

	intent := detectInvestigationIntent(plan.OriginalRequest)
	components := investigationComponents(ctx, db, plan, inv.CommitHash, matchers)

	files := inv.Files
	if len(files) > maxInvestigationFiles {
		files = files[:maxInvestigationFiles]
	}
	roles := make(map[string]investigationRole, len(files))
	pathScores := make(map[string]int, len(files))
	for _, file := range files {
		roles[file.Path] = classifyInvestigationRole(file)
		pathScores[file.Path] = investigationPathScore(file.Path, matchers)
	}
	order := investigationScanOrder(files, intent, pathScores, roles)

	candidates := make([]investigationCandidate, 0)
	coverage := make(map[string]*investigationCoverage, len(matchers))
	for _, matcher := range matchers {
		coverage[matcher.topic.Term] = &investigationCoverage{}
	}
	componentPaths := investigationComponentPaths(components)
	totalMatches, scanned := 0, 0
	for _, file := range order {
		if ctx.Err() != nil {
			return "", nil, "", ctx.Err()
		}
		if scanned >= maxInvestigationScannedFiles || totalMatches >= maxInvestigationMatches {
			break
		}
		scanned++
		candidate := investigationCandidate{path: file.Path, blob: file.BlobHash, role: roles[file.Path], pathScore: pathScores[file.Path], topics: map[string]bool{}}
		for _, matcher := range matchers {
			if investigationPathMatches(file.Path, matcher) {
				candidate.topics[matcher.topic.Term] = true
			}
		}
		if file.Size <= maxPlanSearchFileBytes {
			data, readErr := repository.ReadBlob(ctx, project.RootPath, file.BlobHash)
			if readErr != nil {
				return "", nil, "", fmt.Errorf("read committed file %s: %w", file.Path, readErr)
			}
			if bytes.IndexByte(data, 0) < 0 {
				candidate.lines = strings.Split(string(data), "\n")
				totalMatches += scanInvestigationLines(&candidate, matchers, coverage, totalMatches)
			}
		}
		if len(candidate.topics) == 0 {
			continue
		}
		for topic := range candidate.topics {
			if stats := coverage[topic]; stats != nil {
				stats.files++
			}
		}
		candidate.contentScore = investigationContentScore(candidate, matchers)
		candidate.coverage = investigationCoverageBonus(len(candidate.topics))
		if componentPaths[candidate.path] {
			candidate.component = maxInvestigationComponentBonus
		}
		candidate.score = investigationRoleWeight(intent, candidate.role) + candidate.pathScore + candidate.contentScore + candidate.coverage + candidate.component
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].path < candidates[j].path
	})
	limitedCandidates := len(candidates) > maxInvestigationCandidates
	if limitedCandidates {
		candidates = candidates[:maxInvestigationCandidates]
	}

	evidence := make([]state.WorkEvidence, 0, maxInvestigationEvidence)
	for _, matcher := range matchers {
		stats := coverage[matcher.topic.Term]
		evidence = append(evidence, state.WorkEvidence{
			Kind:   "investigation-topic",
			Detail: fmt.Sprintf("%s — %d matched line(s) across %d scanned file(s)", matcher.topic.Term, stats.lines, stats.files),
			Topics: []string{matcher.topic.Term},
		})
	}
	for _, candidate := range candidates {
		matchedTopics := sortedTopicKeys(candidate.topics)
		evidence = append(evidence, state.WorkEvidence{
			Kind:     "candidate-file",
			Path:     candidate.path,
			BlobHash: candidate.blob,
			Detail:   fmt.Sprintf("inferred relevance score %d from deterministic path/content matches; topics: %s", candidate.score, strings.Join(matchedTopics, ", ")),
			Topics:   matchedTopics,
		})
	}
	for _, item := range components.evidence {
		if len(evidence) >= maxInvestigationEvidence {
			break
		}
		evidence = append(evidence, item)
	}
	excerptCount, excerptChars := investigationExcerpts(candidates, matchers, &evidence)
	if excerptChars >= maxInvestigationExcerptChars {
		excerptChars = maxInvestigationExcerptChars
	}

	missing := make([]string, 0, 4)
	if len(candidates) == 0 {
		missing = append(missing, "no committed paths or source lines matched the selected topics")
	}
	uncovered := uncoveredInvestigationTopics(matchers, coverage)
	if len(uncovered) > 0 {
		missing = append(missing, "no committed content matched topic(s): "+strings.Join(uncovered, ", "))
	}
	if components.status != "available" {
		missing = append(missing, components.message)
	} else if len(components.names) == 0 {
		missing = append(missing, "cached component analysis contained no component matching the selected topics")
	}
	if limitedCandidates || totalMatches >= maxInvestigationMatches || scanned >= maxInvestigationScannedFiles || excerptCount >= maxInvestigationExcerpts {
		missing = append(missing, "one or more discovery bounds were reached; narrower terms may reveal more precise evidence")
	}
	if len(missing) == 0 {
		missing = append(missing, "semantic meaning and root-cause conclusions have not been inferred")
	}

	result := formatInvestigationPackage(plan.OriginalRequest, intent, matchers, coverage, candidates, components, len(evidence), missing)
	return result, evidence, "", nil
}

// newInvestigationTopicMatchers normalizes the grounded topic list into
// matchers. Terms that arrive without a kind are classified from their word
// count so a payload written by an older build still matches.
func newInvestigationTopicMatchers(topics []planner.InvestigationTopic) []investigationTopicMatcher {
	matchers := make([]investigationTopicMatcher, 0, len(topics))
	for _, topic := range topics {
		words := make([]string, 0, len(topic.Words))
		for _, word := range topic.Words {
			word = strings.ToLower(strings.TrimSpace(word))
			if word != "" {
				words = append(words, word)
			}
		}
		kind := strings.TrimSpace(topic.Kind)
		switch kind {
		case planner.InvestigationTopicIdentifier, planner.InvestigationTopicPhrase, planner.InvestigationTopicWord:
		default:
			if len(words) > 1 {
				kind = planner.InvestigationTopicPhrase
			} else {
				kind = planner.InvestigationTopicWord
			}
		}
		matchers = append(matchers, investigationTopicMatcher{
			topic: planner.InvestigationTopic{Term: strings.TrimSpace(topic.Term), Words: words, Kind: kind},
			term:  strings.ToLower(strings.TrimSpace(topic.Term)),
			words: words,
			kind:  kind,
		})
	}
	return matchers
}

// detectInvestigationIntent classifies the request deterministically. The
// intent with the most keyword matches wins and ties keep the default
// behavior intent, so the classification is stable.
func detectInvestigationIntent(request string) investigationIntent {
	lower := strings.ToLower(request)
	best, bestCount := intentBehavior, 0
	for _, intent := range []investigationIntent{intentDocumentation, intentConfiguration, intentTest, intentBehavior} {
		count := 0
		for _, keyword := range investigationIntentKeywords[intent] {
			count += strings.Count(lower, keyword)
		}
		if count > bestCount {
			best, bestCount = intent, count
		}
	}
	return best
}

// investigationPrimaryRole is the role most likely to answer the request.
func investigationPrimaryRole(intent investigationIntent) investigationRole {
	switch intent {
	case intentDocumentation:
		return roleDocumentation
	case intentConfiguration:
		return roleConfiguration
	case intentTest:
		return roleTest
	default:
		return roleImplementation
	}
}

func investigationRoleWeight(intent investigationIntent, role investigationRole) int {
	weights, ok := investigationRoleWeights[intent]
	if !ok {
		return investigationRoleWeights[intentBehavior][role]
	}
	if weight, ok := weights[role]; ok {
		return weight
	}
	return investigationRoleWeights[intentBehavior][role]
}

// classifyInvestigationRole derives a committed file's role from the existing
// inventory classification plus generic markers. It deliberately avoids
// assuming any particular directory layout.
func classifyInvestigationRole(file state.InventoryFile) investigationRole {
	base := strings.ToLower(path.Base(file.Path))
	extension := strings.ToLower(path.Ext(base))
	stem := strings.TrimSuffix(base, extension)
	if isInvestigationTestPath(file.Path, base, stem, extension) {
		return roleTest
	}
	switch inventory.Category(file.Category) {
	case inventory.CategoryDocumentation:
		return roleDocumentation
	case inventory.CategoryConfiguration:
		return roleConfiguration
	case inventory.CategoryBuild:
		return roleBuild
	case inventory.CategoryData:
		return roleData
	case inventory.CategorySource:
		return roleImplementation
	}
	if investigationScriptExtensions[extension] {
		return roleScript
	}
	if file.Language != "" {
		return roleImplementation
	}
	return roleOther
}

// isInvestigationTestPath recognizes test files by generic filename and path
// conventions rather than by any repository-specific layout.
func isInvestigationTestPath(filePath, base, stem, extension string) bool {
	for _, segment := range strings.Split(filePath, "/") {
		switch strings.ToLower(segment) {
		case "test", "tests", "spec", "specs", "__tests__", "testdata", "testing":
			return true
		}
	}
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.Contains(base, ".stories.") {
		return true
	}
	if strings.HasSuffix(stem, "_test") || strings.HasSuffix(stem, "_tests") {
		return true
	}
	if strings.HasPrefix(stem, "test_") {
		return true
	}
	return stem == "test" && extension != ""
}

// investigationPathScore scores how strongly a committed path supports the
// topics. The score is capped so path evidence can never outrank content
// evidence.
func investigationPathScore(filePath string, matchers []investigationTopicMatcher) int {
	base := strings.ToLower(path.Base(filePath))
	directory := strings.ToLower(path.Dir(filePath))
	words := investigationWordSet(filePath)
	score := 0
	for _, matcher := range matchers {
		if matcher.term != "" {
			switch {
			case strings.Contains(base, matcher.term):
				score += 20
			case strings.Contains(directory, matcher.term):
				score += 10
			}
		}
		wordWeight := 5
		if matcher.kind == planner.InvestigationTopicIdentifier {
			wordWeight = 3
		}
		for _, word := range matcher.words {
			switch {
			case words[word]:
				score += wordWeight
			case strings.Contains(base, word):
				score += wordWeight - 2
			case strings.Contains(directory, word):
				score++
			}
		}
	}
	if score > maxInvestigationPathScore {
		score = maxInvestigationPathScore
	}
	return score
}

// investigationPathMatches reports whether a path supports a topic.
func investigationPathMatches(filePath string, matcher investigationTopicMatcher) bool {
	base := strings.ToLower(path.Base(filePath))
	directory := strings.ToLower(path.Dir(filePath))
	switch matcher.kind {
	case planner.InvestigationTopicIdentifier:
		return matcher.term != "" && (strings.Contains(base, matcher.term) || strings.Contains(directory, matcher.term))
	case planner.InvestigationTopicPhrase:
		if matcher.term != "" && (strings.Contains(base, matcher.term) || strings.Contains(directory, matcher.term)) {
			return true
		}
		return investigationWordsPresent(investigationWordSet(filePath), matcher.words)
	default:
		words := investigationWordSet(filePath)
		return len(matcher.words) > 0 && words[matcher.words[0]]
	}
}

// investigationScanOrder orders the content scan. Files of the intent's
// primary role are scanned first, but every other role keeps a reserved
// share of the scan so documentation, configuration, and test files are never
// crowded out by the number of implementation files.
func investigationScanOrder(files []state.InventoryFile, intent investigationIntent, pathScores map[string]int, roles map[string]investigationRole) []state.InventoryFile {
	primaryRole := investigationPrimaryRole(intent)
	primary := make([]state.InventoryFile, 0, len(files))
	groups := map[investigationRole][]state.InventoryFile{}
	for _, file := range files {
		role := roles[file.Path]
		if role == primaryRole {
			primary = append(primary, file)
			continue
		}
		groups[role] = append(groups[role], file)
	}
	byPathScore := func(list []state.InventoryFile) {
		sort.SliceStable(list, func(i, j int) bool {
			left, right := pathScores[list[i].Path], pathScores[list[j].Path]
			if left != right {
				return left > right
			}
			return list[i].Path < list[j].Path
		})
	}
	byPathScore(primary)
	roleOrder := make([]investigationRole, 0, len(groups))
	for role := range groups {
		roleOrder = append(roleOrder, role)
	}
	sort.Slice(roleOrder, func(i, j int) bool {
		left, right := investigationRoleWeight(intent, roleOrder[i]), investigationRoleWeight(intent, roleOrder[j])
		if left != right {
			return left > right
		}
		return roleOrder[i] < roleOrder[j]
	})
	for _, role := range roleOrder {
		byPathScore(groups[role])
	}
	reserved := make([]state.InventoryFile, 0, len(files))
	for round := 0; ; round++ {
		progressed := false
		for _, role := range roleOrder {
			group := groups[role]
			if round < len(group) {
				reserved = append(reserved, group[round])
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}
	order := make([]state.InventoryFile, 0, len(files))
	primaryIndex, reservedIndex := 0, 0
	for primaryIndex < len(primary) || reservedIndex < len(reserved) {
		for taken := 0; taken < 3 && primaryIndex < len(primary); taken++ {
			order = append(order, primary[primaryIndex])
			primaryIndex++
		}
		if reservedIndex < len(reserved) {
			order = append(order, reserved[reservedIndex])
			reservedIndex++
		}
	}
	return order
}

// scanInvestigationLines records at most maxInvestigationMatchesPerFile
// matches for one file, preferring one match per distinct topic so a file
// that matches many topics is not reduced to one topic's lines.
func scanInvestigationLines(candidate *investigationCandidate, matchers []investigationTopicMatcher, coverage map[string]*investigationCoverage, recorded int) int {
	matchedTopics := map[string]bool{}
	added := 0
	for lineIndex, line := range candidate.lines {
		if added >= maxInvestigationMatchesPerFile || recorded+added >= maxInvestigationMatches {
			break
		}
		lineLower := strings.ToLower(line)
		plausible := false
		for _, matcher := range matchers {
			if investigationTopicPrefilter(lineLower, matcher) {
				plausible = true
				break
			}
		}
		if !plausible {
			continue
		}
		lineWords := investigationWordSet(line)
		for _, matcher := range matchers {
			if added >= maxInvestigationMatchesPerFile {
				break
			}
			matched, literal := investigationTopicMatch(lineLower, lineWords, matcher)
			if !matched {
				continue
			}
			if stats := coverage[matcher.topic.Term]; stats != nil {
				stats.lines++
			}
			if matchedTopics[matcher.topic.Term] {
				continue
			}
			matchedTopics[matcher.topic.Term] = true
			candidate.matches = append(candidate.matches, investigationMatch{topic: matcher.topic.Term, lineNumber: lineIndex + 1, literal: literal})
			candidate.topics[matcher.topic.Term] = true
			added++
		}
	}
	return added
}

// investigationTopicPrefilter is a cheap substring test that avoids building
// a word set for lines that cannot match any topic.
func investigationTopicPrefilter(lineLower string, matcher investigationTopicMatcher) bool {
	if matcher.kind == planner.InvestigationTopicIdentifier {
		return matcher.term != "" && strings.Contains(lineLower, matcher.term)
	}
	for _, word := range matcher.words {
		if strings.Contains(lineLower, word) {
			return true
		}
	}
	return false
}

// investigationTopicMatch reports whether one topic matches a committed line.
// Identifiers match literally. Words match only as whole words, so "test"
// never matches "latest". Phrases match literally or when every constituent
// word is present.
func investigationTopicMatch(lineLower string, lineWords map[string]bool, matcher investigationTopicMatcher) (bool, bool) {
	switch matcher.kind {
	case planner.InvestigationTopicIdentifier:
		if matcher.term != "" && strings.Contains(lineLower, matcher.term) {
			return true, true
		}
		return false, false
	case planner.InvestigationTopicPhrase:
		if matcher.term != "" && strings.Contains(lineLower, matcher.term) {
			return true, true
		}
		if investigationWordsPresent(lineWords, matcher.words) {
			return true, false
		}
		return false, false
	default:
		if len(matcher.words) > 0 && lineWords[matcher.words[0]] {
			return true, false
		}
		return false, false
	}
}

func investigationWordsPresent(words map[string]bool, wanted []string) bool {
	if len(wanted) == 0 {
		return false
	}
	for _, word := range wanted {
		if !words[word] {
			return false
		}
	}
	return true
}

// investigationWordSet splits committed text into comparable words using the
// same rules the topic extractor used.
func investigationWordSet(value string) map[string]bool {
	words := planner.SplitTextWords(value)
	set := make(map[string]bool, len(words))
	for _, word := range words {
		set[word] = true
	}
	return set
}

// investigationContentScore scores the committed content a file contributes.
// Per-topic contributions are capped so a single repeated topic cannot
// dominate the ranking.
func investigationContentScore(candidate investigationCandidate, matchers []investigationTopicMatcher) int {
	score := 0
	for _, matcher := range matchers {
		if !candidate.topics[matcher.topic.Term] {
			continue
		}
		literal := false
		for _, match := range candidate.matches {
			if match.topic == matcher.topic.Term {
				literal = literal || match.literal
				break
			}
		}
		switch matcher.kind {
		case planner.InvestigationTopicIdentifier:
			score += 25
		case planner.InvestigationTopicPhrase:
			if literal {
				score += 18
			} else {
				score += 10
			}
		default:
			score += 12
		}
	}
	if score > maxInvestigationContentScore {
		score = maxInvestigationContentScore
	}
	return score
}

// investigationCoverageBonus rewards a file that supports several distinct
// topics, because such a file is more likely to be the implementation of the
// whole subject rather than an incidental mention.
func investigationCoverageBonus(topics int) int {
	if topics <= 1 {
		return 0
	}
	bonus := (topics - 1) * 10
	if bonus > maxInvestigationCoverageBonus {
		bonus = maxInvestigationCoverageBonus
	}
	return bonus
}

// investigationExcerpts distributes cited excerpts across the topic list in
// round-robin order so one topic or one file cannot consume the whole
// excerpt budget.
func investigationExcerpts(candidates []investigationCandidate, matchers []investigationTopicMatcher, evidence *[]state.WorkEvidence) (int, int) {
	byTopic := map[string][]investigationCandidate{}
	for _, candidate := range candidates {
		for _, match := range candidate.matches {
			byTopic[match.topic] = append(byTopic[match.topic], candidate)
		}
	}
	next := map[string]int{}
	perTopic := map[string]int{}
	perFile := map[string]int{}
	excerptCount, excerptChars := 0, 0
	for {
		progressed := false
		for _, matcher := range matchers {
			if excerptCount >= maxInvestigationExcerpts || len(*evidence) >= maxInvestigationEvidence {
				return excerptCount, excerptChars
			}
			if perTopic[matcher.topic.Term] >= maxInvestigationExcerptsPerTopic {
				continue
			}
			items := byTopic[matcher.topic.Term]
			index := next[matcher.topic.Term]
			for index < len(items) {
				candidate := items[index]
				index++
				if perFile[candidate.path] >= maxInvestigationExcerptsPerFile {
					continue
				}
				match, ok := investigationFirstMatch(candidate, matcher.topic.Term)
				if !ok {
					continue
				}
				contextRadius := (maxInvestigationExcerptLines - 1) / 2
				start := max(1, match.lineNumber-contextRadius)
				end := min(len(candidate.lines), match.lineNumber+contextRadius)
				excerpt := strings.Join(candidate.lines[start-1:end], "\n")
				if excerptChars+len(excerpt) > maxInvestigationExcerptChars {
					continue
				}
				excerptChars += len(excerpt)
				excerptCount++
				perFile[candidate.path]++
				perTopic[matcher.topic.Term]++
				next[matcher.topic.Term] = index
				*evidence = append(*evidence, state.WorkEvidence{
					Kind:     "source-excerpt",
					Path:     candidate.path,
					BlobHash: candidate.blob,
					Line:     start,
					EndLine:  end,
					Excerpt:  excerpt,
					Detail:   fmt.Sprintf("discovered %s match for %q at line %d", investigationMatchLabel(match), matcher.topic.Term, match.lineNumber),
					Topics:   []string{matcher.topic.Term},
				})
				progressed = true
				break
			}
			next[matcher.topic.Term] = index
		}
		if !progressed {
			break
		}
	}
	return excerptCount, excerptChars
}

func investigationFirstMatch(candidate investigationCandidate, topic string) (investigationMatch, bool) {
	for _, match := range candidate.matches {
		if match.topic == topic {
			return match, true
		}
	}
	return investigationMatch{}, false
}

func investigationMatchLabel(match investigationMatch) string {
	if match.literal {
		return "literal"
	}
	return "word-coverage"
}

// uncoveredInvestigationTopics lists the topics that produced no committed
// content match.
func uncoveredInvestigationTopics(matchers []investigationTopicMatcher, coverage map[string]*investigationCoverage) []string {
	uncovered := make([]string, 0)
	for _, matcher := range matchers {
		stats := coverage[matcher.topic.Term]
		if stats == nil || stats.lines == 0 {
			uncovered = append(uncovered, matcher.topic.Term)
		}
	}
	return uncovered
}

// investigationComponents reads the cached component analysis and reports
// precisely why it was or was not usable.
func investigationComponents(ctx context.Context, db *state.Database, plan state.WorkPlan, commit string, matchers []investigationTopicMatcher) investigationComponentReport {
	observation, err := db.LatestComponentObservation(ctx, plan.ProjectID, true)
	if err != nil {
		return investigationComponentReport{status: "unreadable", message: "cached component analysis could not be read: " + err.Error()}
	}
	if observation == nil {
		return investigationComponentReport{status: "never-performed", message: "no cached component analysis is available"}
	}
	if observation.CommitHash != commit {
		return investigationComponentReport{status: "stale", message: fmt.Sprintf("cached component analysis is for commit %s, not investigation commit %s", observation.CommitHash, commit)}
	}
	selected := map[string]bool{}
	var names []string
	var evidence []state.WorkEvidence
	paths := map[string]bool{}
	for _, component := range observation.Components {
		if len(names) >= maxInvestigationComponents {
			break
		}
		if !investigationComponentMatches(component.Identity+" "+component.Path, matchers) {
			continue
		}
		selected[component.Identity] = true
		names = append(names, component.Identity)
		if component.Path != "" {
			paths[component.Path] = true
		}
		topics := investigationMatchingTopics(component.Identity+" "+component.Path, matchers)
		for _, source := range component.Evidence {
			if len(evidence) >= maxInvestigationComponentEvidence {
				break
			}
			evidence = append(evidence, state.WorkEvidence{Kind: "relevant-component", Path: source.Path, BlobHash: source.BlobHash, Line: source.Line, EndLine: source.Line, Excerpt: source.Declaration, Detail: fmt.Sprintf("discovered cached %s component %s", component.Kind, component.Identity), Topics: topics})
		}
	}
	dependencyCount := 0
	for _, dependency := range observation.Dependencies {
		if dependencyCount >= maxInvestigationDependencies {
			break
		}
		if !selected[dependency.Source] && !selected[dependency.Target] {
			continue
		}
		dependencyCount++
		topics := investigationMatchingTopics(dependency.Source+" "+dependency.Target, matchers)
		for _, source := range dependency.Evidence {
			if len(evidence) >= maxInvestigationComponentEvidence {
				break
			}
			evidence = append(evidence, state.WorkEvidence{Kind: "relevant-dependency", Path: source.Path, BlobHash: source.BlobHash, Line: source.Line, EndLine: source.Line, Excerpt: source.Declaration, Detail: fmt.Sprintf("discovered cached dependency %s -> %s", dependency.Source, dependency.Target), Topics: topics})
		}
	}
	sort.Strings(names)
	status, message := "available", "cached component analysis is available"
	if !observation.Summary.Complete {
		status = "partial"
		message = "cached component analysis is incomplete; component and relationship coverage may be limited"
	}
	if len(names) == 0 && status == "available" {
		status = "no-match"
		message = "cached component analysis contained no component matching the selected topics"
	}
	return investigationComponentReport{names: names, dependencies: dependencyCount, status: status, message: message, paths: paths, evidence: evidence}
}

// investigationComponentPaths lists the committed paths described by the
// cached analysis so matching files can be credited with it.
func investigationComponentPaths(report investigationComponentReport) map[string]bool {
	return report.paths
}

// investigationComponentMatches reports whether a cached component identity
// or path supports one of the topics.
func investigationComponentMatches(value string, matchers []investigationTopicMatcher) bool {
	return len(investigationMatchingTopics(value, matchers)) > 0
}

// investigationMatchingTopics lists the topics a cached component identity or
// path supports, so component citations name the concepts they answer.
func investigationMatchingTopics(value string, matchers []investigationTopicMatcher) []string {
	lower := strings.ToLower(value)
	words := investigationWordSet(value)
	matched := make([]string, 0, len(matchers))
	for _, matcher := range matchers {
		switch matcher.kind {
		case planner.InvestigationTopicIdentifier:
			if matcher.term != "" && strings.Contains(lower, matcher.term) {
				matched = append(matched, matcher.topic.Term)
			}
		case planner.InvestigationTopicPhrase:
			if matcher.term != "" && strings.Contains(lower, matcher.term) {
				matched = append(matched, matcher.topic.Term)
				continue
			}
			if investigationWordsPresent(words, matcher.words) {
				matched = append(matched, matcher.topic.Term)
			}
		default:
			if len(matcher.words) > 0 && words[matcher.words[0]] {
				matched = append(matched, matcher.topic.Term)
			}
		}
	}
	return matched
}

func sortedTopicKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func formatInvestigationPackage(original string, intent investigationIntent, matchers []investigationTopicMatcher, coverage map[string]*investigationCoverage, candidates []investigationCandidate, components investigationComponentReport, evidenceCount int, missing []string) string {
	terms := make([]string, 0, len(matchers))
	for _, matcher := range matchers {
		terms = append(terms, matcher.topic.Term)
	}
	coverageText := make([]string, 0, len(matchers))
	for _, matcher := range matchers {
		stats := coverage[matcher.topic.Term]
		if stats == nil {
			stats = &investigationCoverage{}
		}
		coverageText = append(coverageText, fmt.Sprintf("%s (%d matched line(s) across %d scanned file(s))", matcher.topic.Term, stats.lines, stats.files))
	}
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		paths = append(paths, fmt.Sprintf("%s (score %d)", candidate.path, candidate.score))
	}
	if len(paths) == 0 {
		paths = []string{"none"}
	}
	componentNames := components.names
	if len(componentNames) == 0 {
		componentNames = []string{"none"}
	}
	return fmt.Sprintf("Deterministic discovery completed.\nOriginal request: %s\nInvestigation intent: %s\nInvestigation topics: %s\nTopic coverage: %s\nCandidate source files (inferred relevance): %s\nRelevant components (discovered): %s\nRelevant direct dependencies (discovered): %d\nStored evidence references: %d\nMissing information: %s\nUnresolved questions: semantic interpretation and conclusions require a qualified capability or operator review.\nRecommended next steps: inspect the cited excerpts, narrow or add search terms if needed, then use qualified planning when available.", original, intent, strings.Join(terms, ", "), strings.Join(coverageText, "; "), strings.Join(paths, "; "), strings.Join(componentNames, "; "), components.dependencies, evidenceCount, strings.Join(missing, "; "))
}
