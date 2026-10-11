package daemon

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"

	_ "modernc.org/sqlite"
)

func TestQueryComponentsAnalysisIPC(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	project := createAnalysisProject(t, ctx, db, newAnalysisRepository(t))
	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("analyzeRegisteredProject(): %v", err)
	}

	response := runQuery(t, db, ipc.Request{
		Command:   "query-components",
		ProjectID: project.ID,
	})
	if !response.OK {
		t.Fatalf("query-components failed: %s", response.Message)
	}
	query := response.Query
	if query == nil || query.Query != "components" {
		t.Fatalf("query response = %#v", response.Query)
	}
	if query.Observation == nil || query.Observation.Status != "current" || !query.Observation.Complete {
		t.Fatalf("observation = %#v", query.Observation)
	}
	if query.Total == 0 || query.Empty {
		t.Fatalf("expected components, got total=%d empty=%t", query.Total, query.Empty)
	}
	for _, component := range query.Components {
		if component.Kind == "module" || component.Kind == "package" || component.Kind == "command" {
			return
		}
	}
	t.Fatalf("no expected component kinds in %#v", query.Components)
}

func TestQueryComponentsKindFilterAndUnknownProject(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	project := createAnalysisProject(t, ctx, db, newAnalysisRepository(t))
	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("analyzeRegisteredProject(): %v", err)
	}

	response := runQuery(t, db, ipc.Request{
		Command:   "query-components",
		ProjectID: project.ID,
		Kind:      "package",
	})
	if !response.OK {
		t.Fatalf("query-components failed: %s", response.Message)
	}
	if response.Query.Total == 0 {
		t.Fatal("kind filter returned no components")
	}
	for _, component := range response.Query.Components {
		if component.Kind != "package" {
			t.Fatalf("kind filter leaked %q", component.Kind)
		}
	}

	missing := runQuery(t, db, ipc.Request{Command: "query-components", ProjectID: 999999})
	if missing.OK {
		t.Fatal("unknown project query succeeded")
	}
	if !strings.Contains(missing.Message, "project not found") {
		t.Fatalf("unexpected message: %s", missing.Message)
	}
}

func TestQueryDependenciesIncludesUnresolvedIPC(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	project := createAnalysisProject(t, ctx, db, newAnalysisRepository(t))
	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("analyzeRegisteredProject(): %v", err)
	}

	response := runQuery(t, db, ipc.Request{
		Command:   "query-dependencies",
		ProjectID: project.ID,
		Identity:  "example.com/app",
	})
	if !response.OK {
		t.Fatalf("query-dependencies failed: %s", response.Message)
	}
	kinds := map[string]bool{}
	for _, dependency := range response.Query.Dependencies {
		kinds[dependency.Kind] = true
		if dependency.Target == "github.com/unknown/missing" && dependency.Kind != "unresolved" {
			t.Fatalf("unresolved dependency misclassified: %#v", dependency)
		}
	}
	for _, want := range []string{"internal", "external-module", "stdlib", "unresolved"} {
		if !kinds[want] {
			t.Fatalf("missing dependency kind %q in %#v", want, response.Query.Dependencies)
		}
	}

	unknown := runQuery(t, db, ipc.Request{
		Command:   "query-dependencies",
		ProjectID: project.ID,
		Identity:  "example.com/app/missing",
	})
	if unknown.OK || !strings.Contains(unknown.Message, "unknown component") {
		t.Fatalf("unknown component response = %#v", unknown)
	}
}

func TestQueryDependentsDirectIPC(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	project := createAnalysisProject(t, ctx, db, newAnalysisRepository(t))
	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("analyzeRegisteredProject(): %v", err)
	}

	response := runQuery(t, db, ipc.Request{
		Command:   "query-dependents",
		ProjectID: project.ID,
		Identity:  "example.com/app/internal/greeting",
	})
	if !response.OK {
		t.Fatalf("query-dependents failed: %s", response.Message)
	}
	sources := map[string]bool{}
	for _, dependent := range response.Query.Dependents {
		sources[dependent.Source] = true
	}
	if !sources["example.com/app"] || !sources["example.com/app/cmd/tool"] {
		t.Fatalf("dependents = %#v", response.Query.Dependents)
	}
}

