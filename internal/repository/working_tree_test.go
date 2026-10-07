package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkingTreeDeltaClean(t *testing.T) {
	root := newCommittedRepository(t)

	changes, err := WorkingTreeDelta(
		context.Background(),
		root,
	)
	if err != nil {
		t.Fatalf("working tree delta: %v", err)
	}

	if len(changes) != 0 {
		t.Fatalf(
			"clean repository returned %d changes: %#v",
			len(changes),
			changes,
		)
	}
}

func TestWorkingTreeDeltaUntracked(t *testing.T) {
	root := newCommittedRepository(t)

	writeRepositoryFile(
		t,
		root,
		"new file.txt",
		"untracked\n",
	)

	changes, err := WorkingTreeDelta(
		context.Background(),
		root,
	)
	if err != nil {
		t.Fatalf("working tree delta: %v", err)
	}

	change := findWorkingTreeChange(
		t,
		changes,
		"new file.txt",
	)

	if !change.Untracked {
		t.Fatal("untracked file not marked untracked")
	}
}

func TestWorkingTreeDeltaUnstagedModification(t *testing.T) {
	root := newCommittedRepository(t)

	if err := os.WriteFile(
		filepath.Join(root, "tracked.txt"),
		[]byte("modified\n"),
		0o644,
	); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}

	changes, err := WorkingTreeDelta(
		context.Background(),
		root,
	)
	if err != nil {
		t.Fatalf("working tree delta: %v", err)
	}

	change := findWorkingTreeChange(
		t,
		changes,
		"tracked.txt",
	)

	if change.IndexStatus != "." {
		t.Fatalf(
			"index status = %q, want .",
			change.IndexStatus,
		)
	}

	if change.WorkStatus != "M" {
		t.Fatalf(
			"work status = %q, want M",
			change.WorkStatus,
		)
	}

	if change.HeadBlob == "" {
		t.Fatal("modified file has empty HEAD blob")
	}
}

func TestWorkingTreeDeltaStagedModification(t *testing.T) {
	root := newCommittedRepository(t)

	if err := os.WriteFile(
		filepath.Join(root, "tracked.txt"),
		[]byte("staged\n"),
		0o644,
	); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}

	runGit(t, root, "add", "tracked.txt")

	changes, err := WorkingTreeDelta(
		context.Background(),
		root,
	)
	if err != nil {
		t.Fatalf("working tree delta: %v", err)
	}

	change := findWorkingTreeChange(
		t,
		changes,
		"tracked.txt",
	)

	if change.IndexStatus != "M" {
		t.Fatalf(
			"index status = %q, want M",
			change.IndexStatus,
		)
	}

	if change.WorkStatus != "." {
		t.Fatalf(
			"work status = %q, want .",
			change.WorkStatus,
		)
	}

	if change.HeadBlob == "" {
		t.Fatal("staged file has empty HEAD blob")
	}

	if change.IndexBlob == "" {
		t.Fatal("staged file has empty index blob")
	}

	if change.HeadBlob == change.IndexBlob {
		t.Fatal("staged modification retained HEAD blob")
	}
}

func TestWorkingTreeDeltaRename(t *testing.T) {
	root := newCommittedRepository(t)

	runGit(
		t,
		root,
		"mv",
		"tracked.txt",
		"renamed.txt",
	)

	changes, err := WorkingTreeDelta(
		context.Background(),
		root,
	)
	if err != nil {
		t.Fatalf("working tree delta: %v", err)
	}

	change := findWorkingTreeChange(
		t,
		changes,
		"renamed.txt",
	)

	if change.IndexStatus != "R" {
		t.Fatalf(
			"index status = %q, want R",
			change.IndexStatus,
		)
	}

	if change.OriginalPath != "tracked.txt" {
		t.Fatalf(
			"original path = %q, want tracked.txt",
			change.OriginalPath,
		)
	}
}

func findWorkingTreeChange(
	t *testing.T,
	changes []WorkingTreeChange,
	path string,
) WorkingTreeChange {
	t.Helper()

	for _, change := range changes {
		if change.Path == path {
			return change
		}
	}

	t.Fatalf(
		"working tree change missing %q: %#v",
		path,
		changes,
	)

	return WorkingTreeChange{}
}
