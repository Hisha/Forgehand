package repocontext

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/inventory"
	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
)

func inventoryForTest(repo string) inventory.Result {
	obs, _ := repository.Observe(context.Background(), repo)
	paths := []string{"README.md"}
	res := inventory.Build(context.Background(), repo, obs.HeadCommit, obs.Dirty, paths, inventory.Options{BudgetMB: 1, MaxFiles: 100})
	return res
}

func TestBuilderIncludesFileContentInPrompt(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# test\nhello world"), 0644); err != nil {
		t.Fatal(err)
	}
	obs, err := repository.Observe(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}

	db, err := state.OpenAt(context.Background(), tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	project, err := db.CreateProject(context.Background(), state.Project{
		Name:     "test",
		RootPath: repo,
	})
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

	_, err = db.CreateProjectInventory(context.Background(), project.ID, obs.HeadCommit, obs.HeadCommit, obs.Dirty, 1, inventoryForTest(repo))
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
