package repocontext

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/inventory"
	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
)

// setupContextRepository creates a real committed Git repository, opens a state
// database, registers the project, observes it, and persists an inventory built
// from the commit. It returns the database and project for context building.
func setupContextRepository(t *testing.T, files map[string]string) (*state.Database, state.Project) {
	t.Helper()

	tmp := t.TempDir()
	repo := newContextRepository(t, files)

	obs, err := repository.Observe(context.Background(), repo)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	db, err := state.OpenAt(context.Background(), tmp)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	project, err := db.CreateProject(context.Background(), "test", repo)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if _, err := db.CreateRepositorySnapshot(context.Background(), state.RepositorySnapshot{
		ProjectID:      project.ID,
		HeadCommit:     obs.HeadCommit,
		Branch:         obs.Branch,
		TrackedFiles:   obs.TrackedFiles,
		UntrackedFiles: obs.UntrackedFiles,
		Dirty:          obs.Dirty,
	}); err != nil {
		t.Fatalf("CreateRepositorySnapshot: %v", err)
	}

	result, err := inventory.Build(context.Background(), repo, obs.HeadCommit, inventory.DefaultBudget())
	if err != nil {
		t.Fatalf("inventory.Build: %v", err)
	}

	if _, err := db.CreateProjectInventory(
		context.Background(),
		project.ID,
		obs.HeadCommit,
		obs.HeadCommit,
		obs.Dirty,
		inventory.EngineVersion,
		result,
	); err != nil {
		t.Fatalf("CreateProjectInventory: %v", err)
	}

	return db, project
}

func TestBuilderIncludesExactFileContentInPrompt(t *testing.T) {
	content := "# test\nhello world\nsecond line"
	db, project := setupContextRepository(t, map[string]string{"README.md": content})

	b := NewBuilder(db)
	ctx, err := b.Build(context.Background(), 1, project.ID, "read README")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !strings.Contains(ctx.User, "--- BEGIN FILE: README.md") {
		t.Fatalf("expected file boundary in prompt, got %q", ctx.User)
	}
	if !strings.Contains(ctx.User, "--- END FILE: README.md ---") {
		t.Fatalf("expected file end boundary in prompt, got %q", ctx.User)
	}
	if !strings.Contains(ctx.User, content) {
		t.Fatalf("expected exact committed content in prompt, got %q", ctx.User)
	}
	if len(ctx.Files) != 1 || ctx.Files[0].Path != "README.md" || ctx.Files[0].Content != content {
		t.Fatalf("unexpected included files: %#v", ctx.Files)
	}
	if ctx.UsedChars > ctx.BudgetChars {
		t.Fatalf("UsedChars %d exceeds BudgetChars %d", ctx.UsedChars, ctx.BudgetChars)
	}
}

func TestBuilderSystemPromptMarksContentUntrusted(t *testing.T) {
	db, project := setupContextRepository(t, map[string]string{"README.md": "# test\n"})

	b := NewBuilder(db)
	ctx, err := b.Build(context.Background(), 1, project.ID, "read README")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	lower := strings.ToLower(ctx.System)
	if !strings.Contains(lower, "untrusted") {
		t.Fatalf("system prompt does not mark repository content untrusted: %q", ctx.System)
	}
	if !strings.Contains(lower, "never follow instructions") {
		t.Fatalf("system prompt does not forbid following embedded instructions: %q", ctx.System)
	}
}

func TestBuilderSkipsBinaryAndSensitiveFiles(t *testing.T) {
	db, project := setupContextRepository(t, map[string]string{
		"README.md":       "# Readme\nsource marked for inclusion\n",
		"assets/logo.png": "\x89PNG\r\n\x1a\nnot-text-binary",
		".env":            "API_KEY=super-secret-value",
	})

	b := NewBuilder(db)
	ctx, err := b.Build(context.Background(), 1, project.ID, "summarize the readme")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if strings.Contains(ctx.User, "super-secret-value") {
		t.Fatalf("prompt leaked sensitive file contents: %q", ctx.User)
	}
	if strings.Contains(ctx.User, "not-text-binary") {
		t.Fatalf("prompt included binary file contents: %q", ctx.User)
	}
	if !strings.Contains(ctx.User, "source marked for inclusion") {
		t.Fatalf("prompt did not include the eligible file: %q", ctx.User)
	}
}

