package query

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Hisha/Forgehand/internal/components"
	"github.com/Hisha/Forgehand/internal/state"
)

const (
	moduleIdentity   = "example.com/app"
	greetingIdentity = "example.com/app/internal/greeting"
	wordIdentity     = "example.com/app/internal/word"
	toolIdentity     = "example.com/app/cmd/tool"
	newToolIdentity  = "example.com/app/cmd/newtool"
	externalTarget   = "github.com/example/dep/pkg"
	externalModule   = "github.com/example/dep"
	missingTarget    = "github.com/unknown/missing"
)

func TestComponentsFiltering(t *testing.T) {
	engine, _, project := newEngineWithObservation(t, appFixtureA())

	tests := []struct {
		name   string
		filter ComponentFilter
		want   []string
	}{
		{
			name:   "all",
			filter: ComponentFilter{},
			want:   []string{toolIdentity, moduleIdentity, moduleIdentity, greetingIdentity, wordIdentity},
		},
		{
			name:   "by kind package",
			filter: ComponentFilter{Kind: KindPackage},
			want:   []string{moduleIdentity, greetingIdentity, wordIdentity},
		},
		{
			name:   "by exact identity",
			filter: ComponentFilter{Identity: greetingIdentity},
			want:   []string{greetingIdentity},
		},
		{
			name:   "by exact path",
			filter: ComponentFilter{Path: "internal/word"},
			want:   []string{wordIdentity},
		},
		{
			name:   "substring contains",
			filter: ComponentFilter{Contains: "internal/"},
			want:   []string{greetingIdentity, wordIdentity},
		},
		{
			name:   "kind and contains",
			filter: ComponentFilter{Kind: KindCommand, Contains: "tool"},
			want:   []string{toolIdentity},
		},
		{
			name:   "no match",
			filter: ComponentFilter{Identity: "example.com/app/nope"},
			want:   []string{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := engine.Components(context.Background(), project, Selection{}, test.filter)
			if err != nil {
				t.Fatalf("Components() error: %v", err)
			}
			got := make([]string, 0, len(result.Components))
			for _, component := range result.Components {
				got = append(got, component.Identity)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("identities = %#v, want %#v", got, test.want)
			}
			if result.Observation.AnalysisID == 0 || result.Observation.ProjectID != project {
				t.Fatalf("observation metadata missing: %#v", result.Observation)
			}
		})
	}
}

func TestComponentsDeterministicOrderingAndLimit(t *testing.T) {
	engine, _, project := newEngineWithObservation(t, appFixtureA())

	first, err := engine.Components(context.Background(), project, Selection{}, ComponentFilter{})
	if err != nil {
		t.Fatalf("Components() error: %v", err)
	}
	second, err := engine.Components(context.Background(), project, Selection{}, ComponentFilter{})
	if err != nil {
		t.Fatalf("Components() error: %v", err)
	}
	if !reflect.DeepEqual(first.Components, second.Components) {
		t.Fatal("component ordering is not deterministic")
	}
	if !sortedComponents(first.Components) {
		t.Fatalf("components not ordered by kind, identity, path: %#v", first.Components)
	}

	limited, err := engine.Components(context.Background(), project, Selection{}, ComponentFilter{Limit: 1})
	if err != nil {
		t.Fatalf("Components() error: %v", err)
	}
	if len(limited.Components) != 1 || !limited.Limited {
		t.Fatalf("limit not applied: %#v limited=%t", limited.Components, limited.Limited)
	}
}

func TestDependenciesOutgoingIncludesUnresolved(t *testing.T) {
	engine, _, project := newEngineWithObservation(t, appFixtureA())

	result, err := engine.Dependencies(context.Background(), project, moduleIdentity, Selection{})
	if err != nil {
		t.Fatalf("Dependencies() error: %v", err)
	}
	if result.Component.Identity != moduleIdentity {
		t.Fatalf("resolved component = %#v", result.Component)
	}

	kinds := map[string]bool{}
	targets := map[string]bool{}
	for _, dependency := range result.Dependencies {
		kinds[dependency.Kind] = true
		targets[dependency.Target] = true
	}
	for _, want := range []string{DependencyInternal, DependencyExternalModule, DependencyStdlib, DependencyUnresolved} {
		if !kinds[want] {
			t.Fatalf("missing dependency kind %q in %#v", want, result.Dependencies)
		}
	}
	if !targets[missingTarget] {
		t.Fatalf("unresolved dependency was discarded: %#v", result.Dependencies)
	}
	if !sortedDependencies(result.Dependencies) {
		t.Fatalf("dependencies not ordered deterministically: %#v", result.Dependencies)
	}
}

