package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDiffTrees(t *testing.T) {
	root := newCommittedRepository(t)
	oldCommit := gitOutputForTest(t, root, "rev-parse", "HEAD")

	if err := os.WriteFile(
		filepath.Join(root, "tracked.txt"),
		[]byte("modified\n"),
		0o644,
	); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(root, "added.txt"),
		[]byte("added\n"),
		0o644,
	); err != nil {
		t.Fatalf("write added file: %v", err)
	}

	runGit(t, root, "add", ".")
	runGit(
		t,
		root,
		"-c",
		"user.name=Forgehand Test",
		"-c",
		"user.email=forgehand@example.invalid",
		"commit",
		"--quiet",
		"-m",
		"Modify and add files",
	)

	newCommit := gitOutputForTest(t, root, "rev-parse", "HEAD")

	changes, err := DiffTrees(
		context.Background(),
		root,
		oldCommit,
		newCommit,
	)
	if err != nil {
		t.Fatalf("diff trees: %v", err)
	}

	assertGitChange(
		t,
		changes,
		"M",
		"tracked.txt",
		"tracked.txt",
	)

	assertGitChange(
		t,
		changes,
		"A",
		"",
		"added.txt",
	)
}

func TestDiffTreesDetectsRename(t *testing.T) {
	root := newCommittedRepository(t)
	oldCommit := gitOutputForTest(t, root, "rev-parse", "HEAD")

	runGit(
		t,
		root,
		"mv",
		"tracked.txt",
		"renamed.txt",
	)

	runGit(
		t,
		root,
		"-c",
		"user.name=Forgehand Test",
		"-c",
		"user.email=forgehand@example.invalid",
		"commit",
		"--quiet",
		"-m",
		"Rename tracked file",
	)

	newCommit := gitOutputForTest(t, root, "rev-parse", "HEAD")

	changes, err := DiffTrees(
		context.Background(),
		root,
		oldCommit,
		newCommit,
	)
	if err != nil {
		t.Fatalf("diff trees: %v", err)
	}

	if len(changes) != 1 {
		t.Fatalf(
			"change count = %d, want 1",
			len(changes),
		)
	}

	change := changes[0]

	if change.Status != "R" {
		t.Fatalf(
			"status = %q, want R",
			change.Status,
		)
	}

	if change.OldPath != "tracked.txt" {
		t.Fatalf(
			"old path = %q, want tracked.txt",
			change.OldPath,
		)
	}

	if change.NewPath != "renamed.txt" {
		t.Fatalf(
			"new path = %q, want renamed.txt",
			change.NewPath,
		)
	}

	if change.Similarity != 100 {
		t.Fatalf(
			"similarity = %d, want 100",
			change.Similarity,
		)
	}
}

func assertGitChange(
	t *testing.T,
	changes []GitChange,
	status string,
	oldPath string,
	newPath string,
) {
	t.Helper()

	for _, change := range changes {
		if change.Status == status &&
			change.OldPath == oldPath &&
			change.NewPath == newPath {
			return
		}
	}

	t.Fatalf(
		"missing change status=%q old=%q new=%q: %#v",
		status,
		oldPath,
		newPath,
		changes,
	)
}