func TestQueryEvidenceIPC(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	project := createAnalysisProject(t, ctx, db, newAnalysisRepository(t))
	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("analyzeRegisteredProject(): %v", err)
	}

	response := runQuery(t, db, ipc.Request{
		Command:   "query-evidence",
		ProjectID: project.ID,
		Identity:  "example.com/app/internal/greeting",
	})
	if !response.OK {
		t.Fatalf("query-evidence failed: %s", response.Message)
	}
	if len(response.Query.Evidence) == 0 {
		t.Fatal("no evidence returned")
	}
	for _, item := range response.Query.Evidence {
		if item.Path == "" || item.BlobHash == "" {
			t.Fatalf("evidence missing provenance: %#v", item)
		}
	}
}

func TestQueryChangesIPC(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)

	first, err := analyzeRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatalf("first analyze: %v", err)
	}
	advanceAnalysisRepository(t, root)
	second, err := analyzeRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatalf("second analyze: %v", err)
	}

	// Snapshot the latest analysis id to prove comparison does not persist one.
	latestBefore, err := db.LatestComponentObservation(ctx, project.ID, false)
	if err != nil {
		t.Fatalf("LatestComponentObservation(): %v", err)
	}

	response := runQuery(t, db, ipc.Request{
		Command:        "query-changes",
		ProjectID:      project.ID,
		FromAnalysisID: first.Analysis.ID,
		ToAnalysisID:   second.Analysis.ID,
	})
	if !response.OK {
		t.Fatalf("query-changes failed: %s", response.Message)
	}
	changes := response.Query.Changes
	if changes == nil {
		t.Fatal("query-changes returned no changes")
	}
	if changes.From.AnalysisID != first.Analysis.ID || changes.To.AnalysisID != second.Analysis.ID {
		t.Fatalf("change observations = %#v -> %#v", changes.From, changes.To)
	}

	assertComponentSet(t, "added", changes.AddedComponents, []string{
		"example.com/app/cmd/newtool",
		"example.com/app/internal/word",
	})
	assertComponentSet(t, "removed", changes.RemovedComponents, []string{"example.com/app/cmd/tool"})
	assertDependencySet(t, "added", changes.AddedDependencies, []string{
		"example.com/app/internal/greeting->example.com/app/internal/word",
		"example.com/app/cmd/newtool->example.com/app/internal/greeting",
	})
	assertDependencySet(t, "removed", changes.RemovedDependencies, []string{
		"example.com/app/cmd/tool->example.com/app/internal/greeting",
	})
	if len(changes.Reclassified) != 1 {
		t.Fatalf("reclassified = %#v", changes.Reclassified)
	}
	reclassified := changes.Reclassified[0]
	if reclassified.Target != "github.com/example/dep/pkg" ||
		reclassified.FromKind != "external-module" || reclassified.ToKind != "unresolved" {
		t.Fatalf("reclassification = %#v", reclassified)
	}

	latestAfter, err := db.LatestComponentObservation(ctx, project.ID, false)
	if err != nil {
		t.Fatalf("LatestComponentObservation(): %v", err)
	}
	if latestAfter.ID != latestBefore.ID {
		t.Fatalf("comparison persisted a new observation: %d -> %d", latestBefore.ID, latestAfter.ID)
	}

	invalid := runQuery(t, db, ipc.Request{
		Command:        "query-changes",
		ProjectID:      project.ID,
		FromAnalysisID: 999999,
		ToAnalysisID:   second.Analysis.ID,
	})
	if invalid.OK || !strings.Contains(invalid.Message, "not found") {
		t.Fatalf("invalid comparison response = %#v", invalid)
	}
}

