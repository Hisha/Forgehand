package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/state"
)

func waitForCondition(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNewDispatcherClampsCapacity(t *testing.T) {
	for _, testCase := range []struct {
		input int
		want  int
	}{
		{0, 1},
		{-3, 1},
		{1, 1},
		{4, 4},
	} {
		disp := newDispatcher(nil, nil, testCase.input, nil, nil)
		if got := disp.capacity(); got != testCase.want {
			t.Fatalf("newDispatcher(%d).capacity() = %d, want %d", testCase.input, got, testCase.want)
		}
	}
}

// TestDispatcherRespectsCapacityAndFIFO verifies that a dispatcher never runs
// more workers than its capacity and that it claims the oldest queued execution
// first.
func TestDispatcherRespectsCapacityAndFIFO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	db := openWorkerTestDatabase(t)
	var workers workerGroup
	defer func() {
		cancel()
		workers.stopAndWait()
	}()

	var executions []state.SessionExecution
	for i := 0; i < 3; i++ {
		session, err := db.CreateSession(ctx, "capacity")
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		execution, err := db.QueueSessionExecution(ctx, session.ID, fakeWorkerSteps)
		if err != nil {
			t.Fatalf("QueueSessionExecution: %v", err)
		}
		executions = append(executions, execution)
	}

	disp := newDispatcher(db, &workers, 2, nil, nil)
	go disp.run(ctx)
	t.Cleanup(func() { <-disp.done })

	waitForCondition(t, 5*time.Second, "two running workers", func() bool {
		return workers.ownerCount() == 2
	})

	for i := 0; i < 2; i++ {
		_, latest, err := db.SessionExecutionStatus(ctx, executions[i].SessionID)
		if err != nil {
			t.Fatalf("SessionExecutionStatus: %v", err)
		}
		if latest == nil || latest.Status != state.ExecutionStatusRunning {
			t.Fatalf("execution %d = %#v, want RUNNING", i, latest)
		}
	}

	_, third, err := db.SessionExecutionStatus(ctx, executions[2].SessionID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if third == nil || third.Status != state.ExecutionStatusQueued {
		t.Fatalf("third execution = %#v, want QUEUED", third)
	}

	queued, err := db.CountQueuedExecutions(ctx)
	if err != nil {
		t.Fatalf("CountQueuedExecutions: %v", err)
	}
	if queued != 1 {
		t.Fatalf("queued count = %d, want 1", queued)
	}
	if workers.ownerCount() != 2 {
		t.Fatalf("owner count = %d, want 2", workers.ownerCount())
	}
}

// TestDispatcherStartsNextWhenCapacityFrees verifies that a canceled worker
// notifies the dispatcher, which then starts the next queued execution.
func TestDispatcherStartsNextWhenCapacityFrees(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	db := openWorkerTestDatabase(t)
	var workers workerGroup
	defer func() {
		cancel()
		workers.stopAndWait()
	}()

	var executions []state.SessionExecution
	for i := 0; i < 2; i++ {
		session, err := db.CreateSession(ctx, "wakeup")
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		execution, err := db.QueueSessionExecution(ctx, session.ID, fakeWorkerSteps)
		if err != nil {
			t.Fatalf("QueueSessionExecution: %v", err)
		}
		executions = append(executions, execution)
	}

	disp := newDispatcher(db, &workers, 1, nil, nil)
	go disp.run(ctx)
	t.Cleanup(func() { <-disp.done })

	waitForCondition(t, 5*time.Second, "first worker running", func() bool {
		return workers.ownerCount() == 1
	})

	_, second, err := db.SessionExecutionStatus(ctx, executions[1].SessionID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if second == nil || second.Status != state.ExecutionStatusQueued {
		t.Fatalf("second execution = %#v, want QUEUED", second)
	}

	if _, err := db.CancelSessionExecution(ctx, executions[0].SessionID, "free capacity"); err != nil {
		t.Fatalf("CancelSessionExecution: %v", err)
	}
	workers.cancel(executions[0].ID)

	waitForCondition(t, 5*time.Second, "second worker running", func() bool {
		_, latest, err := db.SessionExecutionStatus(ctx, executions[1].SessionID)
		if err != nil {
			return false
		}
		return latest != nil && latest.Status == state.ExecutionStatusRunning
	})
}
