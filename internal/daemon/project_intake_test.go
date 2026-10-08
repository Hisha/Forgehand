package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
)

func TestProjectIntakeAcceptsCleanRepository(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	headBefore := strings.TrimSpace(string(projectIntakeGitOutput(t, root, "rev-parse", "HEAD")))

	intake, err := inspectProjectIntake(ctx, root)
	if err != nil {
		t.Fatalf("inspectProjectIntake() returned error: %v", err)
	}

	if len(intake.Changes) != 0 {
		t.Fatalf(
			"clean repository returned %d changes: %#v",
			len(intake.Changes),
			intake.Changes,
		)
	}

	project, err := admitProject(ctx, db, intake)
	if err != nil {
		t.Fatalf("admitProject() returned error: %v", err)
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

	headAfter := strings.TrimSpace(string(projectIntakeGitOutput(t, root, "rev-parse", "HEAD")))
	if headAfter != headBefore {
		t.Fatalf("clean admission changed HEAD from %s to %s", headBefore, headAfter)
	}
}

func TestProjectIntakeRejectsDirtyTrackedRepository(t *testing.T) {
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

	intake, err := inspectProjectIntake(ctx, root)
	if err != nil {
		t.Fatalf("inspectProjectIntake() returned error: %v", err)
	}

	change := findProjectIntakeChange(
		t,
		intake,
		"tracked.txt",
	)

	if change.WorkStatus != "M" {
		t.Fatalf(
			"tracked file work status = %q, want M",
			change.WorkStatus,
		)
	}

	if change.Untracked {
		t.Fatal("tracked modification marked untracked")
	}

	_, err = admitProject(ctx, db, intake)
	if err == nil {
		t.Fatal("admitProject() accepted dirty repository")
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

func TestProjectIntakeRejectsUntrackedFile(t *testing.T) {
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

	intake, err := inspectProjectIntake(ctx, root)
	if err != nil {
		t.Fatalf("inspectProjectIntake() returned error: %v", err)
	}

	change := findProjectIntakeChange(
		t,
		intake,
		"untracked.txt",
	)

	if !change.Untracked {
		t.Fatal("untracked file not marked untracked")
	}

	_, err = admitProject(ctx, db, intake)
	if err == nil {
		t.Fatal("admitProject() accepted repository with untracked file")
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

func TestProjectIntakeCommitResolution(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "tracked.txt", "committed baseline\n")
	writeProjectIntakeFile(t, root, "untracked.txt", "included\n")

	intake := inspectProjectIntakeForTest(t, ctx, root)
	project, err := resolveProjectIntake(
		ctx, db, root, projectIntakeCommit, intake.Fingerprint,
	)
	if err != nil {
		t.Fatalf("resolveProjectIntake(commit) returned error: %v", err)
	}

	assertProjectIntakeClean(t, root)
	assertProjectCount(t, ctx, db, 1)
	if project.RootPath != root {
		t.Fatalf("registered root = %q, want %q", project.RootPath, root)
	}
	message := projectIntakeGitOutput(t, root, "log", "-1", "--format=%s")
	if strings.TrimSpace(string(message)) != repository.BaselineCommitMessage {
		t.Fatalf("baseline message = %q, want %q", strings.TrimSpace(string(message)), repository.BaselineCommitMessage)
	}
}

func TestProjectIntakeDiscardResolution(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "tracked.txt", "discard me\n")
	writeProjectIntakeFile(t, root, "untracked.txt", "discard me too\n")

	intake := inspectProjectIntakeForTest(t, ctx, root)
	if _, err := resolveProjectIntake(
		ctx, db, root, projectIntakeDiscard, intake.Fingerprint,
	); err != nil {
		t.Fatalf("resolveProjectIntake(discard) returned error: %v", err)
	}

	assertProjectIntakeClean(t, root)
	assertProjectCount(t, ctx, db, 1)
	content, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil {
		t.Fatalf("read restored tracked file: %v", err)
	}
	if string(content) != "baseline\n" {
		t.Fatalf("restored tracked content = %q, want baseline", content)
	}
	if _, err := os.Stat(filepath.Join(root, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file still exists after discard: %v", err)
	}
}

func TestProjectIntakeCancelLeavesRepositoryUntouched(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	const content = "keep this work\n"
	writeProjectIntakeFile(t, root, "tracked.txt", content)
	intake := inspectProjectIntakeForTest(t, ctx, root)

	project, err := resolveProjectIntake(
		ctx, db, root, projectIntakeCancel, intake.Fingerprint,
	)
	if err != nil {
		t.Fatalf("resolveProjectIntake(cancel) returned error: %v", err)
	}
	if project.ID != 0 {
		t.Fatalf("cancel returned project ID %d", project.ID)
	}
	assertProjectCount(t, ctx, db, 0)
	got, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil {
		t.Fatalf("read tracked file after cancel: %v", err)
	}
	if string(got) != content {
		t.Fatalf("tracked content after cancel = %q, want %q", got, content)
	}
}

func TestProjectIntakeDiscardPreservesIgnoredFiles(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, ".gitignore", "ignored.log\n")
	runProjectIntakeGit(t, root, "add", ".gitignore")
	runProjectIntakeGit(t, root, "commit", "-m", "ignore file")
	writeProjectIntakeFile(t, root, "ignored.log", "preserve\n")
	writeProjectIntakeFile(t, root, "tracked.txt", "discard\n")

	intake := inspectProjectIntakeForTest(t, ctx, root)
	if _, err := resolveProjectIntake(
		ctx, db, root, projectIntakeDiscard, intake.Fingerprint,
	); err != nil {
		t.Fatalf("resolveProjectIntake(discard) returned error: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "ignored.log"))
	if err != nil {
		t.Fatalf("read ignored file after discard: %v", err)
	}
	if string(content) != "preserve\n" {
		t.Fatalf("ignored content = %q", content)
	}
	assertProjectIntakeClean(t, root)
}

func TestProjectIntakeDiscardRemovesUntrackedDirectories(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "new/deep/file.txt", "discard\n")
	intake := inspectProjectIntakeForTest(t, ctx, root)

	if _, err := resolveProjectIntake(
		ctx, db, root, projectIntakeDiscard, intake.Fingerprint,
	); err != nil {
		t.Fatalf("resolveProjectIntake(discard) returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatalf("untracked directory still exists after discard: %v", err)
	}
}

func TestProjectIntakeDiscardPreservesNestedRepositoryAndDoesNotRegister(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("create nested repository directory: %v", err)
	}
	runProjectIntakeGit(t, nested, "init")
	nestedFile := filepath.Join(nested, "work.txt")
	const nestedContent = "preserve nested work\n"
	if err := os.WriteFile(nestedFile, []byte(nestedContent), 0o644); err != nil {
		t.Fatalf("write nested repository content: %v", err)
	}
	intake := inspectProjectIntakeForTest(t, ctx, root)

	_, err := resolveProjectIntake(
		ctx, db, root, projectIntakeDiscard, intake.Fingerprint,
	)
	if err == nil || !strings.Contains(err.Error(), "not clean after discard") {
		t.Fatalf("discard with nested repository error = %v, want final cleanliness failure", err)
	}
	assertProjectCount(t, ctx, db, 0)
	content, readErr := os.ReadFile(nestedFile)
	if readErr != nil {
		t.Fatalf("read nested repository content after discard: %v", readErr)
	}
	if string(content) != nestedContent {
		t.Fatalf("nested repository content = %q, want %q", content, nestedContent)
	}
}

func TestProjectIntakeCommitHandlesStagedAndUnstagedChanges(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "tracked.txt", "staged version\n")
	runProjectIntakeGit(t, root, "add", "tracked.txt")
	writeProjectIntakeFile(t, root, "tracked.txt", "final unstaged version\n")

	intake := inspectProjectIntakeForTest(t, ctx, root)
	if _, err := resolveProjectIntake(
		ctx, db, root, projectIntakeCommit, intake.Fingerprint,
	); err != nil {
		t.Fatalf("resolveProjectIntake(commit) returned error: %v", err)
	}
	assertProjectIntakeClean(t, root)
	committed := projectIntakeGitOutput(t, root, "show", "HEAD:tracked.txt")
	if string(committed) != "final unstaged version\n" {
		t.Fatalf("committed content = %q", committed)
	}
}

func TestProjectIntakeCommitHandlesUnbornHEAD(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newUnbornProjectIntakeRepository(t, true)
	writeProjectIntakeFile(t, root, "first.txt", "first commit\n")
	intake := inspectProjectIntakeForTest(t, ctx, root)

	if _, err := resolveProjectIntake(
		ctx, db, root, projectIntakeCommit, intake.Fingerprint,
	); err != nil {
		t.Fatalf("resolveProjectIntake(commit unborn) returned error: %v", err)
	}
	assertProjectIntakeClean(t, root)
	if _, hasHead, err := repository.HeadCommit(ctx, root); err != nil || !hasHead {
		t.Fatalf("HEAD after initial commit: hasHead=%v err=%v", hasHead, err)
	}
}

func TestProjectIntakeDiscardRejectsUnbornHEAD(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newUnbornProjectIntakeRepository(t, true)
	writeProjectIntakeFile(t, root, "first.txt", "keep\n")
	intake := inspectProjectIntakeForTest(t, ctx, root)

	_, err := resolveProjectIntake(
		ctx, db, root, projectIntakeDiscard, intake.Fingerprint,
	)
	if err == nil || !strings.Contains(err.Error(), "no HEAD commit") {
		t.Fatalf("discard unborn error = %v, want no HEAD commit", err)
	}
	assertProjectCount(t, ctx, db, 0)
	if _, err := os.Stat(filepath.Join(root, "first.txt")); err != nil {
		t.Fatalf("unborn content removed after rejected discard: %v", err)
	}
}

func TestProjectIntakeChangedTrackedContentInvalidatesFingerprint(t *testing.T) {
	testProjectIntakeFingerprintInvalidation(t, func(t *testing.T, root string) {
		writeProjectIntakeFile(t, root, "tracked.txt", "changed again\n")
	})
}

func TestProjectIntakeChangedStagedContentInvalidatesFingerprint(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "tracked.txt", "first staged version\n")
	runProjectIntakeGit(t, root, "add", "tracked.txt")
	intake := inspectProjectIntakeForTest(t, ctx, root)
	writeProjectIntakeFile(t, root, "tracked.txt", "second staged version\n")
	runProjectIntakeGit(t, root, "add", "tracked.txt")

	_, err := resolveProjectIntake(
		ctx, db, root, projectIntakeCommit, intake.Fingerprint,
	)
	if err == nil || !strings.Contains(err.Error(), "state changed") {
		t.Fatalf("resolve after staged change error = %v, want state changed", err)
	}
	assertProjectCount(t, ctx, db, 0)
}

func TestProjectIntakeChangedUntrackedContentInvalidatesFingerprint(t *testing.T) {
	testProjectIntakeFingerprintInvalidation(t, func(t *testing.T, root string) {
		writeProjectIntakeFile(t, root, "untracked.txt", "changed again\n")
	})
}

func TestProjectIntakeAddedOrRemovedUntrackedFileInvalidatesFingerprint(t *testing.T) {
	t.Run("added", func(t *testing.T) {
		testProjectIntakeFingerprintInvalidation(t, func(t *testing.T, root string) {
			writeProjectIntakeFile(t, root, "added-later.txt", "new\n")
		})
	})

	t.Run("removed", func(t *testing.T) {
		testProjectIntakeFingerprintInvalidation(t, func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "untracked.txt")); err != nil {
				t.Fatalf("remove untracked file: %v", err)
			}
		})
	})
}

