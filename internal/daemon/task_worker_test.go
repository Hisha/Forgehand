package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/config"
	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/provider"
	"github.com/Hisha/Forgehand/internal/state"
)

func newDaemonGitRepo(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}

	root := t.TempDir()
	runDaemonGit(t, root, "init")
	runDaemonGit(t, root, "config", "user.email", "forgehand-test@example.invalid")
	runDaemonGit(t, root, "config", "user.name", "Forgehand Test")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatalf("write tracked.txt: %v", err)
	}
	runDaemonGit(t, root, "add", "tracked.txt")
	runDaemonGit(t, root, "commit", "-m", "baseline")
	return root
}

func runDaemonGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func daemonGitOutput(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func startTaskDispatcher(
	t *testing.T,
	db *state.Database,
	workers *workerGroup,
	model *modelRuntime,
	taskRuntime *taskRuntime,
) *dispatcher {
	t.Helper()

	dispCtx, stop := context.WithCancel(context.Background())
	disp := newDispatcher(db, workers, 4, model, taskRuntime)
	go disp.run(dispCtx)
	t.Cleanup(func() {
		stop()
		<-disp.done
		workers.stopAndWait()
	})
	return disp
}

func taskRuntimeForTest(t *testing.T, model *modelRuntime, workspacesDir string) *taskRuntime {
	t.Helper()

	runtime := buildTaskRuntime(model, config.TaskConfig{
		MaxIterations:     6,
		MaxFileBytes:      1 << 20,
		MaxTotalReadBytes: 4 << 20,
		MaxChanges:        10,
		MaxSearchResults:  50,
		WorkspacesDir:     workspacesDir,
	})
	if runtime == nil {
		t.Fatal("buildTaskRuntime returned nil")
	}
	return runtime
}

func startTaskSession(
	t *testing.T,
	ctx context.Context,
	db *state.Database,
	workers *workerGroup,
	disp *dispatcher,
	projectID int64,
	taskText string,
) int64 {
	t.Helper()

	resp := modelRequest(t, ctx, db, workers, disp, ipc.Request{
		Command:   "task-start",
		ProjectID: projectID,
		Task:      taskText,
	})
	if !resp.OK || resp.Session == nil || resp.Execution == nil {
		t.Fatalf("task-start response = %#v", resp)
	}
	if resp.Session.Kind != "task" {
		t.Fatalf("session kind = %q, want task", resp.Session.Kind)
	}
	return resp.Session.ID
}

func waitForTaskPhase(
	t *testing.T,
	db *state.Database,
	sessionID int64,
	want string,
	timeout time.Duration,
) state.TaskRun {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		run, err := db.GetTaskRunBySession(context.Background(), sessionID)
		if err != nil {
			t.Fatalf("GetTaskRunBySession: %v", err)
		}
		if run != nil && run.Phase == want {
			return *run
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for task phase %q; latest = %#v", want, run)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func proposalProvider(summary, changesJSON string) *stubProvider {
	return &stubProvider{fn: func(_ context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{
			Content: `{"action":"propose","summary":"` + summary + `","changes":[` + changesJSON + `]}`,
			Model:   "test-model",
		}, nil
	}}
}

// answerTaskApproval answers the pending approval question and resumes the
// execution through IPC, exactly as the CLI would.
func answerTaskApproval(
	t *testing.T,
	ctx context.Context,
	db *state.Database,
	workers *workerGroup,
	disp *dispatcher,
	sessionID int64,
	answer string,
) {
	t.Helper()

	interaction, err := db.GetPendingHumanInput(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetPendingHumanInput: %v", err)
	}
	if interaction == nil {
		t.Fatal("no pending approval interaction")
	}

	resp := modelRequest(t, ctx, db, workers, disp, ipc.Request{
		Command:       "interaction-answer",
		InteractionID: interaction.ID,
		Answer:        answer,
	})
	if !resp.OK {
		t.Fatalf("interaction-answer response = %#v", resp)
	}

	resume := modelRequest(t, ctx, db, workers, disp, ipc.Request{
		Command:   "session-resume",
		SessionID: sessionID,
	})
	if !resume.OK {
		t.Fatalf("session-resume response = %#v", resume)
	}
}

func readPrimaryTracked(t *testing.T, repoRoot string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "tracked.txt"))
	if err != nil {
		t.Fatalf("read primary tracked.txt: %v", err)
	}
	return string(data)
}

