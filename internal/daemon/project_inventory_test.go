package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/discovery"
	"github.com/Hisha/Forgehand/internal/inventory"
	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

func TestInventoryRegisteredProjectPersistsCommittedTree(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)

	writeProjectIntakeFile(t, root, "go.mod", "module example.invalid/inventory\n")
	writeProjectIntakeFile(t, root, "main.go", "package main\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add go module")

	project := createProjectInventoryFixture(t, ctx, db, root)

	result, err := inventoryRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatalf("inventoryRegisteredProject(): %v", err)
	}

	commit := strings.TrimSpace(string(projectIntakeGitOutput(t, root, "rev-parse", "HEAD")))
	if result.Inventory.CommitHash != commit {
		t.Fatalf("inventory commit = %q, want %q", result.Inventory.CommitHash, commit)
	}
	if result.Inventory.HeadCommit != commit {
		t.Fatalf("inventory head = %q, want %q", result.Inventory.HeadCommit, commit)
	}
	if result.Inventory.EngineVersion != inventory.EngineVersion {
		t.Fatalf("engine version = %d", result.Inventory.EngineVersion)
	}
	if result.Inventory.TrackedFiles != 3 {
		t.Fatalf("tracked files = %d, want 3", result.Inventory.TrackedFiles)
	}

	latest, err := db.LatestProjectInventory(ctx, project.ID, true)
	if err != nil {
		t.Fatalf("LatestProjectInventory(): %v", err)
	}
	if latest == nil || len(latest.Files) != 3 {
		t.Fatalf("latest inventory files = %#v, want 3", latest.Files)
	}
	for _, file := range latest.Files {
		if file.Path == "main.go" && (file.Language != "Go" || file.Category != "source") {
			t.Fatalf("main.go classified as %q/%q", file.Language, file.Category)
		}
	}
}

func TestInventoryRegisteredProjectRejectsUnbornHEAD(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newUnbornProjectIntakeRepository(t, true)
	project := createProjectInventoryFixture(t, ctx, db, root)

	if _, err := inventoryRegisteredProject(ctx, db, project.ID); err == nil {
		t.Fatal("inventory of unborn-HEAD repository succeeded")
	} else if !strings.Contains(err.Error(), "unborn HEAD") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInventoryRegisteredProjectIsolatesWorkingTree(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project := createProjectInventoryFixture(t, ctx, db, root)

	result, err := inventoryRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatalf("inventoryRegisteredProject(clean): %v", err)
	}
	if result.Inventory.TrackedFiles != 1 || result.Inventory.IsDirty {
		t.Fatalf("clean inventory = %#v", result.Inventory)
	}
	if len(result.Inventory.Files) != 0 {
		t.Fatalf("clean inventory unexpectedly includes files")
	}

	writeProjectIntakeFile(t, root, "untracked.py", "print('untracked')\n")
	second, err := inventoryRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatalf("inventoryRegisteredProject(dirty): %v", err)
	}
	if !second.Inventory.IsDirty {
		t.Fatal("dirty working tree not disclosed")
	}
	latest, err := db.LatestProjectInventory(ctx, project.ID, true)
	if err != nil {
		t.Fatalf("LatestProjectInventory(): %v", err)
	}
	if len(latest.Files) != 1 || latest.Files[0].Path != "tracked.txt" {
		t.Fatalf("inventory contaminated by working tree: %#v", latest.Files)
	}

	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "commit untracked source")
	third, err := inventoryRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatalf("inventoryRegisteredProject(committed): %v", err)
	}
	if third.Inventory.IsDirty || third.Inventory.TrackedFiles != 2 {
		t.Fatalf("post-commit inventory = %#v", third.Inventory)
	}
}