func TestQueryFreshnessAndPartialIPC(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)
	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("analyzeRegisteredProject(): %v", err)
	}
	advanceAnalysisRepository(t, root)

	stale := runQuery(t, db, ipc.Request{Command: "query-components", ProjectID: project.ID})
	if !stale.OK {
		t.Fatalf("stale query failed: %s", stale.Message)
	}
	if stale.Query.Observation.Status != "stale" {
		t.Fatalf("status = %q, want stale", stale.Query.Observation.Status)
	}
	if stale.Query.Total == 0 {
		t.Fatal("stale query dropped available components")
	}
	if !hasWarning(stale.Query.Warnings, "refresh") {
		t.Fatalf("stale warning missing: %#v", stale.Query.Warnings)
	}

	partialDB := openProjectIntakeTestDatabase(t)
	partialProject := createAnalysisProject(t, ctx, partialDB, newPartialAnalysisRepository(t))
	if _, err := analyzeRegisteredProject(ctx, partialDB, partialProject.ID); err != nil {
		t.Fatalf("partial analyze: %v", err)
	}
	partial := runQuery(t, partialDB, ipc.Request{Command: "query-components", ProjectID: partialProject.ID})
	if !partial.OK {
		t.Fatalf("partial query failed: %s", partial.Message)
	}
	if partial.Query.Observation.Complete {
		t.Fatal("partial analysis reported complete")
	}
	if !hasWarning(partial.Query.Warnings, "partial") {
		t.Fatalf("partial warning missing: %#v", partial.Query.Warnings)
	}
}

func TestQueryCrossProjectIsolationIPC(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	projectA := createAnalysisProject(t, ctx, db, newAnalysisRepository(t))
	projectB := createAnalysisProject(t, ctx, db, newAnalysisRepository(t))

	analyzedA, err := analyzeRegisteredProject(ctx, db, projectA.ID)
	if err != nil {
		t.Fatalf("analyze A: %v", err)
	}
	if _, err := analyzeRegisteredProject(ctx, db, projectB.ID); err != nil {
		t.Fatalf("analyze B: %v", err)
	}

	crossed := runQuery(t, db, ipc.Request{
		Command:    "query-components",
		ProjectID:  projectB.ID,
		AnalysisID: analyzedA.Analysis.ID,
	})
	if crossed.OK || !strings.Contains(crossed.Message, "different project") {
		t.Fatalf("cross-project response = %#v", crossed)
	}
}

func TestQueryDoesNotTriggerAnalysisIPC(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	project := createAnalysisProject(t, ctx, db, newAnalysisRepository(t))
	if _, err := inventoryRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("inventoryRegisteredProject(): %v", err)
	}

	response := runQuery(t, db, ipc.Request{Command: "query-components", ProjectID: project.ID})
	if response.OK {
		t.Fatal("query against project without analysis succeeded")
	}
	if !strings.Contains(response.Message, "no stored component analysis") {
		t.Fatalf("unexpected message: %s", response.Message)
	}

	latest, err := db.LatestComponentObservation(ctx, project.ID, false)
	if err != nil {
		t.Fatalf("LatestComponentObservation(): %v", err)
	}
	if latest != nil {
		t.Fatalf("query implicitly created analysis: %#v", latest)
	}
}

func TestQueryExplicitAnalysisSelectionIPC(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)

	first, err := analyzeRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatalf("first analyze: %v", err)
	}
	advanceAnalysisRepository(t, root)
	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("second analyze: %v", err)
	}

	response := runQuery(t, db, ipc.Request{
		Command:    "query-components",
		ProjectID:  project.ID,
		AnalysisID: first.Analysis.ID,
	})
	if !response.OK {
		t.Fatalf("explicit selection failed: %s", response.Message)
	}
	if response.Query.Observation.AnalysisID != first.Analysis.ID {
		t.Fatalf("selected = %d, want %d", response.Query.Observation.AnalysisID, first.Analysis.ID)
	}
	if response.Query.Observation.Status != "stale" {
		t.Fatalf("explicit older analysis status = %q, want stale", response.Query.Observation.Status)
	}

	// The older observation must be returned verbatim rather than silently
	// substituted with the latest analysis of the same project.
	if !containsComponent(response.Query.Components, "example.com/app/cmd/tool") {
		t.Fatalf("historical query dropped a component present only in the older analysis: %#v", response.Query.Components)
	}
	if containsComponent(response.Query.Components, "example.com/app/internal/word") {
		t.Fatalf("historical query substituted the latest analysis: %#v", response.Query.Components)
	}

	missing := runQuery(t, db, ipc.Request{
		Command:    "query-components",
		ProjectID:  project.ID,
		AnalysisID: 999999,
	})
	if missing.OK || !strings.Contains(missing.Message, "not found") {
		t.Fatalf("missing analysis query did not fail explicitly: %#v", missing)
	}
}

