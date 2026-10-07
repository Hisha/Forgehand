package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadBlobPreservesHistoricalContent(t *testing.T) {
	root := newCommittedRepository(t)

	files, err := ListTree(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatalf("list initial tree: %v", err)
	}

	originalBlob := blobForPath(t, files, "tracked.txt")

	path := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(
		path,
		[]byte("changed\n"),
		0o644,
	); err != nil {
		t.Fatalf("change tracked file: %v", err)
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
		"Change tracked file",
	)

	currentFiles, err := ListTree(context.Background(), root, "HEAD")
	if err != nil {
		t.Fatalf("list current tree: %v", err)
	}

	currentBlob := blobForPath(t, currentFiles, "tracked.txt")

	if currentBlob == originalBlob {
		t.Fatal("changed file unexpectedly retained original blob hash")
	}

	originalContent, err := ReadBlob(
		context.Background(),
		root,
		originalBlob,
	)
	if err != nil {
		t.Fatalf("read original blob: %v", err)
	}

	if string(originalContent) != "original\n" {
		t.Fatalf(
			"original blob content = %q, want %q",
			string(originalContent),
			"original\n",
		)
	}

	currentContent, err := ReadBlob(
		context.Background(),
		root,
		currentBlob,
	)
	if err != nil {
		t.Fatalf("read current blob: %v", err)
	}

	if string(currentContent) != "changed\n" {
		t.Fatalf(
			"current blob content = %q, want %q",
			string(currentContent),
			"changed\n",
		)
	}
}

func TestReadBlobRejectsEmptyHash(t *testing.T) {
	root := newCommittedRepository(t)

	if _, err := ReadBlob(
		context.Background(),
		root,
		"",
	); err == nil {
		t.Fatal("empty blob hash unexpectedly accepted")
	}
}

func TestReadBlobRejectsNonBlobObject(t *testing.T) {
	root := newCommittedRepository(t)

	commitHash := gitOutputForTest(
		t,
		root,
		"rev-parse",
		"HEAD",
	)

	if _, err := ReadBlob(
		context.Background(),
		root,
		commitHash,
	); err == nil {
		t.Fatal("commit object unexpectedly accepted as blob")
	}
}

func blobForPath(
	t *testing.T,
	files []GitFile,
	path string,
) string {
	t.Helper()

	for _, file := range files {
		if file.Path == path {
			if file.BlobHash == "" {
				t.Fatalf("%q has empty blob hash", path)
			}

			return file.BlobHash
		}
	}

	t.Fatalf("tree missing %q", path)
	return ""
}
