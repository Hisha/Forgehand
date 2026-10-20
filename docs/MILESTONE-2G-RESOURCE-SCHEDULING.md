# Milestone 2G: Resource-Claim Scheduling

Milestone 2G extends the durable FIFO queue and dispatcher (2F) so a queued
execution can declare exclusive named resources. While one execution holds a
resource, no other execution requiring the same resource is dispatched.
Unrelated executions still dispatch in FIFO order past blocked work, and the
global worker-capacity limit is unchanged. No model provider, TUI, priority
system, or distributed lease is introduced.

## Model

- A resource is a daemon-local exclusive name (for example `gpu`,
  `local-model/llama`, `build/machine-3`). Requirements are persisted;
  ownership is in-memory on the daemon's worker group, matching the existing
  daemon-local execution-ownership model from 2E (no distributed leases).
- An execution declares zero or more resources when it is queued. The full set
  is acquired atomically at dispatch: an execution is never partially
  resourced. Names are trimmed, must be non-empty and at most 100 characters,
  are deduplicated, and are stored sorted for deterministic scheduling.
- FIFO is preserved among *eligible* executions: the dispatcher claims the
  oldest queued execution whose entire resource set is disjoint from the
  currently held set, skipping blocked ones.
- Because ownership is in-memory, a daemon restart (graceful or SIGKILL) drops
  all ownership; startup recovery already marks orphaned `RUNNING` executions
  `INTERRUPTED`, so no stale ownership can survive a restart.
- Resources are released at the single worker-exit point that covers every
  terminal path: completion, failure, cancellation, and entering
  `WAITING_FOR_USER` (the paused worker exits and its resources free, so
  blocked work proceeds while a human is asked).

## Schema

Migration version 16 adds one table:

- `execution_resources(execution_id, resource)` with a primary key on both
  columns and a foreign key to `session_executions(id)` with cascade delete.
  One row per required resource per execution.

Existing databases migrate forward without data loss. Pre-existing executions
have no resource requirements.

## Event Types

- `execution.queued` and `execution.dispatched` now include a `resources`
  array in their details when the execution requires resources.

## API

- `QueueSessionExecution(ctx, sessionID, totalSteps, resources ...string)`.
- `ClaimNextQueuedExecution(ctx, heldResources []string)` claims the oldest
  queued execution whose resources are disjoint from `heldResources`, or
  returns `ok = false` when every queued execution is blocked.
- `CountResourceBlockedQueuedExecutions(ctx, heldResources)` backs the status
  report.

## Worker Group

- `acquireResources(executionID, resources)` records ownership for a claimed
  execution, all-or-nothing; the dispatcher calls it immediately after
  `claim` and compensates with `release` on failure.
- `heldResources()` returns a sorted snapshot passed into claiming.
- `resourceBlocks(required)` pairs each held resource with its holder for
  status annotation.
- `release(executionID)` frees both the ownership record and the execution's
  resources; every worker-exit path ends there.

## IPC and CLI

- `Request.Resources` carries requirements with `session-run`.
- `Execution.Resources` reports an execution's requirements; `Execution.BlockedBy`
  lists `ResourceBlock{resource, held_by_execution_id}` for queued executions
  blocked by a running holder.
- `Response.ResourceBlockedExecutions` counts blocked queued executions in the
  `status` response.
- `forgehand session run <id> [--resource <name>]...` declares resources.
- `forgehand status` prints `Resource-blocked executions: N`.
- `forgehand session run`/`resume`/`status` print `Resources: ...` and, when
  blocked, `Blocked by resource <name> (execution <id>)`.

## Tests

- `internal/state/resource_scheduling_test.go`: normalization and validation,
  persistence across status reads and resume, FIFO-among-eligible claim with
  skip-ahead, all-or-nothing multi-resource claims, blocked counts.
- `internal/daemon/resource_scheduling_test.go`: worker-group acquisition
  semantics; dispatcher exclusion of same-resource executions with skip-ahead
  and subsequent claim; release on human-wait, cancellation, and failure;
  restart leaves no stale ownership; IPC status blocker annotation;
  `session-run` resource passthrough.
- `cmd/forgehand/main_scheduling_test.go`: `--resource` flag pass-through and
  blocker/status output.
- Isolated E2E (criterion 8): disposable state and socket; phase 1 verifies
  A(gpu)+C(cpu) run while B(gpu) stays queued with the blocker surfaced in
  status, and cancellation releases the resource; phase 2 SIGKILLs the daemon
  mid-run and restarts with no stale ownership, then verifies a resumed
  execution releases its resource on `WAITING_FOR_USER`. A background SQLite
  poll asserts no instant ever had two gpu-holding executions `RUNNING`.
