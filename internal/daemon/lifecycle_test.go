package daemon

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/provider"
	"github.com/Hisha/Forgehand/internal/state"
)

type lifecycleTestInspector struct {
	mu    sync.Mutex
	state state.RestartWorkState
	err   error
	calls int
}

func (s *lifecycleTestInspector) InspectRestartWorkState(context.Context) (state.RestartWorkState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.state, s.err
}

func newHealthyTestLifecycle(store restartWorkInspector, workers *workerGroup) (*lifecycleCoordinator, *dispatcher) {
	coordinator := newLifecycleCoordinator(store, workers)
	dispatcher := newDispatcher(nil, workers, 1, nil, nil)
	dispatcher.lifecycle = coordinator
	dispatcher.healthMu.Lock()
	dispatcher.running = true
	dispatcher.healthMu.Unlock()
	coordinator.setDispatcher(dispatcher)
	return coordinator, dispatcher
}

func waitForCoordinatorState(t *testing.T, coordinator *lifecycleCoordinator, want lifecycleState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for coordinator.lifecycleState() != want {
		if time.Now().After(deadline) {
			t.Fatalf("lifecycle state = %q, want %q", coordinator.lifecycleState(), want)
		}
		runtime.Gosched()
	}
}

func TestLifecycleIdleDaemonCanPrepareAbortAndCommit(t *testing.T) {
	coordinator, _ := newHealthyTestLifecycle(&lifecycleTestInspector{}, &workerGroup{})

	preparation, readiness, err := coordinator.tryPrepareRestart(context.Background())
	if err != nil || !readiness.Safe || coordinator.lifecycleState() != lifecycleRestartPreparing {
		t.Fatalf("prepare = %#v readiness=%#v err=%v", preparation, readiness, err)
	}
	if err := coordinator.abortRestart(preparation); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if coordinator.lifecycleState() != lifecycleRunning {
		t.Fatalf("state after abort = %q", coordinator.lifecycleState())
	}

	preparation, readiness, err = coordinator.tryPrepareRestart(context.Background())
	if err != nil || !readiness.Safe {
		t.Fatalf("second prepare readiness=%#v err=%v", readiness, err)
	}
	if err := coordinator.commitRestart(preparation); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := coordinator.abortRestart(preparation); !errors.Is(err, errPreparationCommit) {
		t.Fatalf("abort committed preparation = %v", err)
	}
	if coordinator.lifecycleState() != lifecycleRestartCommitted {
		t.Fatalf("state after committed abort = %q", coordinator.lifecycleState())
	}
}

func TestLifecycleActiveWorkerPreventsPreparationWithoutInterruptingIt(t *testing.T) {
	workers := &workerGroup{}
	workerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !workers.claim(41, cancel) {
		t.Fatal("claim worker")
	}
	defer workers.release(41)
	coordinator, _ := newHealthyTestLifecycle(&lifecycleTestInspector{}, workers)

	_, readiness, err := coordinator.tryPrepareRestart(context.Background())
	if err != nil || readiness.Safe || readiness.ActiveWorkers != 1 {
		t.Fatalf("readiness=%#v err=%v", readiness, err)
	}
	select {
	case <-workerCtx.Done():
		t.Fatal("restart preparation interrupted an active worker")
	default:
	}
	if coordinator.lifecycleState() != lifecycleRunning {
		t.Fatalf("failed preparation left state %q", coordinator.lifecycleState())
	}
}

func TestLifecycleQueuedExecutionPreventsPreparation(t *testing.T) {
	db := openWorkerTestDatabase(t)
	session, err := db.CreateSession(context.Background(), "queued")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueSessionExecution(context.Background(), session.ID, 1); err != nil {
		t.Fatal(err)
	}
	coordinator, dispatcher := newHealthyTestLifecycle(db, &workerGroup{})
	dispatcher.db = db

	_, readiness, err := coordinator.tryPrepareRestart(context.Background())
	if err != nil || readiness.Safe || readiness.Durable.QueuedExecutions != 1 {
		t.Fatalf("readiness=%#v err=%v", readiness, err)
	}
}