func TestProjectIntakeChangedHEADInvalidatesFingerprint(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "untracked.txt", "work\n")
	intake := inspectProjectIntakeForTest(t, ctx, root)
	runProjectIntakeGit(t, root, "commit", "--allow-empty", "-m", "move HEAD")

	_, err := resolveProjectIntake(
		ctx, db, root, projectIntakeCommit, intake.Fingerprint,
	)
	if err == nil || !strings.Contains(err.Error(), "state changed") {
		t.Fatalf("resolve after HEAD change error = %v, want state changed", err)
	}
	assertProjectCount(t, ctx, db, 0)
}

func TestProjectIntakeFailedCommitDoesNotRegister(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newUnbornProjectIntakeRepository(t, false)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeProjectIntakeFile(t, root, "first.txt", "cannot commit without identity\n")
	intake := inspectProjectIntakeForTest(t, ctx, root)

	if _, err := resolveProjectIntake(
		ctx, db, root, projectIntakeCommit, intake.Fingerprint,
	); err == nil {
		t.Fatal("commit without author identity unexpectedly succeeded")
	}
	assertProjectCount(t, ctx, db, 0)
	if _, hasHead, err := repository.HeadCommit(ctx, root); err != nil || hasHead {
		t.Fatalf("failed commit HEAD: hasHead=%v err=%v", hasHead, err)
	}
	status := string(projectIntakeGitOutput(t, root, "status", "--porcelain"))
	if !strings.HasPrefix(status, "?? first.txt") {
		t.Fatalf("failed identity check changed index state: %q", status)
	}
}

