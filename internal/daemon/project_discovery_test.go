package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverRegisteredProjectPersistsExactHEAD(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "go.mod", "module example.invalid/project\n")
	writeProjectIntakeFile(t, root, "main.go", "package main\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add Go project")
	commit := strings.TrimSpace(string(projectIntakeGitOutput(t, root, "rev-parse", "HEAD")))
	project, err := db.CreateProject(ctx, "Discovery Test", root)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	result, err := discoverRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatalf("discover registered project: %v", err)
	}
	if result.Observation.CommitHash != commit || result.Observation.Summary.CommitHash != commit {
		t.Fatalf("discovered commit = %q/%q, want %q", result.Observation.CommitHash, result.Observation.Summary.CommitHash, commit)
	}
	if result.Observation.Summary.TrackedFiles != 3 {
		t.Fatalf("tracked files = %d, want 3", result.Observation.Summary.TrackedFiles)
	}

	observations, err := db.ListDiscoveryObservations(ctx, project.ID)
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(observations) != 1 || observations[0].ID != result.Observation.ID {
		t.Fatalf("persisted observations = %#v", observations)
	}
}

func TestDiscoverRegisteredProjectRetainsHistoricalObservations(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project, err := db.CreateProject(ctx, "History Test", root)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	first, err := discoverRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatalf("first discovery: %v", err)
	}

	writeProjectIntakeFile(t, root, "later.py", "print('later')\n")
	runProjectIntakeGit(t, root, "add", "later.py")
	runProjectIntakeGit(t, root, "commit", "-m", "later commit")
	second, err := discoverRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	if first.Observation.CommitHash == second.Observation.CommitHash {
		t.Fatal("discoveries unexpectedly reference the same commit")
	}

	observations, err := db.ListDiscoveryObservations(ctx, project.ID)
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(observations) != 2 {
		t.Fatalf("observation count = %d, want 2", len(observations))
	}
}

func TestDiscoverRegisteredProjectRejectsUnbornHEAD(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newUnbornProjectIntakeRepository(t, true)
	project, err := db.CreateProject(ctx, "Unborn Test", root)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	_, err = discoverRegisteredProject(ctx, db, project.ID)
	if err == nil || !strings.Contains(err.Error(), "unborn HEAD") {
		t.Fatalf("unborn discovery error = %v, want unborn HEAD", err)
	}
	observations, listErr := db.ListDiscoveryObservations(ctx, project.ID)
	if listErr != nil {
		t.Fatalf("list observations: %v", listErr)
	}
	if len(observations) != 0 {
		t.Fatalf("unborn discovery persisted %d observations", len(observations))
	}
}

func TestDiscoverRegisteredProjectRejectsMissingProject(t *testing.T) {
	db := openProjectIntakeTestDatabase(t)
	_, err := discoverRegisteredProject(context.Background(), db, 999999)
	if err == nil || !strings.Contains(err.Error(), "get registered project") {
		t.Fatalf("missing project discovery error = %v", err)
	}
}

func TestDiscoverRegisteredProjectDoesNotUseWorkingTree(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project, err := db.CreateProject(ctx, "Dirty Test", root)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "tracked.txt")); err != nil {
		t.Fatalf("remove committed file from working tree: %v", err)
	}
	writeProjectIntakeFile(t, root, "untracked.go", "package untracked\n")

	result, err := discoverRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatalf("discover dirty registered project: %v", err)
	}
	if result.Observation.Summary.TrackedFiles != 1 || result.Observation.Summary.UnclassifiedFiles != 1 {
		t.Fatalf("dirty working tree contaminated summary: %#v", result.Observation.Summary)
	}
}