func TestInspectRegisteredProjectStatusCurrent(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project := createProjectInventoryFixture(t, ctx, db, root)

	if _, err := inventoryRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("inventoryRegisteredProject(): %v", err)
	}

	inspection, err := inspectRegisteredProject(ctx, db, project.ID, false)
	if err != nil {
		t.Fatalf("inspectRegisteredProject(): %v", err)
	}
	if inspection.Status != InspectionStatusCurrent {
		t.Fatalf("status = %q, want %q", inspection.Status, InspectionStatusCurrent)
	}
	if inspection.Head != inspection.Inventory.CommitHash {
		t.Fatalf("head %q != inventory commit %q", inspection.Head, inspection.Inventory.CommitHash)
	}
	if inspection.Dirty {
		t.Fatal("inspection reported dirty for clean repository")
	}
	if inspection.Inventory.Files != nil {
		t.Fatalf("inspection without evidence returned %d files", len(inspection.Inventory.Files))
	}
}

func TestInspectRegisteredProjectStatusStale(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project := createProjectInventoryFixture(t, ctx, db, root)

	if _, err := inventoryRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("inventoryRegisteredProject(): %v", err)
	}

	writeProjectIntakeFile(t, root, "main.go", "package main\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "advance head")
	head := strings.TrimSpace(string(projectIntakeGitOutput(t, root, "rev-parse", "HEAD")))

	inspection, err := inspectRegisteredProject(ctx, db, project.ID, true)
	if err != nil {
		t.Fatalf("inspectRegisteredProject(): %v", err)
	}
	if inspection.Status != InspectionStatusStale {
		t.Fatalf("status = %q, want %q", inspection.Status, InspectionStatusStale)
	}
	if inspection.Head != head {
		t.Fatalf("inspection head = %q, want %q", inspection.Head, head)
	}
	if inspection.Inventory.CommitHash == head {
		t.Fatal("inventory commit unexpectedly equals advanced HEAD")
	}
	if inspection.Inventory.Files == nil || len(inspection.Inventory.Files) != 1 {
		t.Fatalf("inspection with evidence returned %#v", inspection.Inventory.Files)
	}
}

func TestInspectRegisteredProjectDisclosesDirtyWorkingTree(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project := createProjectInventoryFixture(t, ctx, db, root)

	if _, err := inventoryRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("inventoryRegisteredProject(): %v", err)
	}

	writeProjectIntakeFile(t, root, "tracked.txt", "modified without commit\n")

	inspection, err := inspectRegisteredProject(ctx, db, project.ID, false)
	if err != nil {
		t.Fatalf("inspectRegisteredProject(): %v", err)
	}
	if inspection.Status != InspectionStatusCurrent {
		t.Fatalf("status = %q, want current", inspection.Status)
	}
	if !inspection.Dirty {
		t.Fatal("inspection failed to disclose dirty working tree")
	}
	if inspection.Inventory.IsDirty {
		t.Fatal("inventory record was mutated by live working-tree state")
	}
}

