package daemon

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

func queueResourceExecution(
	t *testing.T,
	ctx context.Context,
	db *state.Database,
	title string,
	totalSteps int,
	resources ...string,
) state.SessionExecution {
	t.Helper()

	session, err := db.CreateSession(ctx, title)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.QueueSessionExecution(ctx, session.ID, totalSteps, resources...)
	if err != nil {
		t.Fatalf("QueueSessionExecution: %v", err)
	}
	return execution
}

func executionStatus(
	t *testing.T,
	db *state.Database,
	sessionID int64,
) string {
	t.Helper()

	_, latest, err := db.SessionExecutionStatus(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if latest == nil {
		t.Fatalf("session %d has no execution", sessionID)
	}
	return latest.Status
}

func TestWorkerGroupResourceAcquisition(t *testing.T) {
	var workers workerGroup

	if !workers.acquireResources(1, []string{"gpu", "cpu"}) {
		t.Fatal("acquireResources(1, gpu+cpu) failed")
	}
	if got := workers.heldResources(); len(got) != 2 || got[0] != "cpu" || got[1] != "gpu" {
		t.Fatalf("heldResources() = %v, want [cpu gpu]", got)
	}

	// All-or-nothing: a conflicting set is refused entirely.
	if workers.acquireResources(2, []string{"gpu", "disk"}) {
		t.Fatal("acquireResources(2, gpu+disk) succeeded while gpu was held")
	}
	if got := workers.heldResources(); len(got) != 2 {
		t.Fatalf("heldResources() after refused acquire = %v, want [cpu gpu]", got)
	}

	// A disjoint set succeeds and appears in the snapshot.
	if !workers.acquireResources(3, []string{"disk"}) {
		t.Fatal("acquireResources(3, disk) failed")
	}

	blocks := workers.resourceBlocks([]string{"disk", "gpu", "tape"})
	if len(blocks) != 2 {
		t.Fatalf("resourceBlocks = %#v, want 2 blocks", blocks)
	}
	if blocks[0].resource != "disk" || blocks[0].holder != 3 {
		t.Fatalf("blocks[0] = %#v, want disk held by 3", blocks[0])
	}
	if blocks[1].resource != "gpu" || blocks[1].holder != 1 {
		t.Fatalf("blocks[1] = %#v, want gpu held by 1", blocks[1])
	}

	// Releasing an execution frees only its own resources.
	workers.release(1)
	if got := workers.heldResources(); len(got) != 1 || got[0] != "disk" {
		t.Fatalf("heldResources() after release = %v, want [disk]", got)
	}
	// Release is idempotent for unknown executions.
	workers.release(99)
}

// TestDispatcherExcludesSameResourceAndSkipsAhead runs three executions
// through a capacity-2 dispatcher: A(gpu) and C(cpu) must run concurrently
// while B(gpu) stays QUEUED behind A's resource, then B must start once A
// completes and releases the resource.
func TestDispatcherExcludesSameResourceAndSkipsAhead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	db := openWorkerTestDatabase(t)
	var workers workerGroup
	defer func() {
		cancel()
		workers.stopAndWait()
	}()

	a := queueResourceExecution(t, ctx, db, "a-gpu", 1, "gpu")
	b := queueResourceExecution(t, ctx, db, "b-gpu", 1, "gpu")
	c := queueResourceExecution(t, ctx, db, "c-cpu", 1, "cpu")

	disp := newDispatcher(db, &workers, 2, nil, nil)
	go disp.run(ctx)
	t.Cleanup(func() { <-disp.done })

	// A and C run; B is resource-blocked.
	waitForCondition(t, 5*time.Second, "A and C running", func() bool {
		return executionStatus(t, db, a.SessionID) == state.ExecutionStatusRunning &&
			executionStatus(t, db, c.SessionID) == state.ExecutionStatusRunning
	})
	if status := executionStatus(t, db, b.SessionID); status != state.ExecutionStatusQueued {
		t.Fatalf("B status = %q, want QUEUED (blocked by A's gpu)", status)
	}

	// Neither running execution may ever hold the same resource.
	if got := workers.heldResources(); len(got) != 2 || got[0] != "cpu" || got[1] != "gpu" {
		t.Fatalf("heldResources() = %v, want [cpu gpu]", got)
	}

	// A completes; the dispatcher then starts B with the freed gpu.
	waitForCondition(t, 20*time.Second, "B running after A completes", func() bool {
		return executionStatus(t, db, b.SessionID) == state.ExecutionStatusRunning
	})

	waitForCondition(t, 20*time.Second, "B completed", func() bool {
		return executionStatus(t, db, b.SessionID) == state.ExecutionStatusCompleted
	})
}

