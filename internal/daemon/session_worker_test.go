package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

func openWorkerTestDatabase(t *testing.T) *state.Database {
	t.Helper()

	db, err := state.OpenAt(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	return db
}

// TestConcurrentFakeWorkersAllComplete starts four workers at the same instant
// against one database. Every execution must reach COMPLETED; this reproduces
// the checkpoint contention that previously produced SQLITE_BUSY and left
// executions stuck in RUNNING.
func TestConcurrentFakeWorkersAllComplete(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	const workers = 4
	const steps = 5

	executions := make([]state.SessionExecution, 0, workers)
	for i := 0; i < workers; i++ {
		session, err := db.CreateSession(ctx, fmt.Sprintf("worker %d", i))
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		execution, err := db.StartSessionExecution(ctx, session.ID, steps)
		if err != nil {
			t.Fatalf("StartSessionExecution: %v", err)
		}
		executions = append(executions, execution)
	}

	start := make(chan struct{})
	var wait sync.WaitGroup

	for _, execution := range executions {
		wait.Add(1)
		go func(execution state.SessionExecution) {
			defer wait.Done()
			<-start
			runFakeWorker(ctx, db, execution, time.Millisecond)
		}(execution)
	}

	close(start)
	wait.Wait()

	for _, execution := range executions {
		session, latest, err := db.SessionExecutionStatus(ctx, execution.SessionID)
		if err != nil {
			t.Fatalf("SessionExecutionStatus: %v", err)
		}
		if latest == nil || latest.Status != state.ExecutionStatusCompleted {
			t.Fatalf("execution %d status = %#v, want COMPLETED", execution.ID, latest)
		}
		if latest.CurrentStep != steps {
			t.Fatalf("execution %d current step = %d, want %d", execution.ID, latest.CurrentStep, steps)
		}
		if session.State != state.SessionStateCompleted {
			t.Fatalf("session %d state = %q, want %q", session.ID, session.State, state.SessionStateCompleted)
		}
	}
}

// failingExecutionStore wraps a real database and injects a non-contention
// error at a chosen step, exercising the worker's terminal failure path.
type failingExecutionStore struct {
	*state.Database
	failStep int
}

func (s *failingExecutionStore) AdvanceSessionExecution(
	ctx context.Context,
	executionID int64,
	step int,
) error {
	if step == s.failStep {
		return errors.New("injected execution failure")
	}
	return s.Database.AdvanceSessionExecution(ctx, executionID, step)
}

func TestFakeWorkerInjectedFailureLeavesNoRunningExecution(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	session, err := db.CreateSession(ctx, "failing worker")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	store := &failingExecutionStore{Database: db, failStep: 3}
	runFakeWorker(ctx, store, execution, 0)

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if latest == nil {
		t.Fatal("execution missing after injected failure")
	}
	if latest.Status == state.ExecutionStatusRunning {
		t.Fatal("execution abandoned in RUNNING after injected failure")
	}
	if latest.Status != state.ExecutionStatusFailed {
		t.Fatalf("execution status = %q, want %q", latest.Status, state.ExecutionStatusFailed)
	}
	if latest.CurrentStep != 2 {
		t.Fatalf("current step = %d, want 2 (last committed checkpoint)", latest.CurrentStep)
	}
	if sessionState.State != state.SessionStateFailed {
		t.Fatalf("session state = %q, want %q", sessionState.State, state.SessionStateFailed)
	}
}

func TestFakeWorkerCancellationPreservesCheckpoint(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	session, err := db.CreateSession(ctx, "cancelled worker")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	for step := 1; step <= 2; step++ {
		if err := db.AdvanceSessionExecution(ctx, execution.ID, step); err != nil {
			t.Fatalf("advance to step %d: %v", step, err)
		}
	}

	workerCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runFakeWorker(workerCtx, db, execution, time.Hour)
	}()

	cancel()
	<-done

	sessionState, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if latest == nil || latest.Status != state.ExecutionStatusInterrupted {
		t.Fatalf("execution after cancellation = %#v, want INTERRUPTED", latest)
	}
	if latest.CurrentStep != 2 {
		t.Fatalf("current step = %d, want 2 (checkpoint preserved)", latest.CurrentStep)
	}
	if sessionState.State != state.SessionStateInterrupted {
		t.Fatalf("session state = %q, want %q", sessionState.State, state.SessionStateInterrupted)
	}
}

