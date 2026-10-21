package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/provider"
	"github.com/Hisha/Forgehand/internal/repocontext"
	"github.com/Hisha/Forgehand/internal/state"
)

// stubProvider is a deterministic provider.Provider used to drive the model
// worker without any network dependency.
type stubProvider struct {
	mu       sync.Mutex
	calls    int
	requests []provider.ChatRequest
	fn       func(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error)
}

func (p *stubProvider) Complete(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	p.mu.Lock()
	p.calls++
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	if p.fn == nil {
		return provider.ChatResponse{Content: "default content", Model: req.Model}, nil
	}
	return p.fn(ctx, req)
}

func (p *stubProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *stubProvider) lastRequest() provider.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		return provider.ChatRequest{}
	}
	return p.requests[len(p.requests)-1]
}

func modelRuntimeForTest(t *testing.T, db *state.Database, p provider.Provider) *modelRuntime {
	t.Helper()
	runtime := newModelRuntime(p, repocontext.NewBuilder(db))
	runtime.defaultModel = "test-model"
	runtime.planningQualified = true
	return runtime
}

// modelRequest performs one IPC round trip against the supplied dispatcher.
func modelRequest(
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

func startModelDispatcher(
	t *testing.T,
	db *state.Database,
	workers *workerGroup,
	model *modelRuntime,
) *dispatcher {
	t.Helper()

	dispCtx, stop := context.WithCancel(context.Background())
	disp := newDispatcher(db, workers, 4, model, nil)
	go disp.run(dispCtx)
	t.Cleanup(func() {
		stop()
		<-disp.done
		workers.stopAndWait()
	})
	return disp
}

func startCodingSession(
	t *testing.T,
	ctx context.Context,
	db *state.Database,
	workers *workerGroup,
	disp *dispatcher,
	task string,
) int64 {
	t.Helper()

	start := modelRequest(t, ctx, db, workers, disp, ipc.Request{
		Command:   "session-start",
		ProjectID: 1,
		Task:      task,
	})
	if !start.OK || start.Session == nil {
		t.Fatalf("session-start response = %#v", start)
	}
	if start.Session.Kind != "coding" {
		t.Fatalf("session kind = %q, want coding", start.Session.Kind)
	}

	run := modelRequest(t, ctx, db, workers, disp, ipc.Request{
		Command:   "session-run",
		SessionID: start.Session.ID,
	})
	if !run.OK || run.Execution == nil {
		t.Fatalf("session-run response = %#v", run)
	}
	if run.Execution.Kind != "coding" {
		t.Fatalf("execution kind = %q, want coding", run.Execution.Kind)
	}
	return start.Session.ID
}

func waitForTerminalExecution(
	t *testing.T,
	db *state.Database,
	sessionID int64,
	timeout time.Duration,
) state.SessionExecution {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		_, latest, err := db.SessionExecutionStatus(context.Background(), sessionID)
		if err != nil {
			t.Fatalf("SessionExecutionStatus: %v", err)
		}
		if latest != nil && latest.Status != state.ExecutionStatusRunning && latest.Status != state.ExecutionStatusQueued {
			return *latest
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for terminal status; latest = %#v", latest)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestModelWorkerSuccessEndToEnd(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	p := &stubProvider{fn: func(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Content: "summary: done", Model: "test-model"}, nil
	}}

	var workers workerGroup
	disp := startModelDispatcher(t, db, &workers, modelRuntimeForTest(t, db, p))
	sessionID := startCodingSession(t, ctx, db, &workers, disp, "summarize the worker code")

	execution := waitForTerminalExecution(t, db, sessionID, 5*time.Second)
	if execution.Status != state.ExecutionStatusCompleted {
		t.Fatalf("execution status = %q, want COMPLETED", execution.Status)
	}

	session, _, err := db.SessionExecutionStatus(ctx, sessionID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if session.State != state.SessionStateCompleted {
		t.Fatalf("session state = %q, want COMPLETED", session.State)
	}

	result, err := db.LatestModelResult(ctx, sessionID)
	if err != nil {
		t.Fatalf("LatestModelResult: %v", err)
	}
	if result.Content != "summary: done" || result.ExecutionID != execution.ID {
		t.Fatalf("unexpected result: %#v", result)
	}

	if p.callCount() != 1 {
		t.Fatalf("provider calls = %d, want 1", p.callCount())
	}
	last := p.lastRequest()
	if last.User == "" {
		t.Fatal("provider received an empty user message")
	}

	// The result must be retrievable through IPC.
	got := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "session-result",
		SessionID: sessionID,
	})
	if !got.OK || got.Result == nil || got.Result.Content != "summary: done" {
		t.Fatalf("session-result response = %#v", got)
	}

	events, _, err := db.ListSessionEvents(ctx, sessionID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type == state.EventTypeModelResponseCompleted {
			found = true
		}
	}
	if !found {
		t.Fatalf("model.response.completed event missing: %#v", events)
	}
}

