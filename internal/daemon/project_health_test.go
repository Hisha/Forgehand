package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectHealthIsUnobservedWithoutStoredObservations(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project := createProjectInventoryFixture(t, ctx, db, root)

	health := inspectProjectHealth(ctx, db, project)
	if health.Status != ProjectHealthUnobserved {
		t.Fatalf("status = %q, want %q", health.Status, ProjectHealthUnobserved)
	}
	if health.ProjectID != project.ID {
		t.Fatalf("project id = %d, want %d", health.ProjectID, project.ID)
	}
	if health.InventoryCommit != "" || health.AnalysisCommit != "" {
		t.Fatalf("health reported stored observations: %#v", health)
	}
}

func TestProjectHealthIsCurrentWhenTheInventoryMatchesHead(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project := createProjectInventoryFixture(t, ctx, db, root)

	if _, err := inventoryRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("inventoryRegisteredProject(): %v", err)
	}

	health := inspectProjectHealth(ctx, db, project)
	if health.Status != ProjectHealthCurrent {
		t.Fatalf("status = %q, want %q (%#v)", health.Status, ProjectHealthCurrent, health)
	}
	if health.Head == "" {
		t.Fatal("health did not report the live revision")
	}
	if health.InventoryCommit != health.Head {
		t.Fatalf("inventory commit = %q, head = %q", health.InventoryCommit, health.Head)
	}
	if health.Dirty {
		t.Fatal("clean repository was reported as dirty")
	}
}

func TestProjectHealthIsStaleWhenHeadMovedOn(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project := createProjectInventoryFixture(t, ctx, db, root)

	if _, err := inventoryRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("inventoryRegisteredProject(): %v", err)
	}

	writeProjectIntakeFile(t, root, "tracked.txt", "changed\n")
	runProjectIntakeGit(t, root, "add", "tracked.txt")
	runProjectIntakeGit(t, root, "commit", "-m", "second")

	health := inspectProjectHealth(ctx, db, project)
	if health.Status != ProjectHealthStale {
		t.Fatalf("status = %q, want %q (%#v)", health.Status, ProjectHealthStale, health)
	}
	if health.InventoryCommit == health.Head {
		t.Fatalf("stale health reported identical revisions: %#v", health)
	}
}

func TestProjectHealthIsUnavailableForAnUnreadableRepository(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := filepath.Join(t.TempDir(), "missing")
	project := createProjectInventoryFixture(t, ctx, db, root)

	health := inspectProjectHealth(ctx, db, project)
	if health.Status != ProjectHealthUnavailable {
		t.Fatalf("status = %q, want %q (%#v)", health.Status, ProjectHealthUnavailable, health)
	}
}

func TestProjectHealthIsUnavailableForANonRepositoryDirectory(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	project := createProjectInventoryFixture(t, ctx, db, root)

	health := inspectProjectHealth(ctx, db, project)
	if health.Status != ProjectHealthUnavailable {
		t.Fatalf("status = %q, want %q (%#v)", health.Status, ProjectHealthUnavailable, health)
	}
}

func TestProjectHealthListReportsEveryProject(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	project := createProjectInventoryFixture(t, ctx, db, root)

	missing := createProjectInventoryFixture(t, ctx, db, filepath.Join(t.TempDir(), "missing"))

	health, err := inspectProjectHealthList(ctx, db)
	if err != nil {
		t.Fatalf("inspectProjectHealthList(): %v", err)
	}
	if len(health) != 2 {
		t.Fatalf("health entries = %d, want 2", len(health))
	}

	byProject := map[int64]string{}
	for _, entry := range health {
		byProject[entry.ProjectID] = entry.Status
	}
	if byProject[project.ID] != ProjectHealthUnobserved {
		t.Fatalf("project %d status = %q", project.ID, byProject[project.ID])
	}
	if byProject[missing.ID] != ProjectHealthUnavailable {
		t.Fatalf("project %d status = %q", missing.ID, byProject[missing.ID])
	}
}
