package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/planner"
	"github.com/Hisha/Forgehand/internal/state"
)

// investigationRun executes one deterministic investigation against a fixture
// repository and returns the package text with its evidence.
func investigationRun(t *testing.T, ctx context.Context, db *state.Database, project state.Project, request string) (string, []state.WorkEvidence) {
	t.Helper()
	interpreted, decision := planner.InvestigationBootstrap(request, nil, false)
	if !decision.Eligible {
		t.Fatalf("request %q was not eligible: %#v", request, decision)
	}
	_, plan, err := db.CreateWorkPlanSession(ctx, "investigation", project.ID, workPlanSpecFromPlanner(interpreted))
	if err != nil {
		t.Fatal(err)
	}
	result, evidence, blocked, err := executeWorkPlanTask(ctx, db, plan, plan.Tasks[0])
	if err != nil || blocked != "" {
		t.Fatalf("execution: blocked=%q err=%v", blocked, err)
	}
	return result, evidence
}

func investigationEvidencePaths(evidence []state.WorkEvidence, kind string) []string {
	paths := make([]string, 0)
	for _, item := range evidence {
		if item.Kind == kind {
			paths = append(paths, item.Path)
		}
	}
	return paths
}

func investigationEvidenceForTopic(evidence []state.WorkEvidence, topic string) []state.WorkEvidence {
	matches := make([]state.WorkEvidence, 0)
	for _, item := range evidence {
		for _, candidate := range item.Topics {
			if candidate == topic {
				matches = append(matches, item)
				break
			}
		}
	}
	return matches
}

// investigationPlanTerms decodes the grounded topic list a plan carries.
func investigationPlanTerms(plan planner.Plan) []string {
	if len(plan.Tasks) == 0 {
		return nil
	}
	var topics []planner.InvestigationTopic
	if err := json.Unmarshal([]byte(plan.Tasks[0].Inputs["topics"]), &topics); err != nil {
		return nil
	}
	terms := make([]string, 0, len(topics))
	for _, topic := range topics {
		terms = append(terms, topic.Term)
	}
	return terms
}

func TestInvestigationBootstrapRanksImplementationAboveDocumentationForBehavior(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "internal/recovery/daemon.go", "package recovery\n\n// shutdownGraceful stops the daemon loop.\nfunc shutdownGraceful() {}\n")
	writeProjectIntakeFile(t, root, "docs/recovery.md", "# recovery\n\nThe daemon shutdown notes are described here.\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add recovery fixture")
	project := createProjectInventoryFixture(t, ctx, db, root)

	result, evidence := investigationRun(t, ctx, db, project, "Investigate daemon shutdown behavior")
	candidates := investigationEvidencePaths(evidence, "candidate-file")
	if len(candidates) < 2 {
		t.Fatalf("candidates = %#v; documentation must not be excluded", candidates)
	}
	if candidates[0] != "internal/recovery/daemon.go" {
		t.Fatalf("candidates = %#v; implementation file must rank first for a behavior question", candidates)
	}
	if !strings.Contains(result, "Investigation intent: behavior") {
		t.Fatalf("result = %q", result)
	}
}

func TestInvestigationBootstrapFollowsRelevantQualifiedCall(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "internal/dispatch/dispatcher.go", "package dispatch\n\n// dispatch prevents duplicate execution claims.\nfunc dispatch(d *dispatcher) { d.db.ClaimNextQueuedExecution() }\n")
	writeProjectIntakeFile(t, root, "internal/state/queue.go", "package state\n\nfunc (d *Database) ClaimNextQueuedExecution() {}\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add qualified call fixture")
	project := createProjectInventoryFixture(t, ctx, db, root)

	_, evidence := investigationRun(t, ctx, db, project, "Investigate duplicate execution claims read-only")
	found := false
	for _, item := range evidence {
		if item.Path == "internal/state/queue.go" && strings.Contains(item.Excerpt, "ClaimNextQueuedExecution") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("qualified call declaration was not captured: %#v", evidence)
	}
}

