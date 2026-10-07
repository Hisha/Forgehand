package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkingTreeFingerprintDoesNotFollowUntrackedSymlink(t *testing.T) {
	root := newCommittedRepository(t)
	externalRoot := t.TempDir()
	externalPath := filepath.Join(externalRoot, "outside.txt")
	if err := os.WriteFile(externalPath, []byte("first\n"), 0o644); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	if err := os.Symlink(externalPath, filepath.Join(root, "outside-link")); err != nil {
		t.Fatalf("create untracked symlink: %v", err)
	}

	before, err := WorkingTreeFingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("fingerprint before external change: %v", err)
	}
	if err := os.WriteFile(externalPath, []byte("second\n"), 0o644); err != nil {
		t.Fatalf("modify external file: %v", err)
	}
	after, err := WorkingTreeFingerprint(context.Background(), root)
	if err != nil {
		t.Fatalf("fingerprint after external change: %v", err)
	}

	if after != before {
		t.Fatal("external symlink target content changed the repository fingerprint")
	}
}

func TestDiscardWorkingTreePreservesUntrackedNestedRepository(t *testing.T) {
	root := newCommittedRepository(t)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	runGit(t, nested, "init")
	nestedFile := filepath.Join(nested, "work.txt")
	const nestedContent = "nested repository work\n"
	if err := os.WriteFile(nestedFile, []byte(nestedContent), 0o644); err != nil {
		t.Fatalf("write nested repository content: %v", err)
	}

	if err := DiscardWorkingTree(context.Background(), root); err != nil {
		t.Fatalf("discard working tree: %v", err)
	}
	content, err := os.ReadFile(nestedFile)
	if err != nil {
		t.Fatalf("read nested repository content after discard: %v", err)
	}
	if string(content) != nestedContent {
		t.Fatalf("nested repository content = %q, want %q", content, nestedContent)
	}
}
