package daemon

import (
	"context"
	"errors"
	"sync"

	"github.com/Hisha/Forgehand/internal/state"
)

type lifecycleState string

const (
	lifecycleRunning          lifecycleState = "running"
	lifecycleRestartPreparing lifecycleState = "restart_preparing"
	lifecycleRestartCommitted lifecycleState = "restart_committed"
	lifecycleShuttingDown     lifecycleState = "shutting_down"
)

type lifecyclePermitKind uint8

const (
	permitAdmission lifecyclePermitKind = iota
	permitDispatch
)

var (
	errLifecycleConflict = errors.New("execution admission is closed by a daemon lifecycle transition")
	errPreparationOwner  = errors.New("restart preparation is owned by another caller")
	errPreparationCommit = errors.New("restart preparation is already committed")
)

type restartWorkInspector interface {
	InspectRestartWorkState(context.Context) (state.RestartWorkState, error)
}

// restartReadiness is deliberately structured and non-sensitive so a later
// API can translate it without exposing session or project details.
type restartReadiness struct {
	Safe             bool
	State            lifecycleState
	ActiveWorkers    int
	ActiveAdmissions int
	ActiveDispatches int
	Durable          state.RestartWorkState
	Reasons          []string
}

type restartPreparation struct {
	owner uint64
}

type lifecyclePermit struct {
	coordinator *lifecycleCoordinator
	kind        lifecyclePermitKind
	once        sync.Once
}

func (p *lifecyclePermit) release() {
	if p == nil || p.coordinator == nil {
		return
	}
	p.once.Do(func() { p.coordinator.release(p.kind) })
}

// lifecycleCoordinator is the daemon's only restart/admission coordinator.
// Its mutex protects only lifecycle state and permit counters. Database,
// dispatcher, and worker locks are never acquired while it is held.
type lifecycleCoordinator struct {
	mu sync.Mutex

	state        lifecycleState
	owner        uint64
	nextOwner    uint64
	admissions   int
	dispatches   int
	drained      chan struct{}
	shuttingDown bool

	store      restartWorkInspector
	workers    *workerGroup
	dispatcher *dispatcher
}

func newLifecycleCoordinator(store restartWorkInspector, workers *workerGroup) *lifecycleCoordinator {
	drained := make(chan struct{})
	close(drained)
	return &lifecycleCoordinator{
		state:   lifecycleRunning,
		drained: drained,
		store:   store,
		workers: workers,
	}
}

func (c *lifecycleCoordinator) setDispatcher(dispatcher *dispatcher) {
	c.mu.Lock()
	c.dispatcher = dispatcher
	c.mu.Unlock()
}

func (c *lifecycleCoordinator) lifecycleState() lifecycleState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *lifecycleCoordinator) acquire(kind lifecyclePermitKind) (*lifecyclePermit, error) {
	if c == nil {
		return &lifecyclePermit{}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != lifecycleRunning || c.shuttingDown {
		return nil, errLifecycleConflict
	}
	if c.admissions+c.dispatches == 0 {
		c.drained = make(chan struct{})
	}
	if kind == permitDispatch {
		c.dispatches++
	} else {
		c.admissions++
	}
	return &lifecyclePermit{coordinator: c, kind: kind}, nil
}

func (c *lifecycleCoordinator) release(kind lifecyclePermitKind) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if kind == permitDispatch {
		c.dispatches--
	} else {
		c.admissions--
	}
	if c.admissions+c.dispatches == 0 {
		close(c.drained)
	}
}

// tryPrepareRestart closes admission first, drains operations that crossed the
// old boundary, and only then performs the authoritative readiness inspection.
// An unsafe, failed, or cancelled inspection reopens admission.
func (c *lifecycleCoordinator) tryPrepareRestart(ctx context.Context) (restartPreparation, restartReadiness, error) {
	c.mu.Lock()
	if c.state != lifecycleRunning || c.shuttingDown {
		readiness := restartReadiness{Safe: false, State: c.state, Reasons: []string{"lifecycle_conflict"}}
		c.mu.Unlock()
		return restartPreparation{}, readiness, errLifecycleConflict
	}
	c.nextOwner++
	c.owner = c.nextOwner
	owner := c.owner
	c.state = lifecycleRestartPreparing
	drained := c.drained
	c.mu.Unlock()

	select {
	case <-drained:
	case <-ctx.Done():
		_ = c.abortRestart(restartPreparation{owner: owner})
		return restartPreparation{}, restartReadiness{Safe: false, State: c.lifecycleState(), Reasons: []string{"inspection_cancelled"}}, ctx.Err()
	}

	preparation := restartPreparation{owner: owner}
	readiness, err := c.inspectPreparedRestart(ctx, preparation)
	if err != nil || !readiness.Safe {
		_ = c.abortRestart(preparation)
		readiness.State = c.lifecycleState()
		return restartPreparation{}, readiness, err
	}
	c.mu.Lock()
	stillOwned := c.state == lifecycleRestartPreparing && c.owner == preparation.owner && !c.shuttingDown
	stateNow := c.state
	c.mu.Unlock()
	if !stillOwned {
		readiness.Safe = false
		readiness.State = stateNow
		readiness.Reasons = append(readiness.Reasons, "lifecycle_changed_during_inspection")
		return restartPreparation{}, readiness, errLifecycleConflict
	}
	return preparation, readiness, nil
}

