package task

import (
	"context"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/workspace"
)

func TestRenderProposalDiffModification(t *testing.T) {
	original := map[string]string{
		"calculator.go": "package calculator\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n",
	}
	changes := []workspace.Change{
		{Op: "write", Path: "calculator.go", Content: "package calculator\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\nfunc Subtract(a, b int) int {\n\treturn a - b\n}\n"},
	}

	diff, err := RenderProposalDiff(context.Background(), changes, func(path string) (string, bool, error) {
		content, ok := original[path]
		return content, ok, nil
	})
	if err != nil {
		t.Fatalf("RenderProposalDiff: %v", err)
	}
	if !strings.Contains(diff, "diff --git a/calculator.go b/calculator.go") {
		t.Fatalf("diff missing git header:\n%s", diff)
	}
	if !strings.Contains(diff, "--- a/calculator.go") {
		t.Fatalf("diff missing --- header:\n%s", diff)
	}
	if !strings.Contains(diff, "+++ b/calculator.go") {
		t.Fatalf("diff missing +++ header:\n%s", diff)
	}
	if !strings.Contains(diff, "+func Subtract(a, b int) int {") {
		t.Fatalf("diff missing added line:\n%s", diff)
	}
}

func TestRenderProposalDiffCreation(t *testing.T) {
	changes := []workspace.Change{
		{Op: "write", Path: "new.go", Content: "package calculator\n"},
	}
	diff, err := RenderProposalDiff(context.Background(), changes, func(path string) (string, bool, error) {
		return "", false, nil
	})
	if err != nil {
		t.Fatalf("RenderProposalDiff: %v", err)
	}
	if !strings.Contains(diff, "+++ b/new.go") {
		t.Fatalf("creation diff missing +++ header:\n%s", diff)
	}
	if !strings.Contains(diff, "+package calculator") {
		t.Fatalf("creation diff missing added content:\n%s", diff)
	}
}

func TestRenderProposalDiffDeletion(t *testing.T) {
	changes := []workspace.Change{
		{Op: "delete", Path: "old.go", Content: ""},
	}
	diff, err := RenderProposalDiff(context.Background(), changes, func(path string) (string, bool, error) {
		return "package old\n", true, nil
	})
	if err != nil {
		t.Fatalf("RenderProposalDiff: %v", err)
	}
	if !strings.Contains(diff, "--- a/old.go") {
		t.Fatalf("deletion diff missing --- header:\n%s", diff)
	}
	if !strings.Contains(diff, "+++ /dev/null") {
		t.Fatalf("deletion diff missing /dev/null target:\n%s", diff)
	}
	if !strings.Contains(diff, "-package old") {
		t.Fatalf("deletion diff missing removed content:\n%s", diff)
	}
}

func TestRenderProposalDiffMultipleFiles(t *testing.T) {
	original := map[string]string{
		"a.txt": "one\n",
		"b.txt": "two\n",
	}
	changes := []workspace.Change{
		{Op: "write", Path: "a.txt", Content: "one\ntwo\n"},
		{Op: "delete", Path: "b.txt"},
	}
	diff, err := RenderProposalDiff(context.Background(), changes, func(path string) (string, bool, error) {
		content, ok := original[path]
		return content, ok, nil
	})
	if err != nil {
		t.Fatalf("RenderProposalDiff: %v", err)
	}
	if !strings.Contains(diff, "a/a.txt") || !strings.Contains(diff, "b/b.txt") {
		t.Fatalf("multi-file diff missing one of the files:\n%s", diff)
	}
}

func TestRenderProposalDiffEmpty(t *testing.T) {
	diff, err := RenderProposalDiff(context.Background(), nil, func(path string) (string, bool, error) {
		return "", false, nil
	})
	if err != nil {
		t.Fatalf("RenderProposalDiff: %v", err)
	}
	if diff != "" {
		t.Fatalf("empty proposal produced diff: %q", diff)
	}
}

func TestRenderProposalDiffDeleteMissingFails(t *testing.T) {
	changes := []workspace.Change{
		{Op: "delete", Path: "missing.go"},
	}
	_, err := RenderProposalDiff(context.Background(), changes, func(path string) (string, bool, error) {
		return "", false, nil
	})
	if err == nil {
		t.Fatal("expected error deleting a missing file, got nil")
	}
}
