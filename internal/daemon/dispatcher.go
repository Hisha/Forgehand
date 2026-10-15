package daemon

import (
	"context"
	"fmt"
	"os"

	"github.com/Hisha/Forgehand/internal/state"
)

// dispatcher is the single daemon-side scheduling loop for the durable
// execution queue. It claims the oldest QUEUED execution whenever worker
// capacity is free and starts it, so exactly one goroutine decides what runs.
//
// It is event-driven rather than poll-based: a buffered wake channel coalesces
// notifications from enqueue points and worker completions, so a burst of
// events produces at most one pending dispatch and no busy-waiting.
type dispatcher struct {
	db         *state.Database
	workers    *workerGroup
	maxWorkers int
	wake       chan struct{}
	done       chan struct{}
}

func newDispatcher(db *state.Database, workers *workerGroup, maxWorkers int) *dispatcher {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	return &dispatcher{
		db:         db,
		workers:    workers,
		maxWorkers: maxWorkers,
		wake:       make(chan struct{}, 1),
		done:       make(chan struct{}),
	}
}

// notify wakes the dispatcher without blocking. Repeated notifications before
// the dispatcher runs collapse into a single wake, which is all that is needed
// because dispatch drains the queue while capacity remains.
func (d *dispatcher) notify() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *dispatcher) capacity() int {
	return d.maxWorkers
}

// run drives the dispatch loop until ctx is cancelled. It performs an initial
// dispatch so work queued before (or recovered across) a restart begins
// promptly. When run returns it closes done so callers can wait for the loop to
// exit before tearing down the database it uses.
func (d *dispatcher) run(ctx context.Context) {
	defer close(d.done)

	d.dispatch(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
			d.dispatch(ctx)
		}
	}
}

// dispatch claims and launches queued executions until the queue is empty or
// worker capacity is reached. It runs on the single dispatcher goroutine.
func (d *dispatcher) dispatch(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if d.workers.ownerCount() >= d.maxWorkers {
			return
		}

		execution, ok, err := d.db.ClaimNextQueuedExecution(ctx)
		if err != nil {
			// A cancelled dispatch is expected during shutdown; do not treat it
			// as a scheduling fault.
			if ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "forgehand: dispatch queued execution: %v\n", err)
			}
			return
		}
		if !ok {
			return
		}

		if err := launchExecutionWorker(ctx, d.db, d.workers, execution, d.notify); err != nil {
			fmt.Fprintf(
				os.Stderr,
				"forgehand: launch execution %d: %v\n",
				execution.ID,
				err,
			)
			// The execution was already committed as RUNNING. If it could not
			// be started, reconcile it so it does not stay RUNNING silently.
			if err := d.db.InterruptSessionExecution(context.Background(), execution.ID); err != nil {
				fmt.Fprintf(
					os.Stderr,
					"forgehand: reconcile unlaunched execution %d: %v\n",
					execution.ID,
					err,
				)
			}
			return
		}
	}
}