// TestDispatcherReleasesResourceOnHumanWait proves that a worker entering
// WAITING_FOR_USER frees its resources so a conflicting queued execution can
// start while the first session waits for a human.
func TestDispatcherReleasesResourceOnHumanWait(t *testing.T) {
	t.Setenv("FORGEHAND_FAKE_WORKER_PAUSE_STEP", "1")

	ctx, cancel := context.WithCancel(context.Background())

	db := openWorkerTestDatabase(t)
	var workers workerGroup
	defer func() {
		cancel()
		workers.stopAndWait()
	}()

	a := queueResourceExecution(t, ctx, db, "waits", 5, "gpu")
	b := queueResourceExecution(t, ctx, db, "follower", 1, "gpu")

	disp := newDispatcher(db, &workers, 2, nil, nil)
	go disp.run(ctx)
	t.Cleanup(func() { <-disp.done })

	waitForCondition(t, 20*time.Second, "A waiting for user", func() bool {
		return executionStatus(t, db, a.SessionID) == state.ExecutionStatusWaitingForUser
	})

	// A's resource must be free again and B must have started.
	waitForCondition(t, 10*time.Second, "B running while A waits", func() bool {
		return executionStatus(t, db, b.SessionID) == state.ExecutionStatusRunning
	})
}

// TestDispatcherReleasesResourceOnCancel proves that canceling a running
// execution frees its resources so a conflicting queued execution starts.
func TestDispatcherReleasesResourceOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	db := openWorkerTestDatabase(t)
	var workers workerGroup
	defer func() {
		cancel()
		workers.stopAndWait()
	}()

	a := queueResourceExecution(t, ctx, db, "canceled", 5, "gpu")
	b := queueResourceExecution(t, ctx, db, "follower", 1, "gpu")

	disp := newDispatcher(db, &workers, 2, nil, nil)
	go disp.run(ctx)
	t.Cleanup(func() { <-disp.done })

	waitForCondition(t, 5*time.Second, "A running", func() bool {
		return executionStatus(t, db, a.SessionID) == state.ExecutionStatusRunning
	})

	if _, err := db.CancelSessionExecution(ctx, a.SessionID, "test cancel"); err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}

	waitForCondition(t, 10*time.Second, "B running after A canceled", func() bool {
		return executionStatus(t, db, b.SessionID) == state.ExecutionStatusRunning
	})
}

// TestDispatcherReleasesResourceOnFailure proves that a failed execution
// (here a coding execution with no provider configured) frees its resources.
func TestDispatcherReleasesResourceOnFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	db := openWorkerTestDatabase(t)
	var workers workerGroup
	defer func() {
		cancel()
		workers.stopAndWait()
	}()

	projectID := int64(1)
	task := "fail"
	coding, err := db.CreateCodingSession(ctx, "failing", projectID, task, "test", "model")
	if err != nil {
		t.Fatalf("CreateCodingSession: %v", err)
	}
	if _, err := db.QueueSessionExecution(ctx, coding.ID, 1, "gpu"); err != nil {
		t.Fatalf("QueueSessionExecution: %v", err)
	}
	b := queueResourceExecution(t, ctx, db, "follower", 1, "gpu")

	// nil model runtime: the coding execution fails immediately in the worker.
	disp := newDispatcher(db, &workers, 2, nil, nil)
	go disp.run(ctx)
	t.Cleanup(func() { <-disp.done })

	waitForCondition(t, 5*time.Second, "coding execution failed", func() bool {
		return executionStatus(t, db, coding.ID) == state.ExecutionStatusFailed
	})

	waitForCondition(t, 10*time.Second, "B running after failure", func() bool {
		return executionStatus(t, db, b.SessionID) == state.ExecutionStatusRunning
	})
}

// TestResourceOwnershipDoesNotSurviveRestart simulates daemon interruption:
// an execution is dispatched and holding a resource, then the daemon dies
// without any release path running. Startup recovery must leave no stale
// ownership, so an execution requiring the same resource dispatches
// immediately on the fresh worker group.
func TestResourceOwnershipDoesNotSurviveRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	db := openWorkerTestDatabase(t)

	var crashed workerGroup
	defer crashed.stopAndWait()
	a := queueResourceExecution(t, ctx, db, "crashed", 5, "gpu")
	b := queueResourceExecution(t, ctx, db, "after restart", 1, "gpu")

	claimed, ok, err := db.ClaimNextQueuedExecution(ctx, nil)
	if err != nil || !ok {
		t.Fatalf("ClaimNextQueuedExecution ok=%v err=%v", ok, err)
	}
	if err := launchExecutionWorker(ctx, db, &crashed, nil, nil, claimed, nil); err != nil {
		t.Fatalf("launchExecutionWorker: %v", err)
	}
	waitForCondition(t, 5*time.Second, "A holding gpu", func() bool {
		return len(crashed.heldResources()) == 1
	})

	// Simulate SIGKILL: abandon the old worker group entirely and run startup
	// recovery on a fresh one.
	cancel()
	var restarted workerGroup
	interrupted, err := db.InterruptRunningExecutions(context.Background())
	if err != nil {
		t.Fatalf("InterruptRunningExecutions: %v", err)
	}
	if interrupted != 1 {
		t.Fatalf("interrupted executions = %d, want 1", interrupted)
	}

	if got := restarted.heldResources(); len(got) != 0 {
		t.Fatalf("heldResources() after restart = %v, want empty", got)
	}
	// No durable ownership records exist to go stale.
	blocked, err := db.CountResourceBlockedQueuedExecutions(
		context.Background(),
		restarted.heldResources(),
	)
	if err != nil {
		t.Fatalf("CountResourceBlockedQueuedExecutions: %v", err)
	}
	if blocked != 0 {
		t.Fatalf("resource-blocked executions after restart = %d, want 0", blocked)
	}

	// B must dispatch immediately; A (interrupted) stays behind until resumed.
	claimed, ok, err = db.ClaimNextQueuedExecution(context.Background(), restarted.heldResources())
	if err != nil || !ok {
		t.Fatalf("post-restart ClaimNextQueuedExecution ok=%v err=%v", ok, err)
	}
	if claimed.ID != b.ID {
		t.Fatalf("post-restart claim = %d, want B %d", claimed.ID, b.ID)
	}

	if status := executionStatus(t, db, a.SessionID); status != state.ExecutionStatusInterrupted {
		t.Fatalf("A status after restart = %q, want INTERRUPTED", status)
	}
}