func TestTaskWorkerProposeApproveApplyPreservesPrimaryTree(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	repoRoot := newDaemonGitRepo(t)
	project, err := db.CreateProject(ctx, "proj", repoRoot)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	p := proposalProvider("rewrite tracked.txt",
		`{"op":"write","path":"tracked.txt","content":"changed\n"}`)

	var workers workerGroup
	model := modelRuntimeForTest(t, db, p)
	taskRuntime := taskRuntimeForTest(t, model, t.TempDir())
	disp := startTaskDispatcher(t, db, &workers, model, taskRuntime)

	sessionID := startTaskSession(t, ctx, db, &workers, disp, project.ID, "rewrite tracked.txt")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusWaitingForUser, 5*time.Second)

	run := waitForTaskPhase(t, db, sessionID, state.TaskPhaseProposed, 5*time.Second)
	if run.Applied {
		t.Fatal("proposal phase marked applied before authorization")
	}
	if run.WorkspaceFingerprint == "" {
		t.Fatal("proposal did not record a workspace fingerprint")
	}

	// The proposed change must not have been applied yet.
	if got := readPrimaryTracked(t, repoRoot); got != "baseline\n" {
		t.Fatalf("primary edited during proposal: %q", got)
	}

	answerTaskApproval(t, ctx, db, &workers, disp, sessionID, "approve")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusCompleted, 5*time.Second)

	applied := waitForTaskPhase(t, db, sessionID, state.TaskPhaseApplied, 2*time.Second)
	if !applied.Applied {
		t.Fatal("approved proposal was not marked applied")
	}
	if applied.WorkspaceDiff == "" {
		t.Fatal("applied run did not persist a diff")
	}
	if p.callCount() != 1 {
		t.Fatalf("provider calls = %d, want 1", p.callCount())
	}

	// The change is applied in the workspace...
	workspaceFile, err := os.ReadFile(filepath.Join(applied.WorkspacePath, "tracked.txt"))
	if err != nil {
		t.Fatalf("read workspace tracked.txt: %v", err)
	}
	if string(workspaceFile) != "changed\n" {
		t.Fatalf("workspace tracked.txt = %q, want changed", workspaceFile)
	}

	// ...but the primary working tree is untouched.
	if got := readPrimaryTracked(t, repoRoot); got != "baseline\n" {
		t.Fatalf("primary tracked.txt = %q, want baseline", got)
	}
	status := string(daemonGitOutput(t, repoRoot, "status", "--porcelain"))
	if status != "" {
		t.Fatalf("primary repository is dirty:\n%s", status)
	}
}

func TestTaskWorkerRejectAppliesNothing(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	repoRoot := newDaemonGitRepo(t)
	project, _ := db.CreateProject(ctx, "proj", repoRoot)

	p := proposalProvider("rewrite", `{"op":"write","path":"tracked.txt","content":"changed\n"}`)

	var workers workerGroup
	model := modelRuntimeForTest(t, db, p)
	taskRuntime := taskRuntimeForTest(t, model, t.TempDir())
	disp := startTaskDispatcher(t, db, &workers, model, taskRuntime)

	sessionID := startTaskSession(t, ctx, db, &workers, disp, project.ID, "rewrite")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusWaitingForUser, 5*time.Second)

	answerTaskApproval(t, ctx, db, &workers, disp, sessionID, "reject")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusCompleted, 5*time.Second)

	run := waitForTaskPhase(t, db, sessionID, state.TaskPhaseRejected, 2*time.Second)
	if run.Applied {
		t.Fatal("rejected run marked applied")
	}
	if run.WorkspaceDiff != "" {
		t.Fatalf("rejected run kept a diff: %q", run.WorkspaceDiff)
	}
	if got := readPrimaryTracked(t, repoRoot); got != "baseline\n" {
		t.Fatalf("primary changed on rejection: %q", got)
	}
	if p.callCount() != 1 {
		t.Fatalf("provider calls = %d, want 1", p.callCount())
	}
}