// inspectReadiness is an advisory snapshot while running and authoritative
// while a caller owns restart preparation. tryPrepareRestart is the operation
// that provides the atomic close-and-check guarantee.
func (c *lifecycleCoordinator) inspectReadiness(ctx context.Context) (restartReadiness, error) {
	c.mu.Lock()
	readiness := restartReadiness{
		State:            c.state,
		ActiveAdmissions: c.admissions,
		ActiveDispatches: c.dispatches,
	}
	dispatcher := c.dispatcher
	workers := c.workers
	store := c.store
	shuttingDown := c.shuttingDown
	c.mu.Unlock()
	return inspectRestartReadiness(ctx, readiness, store, workers, dispatcher, shuttingDown)
}

func (c *lifecycleCoordinator) inspectPreparedRestart(ctx context.Context, preparation restartPreparation) (restartReadiness, error) {
	c.mu.Lock()
	if c.state != lifecycleRestartPreparing || c.owner != preparation.owner || preparation.owner == 0 {
		stateNow := c.state
		c.mu.Unlock()
		return restartReadiness{Safe: false, State: stateNow, Reasons: []string{"preparation_owner_mismatch"}}, errPreparationOwner
	}
	readiness := restartReadiness{State: c.state, ActiveAdmissions: c.admissions, ActiveDispatches: c.dispatches}
	dispatcher := c.dispatcher
	workers := c.workers
	store := c.store
	shuttingDown := c.shuttingDown
	c.mu.Unlock()
	return inspectRestartReadiness(ctx, readiness, store, workers, dispatcher, shuttingDown)
}

func inspectRestartReadiness(ctx context.Context, readiness restartReadiness, store restartWorkInspector, workers *workerGroup, dispatcher *dispatcher, shuttingDown bool) (restartReadiness, error) {
	if shuttingDown || readiness.State == lifecycleShuttingDown {
		readiness.Reasons = append(readiness.Reasons, "daemon_shutting_down")
	}
	if readiness.ActiveAdmissions != 0 || readiness.ActiveDispatches != 0 {
		readiness.Reasons = append(readiness.Reasons, "execution_transition_in_progress")
	}
	if workers == nil {
		readiness.Reasons = append(readiness.Reasons, "worker_state_unavailable")
	} else {
		readiness.ActiveWorkers, shuttingDown = workers.lifecycleSnapshot()
		if readiness.ActiveWorkers != 0 {
			readiness.Reasons = append(readiness.Reasons, "active_workers")
		}
		if shuttingDown {
			readiness.Reasons = append(readiness.Reasons, "workers_shutting_down")
		}
	}
	if dispatcher == nil || !dispatcher.restartReady() {
		readiness.Reasons = append(readiness.Reasons, "dispatcher_unavailable")
	}
	if store == nil {
		readiness.Reasons = append(readiness.Reasons, "database_unavailable")
		readiness.Safe = false
		return readiness, errors.New("restart readiness database is unavailable")
	}
	durable, err := store.InspectRestartWorkState(ctx)
	if err != nil {
		readiness.Reasons = append(readiness.Reasons, "database_unavailable")
		readiness.Safe = false
		return readiness, err
	}
	readiness.Durable = durable
	if durable.QueuedExecutions != 0 {
		readiness.Reasons = append(readiness.Reasons, "queued_executions")
	}
	if durable.RunningExecutions != 0 {
		readiness.Reasons = append(readiness.Reasons, "durable_running_executions")
	}
	if durable.RunningWorkPlans != 0 {
		readiness.Reasons = append(readiness.Reasons, "active_work_plans")
	}
	if durable.RunningQualificationJobs != 0 {
		readiness.Reasons = append(readiness.Reasons, "active_qualification_jobs")
	}
	if durable.UnknownExecutions != 0 {
		readiness.Reasons = append(readiness.Reasons, "unknown_execution_state")
	}
	readiness.Safe = len(readiness.Reasons) == 0
	return readiness, nil
}

func (c *lifecycleCoordinator) commitRestart(preparation restartPreparation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == lifecycleRestartCommitted {
		if preparation.owner == c.owner && preparation.owner != 0 {
			return nil
		}
		return errPreparationOwner
	}
	if c.state != lifecycleRestartPreparing || c.owner != preparation.owner || preparation.owner == 0 {
		return errPreparationOwner
	}
	c.state = lifecycleRestartCommitted
	return nil
}

func (c *lifecycleCoordinator) abortRestart(preparation restartPreparation) error {
	c.mu.Lock()
	if c.state == lifecycleRestartCommitted {
		c.mu.Unlock()
		return errPreparationCommit
	}
	if c.state != lifecycleRestartPreparing || c.owner != preparation.owner || preparation.owner == 0 {
		c.mu.Unlock()
		return errPreparationOwner
	}
	c.state = lifecycleRunning
	c.owner = 0
	dispatcher := c.dispatcher
	c.mu.Unlock()
	if dispatcher != nil {
		dispatcher.notify()
	}
	return nil
}

func (c *lifecycleCoordinator) beginShutdown() {
	c.mu.Lock()
	c.shuttingDown = true
	c.state = lifecycleShuttingDown
	c.mu.Unlock()
}

func acquireExecutionAdmission(dispatcher *dispatcher) (*lifecyclePermit, error) {
	if dispatcher == nil || dispatcher.lifecycle == nil {
		return &lifecyclePermit{}, nil
	}
	return dispatcher.lifecycle.acquire(permitAdmission)
}