func TestInspectRegisteredProjectWithoutInventory(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project := createProjectInventoryFixture(t, ctx, db, root)

	_, err := inspectRegisteredProject(ctx, db, project.ID, false)
	if err == nil {
		t.Fatal("inspect without inventory succeeded")
	}
	if !strings.Contains(err.Error(), "no inventory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInspectionStatus(t *testing.T) {
	tests := []struct {
		name     string
		commit   string
		head     string
		hasHead  bool
		expected string
	}{
		{name: "matching head", commit: "abc", head: "abc", hasHead: true, expected: InspectionStatusCurrent},
		{name: "advanced head", commit: "abc", head: "def", hasHead: true, expected: InspectionStatusStale},
		{name: "no head", commit: "abc", head: "", hasHead: false, expected: InspectionStatusUnavailable},
	}
	for _, test := range tests {
		if got := inspectionStatus(test.commit, test.head, test.hasHead); got != test.expected {
			t.Fatalf("%s: inspectionStatus() = %q, want %q", test.name, got, test.expected)
		}
	}
}

func TestInventoryToIPC(t *testing.T) {
	record := state.ProjectInventory{
		ID:            7,
		ProjectID:     3,
		CommitHash:    "commit",
		HeadCommit:    "commit",
		IsDirty:       true,
		EngineVersion: 1,
		TrackedFiles:  2,
		Truncated:     false,
		Summary: inventory.Summary{
			TrackedFiles:      2,
			UnclassifiedFiles: 0,
			Languages: []discovery.LanguageCount{
				{Language: "Go", Count: 2},
			},
			BuildSystems: []discovery.BuildSystemIndicator{
				{Name: "Go modules", Evidence: discovery.EvidenceReference{Path: "go.mod"}},
			},
			Directories: []inventory.Directory{
				{Path: "cmd", Files: 1},
			},
			Categories: []inventory.CategoryCount{
				{Category: inventory.CategorySource, Count: 2},
			},
			Truncated: false,
		},
		Files: []state.InventoryFile{
			{Path: "main.go", BlobHash: "blob", Mode: "100644", Size: 10, Language: "Go", Category: "source"},
		},
	}

	want := ipc.Inventory{
		ID:            7,
		ProjectID:     3,
		CommitHash:    "commit",
		HeadCommit:    "commit",
		IsDirty:       true,
		EngineVersion: 1,
		TrackedFiles:  2,
		Truncated:     false,
		Summary: ipc.InventorySummary{
			TrackedFiles:      2,
			UnclassifiedFiles: 0,
			Languages:         []ipc.InventoryLanguage{{Language: "Go", Count: 2}},
			BuildSystems: []ipc.InventoryBuildSystem{
				{Name: "Go modules", Evidence: ipc.EvidenceReference{Path: "go.mod"}},
			},
			Directories: []ipc.InventoryDirectory{
				{Path: "cmd", Files: 1},
			},
			Categories: []ipc.InventoryCategory{
				{Category: "source", Count: 2},
			},
			Truncated: false,
		},
		Files: []ipc.InventoryFile{
			{Path: "main.go", BlobHash: "blob", Mode: "100644", Size: 10, Language: "Go", Category: "source"},
		},
	}

	got := inventoryToIPC(record)
	if got.ID != want.ID || got.ProjectID != want.ProjectID ||
		got.CommitHash != want.CommitHash || got.HeadCommit != want.HeadCommit ||
		got.IsDirty != want.IsDirty || got.EngineVersion != want.EngineVersion ||
		got.TrackedFiles != want.TrackedFiles || got.Truncated != want.Truncated {
		t.Fatalf("inventoryToIPC() header mismatch:\ngot=%#v\nwant=%#v", got, want)
	}
	if len(got.Summary.Languages) != 1 || got.Summary.Languages[0] != want.Summary.Languages[0] {
		t.Fatalf("inventoryToIPC() languages = %#v", got.Summary.Languages)
	}
	if len(got.Summary.BuildSystems) != 1 || got.Summary.BuildSystems[0] != want.Summary.BuildSystems[0] {
		t.Fatalf("inventoryToIPC() build systems = %#v", got.Summary.BuildSystems)
	}
	if len(got.Summary.Directories) != 1 || got.Summary.Directories[0] != want.Summary.Directories[0] {
		t.Fatalf("inventoryToIPC() directories = %#v", got.Summary.Directories)
	}
	if len(got.Summary.Categories) != 1 || got.Summary.Categories[0] != want.Summary.Categories[0] {
		t.Fatalf("inventoryToIPC() categories = %#v", got.Summary.Categories)
	}
	if len(got.Files) != 1 || got.Files[0] != want.Files[0] {
		t.Fatalf("inventoryToIPC() files = %#v", got.Files)
	}
	if got.ObservedAt == "" {
		t.Fatal("inventoryToIPC() omitted observed time")
	}
}

func createProjectInventoryFixture(
	t *testing.T,
	ctx context.Context,
	db *state.Database,
	root string,
) state.Project {
	t.Helper()
	project, err := db.CreateProject(ctx, "Inventory Test", root)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}
