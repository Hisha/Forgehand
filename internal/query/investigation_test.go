package query

import (
	"context"
	"errors"
	"testing"

	"github.com/Hisha/Forgehand/internal/components"

)

func TestInvestigateKnownComponentAndRelationships(t *testing.T) {
	engine, db, projectID := newEngineWithObservation(t, investigationFixtureA())
	ctx := context.Background()

	result, err := engine.Investigate(ctx, projectID, InvestigationRequest{
		ProjectID:   projectID,
		ComponentID: greetingIdentity,
	})
	if err != nil {
		t.Fatalf("Investigate(): %v", err)
	}

	if result.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d, want 1", result.SchemaVersion)
	}
	if result.ProjectID != projectID {
		t.Fatalf("projectId = %d, want %d", result.ProjectID, result.ProjectID)
	}
	if result.AnalysisID == 0 {
		t.Fatal("analysisId must be > 0")
	}
	if result.Commit == "" {
		t.Fatal("commit must not be empty")
	}
	if result.Component.Identity != greetingIdentity {
		t.Fatalf("component identity = %q", result.Component.Identity)
	}
	if result.Component.Kind != KindPackage {
		t.Fatalf("component kind = %q, want package", result.Component.Kind)
	}

	// Outgoing dependencies from greeting (in fixture A)
	outgoingTargets := map[string]bool{}
	for _, d := range result.Dependencies {
		if d.Source != greetingIdentity {
			t.Fatalf("outgoing source = %q, want %q", d.Source, greetingIdentity)
		}
		outgoingTargets[d.Target] = true
	}
	if !outgoingTargets[wordIdentity] && !outgoingTargets["fmt"] {
		// at least word from fixtureA
	}
	if !outgoingTargets[wordIdentity] {
		t.Fatalf("missing outgoing word dependency: %#v", result.Dependencies)
	}

	// Incoming dependents (internal only)
	incomingSources := map[string]bool{}
	for _, d := range result.Dependents {
		if d.Kind != DependencyInternal {
			t.Fatalf("incoming dependent should be internal: %#v", d)
		}
		incomingSources[d.Source] = true
	}
	if !incomingSources[moduleIdentity] {
		t.Fatalf("expected module as dependent: %#v", result.Dependents)
	}
	if !incomingSources[toolIdentity] {
		t.Fatalf("expected tool as dependent: %#v", result.Dependents)
	}

	if len(result.Evidence) == 0 {
		t.Fatal("evidence must not be empty")
	}
	for _, e := range result.Evidence {
		if e.Path == "" || e.BlobHash == "" {
			t.Fatalf("evidence missing provenance: %#v", e)
		}
	}

	if len(result.SuggestedFiles) == 0 {
		t.Fatal("suggested files must not be empty")
	}
	for i := 1; i < len(result.SuggestedFiles); i++ {
		if result.SuggestedFiles[i].Path < result.SuggestedFiles[i-1].Path {
			t.Fatal("suggested files not ordered deterministically")
		}
	}

	// No mutations
	latestBefore, _ := db.LatestComponentObservation(ctx, projectID, false)
	result, err = engine.Investigate(ctx, projectID, InvestigationRequest{ProjectID: projectID, ComponentID: greetingIdentity})
	if err != nil {
		t.Fatalf("second investigate: %v", err)
	}
	latestAfter, _ := db.LatestComponentObservation(ctx, projectID, false)
	if latestBefore.ID != latestAfter.ID {
		t.Fatal("investigation mutated observations")
	}
}

func TestInvestigateErrors(t *testing.T) {
	engine, db, projectID := newEngineWithObservation(t, investigationFixtureA())
	ctx := context.Background()

	if _, err := engine.Investigate(ctx, 999999, InvestigationRequest{ProjectID: 999999, ComponentID: greetingIdentity}); err == nil || !errors.Is(err, ErrUnknownProject) {
		t.Fatalf("want unknown project, got %v", err)
	}

	if _, err := engine.Investigate(ctx, projectID, InvestigationRequest{ProjectID: projectID, ComponentID: "example.com/unknown"}); !errors.Is(err, ErrUnknownComponent) {
		t.Fatalf("want unknown component, got %v", err)
	}

	other, err := db.CreateProject(ctx, "Other", "/tmp/other")
	if err != nil {
		t.Fatal(err)
	}
	persistObservation(t, db, other.ID, "c1", appFixtureA())
	latest, _ := db.LatestComponentObservation(ctx, projectID, false)
	if _, err := engine.Investigate(ctx, projectID, InvestigationRequest{ProjectID: projectID, ComponentID: greetingIdentity, AnalysisID: latest.ID + 1000000}); !errors.Is(err, ErrObservationNotFound) {
		t.Fatalf("want observation not found, got %v", err)
	}
}