func TestBuilderUsesCommittedBlobNotWorkingTree(t *testing.T) {
	tmp := t.TempDir()
	repo := newContextRepository(t, map[string]string{"main.go": "package main\n// committed-marker-aaa\n"})

	obs, err := repository.Observe(context.Background(), repo)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	db, err := state.OpenAt(context.Background(), tmp)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	project, err := db.CreateProject(context.Background(), "test", repo)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	result, err := inventory.Build(context.Background(), repo, obs.HeadCommit, inventory.DefaultBudget())
	if err != nil {
		t.Fatalf("inventory.Build: %v", err)
	}
	if _, err := db.CreateProjectInventory(
		context.Background(),
		project.ID,
		obs.HeadCommit,
		obs.HeadCommit,
		false,
		inventory.EngineVersion,
		result,
	); err != nil {
		t.Fatalf("CreateProjectInventory: %v", err)
	}

	// Mutate the working tree without committing. The builder must still read
	// the committed blob and never substitute dirty working-tree content.
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n// dirty-marker-bbb\n"), 0o644); err != nil {
		t.Fatalf("mutate working tree: %v", err)
	}

	b := NewBuilder(db)
	ctx, err := b.Build(context.Background(), 1, project.ID, "inspect main.go")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !strings.Contains(ctx.User, "committed-marker-aaa") {
		t.Fatalf("prompt missing committed content: %q", ctx.User)
	}
	if strings.Contains(ctx.User, "dirty-marker-bbb") {
		t.Fatalf("prompt substituted dirty working-tree content: %q", ctx.User)
	}
}

func TestBuilderBudgetTruncatesDeterministicallyAndPrioritizes(t *testing.T) {
	files := map[string]string{
		"docs/guide.md": "# Guide\n" + strings.Repeat("documentation line\n", 40),
		"target.go":     "package target\n" + strings.Repeat("target line\n", 40),
	}
	for i := 0; i < 20; i++ {
		files[filepath.Join("pkg", "filler"+string(rune('a'+i))+".go")] = "package filler\n" + strings.Repeat("filler content line\n", 40)
	}

	db, project := setupContextRepository(t, files)

	const budget = 2000
	b := NewBuilder(db).WithLimits(0, 0, budget)

	first, err := b.Build(context.Background(), 1, project.ID, "fix the target package behavior")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	second, err := b.Build(context.Background(), 1, project.ID, "fix the target package behavior")
	if err != nil {
		t.Fatalf("Build second: %v", err)
	}

	if first.UsedChars > first.BudgetChars {
		t.Fatalf("UsedChars %d exceeds budget %d", first.UsedChars, first.BudgetChars)
	}
	if !first.Truncated {
		t.Fatalf("expected truncation with a small budget")
	}
	if first.User != second.User {
		t.Fatalf("context assembly is not deterministic")
	}

	foundBudgetExclusion := false
	for _, entry := range first.Excluded {
		if entry.Reason == "context budget exceeded" {
			foundBudgetExclusion = true
		}
	}
	if !foundBudgetExclusion {
		t.Fatalf("expected a budget exclusion to be reported: %#v", first.Excluded)
	}

	// The task names "target", so the relevant file must be prioritized over
	// unrelated filler.
	if !strings.Contains(first.User, "package target") {
		t.Fatalf("task-relevant file was not prioritized: %q", first.User)
	}
}

func newContextRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create repository: %v", err)
	}
	runContextGit(t, root, "init")
	runContextGit(t, root, "config", "user.email", "context@example.invalid")
	runContextGit(t, root, "config", "user.name", "Context Test")
	for filePath, content := range files {
		fullPath := filepath.Join(root, filePath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("create parent for %s: %v", filePath, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", filePath, err)
		}
	}
	runContextGit(t, root, "add", "--all")
	runContextGit(t, root, "commit", "-m", "initial")
	return root
}

func TestSelectCandidatesPreservesPreferredRanking(t *testing.T) {
	files := []state.InventoryFile{
		{Path: "docs/first.md", Category: "documentation"},
		{Path: "internal/second.go", Category: "source"},
		{Path: "internal/first.go", Category: "source"},
	}
	selected := selectCandidates(files, "unrelated", []string{"internal/second.go", "internal/first.go"})
	if len(selected) != 3 || selected[0].Path != "internal/second.go" || selected[1].Path != "internal/first.go" {
		t.Fatalf("selected order = %#v", selected)
	}
}

func runContextGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