func TestLifecycleAdmissionClosesBeforeReadinessAndCancellationReopensIt(t *testing.T) {
	coordinator, _ := newHealthyTestLifecycle(&lifecycleTestInspector{}, &workerGroup{})
	permit, err := coordinator.acquire(permitAdmission)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := coordinator.tryPrepareRestart(ctx)
		done <- err
	}()
	waitForCoordinatorState(t, coordinator, lifecycleRestartPreparing)
	if _, err := coordinator.acquire(permitAdmission); !errors.Is(err, errLifecycleConflict) {
		t.Fatalf("submission after closure = %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preparation = %v", err)
	}
	permit.release()
	if _, err := coordinator.acquire(permitAdmission); err != nil {
		t.Fatalf("admission did not reopen: %v", err)
	}
}

func TestLifecycleDispatcherClaimAndWorkerStartCannotCrossCommittedBoundary(t *testing.T) {
	db := openWorkerTestDatabase(t)
	workers := &workerGroup{}
	coordinator, dispatcher := newHealthyTestLifecycle(db, workers)
	dispatcher.db = db
	preparation, readiness, err := coordinator.tryPrepareRestart(context.Background())
	if err != nil || !readiness.Safe {
		t.Fatalf("prepare readiness=%#v err=%v", readiness, err)
	}
	if err := coordinator.commitRestart(preparation); err != nil {
		t.Fatal(err)
	}

	session, err := db.CreateSession(context.Background(), "test bypass insertion")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueSessionExecution(context.Background(), session.ID, 1); err != nil {
		t.Fatal(err)
	}
	dispatcher.dispatch(context.Background())
	_, execution, err := db.SessionExecutionStatus(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if execution == nil || execution.Status != state.ExecutionStatusQueued {
		t.Fatalf("execution crossed committed boundary: %#v", execution)
	}
	if workers.ownerCount() != 0 {
		t.Fatalf("worker started across boundary; owners=%d", workers.ownerCount())
	}
}

func TestLifecycleWorkPlanAdmissionIsCovered(t *testing.T) {
	db := openWorkerTestDatabase(t)
	project, err := db.CreateProject(context.Background(), "plan", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := db.CreateWorkPlanSession(context.Background(), "plan", project.ID, state.WorkPlanSpec{
		Objective: "objective", OriginalRequest: "request",
		Tasks: []state.WorkPlanTaskSpec{{Key: "one", Objective: "one", ExecutionMethod: "test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, dispatcher := newHealthyTestLifecycle(db, &workerGroup{})
	dispatcher.db = db
	preparation, readiness, err := coordinator.tryPrepareRestart(context.Background())
	if err != nil || !readiness.Safe {
		t.Fatalf("prepare readiness=%#v err=%v", readiness, err)
	}
	defer coordinator.abortRestart(preparation)
	if _, err := queueSessionForExecution(context.Background(), db, dispatcher, session.ID); !errors.Is(err, errLifecycleConflict) {
		t.Fatalf("work-plan admission = %v", err)
	}
	_, execution, err := db.SessionExecutionStatus(context.Background(), session.ID)
	if err != nil || execution != nil {
		t.Fatalf("rejected work plan persisted execution=%#v err=%v", execution, err)
	}
}

func TestLifecycleQualificationAdmissionIsCovered(t *testing.T) {
	db := openWorkerTestDatabase(t)
	workers := &workerGroup{}
	runtimeModel := newModelRuntime(qualificationFakeProvider(func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{}, nil
	}), nil)
	runtimeModel.defaultModel = "worker"
	runtimeModel.configuration = "fp"
	coordinator, dispatcher := newHealthyTestLifecycle(db, workers)
	dispatcher.db = db
	dispatcher.model = runtimeModel
	preparation, readiness, err := coordinator.tryPrepareRestart(context.Background())
	if err != nil || !readiness.Safe {
		t.Fatalf("prepare readiness=%#v err=%v", readiness, err)
	}
	defer coordinator.abortRestart(preparation)

	response := qualificationRoundTrip(t, db, workers, dispatcher, ipc.Request{
		Command: "qualification-create", Model: "worker", Capability: "planning", Repetitions: 1,
	})
	if response.OK || response.Message != errLifecycleConflict.Error() {
		t.Fatalf("qualification response=%#v", response)
	}
	work, err := db.InspectRestartWorkState(context.Background())
	if err != nil || work.QueuedExecutions != 0 {
		t.Fatalf("qualification created executable work: %#v err=%v", work, err)
	}
}

func TestLifecycleConcurrentPreparationIsRejectedByOwner(t *testing.T) {
	coordinator, _ := newHealthyTestLifecycle(&lifecycleTestInspector{}, &workerGroup{})
	first, readiness, err := coordinator.tryPrepareRestart(context.Background())
	if err != nil || !readiness.Safe {
		t.Fatal(err)
	}
	if _, _, err := coordinator.tryPrepareRestart(context.Background()); !errors.Is(err, errLifecycleConflict) {
		t.Fatalf("concurrent prepare = %v", err)
	}
	if err := coordinator.abortRestart(restartPreparation{owner: first.owner + 1}); !errors.Is(err, errPreparationOwner) {
		t.Fatalf("unrelated abort = %v", err)
	}
	if err := coordinator.abortRestart(first); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleDatabaseFailureAndDispatcherUncertaintyRestoreAdmission(t *testing.T) {
	store := &lifecycleTestInspector{err: errors.New("database unavailable")}
	coordinator, dispatcher := newHealthyTestLifecycle(store, &workerGroup{})
	if _, readiness, err := coordinator.tryPrepareRestart(context.Background()); err == nil || readiness.Safe {
		t.Fatalf("database failure readiness=%#v err=%v", readiness, err)
	}
	if permit, err := coordinator.acquire(permitAdmission); err != nil {
		t.Fatalf("database failure did not reopen admission: %v", err)
	} else {
		permit.release()
	}

	store.err = nil
	dispatcher.markUncertain()
	if _, readiness, err := coordinator.tryPrepareRestart(context.Background()); err != nil || readiness.Safe {
		t.Fatalf("dispatcher uncertainty readiness=%#v err=%v", readiness, err)
	}
	if permit, err := coordinator.acquire(permitAdmission); err != nil {
		t.Fatalf("uncertain dispatcher did not reopen admission: %v", err)
	} else {
		permit.release()
	}
}

func TestLifecycleActivePlansQualificationsAndShutdownAreUnsafe(t *testing.T) {
	store := &lifecycleTestInspector{state: state.RestartWorkState{
		RunningWorkPlans:         1,
		RunningQualificationJobs: 1,
		UnknownExecutions:        1,
	}}
	coordinator, _ := newHealthyTestLifecycle(store, &workerGroup{})
	_, readiness, err := coordinator.tryPrepareRestart(context.Background())
	if err != nil || readiness.Safe {
		t.Fatalf("active durable work readiness=%#v err=%v", readiness, err)
	}
	wantReasons := map[string]bool{
		"active_work_plans": true, "active_qualification_jobs": true, "unknown_execution_state": true,
	}
	for _, reason := range readiness.Reasons {
		delete(wantReasons, reason)
	}
	if len(wantReasons) != 0 {
		t.Fatalf("missing readiness reasons: %v in %#v", wantReasons, readiness)
	}

	coordinator.beginShutdown()
	if _, _, err := coordinator.tryPrepareRestart(context.Background()); !errors.Is(err, errLifecycleConflict) {
		t.Fatalf("shutdown preparation = %v", err)
	}
}

func TestLifecycleDurableWaitingWithoutWorkerIsRestartSafe(t *testing.T) {
	store := &lifecycleTestInspector{state: state.RestartWorkState{WaitingExecutions: 1}}
	coordinator, _ := newHealthyTestLifecycle(store, &workerGroup{})
	preparation, readiness, err := coordinator.tryPrepareRestart(context.Background())
	if err != nil || !readiness.Safe || readiness.Durable.WaitingExecutions != 1 {
		t.Fatalf("waiting readiness=%#v err=%v", readiness, err)
	}
	if err := coordinator.abortRestart(preparation); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleAbortRestoresNormalExecution(t *testing.T) {
	db := openWorkerTestDatabase(t)
	workers := &workerGroup{}
	coordinator, dispatcher := newHealthyTestLifecycle(db, workers)
	dispatcher.db = db
	preparation, readiness, err := coordinator.tryPrepareRestart(context.Background())
	if err != nil || !readiness.Safe {
		t.Fatalf("prepare readiness=%#v err=%v", readiness, err)
	}
	if err := coordinator.abortRestart(preparation); err != nil {
		t.Fatal(err)
	}
	session, err := db.CreateSession(context.Background(), "after abort")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queueSessionForExecution(context.Background(), db, dispatcher, session.ID); err != nil {
		t.Fatalf("normal execution did not resume: %v", err)
	}
	count, err := db.CountQueuedExecutions(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("queued=%d err=%v", count, err)
	}
}

func TestLifecycleDrainsConcurrentAdmissionsWithoutDuplicateOrLostRecords(t *testing.T) {
	const submissions = 12
	db := openWorkerTestDatabase(t)
	coordinator, dispatcher := newHealthyTestLifecycle(db, &workerGroup{})
	dispatcher.db = db

	sessions := make([]state.Session, submissions)
	permits := make([]*lifecyclePermit, submissions)
	for i := range sessions {
		var err error
		sessions[i], err = db.CreateSession(context.Background(), fmt.Sprintf("session-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		permits[i], err = coordinator.acquire(permitAdmission)
		if err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	var group sync.WaitGroup
	var failures atomic.Int32
	for i := range sessions {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			if _, err := db.QueueSessionExecution(context.Background(), sessions[index].ID, 1); err != nil {
				failures.Add(1)
			}
			permits[index].release()
		}(i)
	}
	prepared := make(chan restartReadiness, 1)
	go func() {
		_, readiness, _ := coordinator.tryPrepareRestart(context.Background())
		prepared <- readiness
	}()
	waitForCoordinatorState(t, coordinator, lifecycleRestartPreparing)
	close(start)
	group.Wait()
	readiness := <-prepared
	if failures.Load() != 0 || readiness.Durable.QueuedExecutions != submissions {
		t.Fatalf("failures=%d readiness=%#v", failures.Load(), readiness)
	}
	count, err := db.CountQueuedExecutions(context.Background())
	if err != nil || count != submissions {
		t.Fatalf("durable records=%d want=%d err=%v", count, submissions, err)
	}
}

func TestLifecycleRepeatedConcurrentAttemptsDoNotDeadlock(t *testing.T) {
	coordinator, _ := newHealthyTestLifecycle(&lifecycleTestInspector{}, &workerGroup{})
	const goroutines = 8
	const attempts = 100
	var group sync.WaitGroup
	for worker := 0; worker < goroutines; worker++ {
		group.Add(1)
		go func(id int) {
			defer group.Done()
			for attempt := 0; attempt < attempts; attempt++ {
				if (id+attempt)%2 == 0 {
					permit, err := coordinator.acquire(permitAdmission)
					if err == nil {
						permit.release()
					}
					continue
				}
				preparation, readiness, err := coordinator.tryPrepareRestart(context.Background())
				if err == nil && readiness.Safe {
					_ = coordinator.abortRestart(preparation)
				}
			}
		}(worker)
	}
	done := make(chan struct{})
	go func() { group.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent lifecycle attempts deadlocked")
	}
}
