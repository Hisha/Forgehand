package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDiscoverRepositoryRoot(t *testing.T) {
	ctx := context.Background()

	root := filepath.Join(t.TempDir(), "Forgehand")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create repository directory: %v", err)
	}

	initGitRepository(t, root)

	repository, err := Discover(ctx, root)
	if err != nil {
		t.Fatalf("discover repository: %v", err)
	}

	if repository.Name != "Forgehand" {
		t.Fatalf(
			"repository name = %q, want %q",
			repository.Name,
			"Forgehand",
		)
	}

	if repository.RootPath != root {
		t.Fatalf(
			"repository root = %q, want %q",
			repository.RootPath,
			root,
		)
	}
}

func TestDiscoverRepositoryFromSubdirectory(t *testing.T) {
	ctx := context.Background()

	root := filepath.Join(t.TempDir(), "Forgehand")
	nested := filepath.Join(root, "internal", "repository")

	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}

	initGitRepository(t, root)

	repository, err := Discover(ctx, nested)
	if err != nil {
		t.Fatalf("discover repository: %v", err)
	}

	if repository.RootPath != root {
		t.Fatalf(
			"repository root = %q, want %q",
			repository.RootPath,
			root,
		)
	}
}

func TestDiscoverRejectsNonRepository(t *testing.T) {
	ctx := context.Background()

	path := t.TempDir()

	if _, err := Discover(ctx, path); err == nil {
		t.Fatal("non-Git directory unexpectedly discovered")
	}
}

func TestDiscoverRejectsEmptyPath(t *testing.T) {
	ctx := context.Background()

	if _, err := Discover(ctx, ""); err == nil {
		t.Fatal("empty path unexpectedly discovered")
	}
}

func initGitRepository(t *testing.T, path string) {
	t.Helper()

	command := exec.Command("git", "init", "--quiet", path)

	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf(
			"initialize Git repository: %v: %s",
			err,
			string(output),
		)
	}
}