func TestInvestigationBootstrapRanksConfigurationFirstForConfigurationQuestion(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "internal/app/server.go", "package app\n\n// shutdown waits for the worker to stop after the timeout elapses.\nfunc shutdown() {}\n")
	writeProjectIntakeFile(t, root, "config/app.yaml", "shutdown_timeout: 30s\nshutdown_grace_period: 5s\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add configuration fixture")
	project := createProjectInventoryFixture(t, ctx, db, root)

	result, evidence := investigationRun(t, ctx, db, project, "Investigate the configuration of the shutdown timeout")
	candidates := investigationEvidencePaths(evidence, "candidate-file")
	if len(candidates) < 2 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if candidates[0] != "config/app.yaml" {
		t.Fatalf("candidates = %#v; configuration file must rank first for a configuration question", candidates)
	}
	if !strings.Contains(result, "Investigation intent: configuration") {
		t.Fatalf("result = %q", result)
	}
}

func TestInvestigationBootstrapRanksDocumentationFirstForDocumentationQuestion(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "internal/session/layer.go", "package session\n\n// Layer resolves the session state.\nfunc Layer() {}\n")
	writeProjectIntakeFile(t, root, "docs/architecture.md", "# architecture\n\nThe session layer is described here.\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add architecture fixture")
	project := createProjectInventoryFixture(t, ctx, db, root)

	result, evidence := investigationRun(t, ctx, db, project, "Investigate the architecture documentation for the session layer")
	candidates := investigationEvidencePaths(evidence, "candidate-file")
	if len(candidates) < 2 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if candidates[0] != "docs/architecture.md" {
		t.Fatalf("candidates = %#v; documentation file must rank first for a documentation question", candidates)
	}
	if !strings.Contains(result, "Investigation intent: documentation") {
		t.Fatalf("result = %q", result)
	}
}

func TestInvestigationBootstrapDistributesEvidenceAcrossTopics(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "internal/alpha/one.go", "package alpha\n\n// recovery handles the retry of a failed task.\nfunc recovery() {}\n")
	writeProjectIntakeFile(t, root, "internal/beta/two.go", "package beta\n\n// shutdown stops the worker loop.\nfunc shutdown() {}\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add multi-topic fixture")
	project := createProjectInventoryFixture(t, ctx, db, root)

	interpreted, _ := planner.InvestigationBootstrap("Investigate recovery and shutdown", nil, false)
	terms := investigationPlanTerms(interpreted)
	if len(terms) < 2 {
		t.Fatalf("terms = %#v; expected two grounded topics", terms)
	}
	_, evidence := investigationRun(t, ctx, db, project, "Investigate recovery and shutdown")
	for _, term := range terms {
		items := investigationEvidenceForTopic(evidence, term)
		if len(items) == 0 {
			t.Fatalf("topic %q produced no evidence: %#v", term, evidence)
		}
	}
	excerptTopics := map[string]bool{}
	for _, item := range evidence {
		if item.Kind != "source-excerpt" {
			continue
		}
		for _, topic := range item.Topics {
			excerptTopics[topic] = true
		}
	}
	if len(excerptTopics) < 2 {
		t.Fatalf("excerpt topics = %#v; evidence must be distributed across topics", excerptTopics)
	}
}

func TestInvestigationBootstrapCapsRepeatedMatchesInOneFile(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	noisy := "package noisy\n"
	for i := 0; i < 60; i++ {
		noisy += "// needle needle needle\n"
	}
	writeProjectIntakeFile(t, root, "internal/noisy.go", noisy)
	writeProjectIntakeFile(t, root, "internal/quiet.go", "package quiet\n\n// needle appears once.\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add noisy fixture")
	project := createProjectInventoryFixture(t, ctx, db, root)

	_, evidence := investigationRun(t, ctx, db, project, "Investigate needle")
	perFile := map[string]int{}
	for _, item := range evidence {
		if item.Kind == "source-excerpt" {
			perFile[item.Path]++
		}
	}
	for path, count := range perFile {
		if count > maxInvestigationExcerptsPerFile {
			t.Fatalf("file %s contributed %d excerpts; cap is %d", path, count, maxInvestigationExcerptsPerFile)
		}
	}
	if perFile["internal/quiet.go"] == 0 {
		t.Fatalf("excerpts = %#v; a verbose file must not starve other files", perFile)
	}
}

func TestInvestigationBootstrapReportsStaleComponentAnalysis(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)
	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatal(err)
	}
	writeProjectIntakeFile(t, root, "internal/later.go", "package internal\n\n// greeting arrived later.\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add later commit")

	result, _ := investigationRun(t, ctx, db, project, "Investigate greeting dependencies")
	if !strings.Contains(result, "cached component analysis is for commit") {
		t.Fatalf("result = %q; a stale analysis must be reported as stale", result)
	}
}