func TestFakeWorkerResumesFromCommittedCheckpoint(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	session, err := db.CreateSession(ctx, "resumed worker")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}

	for step := 1; step <= 3; step++ {
		if err := db.AdvanceSessionExecution(ctx, execution.ID, step); err != nil {
			t.Fatalf("advance to step %d: %v", step, err)
		}
	}
	if err := db.InterruptSessionExecution(ctx, execution.ID); err != nil {
		t.Fatalf("InterruptSessionExecution: %v", err)
	}

	resumed, err := db.ResumeSessionExecution(ctx, session.ID)
	if err != nil {
		t.Fatalf("ResumeSessionExecution: %v", err)
	}
	if resumed.CurrentStep != 3 {
		t.Fatalf("resumed current step = %d, want 3", resumed.CurrentStep)
	}

	runFakeWorker(ctx, db, resumed, 0)

	_, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if latest == nil || latest.Status != state.ExecutionStatusCompleted {
		t.Fatalf("resumed execution = %#v, want COMPLETED", latest)
	}
	if latest.CurrentStep != 5 {
		t.Fatalf("current step = %d, want 5", latest.CurrentStep)
	}
}

func roundTripRequest(
	t *testing.T,
	ctx context.Context,
	db *state.Database,
	workers *workerGroup,
	request ipc.Request,
) ipc.Response {
	t.Helper()

	dispCtx, stopDispatcher := context.WithCancel(ctx)
	dispatcher := newDispatcher(db, workers, 16, nil)
	go dispatcher.run(dispCtx)
	t.Cleanup(func() {
		stopDispatcher()
		<-dispatcher.done
	})

	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleConnection(ctx, db, workers, dispatcher, server, ClientIdentity{})
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

func TestSessionStatusIPCReportsExecutionProgress(t *testing.T) {
	db := openWorkerTestDatabase(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var workers workerGroup

	start := roundTripRequest(t, ctx, db, &workers, ipc.Request{
		Command: "session-start",
		Title:   "ipc status",
	})
	if !start.OK || start.Session == nil {
		t.Fatalf("session-start response = %#v", start)
	}
	sessionID := start.Session.ID

	before := roundTripRequest(t, ctx, db, &workers, ipc.Request{
		Command:   "session-status",
		SessionID: sessionID,
	})
	if !before.OK || before.Session == nil {
		t.Fatalf("session-status (before run) = %#v", before)
	}
	if before.Session.State != state.SessionStateReady {
		t.Fatalf("session state = %q, want %q", before.Session.State, state.SessionStateReady)
	}
	if before.Execution != nil {
		t.Fatalf("unexpected execution before run: %#v", before.Execution)
	}

	run := roundTripRequest(t, ctx, db, &workers, ipc.Request{
		Command:   "session-run",
		SessionID: sessionID,
	})
	if !run.OK || run.Execution == nil {
		t.Fatalf("session-run response = %#v", run)
	}

	waitForExecutionStatus(t, db, sessionID, state.ExecutionStatusRunning, 6*time.Second)

	status := roundTripRequest(t, ctx, db, &workers, ipc.Request{
		Command:   "session-status",
		SessionID: sessionID,
	})
	if !status.OK || status.Session == nil || status.Execution == nil {
		t.Fatalf("session-status (running) = %#v", status)
	}
	if status.Session.State != state.SessionStateRunning {
		t.Fatalf("session state = %q, want %q", status.Session.State, state.SessionStateRunning)
	}
	if status.Execution.Status != state.ExecutionStatusRunning {
		t.Fatalf("execution status = %q, want %q", status.Execution.Status, state.ExecutionStatusRunning)
	}
	if status.Execution.TotalSteps != fakeWorkerSteps {
		t.Fatalf("total steps = %d, want %d", status.Execution.TotalSteps, fakeWorkerSteps)
	}

	cancel()
	workers.stopAndWait()

	sessionState, _, err := db.SessionExecutionStatus(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if sessionState.State != state.SessionStateInterrupted {
		t.Fatalf("session state after cancellation = %q, want %q", sessionState.State, state.SessionStateInterrupted)
	}
}

func TestDaemonSessionEventsIPCFollowsEstablishedPattern(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	workers := &workerGroup{}

	resp1 := roundTripRequest(t, ctx, db, workers, ipc.Request{Command: "session-start", Title: "test"})
	if !resp1.OK || resp1.Session == nil {
		t.Fatalf("start failed: %+v", resp1)
	}
	sessionID := resp1.Session.ID

	resp2 := roundTripRequest(t, ctx, db, workers, ipc.Request{Command: "session-events", SessionID: sessionID})
	if !resp2.OK {
		t.Fatalf("events failed: %+v", resp2)
	}
	if len(resp2.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(resp2.Events))
	}

	workers.stopAndWait()
}