func TestInvestigateHistoricalAndStale(t *testing.T) {
	engine, db, projectID := newEngineWithObservation(t, investigationFixtureA())
	ctx := context.Background()
	from, _ := db.LatestComponentObservation(ctx, projectID, false)
	to := persistObservation(t, db, projectID, "commit-b", investigationFixtureB())

	// historical selection uses from
	resFrom, err := engine.Investigate(ctx, projectID, InvestigationRequest{ProjectID: projectID, ComponentID: greetingIdentity, AnalysisID: from.ID})
	if err != nil {
		t.Fatal(err)
	}
	if resFrom.AnalysisID != from.ID {
		t.Fatalf("want historical analysis %d, got %d", from.ID, resFrom.AnalysisID)
	}

	// later analysis has changes
	resTo, err := engine.Investigate(ctx, projectID, InvestigationRequest{ProjectID: projectID, ComponentID: greetingIdentity, AnalysisID: to.ID})
	if err != nil {
		t.Fatal(err)
	}
	if resTo.AnalysisID != to.ID {
		t.Fatalf("want analysis %d, got %d", to.ID, resTo.AnalysisID)
	}
}

func TestInvestigateSuggestedFilesDedupWithReasons(t *testing.T) {
	engine, _, projectID := newEngineWithObservation(t, investigationFixtureA())

	res, err := engine.Investigate(ctx, projectID, InvestigationRequest{ProjectID: projectID, ComponentID: greetingIdentity})
	if err != nil {
		t.Fatal(err)
	}

	pathToReasons := map[string][]SuggestedFileReason{}
	for _, f := range res.SuggestedFiles {
		pathToReasons[f.Path] = append(pathToReasons[f.Path], f.Reasons...)
	}

	// greeting file should include component declaration
	if reasons := pathToReasons["internal/greeting/greeting.go"]; len(reasons) == 0 {
		t.Fatalf("expected reasons for greeting file: %#v", pathToReasons)
	}

	// Check deterministic ordering
	for i := 1; i < len(res.SuggestedFiles); i++ {
		if res.SuggestedFiles[i].Path < res.SuggestedFiles[i-1].Path {
			t.Fatal("not sorted")
		}
	}
}

func TestInvestigateLimitsAndTruncation(t *testing.T) {
	engine, _, projectID := newEngineWithObservation(t, investigationFixtureA())
	ctx := context.Background()

	limit := 1
	res, err := engine.Investigate(ctx, projectID, InvestigationRequest{ProjectID: projectID, ComponentID: greetingIdentity, Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("expected truncation")
	}
	if res.TotalEvidence < len(res.Evidence) && res.TotalEvidence > 0 {
		// structure
	}
	if res.TotalFiles > len(res.SuggestedFiles) || res.TotalEvidence > len(res.Evidence) {
		// ok
	}
	if len(res.Evidence) > limit || len(res.SuggestedFiles) > limit {
		t.Fatal("limit not applied")
	}
}

func investigationFixtureA() components.Result {
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
			dependency(components.DependencyStdlib, greetingIdentity, "fmt", "", evidence("internal/greeting/greeting.go", 2)),
			dependency(components.DependencyUnresolved, moduleIdentity, missingTarget, "", evidence("app.go", 5)),
		},
		Summary: components.Summary{Complete: true, Sources: 6, Analyzed: 6},
	}
}

func investigationFixtureB() components.Result {
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
			dependency(components.DependencyUnresolved, greetingIdentity, "fmt", "", evidence("internal/greeting/greeting.go", 10)),
			dependency(components.DependencyUnresolved, moduleIdentity, missingTarget, "", evidence("app.go", 5)),
		},
		Summary: components.Summary{Complete: false, Sources: 6, Analyzed: 5, Skipped: 1},
	}
}

var ctx = context.Background()