// roundTripRequestOn runs one IPC request against a shared dispatcher and
// worker group so cross-request state (claims, resource ownership) survives
// between calls, unlike the per-request roundTripRequest helper.
func roundTripRequestOn(
	t *testing.T,
	ctx context.Context,
	db *state.Database,
	workers *workerGroup,
	disp *dispatcher,
	request ipc.Request,
) ipc.Response {
	t.Helper()

	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleConnection(ctx, db, workers, disp, server, ClientIdentity{})
	}()

	if err := json.NewEncoder(client).Encode(request); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	var response ipc.Response
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	client.Close()
	<-done

	return response
}

// TestSessionStatusReportsResourceBlocker proves the IPC status surface names
// the resource and holding execution that keep a queued execution waiting.
func TestSessionStatusReportsResourceBlocker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	db := openWorkerTestDatabase(t)
	var workers workerGroup
	defer func() {
		cancel()
		workers.stopAndWait()
	}()

	a := queueResourceExecution(t, ctx, db, "holder", 5, "gpu")
	if _, ok, err := db.ClaimNextQueuedExecution(ctx, nil); err != nil || !ok {
		t.Fatalf("ClaimNextQueuedExecution ok=%v err=%v", ok, err)
	}
	if err := launchExecutionWorker(ctx, db, &workers, nil, nil, a, nil); err != nil {
		t.Fatalf("launchExecutionWorker: %v", err)
	}
	waitForCondition(t, 5*time.Second, "A holding gpu", func() bool {
		return len(workers.heldResources()) == 1
	})

	b := queueResourceExecution(t, ctx, db, "blocked", 1, "gpu")

	status := roundTripRequestOn(t, ctx, db, &workers, nil, ipc.Request{
		Command:   "session-status",
		SessionID: b.SessionID,
	})
	if !status.OK || status.Execution == nil {
		t.Fatalf("session-status response = %#v", status)
	}
	if len(status.Execution.Resources) != 1 || status.Execution.Resources[0] != "gpu" {
		t.Fatalf("execution resources = %v, want [gpu]", status.Execution.Resources)
	}
	if len(status.Execution.BlockedBy) != 1 {
		t.Fatalf("blocked_by = %#v, want one block", status.Execution.BlockedBy)
	}
	block := status.Execution.BlockedBy[0]
	if block.Resource != "gpu" || block.HeldByExecutionID != a.ID {
		t.Fatalf("block = %#v, want gpu held by %d", block, a.ID)
	}

	daemonStatus := roundTripRequestOn(t, ctx, db, &workers, nil, ipc.Request{
		Command: "status",
	})
	if !daemonStatus.OK {
		t.Fatalf("status response = %#v", daemonStatus)
	}
	if daemonStatus.ResourceBlockedExecutions != 1 {
		t.Fatalf(
			"resource-blocked executions = %d, want 1",
			daemonStatus.ResourceBlockedExecutions,
		)
	}
}

// TestSessionRunAcceptsResources verifies the session-run IPC path persists
// requested resources and reflects them back to the caller.
func TestSessionRunAcceptsResources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	db := openWorkerTestDatabase(t)
	var workers workerGroup
	defer func() {
		cancel()
		workers.stopAndWait()
	}()

	created := roundTripRequestOn(t, ctx, db, &workers, nil, ipc.Request{
		Command: "session-start",
		Title:   "resources",
	})
	if !created.OK || created.Session == nil {
		t.Fatalf("session-start response = %#v", created)
	}

	run := roundTripRequestOn(t, ctx, db, &workers, nil, ipc.Request{
		Command:   "session-run",
		SessionID: created.Session.ID,
		Resources: []string{" gpu ", "cpu"},
	})
	if !run.OK || run.Execution == nil {
		t.Fatalf("session-run response = %#v", run)
	}
	if len(run.Execution.Resources) != 2 ||
		run.Execution.Resources[0] != "cpu" ||
		run.Execution.Resources[1] != "gpu" {
		t.Fatalf("execution resources = %v, want [cpu gpu]", run.Execution.Resources)
	}
}
