package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/state"
)

func TestAddProjectAcceptsCleanRepository(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)

	project, err := addProject(ctx, db, root)
	if err != nil {
		t.Fatalf("addProject() returned error: %v", err)
	}

	canonicalRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("absolute repository path: %v", err)
	}
	canonicalRoot = filepath.Clean(canonicalRoot)

	if project.RootPath != canonicalRoot {
		t.Fatalf(
			"project root = %q, want %q",
			project.RootPath,
			canonicalRoot,
		)
	}

	projects, err := db.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects() returned error: %v", err)
	}

	if len(projects) != 1 {
		t.Fatalf(
			"persisted project count = %d, want 1",
			len(projects),
		)
	}

	if projects[0].ID != project.ID {
		t.Fatalf(
			"persisted project ID = %d, want %d",
			projects[0].ID,
			project.ID,
		)
	}
}

func TestAddProjectRejectsDirtyTrackedRepository(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)

	path := filepath.Join(root, "tracked.txt")
	const dirtyContent = "dirty tracked content\n"

	if err := os.WriteFile(
		path,
		[]byte(dirtyContent),
		0o644,
	); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}

	_, err := addProject(ctx, db, root)
	if err == nil {
		t.Fatal("addProject() succeeded for dirty repository")
	}

	if !strings.Contains(err.Error(), "tracked.txt") {
		t.Fatalf(
			"dirty repository error %q does not report tracked.txt",
			err,
		)
	}

	assertNoProjects(t, ctx, db)

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tracked file after rejected intake: %v", err)
	}

	if string(content) != dirtyContent {
		t.Fatalf(
			"tracked file changed during rejected intake: %q",
			string(content),
		)
	}
}

func TestAddProjectRejectsUntrackedFile(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)

	path := filepath.Join(root, "untracked.txt")
	const content = "existing untracked work\n"

	if err := os.WriteFile(
		path,
		[]byte(content),
		0o644,
	); err != nil {
		t.Fatalf("create untracked file: %v", err)
	}

	_, err := addProject(ctx, db, root)
	if err == nil {
		t.Fatal("addProject() succeeded with untracked file")
	}

	if !strings.Contains(err.Error(), "untracked.txt") {
		t.Fatalf(
			"dirty repository error %q does not report untracked.txt",
			err,
		)
	}

	assertNoProjects(t, ctx, db)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read untracked file after rejected intake: %v", err)
	}

	if string(got) != content {
		t.Fatalf(
			"untracked file changed during rejected intake: %q",
			string(got),
		)
	}
}

func openProjectIntakeTestDatabase(
	t *testing.T,
) *state.Database {
	t.Helper()

	t.Setenv("XDG_STATE_HOME", t.TempDir())

	db, err := state.Open(context.Background())
	if err != nil {
		t.Fatalf("state.Open() returned error: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("state database Close() returned error: %v", err)
		}
	})

	return db
}

func newProjectIntakeRepository(t *testing.T) string {
	t.Helper()

	root := t.TempDir()

	runProjectIntakeGit(t, root, "init")
	runProjectIntakeGit(t, root, "config", "user.email", "forgehand-test@example.invalid")
	runProjectIntakeGit(t, root, "config", "user.name", "Forgehand Test")

	if err := os.WriteFile(
		filepath.Join(root, "tracked.txt"),
		[]byte("baseline\n"),
		0o644,
	); err != nil {
		t.Fatalf("write baseline file: %v", err)
	}

	runProjectIntakeGit(t, root, "add", "tracked.txt")
	runProjectIntakeGit(t, root, "commit", "-m", "baseline")

	return root
}

func runProjectIntakeGit(
	t *testing.T,
	root string,
	args ...string,
) {
	t.Helper()

	commandArgs := append(
		[]string{"-C", root},
		args...,
	)

	cmd := exec.Command("git", commandArgs...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf(
			"git %v failed: %v\n%s",
			args,
			err,
			output,
		)
	}
}

func assertNoProjects(
	t *testing.T,
	ctx context.Context,
	db *state.Database,
) {
	t.Helper()

	projects, err := db.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects() returned error: %v", err)
	}

	if len(projects) != 0 {
		t.Fatalf(
			"persisted project count = %d, want 0",
			len(projects),
		)
	}
}
