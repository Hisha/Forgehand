package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestListTree(t *testing.T) {
	root := newCommittedRepository(t)

	writeRepositoryFile(t, root, "nested/example.go", "package example\n")
	writeRepositoryFile(t, root, "file with spaces.txt", "hello\n")

	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "add tree files")

	files, err := ListTree(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatalf("list tree: %v", err)
	}

	byPath := make(map[string]GitFile, len(files))
	for _, file := range files {
		byPath[file.Path] = file
	}

	for _, path := range []string{
		"tracked.txt",
		"file with spaces.txt",
		"nested/example.go",
	} {
		file, ok := byPath[path]
		if !ok {
			t.Fatalf("tree missing %q", path)
		}

		if file.BlobHash == "" {
			t.Fatalf("%q has empty blob hash", path)
		}

		if file.Mode == "" {
			t.Fatalf("%q has empty mode", path)
		}

		if file.Size < 0 {
			t.Fatalf("%q has invalid size %d", path, file.Size)
		}
	}
}

func TestListTreeUsesRequestedCommit(t *testing.T) {
	root := newCommittedRepository(t)

	firstCommit := gitOutputForTest(t, root, "rev-parse", "HEAD")

	writeRepositoryFile(t, root, "later.txt", "later\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "later")

	oldFiles, err := ListTree(context.Background(), root, firstCommit)
	if err != nil {
		t.Fatalf("list old tree: %v", err)
	}

	newFiles, err := ListTree(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatalf("list new tree: %v", err)
	}

	if containsGitPath(oldFiles, "later.txt") {
		t.Fatal("old commit unexpectedly contains later.txt")
	}

	if !containsGitPath(newFiles, "later.txt") {
		t.Fatal("new commit missing later.txt")
	}
}

func TestListTreeRejectsEmptyCommit(t *testing.T) {
	root := newCommittedRepository(t)

	if _, err := ListTree(context.Background(), root, ""); err == nil {
		t.Fatal("empty commit unexpectedly accepted")
	}
}

func writeRepositoryFile(
	t *testing.T,
	root string,
	relativePath string,
	content string,
) {
	t.Helper()

	path := filepath.Join(root, relativePath)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent directory: %v", err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relativePath, err)
	}
}

func containsGitPath(files []GitFile, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}

	return false
}

func gitOutputForTest(
	t *testing.T,
	root string,
	args ...string,
) string {
	t.Helper()

	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}

	return strings.TrimSpace(string(output))
}
