# Milestone 2E: Session Lifecycle Control

Milestone 2E adds an explicit, durable way to permanently stop a session on top
of the fake-worker execution lifecycle (2B), session event history (2C), and
durable human interaction (2D). A user can cancel a `RUNNING`, `WAITING_FOR_USER`,
or `INTERRUPTED` execution; the live worker stops at a safe point, and the
terminal `CANCELED` state survives daemon restarts. No model provider, scheduler,
or TUI is required.

## States

- Session state `CANCELED` and execution status `CANCELED` are added alongside
  `READY`/`RUNNING`/`WAITING_FOR_USER`/`COMPLETED`/`INTERRUPTED`/`FAILED`.
- `CANCELED` is a terminal, intentional stop. It is distinct from `COMPLETED`
  (success), `FAILED` (error), `INTERRUPTED` (recoverable), and
  `WAITING_FOR_USER` (awaiting a human).
- Cancellation is accepted from `RUNNING`, `WAITING_FOR_USER`, and
  `INTERRUPTED`. `INTERRUPTED` is cancellable so a user can permanently abandon
  work they no longer wish to resume.
- Cancellation is rejected for `COMPLETED` and `FAILED` executions.
- The committed checkpoint (`current_step`) is preserved; cancellation never
  rewinds progress.
- Start-up recovery (`InterruptRunningExecutions`) only reconciles `RUNNING`
  executions, so `CANCELED` executions are never re-interrupted or resumed.

## Schema

Migration version 11 adds two nullable columns to `session_executions`:

- `canceled_at` (TEXT): UTC timestamp in RFC3339Nano format; set when canceled.
- `cancel_reason` (TEXT): Optional trimmed reason, truncated to 200 characters.

Existing databases migrate forward without data loss. Historical executions are
not retroactively canceled and receive no cancellation columns' values.

## Event Types

- `execution.canceled`: an execution transitioned to `CANCELED`. Details include
  `execution_id`, `status`, `session_state`, `current_step`, `total_steps`, and,
  when a non-empty reason was supplied, `reason`. The event is appended in the
  same transaction as the state change.

## Cancellation Outcomes

`CancelSessionExecution` returns one of:

- `canceled`: the execution and session transitioned to `CANCELED` and an
  `execution.canceled` event was appended.
- `already_canceled`: the execution was already `CANCELED`; no state change or
  event was made and the original reason is preserved.
- `rejected_terminal`: the execution is `COMPLETED` or `FAILED`; cancellation is
  refused and no event is appended.

Requesting cancellation for a session that has no execution is an error
(`session N has no execution to cancel`), as is an unknown session
(`session N does not exist`).

## Idempotency

- Repeated cancellation is idempotent: the first request applies, later requests
  report `already_canceled` without appending a second event or overwriting the
  recorded reason.
- A single `status` column plus `WHERE status = <from-state>` guards make the
  database authoritative, so `COMPLETED`/`FAILED`/`CANCELED` cannot be combined.

## Atomicity

- Both state transitions (execution and session) and the `execution.canceled`
  event occur in one SQLite transaction.
- If the event insert fails, the whole cancellation rolls back; no partial
  cancellation is committed and the execution stays in its prior state.

## Canceled Interaction Guard

- A `PENDING` interaction whose execution has been `CANCELED` can no longer be
  answered. `AnswerHumanInput` rejects the answer, leaves the interaction row
  `PENDING` so the question is preserved for historical inspection, and appends
  no `interaction.answered` event.
- An interaction answered before cancellation keeps its recorded answer. A
  duplicate answer submitted after cancellation remains idempotent
  (`applied=false`) and never replaces the committed answer.
- The execution status is read inside the answer transaction, so concurrent
  answer and cancel requests resolve deterministically: an answer that commits
  first is preserved and cancellation still succeeds; a cancellation that commits
  first makes the subsequent answer fail. Valid `WAITING_FOR_USER` answering is
  unchanged.

## Worker Semantics

- The daemon keeps a daemon-local ownership map from execution ID to the worker's
  context cancel function. At most one live worker may own an execution.
- `session-cancel` commits the durable `CANCELED` state first, then signals the
  owning worker (if any). The database is authoritative: a worker that misses the
  signal still cannot advance a canceled execution because its step update is
  guarded by `status = RUNNING`.
- When a worker leaves its loop it reconciles through `handleWorkerStop`, which
  reads the durable status. A `CANCELED` (or otherwise terminal) execution is
  never overwritten; a still-`RUNNING` execution is interrupted when the stop was
  a daemon/worker cancellation and failed when the worker hit a real error.
- Ownership is in-memory only and does not survive a daemon restart. This is the
  intended Milestone 2E scope: no distributed leases, process-level termination,
  automatic retry, or priority scheduling.

## Daemon IPC

- `session-cancel`: cancels the session's most recent execution. The request
  accepts an optional `reason`. The response reports `Canceled`, `CancelOutcome`,
  and the updated session and execution (including `canceled_at`/`cancel_reason`).
  Terminal executions return `OK=false` with `CancelOutcome=rejected_terminal`.
- `session-status`: reports `CANCELED` state along with `canceled_at` and
  `cancel_reason` when present.

## CLI

- `forgehand session cancel <id> [--reason "..."]` cancels a session. It reports
  `Canceled session <id>`, the resulting state, and the recorded reason.
- `forgehand session status <id>` prints `Canceled:` and `Cancel reason:` when a
  session was canceled.
- `session resume`, `session run`, and `session-answer`-driven resume all fail for
  a canceled session.

## Verification

- State tests cover cancellation from `RUNNING`, `WAITING_FOR_USER`, and
  `INTERRUPTED`; idempotent repeats; terminal rejection for `COMPLETED`/`FAILED`;
  missing execution/session; reason trimming and truncation; persistence across
  close/reopen; and a fault-injection test proving an event-insert failure rolls
  the cancellation back.
- State tests cover the canceled interaction guard: an answer after cancellation
  is rejected with the interaction left `PENDING` and no `interaction.answered`
  event; an answer committed before cancellation is preserved and stays
  idempotent; and a race test drives `AnswerHumanInput` and
  `CancelSessionExecution` concurrently to confirm the outcome is deterministic.
- Daemon tests cover the worker ownership map (claim/release/cancel, shutdown
  behavior), `handleWorkerStop` for canceled/interrupted/failed cases, and the
  `session-cancel` IPC operation (applied, idempotent, terminal-rejected,
  missing-execution, and stopping a live worker).
- Daemon tests confirm `interaction-answer` over IPC is rejected for a canceled
  execution while `interaction-get` still reports the interaction as `PENDING`
  and `session-events` contains no `interaction.answered` event.
- CLI tests cover reason forwarding, already-canceled reporting, daemon error
  surfacing, status rendering, and argument validation.
- Integration tests run the full daemon process and cover canceling a running
  worker, canceling and repeating while `WAITING_FOR_USER`, rejecting a completed
  execution, and preserving `CANCELED` across a restart.
- Migration tests assert schema version 11 and that cancellation columns are added
  without disturbing pre-existing sessions and executions.
