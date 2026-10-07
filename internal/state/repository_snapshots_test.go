package state

import (
	"context"
	"testing"
)

func TestCreateAndListRepositorySnapshots(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	project, err := db.CreateProject(
		ctx,
		"Forgehand",
		"/tmp/forgehand",
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	first, err := db.CreateRepositorySnapshot(
		ctx,
		RepositorySnapshot{
			ProjectID:      project.ID,
			HeadCommit:     "abc123",
			Branch:         "main",
			Dirty:          false,
			TrackedFiles:   10,
			UntrackedFiles: 0,
		},
	)
	if err != nil {
		t.Fatalf("create first snapshot: %v", err)
	}

	second, err := db.CreateRepositorySnapshot(
		ctx,
		RepositorySnapshot{
			ProjectID:      project.ID,
			HeadCommit:     "abc123",
			Branch:         "main",
			Dirty:          true,
			TrackedFiles:   10,
			UntrackedFiles: 2,
		},
	)
	if err != nil {
		t.Fatalf("create second snapshot: %v", err)
	}

	if first.ID == second.ID {
		t.Fatal("snapshots unexpectedly have the same ID")
	}

	snapshots, err := db.ListRepositorySnapshots(ctx, project.ID)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}

	if len(snapshots) != 2 {
		t.Fatalf("snapshot count = %d, want 2", len(snapshots))
	}

	if snapshots[0].HeadCommit != "abc123" {
		t.Fatalf(
			"first HEAD = %q, want abc123",
			snapshots[0].HeadCommit,
		)
	}

	if snapshots[0].Dirty {
		t.Fatal("first snapshot unexpectedly dirty")
	}

	if !snapshots[1].Dirty {
		t.Fatal("second snapshot unexpectedly clean")
	}

	if snapshots[1].UntrackedFiles != 2 {
		t.Fatalf(
			"second untracked files = %d, want 2",
			snapshots[1].UntrackedFiles,
		)
	}
}

func TestRepositorySnapshotAllowsNoCommit(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	project, err := db.CreateProject(
		ctx,
		"Empty",
		"/tmp/empty",
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	snapshot, err := db.CreateRepositorySnapshot(
		ctx,
		RepositorySnapshot{
			ProjectID: project.ID,
			Branch:    "main",
		},
	)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}

	if snapshot.HeadCommit != "" {
		t.Fatalf(
			"HEAD commit = %q, want empty",
			snapshot.HeadCommit,
		)
	}
}

func TestRepositorySnapshotRejectsInvalidCounts(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	if _, err := db.CreateRepositorySnapshot(
		ctx,
		RepositorySnapshot{
			ProjectID:    1,
			TrackedFiles: -1,
		},
	); err == nil {
		t.Fatal("negative tracked count unexpectedly succeeded")
	}

	if _, err := db.CreateRepositorySnapshot(
		ctx,
		RepositorySnapshot{
			ProjectID:      1,
			UntrackedFiles: -1,
		},
	); err == nil {
		t.Fatal("negative untracked count unexpectedly succeeded")
	}
}

func TestRepositorySnapshotRejectsMissingProject(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	_, err := db.CreateRepositorySnapshot(
		ctx,
		RepositorySnapshot{
			ProjectID:      999999,
			HeadCommit:     "abc123",
			Branch:         "main",
			TrackedFiles:   1,
			UntrackedFiles: 0,
		},
	)

	if err == nil {
		t.Fatal("snapshot for nonexistent project unexpectedly succeeded")
	}
}
