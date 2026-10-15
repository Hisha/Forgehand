# Milestone 2F: Bounded Worker Scheduling

Milestone 2F adds a durable FIFO execution queue and a single daemon-side
dispatcher that bounds how many fake workers run at once. It builds on the
fake-worker execution lifecycle (2B), event history (2C), durable human
interaction (2D), and session cancellation (2E). `session run` and
`session resume` now enqueue work instead of starting a worker inline, and the
dispatcher starts queued executions as capacity frees, in order. No model
provider, TUI, priority, or resource-claim system is introduced.

## States

- Session state `QUEUED` and execution status `QUEUED` are added alongside
  `READY`/`RUNNING`/`WAITING_FOR_USER`/`COMPLETED`/`INTERRUPTED`/`FAILED`/
  `CANCELED`.
- `QUEUED` is a durable, non-terminal state: the execution has been accepted for
  work but has not been assigned a worker yet.
- A session is `QUEUED` between enqueue and dispatch. The dispatcher transitions
  both the execution (`QUEUED` -> `RUNNING`) and the session
  (`QUEUED` -> `RUNNING`) atomically when it claims work.
- Only a `READY` session can be newly queued. A session that is already
  `QUEUED` or `RUNNING` is rejected, so a session never has two queued
  executions at once.

## Schema

Migration version 12 adds one nullable column and two indexes to
`session_executions`:

- `queue_seq` (INTEGER): a globally monotonic FIFO sequence assigned at
  enqueue. It is retained after dispatch for ordering and auditing and is never
  cleared.
- `idx_session_executions_queue_seq`: a unique index on `queue_seq` where it is
  not null, guaranteeing no two enqueued executions share a sequence.
- `idx_session_executions_status_queue_seq`: a composite index supporting the
  oldest-`QUEUED` claim query.

Existing databases migrate forward without data loss. Pre-existing executions
receive `queue_seq = NULL` and are never retroactively queued.

## Event Types

- `execution.queued`: an execution entered the queue. Details include
  `execution_id`, `current_step`, `total_steps`, `queue_seq`, and `status`;
  resume-after-input entries also include `interaction_id`, and resumptions
  include `resumed: true`.
- `execution.dispatched`: the dispatcher claimed a queued execution. Details
  include `execution_id`, `current_step`, `total_steps`, `status`, and
  `queue_seq` when present.

Both events are appended in the same transaction as their state change.

## Queue Semantics

- `QueueSessionExecution` enqueues a new `QUEUED` execution for a `READY`
  session.
- `QueueResumeExecution` re-queues an `INTERRUPTED` execution, preserving its
  committed checkpoint. It is idempotent: an already-`QUEUED` execution is
  returned unchanged without a second `execution.queued` event.
- `QueueResumeAfterHumanInput` re-queues an answered `WAITING_FOR_USER`
  execution, preserving its checkpoint. It rejects a waiting execution whose
  latest interaction is not yet answered and is likewise idempotent.
- `ClaimNextQueuedExecution` transitions the oldest `QUEUED` execution (ordered
  by `queue_seq`, then row id) to `RUNNING`, sets the session to `RUNNING`, and
  appends `execution.dispatched`, all in one immediate-write transaction. It
  returns `ok=false` when the queue is empty.
- Queue sequence assignment uses `COALESCE(MAX(queue_seq), 0) + 1` inside the
  write transaction. Because every writer takes the write lock at `BEGIN`
  (`txlock=immediate`), two concurrent enqueues cannot observe the same maximum.
- Queue position is the 1-based count of `QUEUED` executions with a
  `queue_seq` less than or equal to the execution's own. It is computed on read
  and is `0` for non-queued executions.
- Cancellation is accepted for `QUEUED` executions. A canceled queued execution
  is never claimed, because `ClaimNextQueuedExecution` selects only `QUEUED`
  rows.

## Dispatcher

- A single dispatcher goroutine owns scheduling. `run` performs an initial
  dispatch (so work recovered across a restart starts promptly) and then waits
  on a buffered wake channel or context cancellation.
- `dispatch` repeatedly claims queued executions while `ownerCount()` is below
  the configured capacity, launching each through `launchExecutionWorker`.
- `launchExecutionWorker` reserves a worker slot (`workers.start`), claims
  ownership of the execution, and starts the fake worker. When the worker exits
  it releases ownership, releases the slot, and notifies the dispatcher so the
  next queued execution can start.
- The wake channel is a one-slot buffered channel with a non-blocking send, so a
  burst of enqueues or completions collapses into a single pending dispatch and
  the dispatcher never busy-waits.