func TestTaskWorkerRejectsStaleApproval(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	repoRoot := newDaemonGitRepo(t)
	project, _ := db.CreateProject(ctx, "proj", repoRoot)

	p := proposalProvider("rewrite", `{"op":"write","path":"tracked.txt","content":"changed\n"}`)

	var workers workerGroup
	model := modelRuntimeForTest(t, db, p)
	taskRuntime := taskRuntimeForTest(t, model, t.TempDir())
	disp := startTaskDispatcher(t, db, &workers, model, taskRuntime)

	sessionID := startTaskSession(t, ctx, db, &workers, disp, project.ID, "rewrite")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusWaitingForUser, 5*time.Second)

	run := waitForTaskPhase(t, db, sessionID, state.TaskPhaseProposed, 5*time.Second)

	// Mutate the workspace after the proposal was recorded, simulating an
	// out-of-band change. The approval must no longer be honored.
	if err := os.WriteFile(filepath.Join(run.WorkspacePath, "tampered.txt"), []byte("surprise\n"), 0o644); err != nil {
		t.Fatalf("tamper with workspace: %v", err)
	}

	answerTaskApproval(t, ctx, db, &workers, disp, sessionID, "approve")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusCompleted, 5*time.Second)

	stale := waitForTaskPhase(t, db, sessionID, state.TaskPhaseRejected, 2*time.Second)
	if stale.Applied {
		t.Fatal("stale approval was applied")
	}
	if !strings.Contains(strings.ToLower(stale.Error), "workspace") {
		t.Fatalf("stale rejection reason = %q, want a workspace mismatch", stale.Error)
	}
	if got := readPrimaryTracked(t, repoRoot); got != "baseline\n" {
		t.Fatalf("primary changed on stale approval: %q", got)
	}
}

func TestTaskWorkerCancellationLeavesPrimaryUntouched(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	repoRoot := newDaemonGitRepo(t)
	project, _ := db.CreateProject(ctx, "proj", repoRoot)

	started := make(chan struct{})
	p := &stubProvider{fn: func(ctx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
		close(started)
		<-ctx.Done()
		return provider.ChatResponse{}, ctx.Err()
	}}

	var workers workerGroup
	model := modelRuntimeForTest(t, db, p)
	taskRuntime := taskRuntimeForTest(t, model, t.TempDir())
	disp := startTaskDispatcher(t, db, &workers, model, taskRuntime)

	sessionID := startTaskSession(t, ctx, db, &workers, disp, project.ID, "long task")

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was never invoked")
	}

	cancel := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "session-cancel",
		SessionID: sessionID,
		Reason:    "test cancel",
	})
	if !cancel.OK {
		t.Fatalf("session-cancel response = %#v", cancel)
	}

	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusCanceled, 5*time.Second)

	if got := readPrimaryTracked(t, repoRoot); got != "baseline\n" {
		t.Fatalf("primary changed after cancellation: %q", got)
	}
}

func TestTaskWorkerRestartResolvesExistingProposalWithoutRerun(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	repoRoot := newDaemonGitRepo(t)
	project, _ := db.CreateProject(ctx, "proj", repoRoot)

	p := proposalProvider("rewrite", `{"op":"write","path":"tracked.txt","content":"changed\n"}`)

	var workers workerGroup
	model := modelRuntimeForTest(t, db, p)
	taskRuntime := taskRuntimeForTest(t, model, t.TempDir())
	disp := startTaskDispatcher(t, db, &workers, model, taskRuntime)

	sessionID := startTaskSession(t, ctx, db, &workers, disp, project.ID, "rewrite")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusWaitingForUser, 5*time.Second)

	// Answer the approval, then simulate a restart: the execution is resumed in
	// the database and a fresh worker is invoked directly, as startup recovery
	// would, without the dispatcher having re-queued or re-run the model loop.
	interaction, err := db.GetPendingHumanInput(ctx, sessionID)
	if err != nil || interaction == nil {
		t.Fatalf("GetPendingHumanInput: %v, %#v", err, interaction)
	}
	if _, _, err := db.AnswerHumanInput(ctx, interaction.ID, "approve"); err != nil {
		t.Fatalf("AnswerHumanInput: %v", err)
	}
	resumed, err := db.ResumeAfterHumanInput(ctx, sessionID)
	if err != nil {
		t.Fatalf("ResumeAfterHumanInput: %v", err)
	}

	taskRuntime.runTaskWorker(ctx, db, resumed)

	applied := waitForTaskPhase(t, db, sessionID, state.TaskPhaseApplied, 2*time.Second)
	if !applied.Applied {
		t.Fatal("restart did not apply the existing authorized proposal")
	}
	if p.callCount() != 1 {
		t.Fatalf("restart reran the model loop: provider calls = %d, want 1", p.callCount())
	}
	if got := readPrimaryTracked(t, repoRoot); got != "baseline\n" {
		t.Fatalf("primary changed during restart recovery: %q", got)
	}
}