func TestDependenciesUnknownVersusEmpty(t *testing.T) {
	engine, _, project := newEngineWithObservation(t, appFixtureA())
	ctx := context.Background()

	if _, err := engine.Dependencies(ctx, project, "example.com/app/missing", Selection{}); !errors.Is(err, ErrUnknownComponent) {
		t.Fatalf("unknown component error = %v, want ErrUnknownComponent", err)
	}

	result, err := engine.Dependencies(ctx, project, wordIdentity, Selection{})
	if err != nil {
		t.Fatalf("known component with no dependencies errored: %v", err)
	}
	if len(result.Dependencies) != 0 {
		t.Fatalf("expected empty dependencies, got %#v", result.Dependencies)
	}
}

func TestDependentsAreDirectOnly(t *testing.T) {
	engine, _, project := newEngineWithObservation(t, appFixtureA())
	ctx := context.Background()

	result, err := engine.Dependents(ctx, project, greetingIdentity, Selection{})
	if err != nil {
		t.Fatalf("Dependents() error: %v", err)
	}
	got := make([]string, 0, len(result.Dependents))
	for _, dependent := range result.Dependents {
		got = append(got, dependent.Source)
	}
	want := []string{moduleIdentity, toolIdentity}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dependents = %#v, want %#v", got, want)
	}

	// word depends on nothing; greeting is a dependency but not a dependent.
	if _, err := engine.Dependents(ctx, project, wordIdentity, Selection{}); err != nil {
		t.Fatalf("Dependents() error: %v", err)
	}
	wordDependents, err := engine.Dependents(ctx, project, wordIdentity, Selection{})
	if err != nil {
		t.Fatalf("Dependents() error: %v", err)
	}
	if len(wordDependents.Dependents) != 1 || wordDependents.Dependents[0].Source != greetingIdentity {
		t.Fatalf("word dependents = %#v", wordDependents.Dependents)
	}

	if _, err := engine.Dependents(ctx, project, "example.com/app/missing", Selection{}); !errors.Is(err, ErrUnknownComponent) {
		t.Fatalf("unknown component error = %v, want ErrUnknownComponent", err)
	}
}

func TestDependentsKnownButZero(t *testing.T) {
	fixture := appFixtureA()
	fixture.Components = append(fixture.Components, components.Component{
		Kind:     components.KindPackage,
		Identity: "example.com/app/internal/leaf",
		Path:     "internal/leaf",
		Language: "Go",
		Internal: true,
		Evidence: []components.SourceEvidence{evidence("internal/leaf/leaf.go", 1)},
	})
	engine, _, project := newEngineWithObservation(t, fixture)

	result, err := engine.Dependents(context.Background(), project, "example.com/app/internal/leaf", Selection{})
	if err != nil {
		t.Fatalf("Dependents() error: %v", err)
	}
	if len(result.Dependents) != 0 {
		t.Fatalf("expected zero dependents, got %#v", result.Dependents)
	}
	if result.Component.Identity != "example.com/app/internal/leaf" {
		t.Fatalf("component = %#v", result.Component)
	}
}

func TestEvidenceProvenance(t *testing.T) {
	engine, _, project := newEngineWithObservation(t, appFixtureA())
	ctx := context.Background()

	result, err := engine.Evidence(ctx, project, greetingIdentity, Selection{})
	if err != nil {
		t.Fatalf("Evidence() error: %v", err)
	}
	if len(result.Evidence) != 1 {
		t.Fatalf("evidence = %#v", result.Evidence)
	}
	got := result.Evidence[0]
	if got.Path != "internal/greeting/greeting.go" || got.BlobHash == "" || got.Declaration == "" || got.Line == 0 {
		t.Fatalf("evidence provenance lost: %#v", got)
	}

	if _, err := engine.Evidence(ctx, project, "example.com/app/missing", Selection{}); !errors.Is(err, ErrUnknownComponent) {
		t.Fatalf("unknown component error = %v, want ErrUnknownComponent", err)
	}
}

func TestMissingObservationVersusEmptyResult(t *testing.T) {
	ctx := context.Background()
	engine, db, _ := newTestEngine(t)
	project, err := db.CreateProject(ctx, "Empty", "/tmp/empty")
	if err != nil {
		t.Fatalf("CreateProject() error: %v", err)
	}

	if _, err := engine.Components(ctx, project.ID, Selection{}, ComponentFilter{}); !errors.Is(err, ErrNoObservation) {
		t.Fatalf("missing observation error = %v, want ErrNoObservation", err)
	}

	engine2, _, project2 := newEngineWithObservation(t, appFixtureA())
	result, err := engine2.Components(ctx, project2, Selection{}, ComponentFilter{Identity: "nothing"})
	if err != nil {
		t.Fatalf("empty result errored: %v", err)
	}
	if len(result.Components) != 0 {
		t.Fatalf("expected empty result, got %#v", result.Components)
	}
}

