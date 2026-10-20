package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

// newGoModuleGitRepo creates a git repo that is a minimal Go module with a
// passing test, so the validation stage has something real to format and test.
func newGoModuleGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not available")
	}

	root := t.TempDir()
	runDaemonGit(t, root, "init")
	runDaemonGit(t, root, "config", "user.email", "forgehand-test@example.invalid")
	runDaemonGit(t, root, "config", "user.name", "Forgehand Test")

	writeFile := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeFile("go.mod", "module example.com/daemonval\n\ngo 1.22\n")
	writeFile("sum.go", "package daemonval\n\nfunc Add(a, b int) int { return a + b }\n")
	writeFile("sum_test.go", "package daemonval\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")

	runDaemonGit(t, root, "add", ".")
	runDaemonGit(t, root, "commit", "-m", "baseline")
	return root
}

func TestTaskValidationRunsAfterApply(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	repoRoot := newGoModuleGitRepo(t)
	project, err := db.CreateProject(ctx, "proj", repoRoot)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Propose adding a second function; the existing test still passes.
	p := proposalProvider("add Mul",
		`{"op":"write","path":"mul.go","content":"package daemonval\n\nfunc Mul(a, b int) int { return a * b }\n"}`)

	var workers workerGroup
	model := modelRuntimeForTest(t, db, p)
	taskRuntime := taskRuntimeForTest(t, model, t.TempDir())
	taskRuntime.validate = true
	taskRuntime.validateTimeout = 60 * time.Second
	disp := startTaskDispatcher(t, db, &workers, model, taskRuntime)

	sessionID := startTaskSession(t, ctx, db, &workers, disp, project.ID, "add Mul")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusWaitingForUser, 5*time.Second)
	waitForTaskPhase(t, db, sessionID, state.TaskPhaseProposed, 5*time.Second)

	approve := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "task-approve",
		SessionID: sessionID,
	})
	if !approve.OK {
		t.Fatalf("task-approve response = %#v", approve)
	}
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusCompleted, 15*time.Second)
	run := waitForTaskPhase(t, db, sessionID, state.TaskPhaseApplied, 2*time.Second)

	if run.ValidationStatus != state.TaskValidationPassed {
		t.Fatalf("validation status = %q, want passed; json = %s", run.ValidationStatus, run.ValidationJSON)
	}
	if run.ValidationJSON == "" {
		t.Fatal("validation detail not persisted")
	}
	// The change was applied to the isolated workspace.
	if _, err := os.Stat(filepath.Join(run.WorkspacePath, "mul.go")); err != nil {
		t.Fatalf("mul.go missing in workspace: %v", err)
	}
}

func TestTaskValidationRecordsFailure(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	repoRoot := newGoModuleGitRepo(t)
	project, _ := db.CreateProject(ctx, "proj", repoRoot)

	// Propose a change that breaks the existing test: change Add so the test
	// assertion fails.
	p := proposalProvider("break test",
		`{"op":"write","path":"sum.go","content":"package daemonval\n\nfunc Add(a, b int) int { return a + b + 1 }\n"}`)

	var workers workerGroup
	model := modelRuntimeForTest(t, db, p)
	taskRuntime := taskRuntimeForTest(t, model, t.TempDir())
	taskRuntime.validate = true
	taskRuntime.validateTimeout = 60 * time.Second
	disp := startTaskDispatcher(t, db, &workers, model, taskRuntime)

	sessionID := startTaskSession(t, ctx, db, &workers, disp, project.ID, "break test")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusWaitingForUser, 5*time.Second)
	waitForTaskPhase(t, db, sessionID, state.TaskPhaseProposed, 5*time.Second)

	approve := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "task-approve",
		SessionID: sessionID,
	})
	if !approve.OK {
		t.Fatalf("task-approve response = %#v", approve)
	}
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusCompleted, 15*time.Second)
	run := waitForTaskPhase(t, db, sessionID, state.TaskPhaseApplied, 2*time.Second)

	// The changes are applied but validation failed; the run must distinguish
	// applied-but-failed-validation from a clean apply.
	if run.ValidationStatus != state.TaskValidationFailed {
		t.Fatalf("validation status = %q, want failed; json = %s", run.ValidationStatus, run.ValidationJSON)
	}
	if !run.Applied {
		t.Fatal("changes should still be applied despite validation failure")
	}
}

func TestTaskValidationPersistedDiffIncludesFormatting(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	repoRoot := newGoModuleGitRepo(t)
	project, _ := db.CreateProject(ctx, "proj", repoRoot)

	// Propose an unformatted Go file. Validation must format it in place, and
	// the persisted workspace diff must reflect the formatted result.
	p := proposalProvider("add unformatted",
		`{"op":"write","path":"mul.go","content":"package daemonval\n\nfunc Mul(a, b int) int {\nreturn a * b\n}\n"}`)

	var workers workerGroup
	model := modelRuntimeForTest(t, db, p)
	taskRuntime := taskRuntimeForTest(t, model, t.TempDir())
	taskRuntime.validate = true
	taskRuntime.validateTimeout = 60 * time.Second
	disp := startTaskDispatcher(t, db, &workers, model, taskRuntime)

	sessionID := startTaskSession(t, ctx, db, &workers, disp, project.ID, "add unformatted")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusWaitingForUser, 5*time.Second)
	waitForTaskPhase(t, db, sessionID, state.TaskPhaseProposed, 5*time.Second)

	approve := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "task-approve",
		SessionID: sessionID,
	})
	if !approve.OK {
		t.Fatalf("task-approve response = %#v", approve)
	}
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusCompleted, 15*time.Second)
	run := waitForTaskPhase(t, db, sessionID, state.TaskPhaseApplied, 2*time.Second)

	if run.ValidationStatus != state.TaskValidationPassed {
		t.Fatalf("validation status = %q, want passed; json = %s", run.ValidationStatus, run.ValidationJSON)
	}
	// The persisted diff must contain the gofmt-formatted line, proving
	// formatting happened before the diff was rendered.
	if !strings.Contains(run.WorkspaceDiff, "+\treturn a * b") {
		t.Fatalf("persisted diff does not include formatted content:\n%s", run.WorkspaceDiff)
	}
	if strings.Contains(run.WorkspaceDiff, "+\nreturn a * b") {
		t.Fatalf("persisted diff still shows unformatted content:\n%s", run.WorkspaceDiff)
	}
}

func TestTaskProgressDerivedFromPhase(t *testing.T) {
	cases := []struct {
		phase       string
		wantCurrent int
		wantTotal   int
	}{
		{state.TaskPhasePrepared, 1, 3},
		{state.TaskPhaseExecuting, 1, 3},
		{state.TaskPhaseProposed, 2, 3},
		{state.TaskPhaseApplied, 3, 3},
		{state.TaskPhaseRejected, 3, 3},
		{state.TaskPhaseCompleted, 3, 3},
		{state.TaskPhaseFailed, 3, 3},
	}
	for _, tc := range cases {
		current, total := taskProgressFromPhase(tc.phase)
		if current != tc.wantCurrent || total != tc.wantTotal {
			t.Errorf(
				"taskProgressFromPhase(%q) = (%d, %d), want (%d, %d)",
				tc.phase, current, total, tc.wantCurrent, tc.wantTotal,
			)
		}
	}
}