func TestTaskCleanupRemovesWorkspaceAndPreservesDiff(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	repoRoot := newDaemonGitRepo(t)
	project, _ := db.CreateProject(ctx, "proj", repoRoot)

	p := proposalProvider("rewrite", `{"op":"write","path":"tracked.txt","content":"changed\n"}`)

	var workers workerGroup
	model := modelRuntimeForTest(t, db, p)
	taskRuntime := taskRuntimeForTest(t, model, t.TempDir())
	disp := startTaskDispatcher(t, db, &workers, model, taskRuntime)

	sessionID := startTaskSession(t, ctx, db, &workers, disp, project.ID, "rewrite")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusWaitingForUser, 5*time.Second)
	answerTaskApproval(t, ctx, db, &workers, disp, sessionID, "approve")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusCompleted, 5*time.Second)
	applied := waitForTaskPhase(t, db, sessionID, state.TaskPhaseApplied, 2*time.Second)

	cleanup := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "task-cleanup",
		SessionID: sessionID,
	})
	if !cleanup.OK {
		t.Fatalf("task-cleanup response = %#v", cleanup)
	}
	if _, err := os.Stat(applied.WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after cleanup: %v", err)
	}

	status := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "task-status",
		SessionID: sessionID,
	})
	if !status.OK || status.TaskRun == nil {
		t.Fatalf("task-status response = %#v", status)
	}
	if status.TaskRun.CleanedAt == nil {
		t.Fatal("cleanup did not record cleaned_at")
	}
	if status.TaskRun.WorkspaceDiff == "" {
		t.Fatal("cleanup discarded the durable diff")
	}
}

func TestTaskCleanupRefusesActiveExecution(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	repoRoot := newDaemonGitRepo(t)
	project, _ := db.CreateProject(ctx, "proj", repoRoot)

	started := make(chan struct{})
	release := make(chan struct{})
	p := &stubProvider{fn: func(_ context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
		close(started)
		<-release
		return provider.ChatResponse{Content: `{"action":"final","summary":"done"}`, Model: "test-model"}, nil
	}}

	var workers workerGroup
	model := modelRuntimeForTest(t, db, p)
	workspacesDir := t.TempDir()
	taskRuntime := taskRuntimeForTest(t, model, workspacesDir)
	disp := startTaskDispatcher(t, db, &workers, model, taskRuntime)

	sessionID := startTaskSession(t, ctx, db, &workers, disp, project.ID, "slow task")

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was never invoked")
	}

	// The workspace has been provisioned by now; cleanup must refuse while a
	// worker is live so it cannot delete a workspace in use.
	run := waitForTaskPhase(t, db, sessionID, state.TaskPhaseExecuting, 5*time.Second)
	if run.WorkspacePath == "" {
		t.Fatal("running task did not record a workspace path")
	}
	workspacePath := run.WorkspacePath
	if _, err := os.Stat(workspacePath); err != nil {
		t.Fatalf("expected workspace to exist: %v", err)
	}

	cleanup := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "task-cleanup",
		SessionID: sessionID,
	})
	if cleanup.OK {
		t.Fatalf("task-cleanup removed an active workspace: %#v", cleanup)
	}
	if _, err := os.Stat(workspacePath); err != nil {
		t.Fatalf("active workspace was deleted: %v", err)
	}

	close(release)
}