func TestExplicitSelectionAndProjectBoundaries(t *testing.T) {
	ctx := context.Background()
	engine, db, projectA := newEngineWithObservation(t, appFixtureA())
	projectB, err := db.CreateProject(ctx, "Other", "/tmp/other")
	if err != nil {
		t.Fatalf("CreateProject() error: %v", err)
	}

	latest, err := db.LatestComponentObservation(ctx, projectA, false)
	if err != nil {
		t.Fatalf("LatestComponentObservation() error: %v", err)
	}

	explicit, err := engine.Components(ctx, projectA, Selection{AnalysisID: latest.ID}, ComponentFilter{})
	if err != nil {
		t.Fatalf("explicit selection error: %v", err)
	}
	if explicit.Observation.AnalysisID != latest.ID {
		t.Fatalf("selected analysis = %d, want %d", explicit.Observation.AnalysisID, latest.ID)
	}

	if _, err := engine.Components(ctx, projectB.ID, Selection{AnalysisID: latest.ID}, ComponentFilter{}); !errors.Is(err, ErrCrossProject) {
		t.Fatalf("cross-project error = %v, want ErrCrossProject", err)
	}
	if _, err := engine.Components(ctx, projectA, Selection{AnalysisID: 999999}, ComponentFilter{}); !errors.Is(err, ErrObservationNotFound) {
		t.Fatalf("missing analysis error = %v, want ErrObservationNotFound", err)
	}
	if _, err := engine.Components(ctx, 999999, Selection{}, ComponentFilter{}); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("missing project error = %v, want ErrProjectNotFound", err)
	}
}

func TestCompareSemantics(t *testing.T) {
	ctx := context.Background()
	engine, db, project := newEngineWithObservation(t, appFixtureA())
	from, err := db.LatestComponentObservation(ctx, project, false)
	if err != nil {
		t.Fatalf("LatestComponentObservation() error: %v", err)
	}
	to := persistObservation(t, db, project, "commit-b", appFixtureB())

	comparison, err := engine.Compare(ctx, project, from.ID, to.ID)
	if err != nil {
		t.Fatalf("Compare() error: %v", err)
	}

	assertComponentIdentities(t, "added components", comparison.AddedComponents, []string{newToolIdentity})
	assertComponentIdentities(t, "removed components", comparison.RemovedComponents, []string{wordIdentity})
	assertDependencyPairs(t, "added dependencies", comparison.AddedDependencies, []string{newToolIdentity + "->" + greetingIdentity})
	assertDependencyPairs(t, "removed dependencies", comparison.RemovedDependencies, []string{greetingIdentity + "->" + wordIdentity})

	if len(comparison.Reclassified) != 1 {
		t.Fatalf("reclassifications = %#v", comparison.Reclassified)
	}
	reclassified := comparison.Reclassified[0]
	if reclassified.Source != moduleIdentity || reclassified.Target != "fmt" ||
		reclassified.FromKind != DependencyStdlib || reclassified.ToKind != DependencyUnresolved {
		t.Fatalf("reclassification = %#v", reclassified)
	}

	// Evidence-only changes to unchanged relationships must not appear.
	for _, dependency := range comparison.AddedDependencies {
		if dependency.Target == greetingIdentity && dependency.Source == toolIdentity {
			t.Fatal("evidence-only change reported as added dependency")
		}
	}
	for _, dependency := range comparison.Reclassified {
		if dependency.Target == greetingIdentity {
			t.Fatal("evidence-only change reported as reclassification")
		}
	}

	second, err := engine.Compare(ctx, project, from.ID, to.ID)
	if err != nil {
		t.Fatalf("Compare() error: %v", err)
	}
	if !reflect.DeepEqual(comparison, second) {
		t.Fatal("comparison is not deterministic")
	}
}

func TestCompareRequiresSameProject(t *testing.T) {
	ctx := context.Background()
	engine, db, projectA := newEngineWithObservation(t, appFixtureA())
	projectB, err := db.CreateProject(ctx, "Other", "/tmp/other")
	if err != nil {
		t.Fatalf("CreateProject() error: %v", err)
	}
	a, err := db.LatestComponentObservation(ctx, projectA, false)
	if err != nil {
		t.Fatalf("LatestComponentObservation() error: %v", err)
	}
	b := persistObservation(t, db, projectB.ID, "commit-b", appFixtureB())

	if _, err := engine.Compare(ctx, projectA, a.ID, b.ID); !errors.Is(err, ErrCrossProject) {
		t.Fatalf("cross-project compare error = %v, want ErrCrossProject", err)
	}
	if _, err := engine.Compare(ctx, projectA, 123456, a.ID); !errors.Is(err, ErrObservationNotFound) {
		t.Fatalf("missing analysis error = %v, want ErrObservationNotFound", err)
	}
}

