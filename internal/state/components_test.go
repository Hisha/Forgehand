package state

import (
	"context"
	"reflect"
	"testing"

	"github.com/Hisha/Forgehand/internal/components"
)

func sampleComponentResult() components.Result {
	return components.Result{
		Components: []components.Component{
			{
				Kind:     components.KindModule,
				Identity: "example.com/app",
				Path:     ".",
				Language: "Go",
				Internal: false,
				Evidence: []components.SourceEvidence{
					{Path: "go.mod", BlobHash: "gomod-blob", Declaration: "module example.com/app", Line: 1},
				},
			},
			{
				Kind:     components.KindPackage,
				Identity: "internal/greeting",
				Path:     "internal/greeting",
				Language: "Go",
				Internal: true,
				Evidence: []components.SourceEvidence{
					{Path: "internal/greeting/greeting.go", BlobHash: "greeting-blob", Declaration: "package greeting", Line: 1},
				},
			},
		},
		Dependencies: []components.Dependency{
			{
				Kind:         components.DependencyInternal,
				Source:       "example.com/app",
				Target:       "internal/greeting",
				TargetModule: "",
				Language:     "Go",
				Evidence: []components.SourceEvidence{
					{Path: "app.go", BlobHash: "app-blob", Declaration: "internal/greeting", Line: 4},
				},
			},
			{
				Kind:         components.DependencyExternalModule,
				Source:       "example.com/app",
				Target:       "github.com/example/dep/pkg",
				TargetModule: "github.com/example/dep",
				Language:     "Go",
				Evidence: []components.SourceEvidence{
					{Path: "app.go", BlobHash: "app-blob", Declaration: "github.com/example/dep/pkg", Line: 6},
				},
			},
			{
				Kind:         components.DependencyUnresolved,
				Source:       "example.com/app",
				Target:       "github.com/unknown/missing",
				TargetModule: "",
				Language:     "Go",
				Evidence: []components.SourceEvidence{
					{Path: "app.go", BlobHash: "app-blob", Declaration: "github.com/unknown/missing", Line: 7},
				},
			},
		},
		Summary: components.Summary{
			Complete:                false,
			Truncated:               true,
			Sources:                 5,
			Analyzed:                4,
			Skipped:                 1,
			Modules:                 1,
			Packages:                2,
			Commands:                0,
			BuildComponents:         0,
			ConfigurationComponents: 0,
			ModuleRequirements: []components.DeclaredModule{
				{Path: "github.com/example/dep", Version: "v1.2.3"},
				{Path: "github.com/example/indirect", Version: "v0.1.0", Indirect: true},
			},
			SkippedFiles: []components.SkippedFile{
				{Path: "internal/broken/broken.go", BlobHash: "broken-blob", Reason: "malformed source"},
			},
			Dependencies: []components.DependencyCount{
				{Kind: components.DependencyInternal, Count: 1},
				{Kind: components.DependencyExternalModule, Count: 1},
				{Kind: components.DependencyUnresolved, Count: 1},
			},
		},
	}
}

func TestCreateAndReadComponentObservation(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	project := createComponentProject(t, ctx, db)

	created, err := db.CreateComponentObservation(
		ctx,
		project.ID,
		"commit-1",
		"commit-1",
		true,
		components.EngineVersion,
		sampleComponentResult(),
	)
	if err != nil {
		t.Fatalf("CreateComponentObservation(): %v", err)
	}
	if created.ID == 0 {
		t.Fatal("created component observation has no ID")
	}
	if created.ObservedAt.IsZero() {
		t.Fatal("created component observation has no observed time")
	}

	loaded, err := db.GetComponentObservation(ctx, created.ID, true)
	if err != nil {
		t.Fatalf("GetComponentObservation(): %v", err)
	}
	if loaded == nil {
		t.Fatal("GetComponentObservation() returned nil")
	}
	if loaded.CommitHash != "commit-1" || loaded.HeadCommit != "commit-1" {
		t.Fatalf("loaded commits = %q/%q", loaded.CommitHash, loaded.HeadCommit)
	}
	if !loaded.IsDirty || loaded.EngineVersion != components.EngineVersion {
		t.Fatalf("loaded flags = dirty=%v engine=%d", loaded.IsDirty, loaded.EngineVersion)
	}
	if !reflect.DeepEqual(loaded.Summary, sampleComponentResult().Summary) {
		t.Fatalf("loaded summary differs:\n%#v\nvs\n%#v", loaded.Summary, sampleComponentResult().Summary)
	}

	if len(loaded.Components) != 2 {
		t.Fatalf("loaded components = %#v", loaded.Components)
	}
	if loaded.Components[0].Identity != "example.com/app" || loaded.Components[1].Identity != "internal/greeting" {
		t.Fatalf("component order = %#v", loaded.Components)
	}
	if !loaded.Components[1].Internal {
		t.Fatal("internal component flag lost")
	}
	if len(loaded.Components[0].Evidence) != 1 || loaded.Components[0].Evidence[0].BlobHash != "gomod-blob" {
		t.Fatalf("component evidence lost: %#v", loaded.Components[0].Evidence)
	}

	if len(loaded.Dependencies) != 3 {
		t.Fatalf("loaded dependencies = %#v", loaded.Dependencies)
	}
	// Dependencies are ordered by kind, source, target.
	if loaded.Dependencies[0].Kind != string(components.DependencyExternalModule) {
		t.Fatalf("dependency order = %#v", loaded.Dependencies)
	}
	var found bool
	for _, dependency := range loaded.Dependencies {
		if dependency.Target == "github.com/example/dep/pkg" {
			found = true
			if dependency.TargetModule != "github.com/example/dep" {
				t.Fatalf("target module lost: %#v", dependency)
			}
			if len(dependency.Evidence) != 1 || dependency.Evidence[0].BlobHash != "app-blob" {
				t.Fatalf("dependency evidence lost: %#v", dependency.Evidence)
			}
		}
	}
	if !found {
		t.Fatalf("external dependency missing: %#v", loaded.Dependencies)
	}
}

