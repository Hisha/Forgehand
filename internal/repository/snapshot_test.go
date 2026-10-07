package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestObserveCleanRepository(t *testing.T) {
	ctx := context.Background()
	root := newCommittedRepository(t)

	snapshot, err := Observe(ctx, root)
	if err != nil {
		t.Fatalf("observe repository: %v", err)
	}

	if snapshot.HeadCommit == "" {
		t.Fatal("HEAD commit is empty")
	}

	if snapshot.Branch == "" {
		t.Fatal("branch is empty")
	}

	if snapshot.Detached {
		t.Fatal("repository unexpectedly reported detached HEAD")
	}

	if snapshot.Dirty {
		t.Fatal("clean repository reported dirty")
	}

	if snapshot.TrackedFiles != 1 {
		t.Fatalf(
			"tracked files = %d, want 1",
			snapshot.TrackedFiles,
		)
	}

	if snapshot.UntrackedFiles != 0 {
		t.Fatalf(
			"untracked files = %d, want 0",
			snapshot.UntrackedFiles,
		)
	}
}

func TestObserveDirtyRepository(t *testing.T) {
	ctx := context.Background()
	root := newCommittedRepository(t)

	trackedPath := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(
		trackedPath,
		[]byte("changed\n"),
		0o644,
	); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}

	untrackedPath := filepath.Join(root, "untracked.txt")
	if err := os.WriteFile(
		untrackedPath,
		[]byte("new\n"),
		0o644,
	); err != nil {
		t.Fatalf("create untracked file: %v", err)
	}

	snapshot, err := Observe(ctx, root)
	if err != nil {
		t.Fatalf("observe repository: %v", err)
	}

	if !snapshot.Dirty {
		t.Fatal("dirty repository reported clean")
	}

	if snapshot.TrackedFiles != 1 {
		t.Fatalf(
			"tracked files = %d, want 1",
			snapshot.TrackedFiles,
		)
	}

	if snapshot.UntrackedFiles != 1 {
		t.Fatalf(
			"untracked files = %d, want 1",
			snapshot.UntrackedFiles,
		)
	}
}

func TestObserveDetachedHead(t *testing.T) {
	ctx := context.Background()
	root := newCommittedRepository(t)

	runGit(t, root, "checkout", "--quiet", "--detach")

	snapshot, err := Observe(ctx, root)
	if err != nil {
		t.Fatalf("observe repository: %v", err)
	}

	if snapshot.HeadCommit == "" {
		t.Fatal("HEAD commit is empty")
	}

	if snapshot.Branch != "" {
		t.Fatalf(
			"branch = %q, want empty",
			snapshot.Branch,
		)
	}

	if !snapshot.Detached {
		t.Fatal("detached HEAD not detected")
	}
}

func TestObserveRepositoryWithoutCommit(t *testing.T) {
	ctx := context.Background()

	root := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create repository directory: %v", err)
	}

	initGitRepository(t, root)

	snapshot, err := Observe(ctx, root)
	if err != nil {
		t.Fatalf("observe repository: %v", err)
	}

	if snapshot.HeadCommit != "" {
		t.Fatalf(
			"HEAD commit = %q, want empty",
			snapshot.HeadCommit,
		)
	}

	if snapshot.Detached {
		t.Fatal("unborn branch reported detached")
	}
}

func newCommittedRepository(t *testing.T) string {
	t.Helper()

	root := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create repository directory: %v", err)
	}

	initGitRepository(t, root)

	path := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(
		path,
		[]byte("original\n"),
		0o644,
	); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	runGit(t, root, "add", "tracked.txt")
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
		"Initial commit",
	)

	return root
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()

	commandArgs := append([]string{"-C", root}, args...)
	command := exec.Command("git", commandArgs...)

	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf(
			"git %v: %v: %s",
			args,
			err,
			string(output),
		)
	}
}