func TestQueriesDoNotMutatePersistedObservations(t *testing.T) {
	ctx := context.Background()
	engine, db, dir := newTestEngine(t)
	project, err := db.CreateProject(ctx, "App", "/tmp/app")
	if err != nil {
		t.Fatalf("CreateProject() error: %v", err)
	}
	persistObservation(t, db, project.ID, "commit-a", appFixtureA())

	before := countRows(t, dir, "component_observations")
	beforeComponents := countRows(t, dir, "components")

	from, err := db.LatestComponentObservation(ctx, project.ID, false)
	if err != nil {
		t.Fatalf("LatestComponentObservation() error: %v", err)
	}

	if _, err := engine.Components(ctx, project.ID, Selection{}, ComponentFilter{}); err != nil {
		t.Fatalf("Components() error: %v", err)
	}
	if _, err := engine.Dependencies(ctx, project.ID, moduleIdentity, Selection{}); err != nil {
		t.Fatalf("Dependencies() error: %v", err)
	}
	if _, err := engine.Dependents(ctx, project.ID, greetingIdentity, Selection{}); err != nil {
		t.Fatalf("Dependents() error: %v", err)
	}
	if _, err := engine.Evidence(ctx, project.ID, greetingIdentity, Selection{}); err != nil {
		t.Fatalf("Evidence() error: %v", err)
	}
	if _, err := engine.Compare(ctx, project.ID, from.ID, from.ID); err != nil {
		t.Fatalf("Compare() error: %v", err)
	}

	if after := countRows(t, dir, "component_observations"); after != before {
		t.Fatalf("observation rows changed: %d -> %d", before, after)
	}
	if after := countRows(t, dir, "components"); after != beforeComponents {
		t.Fatalf("component rows changed: %d -> %d", beforeComponents, after)
	}
}

func TestResolveComponentRepresentative(t *testing.T) {
	engine, _, project := newEngineWithObservation(t, appFixtureA())
	// moduleIdentity is shared by a module component and the root package
	// component; the package is the deterministic representative.
	result, err := engine.Dependencies(context.Background(), project, moduleIdentity, Selection{})
	if err != nil {
		t.Fatalf("Dependencies() error: %v", err)
	}
	if result.Component.Kind != KindPackage || len(result.Warnings) == 0 {
		t.Fatalf("representative = %#v warnings = %#v", result.Component, result.Warnings)
	}
}

func newTestEngine(t *testing.T) (*Engine, *state.Database, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := state.OpenAt(context.Background(), dir)
	if err != nil {
		t.Fatalf("state.OpenAt() error: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error: %v", err)
		}
	})
	return New(db), db, dir
}

func newEngineWithObservation(t *testing.T, fixture components.Result) (*Engine, *state.Database, int64) {
	t.Helper()
	engine, db, _ := newTestEngine(t)
	project, err := db.CreateProject(context.Background(), "App", "/tmp/app")
	if err != nil {
		t.Fatalf("CreateProject() error: %v", err)
	}
	persistObservation(t, db, project.ID, "commit-a", fixture)
	return engine, db, project.ID
}

func persistObservation(
	t *testing.T,
	db *state.Database,
	projectID int64,
	commit string,
	fixture components.Result,
) state.ComponentObservation {
	t.Helper()
	record, err := db.CreateComponentObservation(
		context.Background(),
		projectID,
		commit,
		commit,
		false,
		components.EngineVersion,
		fixture,
	)
	if err != nil {
		t.Fatalf("CreateComponentObservation() error: %v", err)
	}
	return *record
}

func evidence(path string, line int) components.SourceEvidence {
	return components.SourceEvidence{
		Path:        path,
		BlobHash:    "blob-" + path,
		Declaration: "decl",
		Line:        line,
	}
}

