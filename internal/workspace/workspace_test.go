package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newTestRepository creates a Git repository with a single committed file and
// returns its root. Tests are skipped when Git is unavailable.
func newTestRepository(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}

	root := t.TempDir()
	runGitTest(t, root, "init", "-q")
	runGitTest(t, root, "config", "user.email", "forgehand-test@example.invalid")
	runGitTest(t, root, "config", "user.name", "Forgehand Test")
	writeFileTest(t, root, "tracked.txt", "baseline\n")
	runGitTest(t, root, "add", "tracked.txt")
	runGitTest(t, root, "commit", "-q", "-m", "baseline")

	return root
}

func runGitTest(t *testing.T, root string, args ...string) string {
	t.Helper()

	commandArgs := append([]string{"-C", root}, args...)
	cmd := exec.Command("git", commandArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeFileTest(t *testing.T, root, path, content string) string {
	t.Helper()

	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return full
}

// createWorkspace provisions a worktree for the repository at its HEAD.
func createWorkspace(t *testing.T, repoRoot, worktreePath string) *Workspace {
	t.Helper()

	base := runGitTest(t, repoRoot, "rev-parse", "HEAD")
	if err := Ensure(context.Background(), repoRoot, base, worktreePath); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	ws, err := Open(worktreePath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ws
}

func TestEnsureCreatesDetachedWorktreeAtBaseCommit(t *testing.T) {
	repoRoot := newTestRepository(t)
	base := runGitTest(t, repoRoot, "rev-parse", "HEAD")
	worktreePath := filepath.Join(t.TempDir(), "ws")

	if err := Ensure(context.Background(), repoRoot, base, worktreePath); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !Exists(worktreePath) {
		t.Fatal("worktree directory was not created")
	}

	head := runGitTest(t, worktreePath, "rev-parse", "HEAD")
	if head != base {
		t.Fatalf("worktree HEAD = %s, want %s", head, base)
	}
}

func TestEnsureReusesCleanWorktreeAndRefusesDirtyOne(t *testing.T) {
	repoRoot := newTestRepository(t)
	base := runGitTest(t, repoRoot, "rev-parse", "HEAD")
	worktreePath := filepath.Join(t.TempDir(), "ws")

	if err := Ensure(context.Background(), repoRoot, base, worktreePath); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	// A second Ensure on a clean worktree at the same commit is idempotent.
	if err := Ensure(context.Background(), repoRoot, base, worktreePath); err != nil {
		t.Fatalf("idempotent Ensure: %v", err)
	}

	// Dirty the worktree: Ensure must refuse rather than discard the change.
	writeFileTest(t, worktreePath, "tracked.txt", "modified\n")
	if err := Ensure(context.Background(), repoRoot, base, worktreePath); err == nil {
		t.Fatal("Ensure accepted a dirty worktree; applied work could be lost")
	}
}

func TestReadRejectsPathTraversalAndReservedPaths(t *testing.T) {
	repoRoot := newTestRepository(t)
	ws := createWorkspace(t, repoRoot, filepath.Join(t.TempDir(), "ws"))

	// A secret outside the worktree that traversal would otherwise reach.
	writeFileTest(t, repoRoot, "outside-secret.txt", "top secret\n")

	cases := []struct {
		name string
		path string
		want error
	}{
		{"parent traversal", "../outside-secret.txt", ErrPathEscape},
		{"deep traversal", "a/../../outside-secret.txt", ErrPathEscape},
		{"absolute path", "/etc/hostname", ErrPathEscape},
		{"git metadata", ".git/config", ErrReservedPath},
		{"empty path", "", ErrEmptyPath},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ws.Read(tc.path, 1<<20)
			if err == nil {
				t.Fatalf("Read(%q) succeeded, want error", tc.path)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Read(%q) error = %v, want %v", tc.path, err, tc.want)
			}
		})
	}
}

func TestReadRejectsSymlinkEscape(t *testing.T) {
	repoRoot := newTestRepository(t)
	ws := createWorkspace(t, repoRoot, filepath.Join(t.TempDir(), "ws"))

	outside := writeFileTest(t, t.TempDir(), "secret.txt", "cannot read me\n")
	if err := os.Symlink(outside, filepath.Join(ws.Root(), "escape")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	_, err := ws.Read("escape", 1<<20)
	if err == nil {
		t.Fatal("Read followed a symlink out of the workspace")
	}
	if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("Read error = %v, want ErrPathEscape", err)
	}
}

func TestApplyRejectsUnauthorizedChangesBeforeWriting(t *testing.T) {
	repoRoot := newTestRepository(t)
	ws := createWorkspace(t, repoRoot, filepath.Join(t.TempDir(), "ws"))

	writeFileTest(t, ws.Root(), "existing.txt", "original\n")

	cases := []struct {
		name   string
		change Change
		want   error
	}{
		{
			name:   "escaping path",
			change: Change{Op: "write", Path: "../escape.txt", Content: "x"},
			want:   ErrPathEscape,
		},
		{
			name:   "reserved git path",
			change: Change{Op: "write", Path: ".git/config", Content: "x"},
			want:   ErrReservedPath,
		},
		{
			name:   "unsupported operation",
			change: Change{Op: "chmod", Path: "existing.txt"},
			want:   nil, // message-based; still an error
		},
		{
			name:   "delete missing file",
			change: Change{Op: "delete", Path: "missing.txt"},
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ws.Apply([]Change{tc.change})
			if err == nil {
				t.Fatalf("Apply(%#v) succeeded, want error", tc.change)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("Apply error = %v, want %v", err, tc.want)
			}
		})
	}

	// The escaping write must not have created a file outside the workspace.
	if Exists(filepath.Join(filepath.Dir(ws.Root()), "escape.txt")) {
		t.Fatal("Apply wrote outside the workspace root")
	}
	if data, err := os.ReadFile(filepath.Join(ws.Root(), "existing.txt")); err != nil ||
		string(data) != "original\n" {
		t.Fatalf("existing file was mutated by a rejected apply: %q, %v", data, err)
	}
}

