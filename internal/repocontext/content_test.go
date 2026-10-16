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

func TestBuilderIncludesFileContentInPrompt(t *testing.T) {
	tmp := t.TempDir()
	repo := newContextRepository(t, map[string]string{
		"README.md": "# test\nhello world",
	})

	obs, err := repository.Observe(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}

	db, err := state.OpenAt(context.Background(), tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	project, err := db.CreateProject(context.Background(), "test", repo)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.CreateRepositorySnapshot(context.Background(), state.RepositorySnapshot{
		ProjectID:      project.ID,
		HeadCommit:     obs.HeadCommit,
		Branch:         obs.Branch,
		TrackedFiles:   obs.TrackedFiles,
		UntrackedFiles: obs.UntrackedFiles,
		Dirty:          obs.Dirty,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := inventory.Build(
		context.Background(),
		repo,
		obs.HeadCommit,
		inventory.DefaultBudget(),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.CreateProjectInventory(
		context.Background(),
		project.ID,
		obs.HeadCommit,
		obs.HeadCommit,
		obs.Dirty,
		inventory.EngineVersion,
		result,
	)
	if err != nil {
		t.Fatal(err)
	}

	b := NewBuilder(db)
	ctx, err := b.Build(context.Background(), 1, project.ID, "read README")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctx.User, "# test") || !strings.Contains(ctx.User, "hello world") {
		t.Fatalf("expected content in user prompt, got %q", ctx.User)
	}
	if !strings.Contains(ctx.User, "--- BEGIN FILE: README.md") {
		t.Fatalf("expected file boundary in prompt")
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

func runContextGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