func appFixtureA() components.Result {
	return components.Result{
		Components: []components.Component{
			component(components.KindModule, moduleIdentity, ".", false, evidence("go.mod", 1)),
			component(components.KindPackage, moduleIdentity, ".", false, evidence("app.go", 1)),
			component(components.KindPackage, greetingIdentity, "internal/greeting", true, evidence("internal/greeting/greeting.go", 1)),
			component(components.KindPackage, wordIdentity, "internal/word", true, evidence("internal/word/word.go", 1)),
			component(components.KindCommand, toolIdentity, "cmd/tool", true, evidence("cmd/tool/main.go", 3)),
		},
		Dependencies: []components.Dependency{
			dependency(components.DependencyInternal, moduleIdentity, greetingIdentity, "", evidence("app.go", 3)),
			dependency(components.DependencyInternal, greetingIdentity, wordIdentity, "", evidence("internal/greeting/greeting.go", 5)),
			dependency(components.DependencyInternal, toolIdentity, greetingIdentity, "", evidence("cmd/tool/main.go", 5)),
			dependency(components.DependencyExternalModule, moduleIdentity, externalTarget, externalModule, evidence("app.go", 4)),
			dependency(components.DependencyStdlib, moduleIdentity, "fmt", "", evidence("app.go", 2)),
			dependency(components.DependencyUnresolved, moduleIdentity, missingTarget, "", evidence("app.go", 5)),
		},
		Summary: components.Summary{Complete: true, Sources: 6, Analyzed: 6},
	}
}

func appFixtureB() components.Result {
	return components.Result{
		Components: []components.Component{
			component(components.KindModule, moduleIdentity, ".", false, evidence("go.mod", 1)),
			component(components.KindPackage, moduleIdentity, ".", false, evidence("app.go", 1)),
			component(components.KindPackage, greetingIdentity, "internal/greeting", true, evidence("internal/greeting/greeting.go", 1)),
			component(components.KindCommand, toolIdentity, "cmd/tool", true, evidence("cmd/tool/main.go", 3)),
			component(components.KindCommand, newToolIdentity, "cmd/newtool", true, evidence("cmd/newtool/main.go", 3)),
		},
		Dependencies: []components.Dependency{
			dependency(components.DependencyInternal, moduleIdentity, greetingIdentity, "", evidence("app.go", 9)),
			dependency(components.DependencyInternal, toolIdentity, greetingIdentity, "", evidence("cmd/tool/main.go", 7)),
			dependency(components.DependencyInternal, newToolIdentity, greetingIdentity, "", evidence("cmd/newtool/main.go", 5)),
			dependency(components.DependencyExternalModule, moduleIdentity, externalTarget, externalModule, evidence("app.go", 4)),
			dependency(components.DependencyUnresolved, moduleIdentity, "fmt", "", evidence("app.go", 2)),
			dependency(components.DependencyUnresolved, moduleIdentity, missingTarget, "", evidence("app.go", 5)),
		},
		Summary: components.Summary{Complete: false, Sources: 6, Analyzed: 5, Skipped: 1},
	}
}

func component(
	kind components.ComponentKind,
	identity string,
	path string,
	internal bool,
	evs ...components.SourceEvidence,
) components.Component {
	return components.Component{
		Kind:     kind,
		Identity: identity,
		Path:     path,
		Language: "Go",
		Internal: internal,
		Evidence: evs,
	}
}

func dependency(
	kind components.DependencyKind,
	source string,
	target string,
	module string,
	evs ...components.SourceEvidence,
) components.Dependency {
	return components.Dependency{
		Kind:         kind,
		Source:       source,
		Target:       target,
		TargetModule: module,
		Language:     "Go",
		Evidence:     evs,
	}
}

func sortedComponents(components []Component) bool {
	for i := 1; i < len(components); i++ {
		if componentKey(components[i-1].Kind, components[i-1].Identity) > componentKey(components[i].Kind, components[i].Identity) {
			return false
		}
	}
	return true
}

func sortedDependencies(dependencies []Dependency) bool {
	for i := 1; i < len(dependencies); i++ {
		prev := dependencies[i-1]
		curr := dependencies[i]
		if prev.Source != curr.Source {
			if prev.Source > curr.Source {
				return false
			}
			continue
		}
		if prev.Kind != curr.Kind {
			if prev.Kind > curr.Kind {
				return false
			}
			continue
		}
		if prev.Target > curr.Target {
			return false
		}
	}
	return true
}

func assertComponentIdentities(t *testing.T, label string, components []Component, want []string) {
	t.Helper()
	got := make([]string, 0, len(components))
	for _, component := range components {
		got = append(got, component.Identity)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", label, got, want)
	}
}

func assertDependencyPairs(t *testing.T, label string, dependencies []Dependency, want []string) {
	t.Helper()
	got := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		got = append(got, dependency.Source+"->"+dependency.Target)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", label, got, want)
	}
}

func countRows(t *testing.T, dir, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "forgehand.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}