func TestComponentObservationDetailLoading(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	project := createComponentProject(t, ctx, db)

	created, err := db.CreateComponentObservation(
		ctx,
		project.ID,
		"commit-1",
		"commit-1",
		false,
		components.EngineVersion,
		sampleComponentResult(),
	)
	if err != nil {
		t.Fatalf("CreateComponentObservation(): %v", err)
	}
	if created.Components == nil || created.Dependencies == nil {
		t.Fatalf("creation did not return details: %#v", created)
	}

	loaded, err := db.GetComponentObservation(ctx, created.ID, false)
	if err != nil {
		t.Fatalf("GetComponentObservation(without details): %v", err)
	}
	if loaded.Components != nil || loaded.Dependencies != nil {
		t.Fatalf("details were loaded despite includeDetails=false")
	}
}

func TestLatestComponentObservation(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	project := createComponentProject(t, ctx, db)

	latest, err := db.LatestComponentObservation(ctx, project.ID, false)
	if err != nil {
		t.Fatalf("LatestComponentObservation(empty): %v", err)
	}
	if latest != nil {
		t.Fatalf("LatestComponentObservation(empty) = %#v, want nil", latest)
	}

	first, err := db.CreateComponentObservation(
		ctx, project.ID, "commit-1", "commit-1", false, components.EngineVersion, sampleComponentResult(),
	)
	if err != nil {
		t.Fatalf("create first observation: %v", err)
	}
	second, err := db.CreateComponentObservation(
		ctx, project.ID, "commit-2", "commit-2", true, components.EngineVersion, sampleComponentResult(),
	)
	if err != nil {
		t.Fatalf("create second observation: %v", err)
	}

	latest, err = db.LatestComponentObservation(ctx, project.ID, true)
	if err != nil {
		t.Fatalf("LatestComponentObservation(two): %v", err)
	}
	if latest == nil || latest.ID != second.ID {
		t.Fatalf("latest = %#v, want id %d (first %d)", latest, second.ID, first.ID)
	}
	if len(latest.Components) != 2 || len(latest.Dependencies) != 3 {
		t.Fatalf("latest details = %d components, %d dependencies", len(latest.Components), len(latest.Dependencies))
	}
}

func TestCreateComponentObservationValidates(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	project := createComponentProject(t, ctx, db)
	result := sampleComponentResult()

	if _, err := db.CreateComponentObservation(ctx, 0, "c", "c", false, 1, result); err == nil {
		t.Fatal("zero project ID was accepted")
	}
	if _, err := db.CreateComponentObservation(ctx, project.ID, " ", "c", false, 1, result); err == nil {
		t.Fatal("empty commit was accepted")
	}
	if _, err := db.CreateComponentObservation(ctx, project.ID, "c", " ", false, 1, result); err == nil {
		t.Fatal("empty head commit was accepted")
	}
	if _, err := db.CreateComponentObservation(ctx, project.ID, "c", "c", false, 0, result); err == nil {
		t.Fatal("zero engine version was accepted")
	}
}

func TestGetComponentObservationMissing(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	loaded, err := db.GetComponentObservation(ctx, 999, true)
	if err != nil {
		t.Fatalf("GetComponentObservation(missing): %v", err)
	}
	if loaded != nil {
		t.Fatalf("GetComponentObservation(missing) = %#v, want nil", loaded)
	}
}

func TestComponentDetailsAreDeterministicallyOrdered(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	project := createComponentProject(t, ctx, db)

	result := components.Result{
		Components: []components.Component{
			{Kind: components.KindPackage, Identity: "z", Path: "z", Language: "Go"},
			{Kind: components.KindModule, Identity: "m", Path: "b", Language: "Go"},
			{Kind: components.KindPackage, Identity: "a", Path: "a", Language: "Go"},
			{Kind: components.KindCommand, Identity: "cmd", Path: "a", Language: "Go"},
		},
		Dependencies: []components.Dependency{
			{Kind: components.DependencyInternal, Source: "z", Target: "b", Language: "Go"},
			{Kind: components.DependencyInternal, Source: "a", Target: "z", Language: "Go"},
			{Kind: components.DependencyStdlib, Source: "a", Target: "fmt", Language: "Go"},
		},
	}

	created, err := db.CreateComponentObservation(
		ctx, project.ID, "commit-1", "commit-1", false, components.EngineVersion, result,
	)
	if err != nil {
		t.Fatalf("CreateComponentObservation(): %v", err)
	}
	loaded, err := db.GetComponentObservation(ctx, created.ID, true)
	if err != nil {
		t.Fatalf("GetComponentObservation(): %v", err)
	}

	wantComponents := []string{"a", "a", "b", "z"}
	for index, want := range wantComponents {
		if loaded.Components[index].Path != want {
			t.Fatalf("component %d path = %q, want %q (%#v)", index, loaded.Components[index].Path, want, loaded.Components)
		}
	}
	wantDependencies := []string{"z", "b", "fmt"}
	for index, want := range wantDependencies {
		if loaded.Dependencies[index].Target != want {
			t.Fatalf("dependency %d target = %q, want %q (%#v)", index, loaded.Dependencies[index].Target, want, loaded.Dependencies)
		}
	}
}

func createComponentProject(t *testing.T, ctx context.Context, db *Database) Project {
	t.Helper()
	project, err := db.CreateProject(ctx, "Component Test", "/tmp/component-test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}