func containsComponent(components []ipc.AnalysisComponent, identity string) bool {
	for _, component := range components {
		if component.Identity == identity {
			return true
		}
	}
	return false
}

func runQuery(t *testing.T, db *state.Database, request ipc.Request) ipc.Response {
	t.Helper()
	return runDaemonRequest(t, db, request)
}

func advanceAnalysisRepository(t *testing.T, root string) string {
	t.Helper()
	writeProjectIntakeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.22\n")
	writeProjectIntakeFile(t, root, "internal/word/word.go", "package word\n\nvar Word = \"word\"\n")
	writeProjectIntakeFile(t, root, "internal/greeting/greeting.go", `package greeting

import (
	"fmt"

	"example.com/app/internal/word"
)

var Hello = fmt.Sprint(word.Word)
`)
	writeProjectIntakeFile(t, root, "cmd/newtool/main.go", `package main

import "example.com/app/internal/greeting"

func main() {
	_ = greeting.Hello
}
`)
	runProjectIntakeGit(t, root, "rm", "-r", "cmd/tool")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "advance components and dependencies")
	return strings.TrimSpace(string(projectIntakeGitOutput(t, root, "rev-parse", "HEAD")))
}

func assertComponentSet(t *testing.T, label string, components []ipc.AnalysisComponent, want []string) {
	t.Helper()
	got := make([]string, 0, len(components))
	for _, component := range components {
		got = append(got, component.Identity)
	}
	if !sameStrings(got, want) {
		t.Fatalf("%s components = %#v, want %#v", label, got, want)
	}
}

func assertDependencySet(t *testing.T, label string, dependencies []ipc.AnalysisDependency, want []string) {
	t.Helper()
	got := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		got = append(got, dependency.Source+"->"+dependency.Target)
	}
	if !sameStrings(got, want) {
		t.Fatalf("%s dependencies = %#v, want %#v", label, got, want)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := map[string]int{}
	for _, value := range got {
		counts[value]++
	}
	for _, value := range want {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	for _, remaining := range counts {
		if remaining != 0 {
			return false
		}
	}
	return true
}

func hasWarning(warnings []string, substring string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substring) {
			return true
		}
	}
	return false
}

func TestQueryNeverCreatesObservationsIPC(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	project := createAnalysisProject(t, ctx, db, newAnalysisRepository(t))
	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("analyzeRegisteredProject(): %v", err)
	}

	path, err := state.DatabasePath()
	if err != nil {
		t.Fatalf("state.DatabasePath(): %v", err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	defer raw.Close()

	tables := []string{
		"repository_snapshots",
		"discovery_observations",
		"project_inventories",
		"inventory_files",
		"component_observations",
		"components",
		"dependencies",
	}
	before := countObservationRows(t, ctx, raw, tables)

	observation, err := db.LatestComponentObservation(ctx, project.ID, false)
	if err != nil || observation == nil {
		t.Fatalf("LatestComponentObservation(): %v %#v", err, observation)
	}

	for _, req := range []ipc.Request{
		{Command: "query-components", ProjectID: project.ID, IncludeEvidence: true},
		{Command: "query-dependencies", ProjectID: project.ID, Identity: "example.com/app", IncludeEvidence: true},
		{Command: "query-dependents", ProjectID: project.ID, Identity: "example.com/app/internal/greeting"},
		{Command: "query-evidence", ProjectID: project.ID, Identity: "example.com/app/internal/greeting"},
		{Command: "query-changes", ProjectID: project.ID, FromAnalysisID: observation.ID, ToAnalysisID: observation.ID},
	} {
		response := runQuery(t, db, req)
		if !response.OK {
			t.Fatalf("%s failed: %s", req.Command, response.Message)
		}
	}

	after := countObservationRows(t, ctx, raw, tables)
	for _, table := range tables {
		if before[table] != after[table] {
			t.Fatalf("table %s mutated by read-only query: %d -> %d", table, before[table], after[table])
		}
	}
}

func countObservationRows(
	t *testing.T,
	ctx context.Context,
	raw *sql.DB,
	tables []string,
) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, table := range tables {
		var count int
		if err := raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}
