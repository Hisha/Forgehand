package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

func TestTaskDiffShowsProposedAndAppliedChanges(t *testing.T) {
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
	waitForTaskPhase(t, db, sessionID, state.TaskPhaseProposed, 5*time.Second)

	// The proposed diff must be visible before approval.
	diffResp := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "task-diff",
		SessionID: sessionID,
	})
	if !diffResp.OK {
		t.Fatalf("task-diff response = %#v", diffResp)
	}
	if diffResp.DiffSource != "proposed" {
		t.Fatalf("diff source = %q, want proposed", diffResp.DiffSource)
	}
	if !strings.Contains(diffResp.Diff, "diff --git a/tracked.txt b/tracked.txt") {
		t.Fatalf("proposed diff missing git header:\n%s", diffResp.Diff)
	}
	if !strings.Contains(diffResp.Diff, "+changed") {
		t.Fatalf("proposed diff missing added content:\n%s", diffResp.Diff)
	}

	// Previewing must not modify the workspace.
	if got := readPrimaryTracked(t, repoRoot); got != "baseline\n" {
		t.Fatalf("primary changed after preview: %q", got)
	}

	// Approve through the one-command path.
	approve := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "task-approve",
		SessionID: sessionID,
	})
	if !approve.OK || !approve.AnswerApplied {
		t.Fatalf("task-approve response = %#v", approve)
	}
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusCompleted, 5*time.Second)
	applied := waitForTaskPhase(t, db, sessionID, state.TaskPhaseApplied, 2*time.Second)

	// After apply the diff reflects the applied workspace state.
	appliedDiff := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "task-diff",
		SessionID: sessionID,
	})
	if !appliedDiff.OK {
		t.Fatalf("applied task-diff response = %#v", appliedDiff)
	}
	if appliedDiff.DiffSource != "applied" {
		t.Fatalf("applied diff source = %q, want applied", appliedDiff.DiffSource)
	}
	if !strings.Contains(appliedDiff.Diff, "tracked.txt") {
		t.Fatalf("applied diff missing tracked.txt:\n%s", appliedDiff.Diff)
	}
	if applied.WorkspaceDiff == "" {
		t.Fatal("applied run did not persist a diff")
	}
}

func TestTaskRejectOneCommandAppliesNothing(t *testing.T) {
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
	waitForTaskPhase(t, db, sessionID, state.TaskPhaseProposed, 5*time.Second)

	reject := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "task-reject",
		SessionID: sessionID,
	})
	if !reject.OK || !reject.AnswerApplied {
		t.Fatalf("task-reject response = %#v", reject)
	}
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusCompleted, 5*time.Second)

	run := waitForTaskPhase(t, db, sessionID, state.TaskPhaseRejected, 2*time.Second)
	if run.Applied {
		t.Fatal("rejected run marked applied")
	}
	if got := readPrimaryTracked(t, repoRoot); got != "baseline\n" {
		t.Fatalf("primary changed on rejection: %q", got)
	}
}

func TestTaskApproveRejectsNonProposedPhase(t *testing.T) {
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
	waitForTaskPhase(t, db, sessionID, state.TaskPhaseProposed, 5*time.Second)
	answerTaskApproval(t, ctx, db, &workers, disp, sessionID, "approve")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusCompleted, 5*time.Second)
	waitForTaskPhase(t, db, sessionID, state.TaskPhaseApplied, 2*time.Second)

	// Approving an already-applied task must fail cleanly.
	approve := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "task-approve",
		SessionID: sessionID,
	})
	if approve.OK {
		t.Fatalf("task-approve accepted an applied task: %#v", approve)
	}
	if !strings.Contains(approve.Message, "not awaiting approval") {
		t.Fatalf("approve message = %q", approve.Message)
	}
}

func TestTaskDiffUnknownSession(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	var workers workerGroup
	disp := startTaskDispatcher(t, db, &workers, nil, nil)

	resp := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "task-diff",
		SessionID: 99999,
	})
	if resp.OK {
		t.Fatalf("task-diff accepted unknown session: %#v", resp)
	}
}

func TestTaskDiffCreationAndDeletion(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	repoRoot := newDaemonGitRepo(t)
	project, _ := db.CreateProject(ctx, "proj", repoRoot)

	p := proposalProvider("add and remove", `{"op":"write","path":"added.go","content":"package main\n"},{"op":"delete","path":"tracked.txt"}`)

	var workers workerGroup
	model := modelRuntimeForTest(t, db, p)
	taskRuntime := taskRuntimeForTest(t, model, t.TempDir())
	disp := startTaskDispatcher(t, db, &workers, model, taskRuntime)

	sessionID := startTaskSession(t, ctx, db, &workers, disp, project.ID, "add and remove")
	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusWaitingForUser, 5*time.Second)
	waitForTaskPhase(t, db, sessionID, state.TaskPhaseProposed, 5*time.Second)

	diffResp := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "task-diff",
		SessionID: sessionID,
	})
	if !diffResp.OK {
		t.Fatalf("task-diff response = %#v", diffResp)
	}
	if !strings.Contains(diffResp.Diff, "+++ b/added.go") {
		t.Fatalf("diff missing creation of added.go:\n%s", diffResp.Diff)
	}
	if !strings.Contains(diffResp.Diff, "--- a/tracked.txt") {
		t.Fatalf("diff missing deletion of tracked.txt:\n%s", diffResp.Diff)
	}

	// The workspace must still be untouched by the preview.
	if got := readPrimaryTracked(t, repoRoot); got != "baseline\n" {
		t.Fatalf("primary changed after preview: %q", got)
	}
	_ = os.Remove(filepath.Join(repoRoot, "added.go"))
}

func TestVerifyApprovalBinding(t *testing.T) {
	run := state.TaskRun{ProposalHash: "hash-a"}
	interaction := state.HumanInteraction{
		Context: map[string]any{"proposal_hash": "hash-a"},
	}
	if err := verifyApprovalBinding(run, interaction); err != nil {
		t.Fatalf("matching binding rejected: %v", err)
	}

	stale := state.HumanInteraction{
		Context: map[string]any{"proposal_hash": "hash-b"},
	}
	if err := verifyApprovalBinding(run, stale); err == nil {
		t.Fatal("mismatched binding accepted")
	}

	unbound := state.HumanInteraction{}
	if err := verifyApprovalBinding(run, unbound); err == nil {
		t.Fatal("unbound interaction accepted")
	}

	missing := state.HumanInteraction{Context: map[string]any{}}
	if err := verifyApprovalBinding(run, missing); err == nil {
		t.Fatal("interaction without hash accepted")
	}

	noHash := state.TaskRun{ProposalHash: ""}
	ok := state.HumanInteraction{Context: map[string]any{"proposal_hash": "hash-a"}}
	if err := verifyApprovalBinding(noHash, ok); err == nil {
		t.Fatal("run without proposal hash accepted")
	}
}