- If a launch fails after the execution was committed as `RUNNING` (for example,
  shutdown began or ownership was already held), the dispatcher reconciles the
  execution with `InterruptSessionExecution` so it does not remain `RUNNING`
  silently.
- Shutdown cancels the daemon context first (stopping the dispatcher from
  claiming further work) and then `workerGroup.stopAndWait()` drains live
  workers.
- In tests a dispatcher exposes a `done` channel closed when `run` returns, so
  test helpers can join the dispatcher before tearing down its database.

## Configuration

- `FORGEHAND_MAX_CONCURRENT_WORKERS` bounds simultaneously running workers.
- An unset or empty value uses the default of `2`.
- A non-integer value, or a value below `1`, is a configuration error; the
  daemon fails closed rather than silently changing scheduling behavior.
- Invalid values (including `0`, `-1`, `nope`, `2.5`) are rejected.
- The configured capacity is surfaced as `Worker capacity` in `status`.

## Daemon IPC

- `session-run`: queues a `READY` session and returns the `QUEUED` execution
  (including `queue_seq` and `queue_position`). The handler notifies the
  dispatcher after the enqueue commits.
- `session-resume`: queues an `INTERRUPTED` execution, or an answered
  `WAITING_FOR_USER` execution, and returns it as `QUEUED`.
- `session-status`: reports `QUEUED` state with the execution's
  `queue_position`.
- `session-cancel`: cancels `QUEUED`, `RUNNING`, `WAITING_FOR_USER`, or
  `INTERRUPTED` executions; then signals the owning worker, if any.
- `status`: reports `WorkerCapacity`, `ActiveWorkers`, and `QueuedExecutions`
  in addition to the daemon version and PID.
- `Execution` responses carry `queue_seq` (omitted when null) and
  `queue_position` (omitted when `0`).

## CLI

- `forgehand session run <id>` prints `Queued session <id>`, the execution,
  `Status: QUEUED`, and `Queue position: <n>` when queued.
- `forgehand session resume <id>` prints `Queued resume for session <id>` with
  the same fields.
- `forgehand session status <id>` prints `Queue position: <n>` for a queued
  session.
- `forgehand status` prints `Worker capacity`, `Active workers`, and
  `Queued executions`.

## Cancellation and Interaction Integration

- Canceling a running worker (2E) now also frees worker capacity: the worker
  exits, notifies the dispatcher, and the next queued execution starts.
- A `WAITING_FOR_USER` worker returns its slot when it pauses, so other queued
  sessions can run while it waits. Answering and resuming re-queues the waiting
  execution, which the dispatcher then starts when capacity is available.
- Canceling a queued execution removes it from consideration without needing a
  worker.

## Recovery

- On restart, `InterruptRunningExecutions` reconciles only `RUNNING`
  executions; `QUEUED` executions are left queued. The dispatcher's initial
  dispatch then starts them when capacity is available.
- `queue_seq` is retained across restarts, so FIFO ordering survives.

## Scope Exclusions

This milestone intentionally does not add: real model workers; a TUI; priority
or preemption; CPU/GPU or resource-claim allocation; distributed or
multi-daemon coordination; dynamic capacity changes at runtime; or per-session
fairness beyond global FIFO.

## Verification

- State tests cover ordered queue-sequence assignment, FIFO claim order,
  position recomputation after dispatch, rejection of non-`READY` sessions,
  resume-of-interrupted preservation and idempotency, resume-after-input
  requiring an answer, skipping canceled queued executions, and
  `CountQueuedExecutions`.
- A migration test constructs a schema-v11 database with existing data and
  asserts the v12 migration adds `queue_seq` and both indexes without altering
  rows or retroactively queuing anything.
- Config tests cover default, explicit, and invalid values for
  `FORGEHAND_MAX_CONCURRENT_WORKERS`.
- Dispatcher tests cover capacity clamping and that the dispatcher never
  exceeds its capacity, claims the oldest queued execution first, and starts the
  next queued execution when a running worker is canceled.
- CLI tests cover the status scheduling fields and queue positions for
  `session run`, `session resume`, and `session status`.
- Existing daemon tests were updated for asynchronous dispatch (polling for
  `RUNNING`) and the new `handleConnection` signature; the daemon and state
  packages pass under `go test -race`.
- Isolated end-to-end scenarios run a disposable daemon under `/tmp` with a
  disposable state directory and socket and verify: capacity bounds concurrent
  workers; canceling a running worker frees capacity and dispatches the next
  queued session in FIFO order; a restart interrupts running executions while
  preserving canceled ones; resume re-queues and dispatches; and a waiting
  session frees capacity so other sessions run, then answer+resume re-queues it.