func testProjectIntakeFingerprintInvalidation(
	t *testing.T,
	change func(*testing.T, string),
) {
	t.Helper()
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "tracked.txt", "initial dirty\n")
	writeProjectIntakeFile(t, root, "untracked.txt", "initial untracked\n")
	intake := inspectProjectIntakeForTest(t, ctx, root)
	change(t, root)

	_, err := resolveProjectIntake(
		ctx, db, root, projectIntakeCommit, intake.Fingerprint,
	)
	if err == nil || !strings.Contains(err.Error(), "state changed") {
		t.Fatalf("resolve after state change error = %v, want state changed", err)
	}
	assertProjectCount(t, ctx, db, 0)
}

func findProjectIntakeChange(
	t *testing.T,
	intake projectIntake,
	path string,
) repository.WorkingTreeChange {
	t.Helper()

	for _, change := range intake.Changes {
		if change.Path == path {
			return change
		}
	}

	t.Fatalf(
		"project intake missing change %q: %#v",
		path,
		intake.Changes,
	)

	return repository.WorkingTreeChange{}
}

func openProjectIntakeTestDatabase(
	t *testing.T,
) *state.Database {
	t.Helper()

	t.Setenv("FORGEHAND_STATE_DIR", "")
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

func newUnbornProjectIntakeRepository(t *testing.T, configureIdentity bool) string {
	t.Helper()
	root := t.TempDir()
	runProjectIntakeGit(t, root, "init")
	if configureIdentity {
		runProjectIntakeGit(t, root, "config", "user.email", "forgehand-test@example.invalid")
		runProjectIntakeGit(t, root, "config", "user.name", "Forgehand Test")
	}
	return root
}

func inspectProjectIntakeForTest(
	t *testing.T,
	ctx context.Context,
	root string,
) projectIntake {
	t.Helper()
	intake, err := inspectProjectIntake(ctx, root)
	if err != nil {
		t.Fatalf("inspectProjectIntake() returned error: %v", err)
	}
	if intake.Fingerprint == "" {
		t.Fatal("project intake returned empty fingerprint")
	}
	return intake
}

func writeProjectIntakeFile(t *testing.T, root, path, content string) {
	t.Helper()
	fullPath := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("create parent directory for %s: %v", path, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func projectIntakeGitOutput(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	cmd := exec.Command("git", commandArgs...)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return output
}

func assertProjectIntakeClean(t *testing.T, root string) {
	t.Helper()
	output := projectIntakeGitOutput(
		t,
		root,
		"status",
		"--porcelain=v2",
		"--untracked-files=all",
	)
	if len(output) != 0 {
		t.Fatalf("repository is dirty:\n%s", output)
	}
}

func assertProjectCount(
	t *testing.T,
	ctx context.Context,
	db *state.Database,
	want int,
) {
	t.Helper()
	projects, err := db.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects() returned error: %v", err)
	}
	if len(projects) != want {
		t.Fatalf("persisted project count = %d, want %d", len(projects), want)
	}
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