func TestInvestigationBootstrapReportsUncoveredTopics(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "internal/present.go", "package present\n\n// recovery is present.\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add partial fixture")
	project := createProjectInventoryFixture(t, ctx, db, root)

	result, _ := investigationRun(t, ctx, db, project, "Investigate recovery and absenttopic")
	if !strings.Contains(result, "no committed content matched topic(s)") {
		t.Fatalf("result = %q; an uncovered topic must be reported", result)
	}
	if !strings.Contains(result, "absenttopic") {
		t.Fatalf("result = %q; the uncovered topic must be named", result)
	}
}

func TestInvestigationBootstrapIsRepeatableAcrossRuns(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "internal/recovery/daemon.go", "package recovery\n\n// recovery and shutdown handling.\n")
	writeProjectIntakeFile(t, root, "docs/recovery.md", "# recovery\n\nshutdown notes\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add repeatable fixture")
	project := createProjectInventoryFixture(t, ctx, db, root)

	firstResult, firstEvidence := investigationRun(t, ctx, db, project, "Investigate daemon shutdown recovery")
	secondResult, secondEvidence := investigationRun(t, ctx, db, project, "Investigate daemon shutdown recovery")
	if firstResult != secondResult {
		t.Fatalf("results differ:\n%s\n---\n%s", firstResult, secondResult)
	}
	if len(firstEvidence) != len(secondEvidence) {
		t.Fatalf("evidence counts differ: %d vs %d", len(firstEvidence), len(secondEvidence))
	}
	for index := range firstEvidence {
		if firstEvidence[index].Path != secondEvidence[index].Path || firstEvidence[index].Detail != secondEvidence[index].Detail {
			t.Fatalf("evidence %d differs: %#v vs %#v", index, firstEvidence[index], secondEvidence[index])
		}
	}
}

func TestInvestigationBootstrapStaysWithinPublishedBounds(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	for i := 0; i < 60; i++ {
		writeProjectIntakeFile(t, root, "internal/bounded/file-"+string(rune('a'+i%26))+string(rune('a'+i/26))+".go", "package bounded\n\n// boundedneedle marker\n")
	}
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add bounded fixture")
	project := createProjectInventoryFixture(t, ctx, db, root)

	result, evidence := investigationRun(t, ctx, db, project, "Investigate boundedneedle")
	if len(evidence) > maxInvestigationEvidence {
		t.Fatalf("evidence count = %d; limit is %d", len(evidence), maxInvestigationEvidence)
	}
	candidates := investigationEvidencePaths(evidence, "candidate-file")
	if len(candidates) > maxInvestigationCandidates {
		t.Fatalf("candidate count = %d; limit is %d", len(candidates), maxInvestigationCandidates)
	}
	if !strings.Contains(result, "bounds were reached") {
		t.Fatalf("result = %q; a reached bound must be reported", result)
	}
}

func TestInvestigationCorpusIsRankedAsData(t *testing.T) {
	role := classifyInvestigationRole(state.InventoryFile{Path: "internal/eval/corpus.go", Category: "source", Language: "Go"})
	if role != roleData {
		t.Fatalf("corpus role = %q, want %q", role, roleData)
	}
}

func TestInvestigationPhraseMatchesAcrossCommittedFileLines(t *testing.T) {
	matcher := investigationTopicMatcher{topic: planner.InvestigationTopic{Term: "interrupted sessions", Words: []string{"interrupted", "sessions"}, Kind: planner.InvestigationTopicPhrase}, term: "interrupted sessions", words: []string{"interrupted", "sessions"}, kind: planner.InvestigationTopicPhrase}
	candidate := investigationCandidate{path: "internal/state/executions.go", lines: []string{"// interrupted executions are persisted", "sessions := loadSessions()", "}"}, topics: map[string]bool{}}
	coverage := map[string]*investigationCoverage{"interrupted sessions": {}}
	if added := scanInvestigationLines(&candidate, []investigationTopicMatcher{matcher}, coverage, 0); added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}
	if len(candidate.matches) != 1 || candidate.matches[0].lineNumber != 1 || candidate.matches[0].literal {
		t.Fatalf("matches = %#v", candidate.matches)
	}
}