func TestModelWorkerProviderFailureFailsAndContinuesQueue(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	p := &stubProvider{fn: func(_ context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{}, errors.New("provider unavailable")
	}}

	var workers workerGroup
	disp := startModelDispatcher(t, db, &workers, modelRuntimeForTest(t, db, p))

	first := startCodingSession(t, ctx, db, &workers, disp, "first task")
	second := startCodingSession(t, ctx, db, &workers, disp, "second task")

	for _, sessionID := range []int64{first, second} {
		execution := waitForTerminalExecution(t, db, sessionID, 5*time.Second)
		if execution.Status != state.ExecutionStatusFailed {
			t.Fatalf("session %d execution status = %q, want FAILED", sessionID, execution.Status)
		}
		if _, err := db.LatestModelResult(ctx, sessionID); !errors.Is(err, state.ErrNoModelResult) {
			t.Fatalf("session %d unexpectedly has a model result: %v", sessionID, err)
		}
		if execution.FailureKind != state.FailureKindProvider || !strings.Contains(execution.FailureReason, "provider unavailable") {
			t.Fatalf("session %d failure diagnostic = %#v", sessionID, execution)
		}
	}

	// Capacity must be released after both failures: a further execution runs.
	third := startCodingSession(t, ctx, db, &workers, disp, "third task")
	waitForTerminalExecution(t, db, third, 5*time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for workers.ownerCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("worker capacity not released: %d active", workers.ownerCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestModelWorkerCancellationLeavesNoResult(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	started := make(chan struct{})
	p := &stubProvider{fn: func(ctx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
		close(started)
		<-ctx.Done()
		return provider.ChatResponse{}, ctx.Err()
	}}

	var workers workerGroup
	disp := startModelDispatcher(t, db, &workers, modelRuntimeForTest(t, db, p))
	sessionID := startCodingSession(t, ctx, db, &workers, disp, "cancel me")

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not invoked")
	}

	cancel := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "session-cancel",
		SessionID: sessionID,
		Reason:    "test cancel",
	})
	if !cancel.OK {
		t.Fatalf("session-cancel response = %#v", cancel)
	}

	execution := waitForTerminalExecution(t, db, sessionID, 5*time.Second)
	if execution.Status != state.ExecutionStatusCanceled {
		t.Fatalf("execution status = %q, want CANCELED", execution.Status)
	}
	if _, err := db.LatestModelResult(ctx, sessionID); !errors.Is(err, state.ErrNoModelResult) {
		t.Fatalf("canceled execution committed a result: %v", err)
	}
}

func TestModelWorkerLateResponseCannotCompleteCanceledExecution(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)

	started := make(chan struct{})
	release := make(chan struct{})
	p := &stubProvider{fn: func(_ context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
		close(started)
		<-release
		// Ignore cancellation and return a valid response after the fact.
		return provider.ChatResponse{Content: "late success", Model: "test-model"}, nil
	}}

	var workers workerGroup
	disp := startModelDispatcher(t, db, &workers, modelRuntimeForTest(t, db, p))
	sessionID := startCodingSession(t, ctx, db, &workers, disp, "cancel then respond")

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not invoked")
	}

	cancel := modelRequest(t, ctx, db, &workers, disp, ipc.Request{
		Command:   "session-cancel",
		SessionID: sessionID,
		Reason:    "cancel before response",
	})
	if !cancel.OK {
		t.Fatalf("session-cancel response = %#v", cancel)
	}

	// Release the provider only after cancellation has been committed.
	close(release)

	execution := waitForTerminalExecution(t, db, sessionID, 5*time.Second)
	if execution.Status != state.ExecutionStatusCanceled {
		t.Fatalf("execution status = %q, want CANCELED", execution.Status)
	}

	// Give the worker a moment to attempt the late completion.
	time.Sleep(100 * time.Millisecond)
	if _, err := db.LatestModelResult(ctx, sessionID); !errors.Is(err, state.ErrNoModelResult) {
		t.Fatalf("late response committed a result for a canceled execution: %v", err)
	}
}