func TestApplyRejectsSymlinkTarget(t *testing.T) {
	repoRoot := newTestRepository(t)
	ws := createWorkspace(t, repoRoot, filepath.Join(t.TempDir(), "ws"))

	outside := writeFileTest(t, t.TempDir(), "target.txt", "outside content\n")
	if err := os.Symlink(outside, filepath.Join(ws.Root(), "link")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	err := ws.Apply([]Change{{Op: "write", Path: "link", Content: "overwrite\n"}})
	if err == nil {
		t.Fatal("Apply wrote through a symlink")
	}
	if !errors.Is(err, ErrReservedPath) {
		t.Fatalf("Apply error = %v, want ErrReservedPath", err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "outside content\n" {
		t.Fatalf("symlink target outside the workspace was modified: %q", data)
	}
}

func TestApplyWritesChangesAndProducesDiff(t *testing.T) {
	repoRoot := newTestRepository(t)
	ws := createWorkspace(t, repoRoot, filepath.Join(t.TempDir(), "ws"))

	changes := []Change{
		{Op: "write", Path: "tracked.txt", Content: "updated\n"},
		{Op: "write", Path: "nested/new.txt", Content: "new file\n"},
	}
	if err := ws.Apply(changes); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	clean, err := ws.IsClean(context.Background())
	if err != nil {
		t.Fatalf("IsClean: %v", err)
	}
	if clean {
		t.Fatal("workspace reports clean after applying changes")
	}

	diff, err := ws.Diff(context.Background())
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "updated") || !strings.Contains(diff, "nested/new.txt") {
		t.Fatalf("diff missing applied changes:\n%s", diff)
	}
}

// TestApplyDoesNotTouchPrimaryWorkingTree verifies that changes written to the
// worktree never leak into the primary repository, which must stay clean and
// keep its original content.
func TestApplyDoesNotTouchPrimaryWorkingTree(t *testing.T) {
	repoRoot := newTestRepository(t)
	ws := createWorkspace(t, repoRoot, filepath.Join(t.TempDir(), "ws"))

	if err := ws.Apply([]Change{
		{Op: "write", Path: "tracked.txt", Content: "workspace edit\n"},
		{Op: "write", Path: "primary-leak.txt", Content: "should not appear\n"},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	primary, err := os.ReadFile(filepath.Join(repoRoot, "tracked.txt"))
	if err != nil {
		t.Fatalf("read primary tracked.txt: %v", err)
	}
	if string(primary) != "baseline\n" {
		t.Fatalf("primary tracked.txt = %q, want unchanged baseline", primary)
	}
	if Exists(filepath.Join(repoRoot, "primary-leak.txt")) {
		t.Fatal("a worktree change leaked into the primary repository")
	}

	status := runGitTest(t, repoRoot, "status", "--porcelain")
	if status != "" {
		t.Fatalf("primary repository is dirty after a workspace apply:\n%s", status)
	}
}

func TestRemoveDeletesOnlyRegisteredWorktree(t *testing.T) {
	repoRoot := newTestRepository(t)
	worktreePath := filepath.Join(t.TempDir(), "ws")
	createWorkspace(t, repoRoot, worktreePath)

	if err := Remove(context.Background(), repoRoot, worktreePath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if Exists(worktreePath) {
		t.Fatal("worktree still exists after Remove")
	}
}

// TestRemoveRefusesUnrelatedDirectory verifies cleanup cannot delete a directory
// that is not a worktree of the given repository.
func TestRemoveRefusesUnrelatedDirectory(t *testing.T) {
	repoRoot := newTestRepository(t)
	unrelated := filepath.Join(t.TempDir(), "unrelated")
	if err := os.MkdirAll(unrelated, 0o755); err != nil {
		t.Fatalf("create unrelated dir: %v", err)
	}
	writeFileTest(t, unrelated, "keep.txt", "important\n")

	if err := Remove(context.Background(), repoRoot, unrelated); err == nil {
		t.Fatal("Remove deleted an unrelated directory")
	}
	if !Exists(filepath.Join(unrelated, "keep.txt")) {
		t.Fatal("Remove deleted an unrelated directory's contents")
	}
}

// TestRemoveRefusesPrimaryWorkingTree verifies cleanup cannot delete the primary
// repository working tree.
func TestRemoveRefusesPrimaryWorkingTree(t *testing.T) {
	repoRoot := newTestRepository(t)

	if err := Remove(context.Background(), repoRoot, repoRoot); err == nil {
		t.Fatal("Remove deleted the primary working tree")
	}
	if !Exists(filepath.Join(repoRoot, "tracked.txt")) {
		t.Fatal("primary working tree was damaged by Remove")
	}
}

// TestRemoveRefusesAnotherRepositorysWorktree verifies cleanup is scoped to the
// owning repository and cannot delete a worktree registered elsewhere.
func TestRemoveRefusesAnotherRepositorysWorktree(t *testing.T) {
	repoA := newTestRepository(t)
	repoB := newTestRepository(t)
	worktreeB := filepath.Join(t.TempDir(), "ws-b")
	createWorkspace(t, repoB, worktreeB)

	if err := Remove(context.Background(), repoA, worktreeB); err == nil {
		t.Fatal("Remove deleted a worktree owned by another repository")
	}
	if !Exists(filepath.Join(worktreeB, "tracked.txt")) {
		t.Fatal("another repository's worktree was damaged by Remove")
	}
}
