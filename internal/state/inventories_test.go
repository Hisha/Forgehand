package state

import (
	"context"
	"reflect"
	"testing"

	"github.com/Hisha/Forgehand/internal/discovery"
	"github.com/Hisha/Forgehand/internal/inventory"
)

func sampleInventoryResult() inventory.Result {
	return inventory.Result{
		Files: []inventory.File{
			{
				Path:     "README.md",
				BlobHash: "readme-blob",
				Mode:     "100644",
				Size:     40,
				Language: "Markdown",
				Category: inventory.CategoryDocumentation,
			},
			{
				Path:     "main.go",
				BlobHash: "main-blob",
				Mode:     "100644",
				Size:     30,
				Language: "Go",
				Category: inventory.CategorySource,
			},
		},
		Summary: inventory.Summary{
			TrackedFiles:      2,
			UnclassifiedFiles: 0,
			Languages: []discovery.LanguageCount{
				{Language: "Go", Count: 1},
				{Language: "Markdown", Count: 1},
			},
			BuildSystems: []discovery.BuildSystemIndicator{
				{Name: "Go modules", Evidence: discovery.EvidenceReference{Path: "go.mod"}},
			},
			Directories: []inventory.Directory{
				{Path: "docs", Files: 1},
			},
			Categories: []inventory.CategoryCount{
				{Category: inventory.CategorySource, Count: 1},
			},
		},
	}
}

func TestCreateAndReadProjectInventory(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	project := createInventoryProject(t, ctx, db)

	created, err := db.CreateProjectInventory(
		ctx,
		project.ID,
		"commit-1",
		"commit-1",
		false,
		inventory.EngineVersion,
		sampleInventoryResult(),
	)
	if err != nil {
		t.Fatalf("CreateProjectInventory(): %v", err)
	}
	if created.ID == 0 {
		t.Fatal("created inventory has no ID")
	}
	if created.ObservedAt.IsZero() {
		t.Fatal("created inventory has no observed time")
	}
	if created.Files != nil {
		t.Fatal("created inventory unexpectedly includes files")
	}

	loaded, err := db.GetProjectInventory(ctx, created.ID, true)
	if err != nil {
		t.Fatalf("GetProjectInventory(): %v", err)
	}
	if loaded == nil {
		t.Fatal("GetProjectInventory() returned nil")
	}
	if loaded.CommitHash != "commit-1" || loaded.HeadCommit != "commit-1" {
		t.Fatalf("loaded inventory commits = %q/%q", loaded.CommitHash, loaded.HeadCommit)
	}
	if loaded.IsDirty || loaded.Truncated {
		t.Fatalf("loaded inventory flags = dirty=%v truncated=%v", loaded.IsDirty, loaded.Truncated)
	}
	if loaded.EngineVersion != inventory.EngineVersion {
		t.Fatalf("engine version = %d", loaded.EngineVersion)
	}
	if !reflect.DeepEqual(loaded.Summary, sampleInventoryResult().Summary) {
		t.Fatalf("loaded summary differs:\n%#v\nvs\n%#v", loaded.Summary, sampleInventoryResult().Summary)
	}

	wantFiles := []InventoryFile{
		{Path: "README.md", BlobHash: "readme-blob", Mode: "100644", Size: 40, Language: "Markdown", Category: "documentation"},
		{Path: "main.go", BlobHash: "main-blob", Mode: "100644", Size: 30, Language: "Go", Category: "source"},
	}
	if !reflect.DeepEqual(loaded.Files, wantFiles) {
		t.Fatalf("loaded files = %#v, want %#v", loaded.Files, wantFiles)
	}

	withoutFiles, err := db.GetProjectInventory(ctx, created.ID, false)
	if err != nil {
		t.Fatalf("GetProjectInventory(without files): %v", err)
	}
	if withoutFiles.Files != nil {
		t.Fatalf("inventory without files returned %d file rows", len(withoutFiles.Files))
	}
}

func TestLatestProjectInventory(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	project := createInventoryProject(t, ctx, db)

	_, err := db.LatestProjectInventory(ctx, project.ID, false)
	if err != nil {
		t.Fatalf("LatestProjectInventory(empty): %v", err)
	}

	first, err := db.CreateProjectInventory(
		ctx,
		project.ID,
		"commit-1",
		"commit-1",
		false,
		inventory.EngineVersion,
		sampleInventoryResult(),
	)
	if err != nil {
		t.Fatalf("create first inventory: %v", err)
	}
	latest, err := db.LatestProjectInventory(ctx, project.ID, false)
	if err != nil {
		t.Fatalf("LatestProjectInventory(one): %v", err)
	}
	if latest == nil || latest.ID != first.ID {
		t.Fatalf("latest inventory = %#v, want id %d", latest, first.ID)
	}

	second, err := db.CreateProjectInventory(
		ctx,
		project.ID,
		"commit-2",
		"commit-2",
		true,
		inventory.EngineVersion,
		sampleInventoryResult(),
	)
	if err != nil {
		t.Fatalf("create second inventory: %v", err)
	}
	latest, err = db.LatestProjectInventory(ctx, project.ID, true)
	if err != nil {
		t.Fatalf("LatestProjectInventory(two): %v", err)
	}
	if latest == nil || latest.ID != second.ID {
		t.Fatalf("latest inventory = %#v, want id %d", latest, second.ID)
	}
	if !latest.IsDirty {
		t.Fatal("latest inventory lost dirty flag")
	}
	if len(latest.Files) != 2 {
		t.Fatalf("latest inventory files = %d, want 2", len(latest.Files))
	}
}

func TestCreateProjectInventoryValidates(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	project := createInventoryProject(t, ctx, db)
	result := sampleInventoryResult()

	if _, err := db.CreateProjectInventory(ctx, 0, "c", "c", false, inventory.EngineVersion, result); err == nil {
		t.Fatal("zero project ID was accepted")
	}
	if _, err := db.CreateProjectInventory(ctx, project.ID, " ", "c", false, inventory.EngineVersion, result); err == nil {
		t.Fatal("empty commit was accepted")
	}
	if _, err := db.CreateProjectInventory(ctx, project.ID, "c", " ", false, inventory.EngineVersion, result); err == nil {
		t.Fatal("empty head commit was accepted")
	}
	if _, err := db.CreateProjectInventory(ctx, project.ID, "c", "c", false, 0, result); err == nil {
		t.Fatal("zero engine version was accepted")
	}

	badSummary := sampleInventoryResult()
	badSummary.Summary.TrackedFiles = -1
	if _, err := db.CreateProjectInventory(ctx, project.ID, "c", "c", false, inventory.EngineVersion, badSummary); err == nil {
		t.Fatal("negative tracked count was accepted")
	}
}

func createInventoryProject(t *testing.T, ctx context.Context, db *Database) Project {
	t.Helper()
	project, err := db.CreateProject(ctx, "Inventory Test", "/tmp/inventory-test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

func TestGetProjectInventoryMissing(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	loaded, err := db.GetProjectInventory(ctx, 999, false)
	if err != nil {
		t.Fatalf("GetProjectInventory(missing): %v", err)
	}
	if loaded != nil {
		t.Fatalf("GetProjectInventory(missing) = %#v, want nil", loaded)
	}

	project := createInventoryProject(t, ctx, db)
	latest, err := db.LatestProjectInventory(ctx, project.ID, false)
	if err != nil {
		t.Fatalf("LatestProjectInventory(never inventoried): %v", err)
	}
	if latest != nil {
		t.Fatalf("LatestProjectInventory(never inventoried) = %#v, want nil", latest)
	}
}

func TestCreateProjectInventoryTruncatedRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	project := createInventoryProject(t, ctx, db)

	result := sampleInventoryResult()
	result.Summary.Truncated = true
	created, err := db.CreateProjectInventory(
		ctx,
		project.ID,
		"commit-1",
		"commit-1",
		false,
		inventory.EngineVersion,
		result,
	)
	if err != nil {
		t.Fatalf("CreateProjectInventory(truncated): %v", err)
	}
	loaded, err := db.GetProjectInventory(ctx, created.ID, false)
	if err != nil {
		t.Fatalf("GetProjectInventory(truncated): %v", err)
	}
	if loaded == nil || !loaded.Truncated || !loaded.Summary.Truncated {
		t.Fatalf("truncated flag lost: %#v", loaded)
	}
}

func TestListInventoryFilesForNonexistentInventoryIsEmpty(t *testing.T) {
	db := openTestDatabase(t)
	files, err := db.listInventoryFiles(context.Background(), 999)
	if err != nil {
		t.Fatalf("listInventoryFiles(nonexistent): %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("listInventoryFiles(nonexistent) = %#v, want empty", files)
	}
}
