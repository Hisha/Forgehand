# Milestone 2D: Durable Human Interaction

Milestone 2D adds a durable worker-to-human request/answer flow on top of the
fake-worker execution lifecycle (2B) and session event history (2C). A worker can
pause at a checkpoint, ask a question that survives daemon restarts, and resume
after a human answers. No model provider, scheduler, or TUI is required.

## States

- Session state `WAITING_FOR_USER` and execution status `WAITING_FOR_USER` are
  added alongside `READY`/`RUNNING`/`COMPLETED`/`INTERRUPTED`.
- Entering the state is atomic with persisting the pending question: the
  execution moves `RUNNING -> WAITING_FOR_USER` and the session moves
  `RUNNING -> WAITING_FOR_USER` in the same transaction.
- Resuming moves the execution and session back to `RUNNING` and leaves the
  committed checkpoint (`current_step`) unchanged.
- Start-up recovery (`InterruptRunningExecutions`) only reconciles `RUNNING`
  executions, so a `WAITING_FOR_USER` execution is never interrupted and is not
  auto-resumed on restart.

## Schema

### human_interactions
- `id` (INTEGER PRIMARY KEY AUTOINCREMENT): Interaction ID.
- `session_id` (INTEGER NOT NULL): Foreign key to `sessions(id)`.
- `execution_id` (INTEGER NOT NULL): Foreign key to `session_executions(id)`.
- `question` (TEXT NOT NULL): The human-readable question.
- `context_json` (TEXT): Optional JSON-encoded structured context.
- `status` (TEXT NOT NULL): `PENDING` or `ANSWERED`.
- `created_at` (TEXT NOT NULL): UTC timestamp in RFC3339Nano format.
- `answer` (TEXT): Populated once answered.
- `answered_at` (TEXT): UTC timestamp in RFC3339Nano format; populated once answered.
- Indexes: `(session_id)`, `(execution_id)`, and a partial unique index
  `idx_human_interactions_one_pending_per_execution` on `(execution_id) WHERE
  status = 'PENDING'` enforcing at most one pending question per execution.

Migration version 10 adds this table. Existing databases migrate forward without
data loss. Historical interactions are not retroactively generated.

## Event Types
- `interaction.requested`: A pending question was persisted. Details include
  interaction_id, execution_id, status.
- `interaction.answered`: A question was answered. Details include
  interaction_id, execution_id, status. The answer text is **not** recorded in
  the event payload.
- `execution.waiting_for_user`: Execution entered `WAITING_FOR_USER`. Details
  include interaction_id, execution_id, current_step, total_steps, status.
- `execution.resumed_after_input`: Execution resumed after an answer. Details
  include execution_id, interaction_id, current_step, total_steps, status.

## Idempotency
- `RequestHumanInput` for an execution that already has a `PENDING` interaction
  returns the existing interaction with `created=false` and appends no event, so
  retries, restarts, and repeated worker calls cannot create duplicate questions
  or overwrite the existing question.
- `AnswerHumanInput` on an already `ANSWERED` interaction returns the existing
  interaction with `applied=false` and appends no event, so an answer can never be
  lost or replaced. Concurrent submissions apply exactly once.
- `ResumeAfterHumanInput` requires an `ANSWERED` interaction; concurrent resumes
  succeed exactly once. Resuming before answering is rejected.

## Atomicity
- Persisting a question, both state transitions, and both events occur in a
  single SQLite transaction.
- Recording an answer and its event occur in a single transaction.
- Resuming and its event occur in a single transaction.
- If an event insert fails, the whole transition rolls back; no partial
  interaction row, answer, or state change is committed.

## Worker Semantics
- The deterministic fake worker accepts a configured pause step. After
  committing that checkpoint it calls `RequestHumanInput` and returns, releasing
  its worker slot; it never stays alive while waiting and never repeats a
  committed checkpoint.
- Resume drives a new worker from `execution.CurrentStep`, so no checkpoint step
  is emitted twice.
- A pause configured at the final step still completes on resume without an
  extra checkpoint.

## Configuration
- `FORGEHAND_FAKE_WORKER_PAUSE_STEP` (positive integer) pauses the fake worker
  after the given checkpoint. Unset, `0`, or any invalid value disables the
  pause, leaving default fake-worker behavior unchanged.

## Daemon IPC
- `session-status`: includes a pending `Interaction` when the session is
  `WAITING_FOR_USER`.
- `session-questions`: lists pending interactions for a session.
- `interaction-get`: returns a single interaction by ID.
- `interaction-answer`: records an answer and reports `AnswerApplied`.
- `session-resume`: dispatches to `ResumeAfterHumanInput` for a
  `WAITING_FOR_USER` session, otherwise to `ResumeSessionExecution`.

## CLI
- `forgehand session questions <id>` lists pending questions.
- `forgehand session answer <interaction-id> "answer text"` answers a pending
  question (idempotent).
- `forgehand session status <id>` prints `Awaiting input: interaction <id>` and
  the question when waiting.

## Verification
- State tests cover atomic transitions, one-pending-per-execution, request/answer
  idempotency, checkpoint preservation and absence of duplicate checkpoint
  events, resume-before-answer rejection, invalid IDs, persistence across
  close/reopen, and concurrent answer/resume submissions applying exactly once.
- Fault-injection tests confirm interaction or event insertion failures roll back
  the question request, the answer, and the resume.
- Daemon tests cover the IPC operations, pending status reporting, end-to-end
  pause/answer/resume through IPC, and releasing active worker capacity while
  waiting.
- Integration tests cover graceful SIGTERM and SIGKILL while waiting, restart
  preserving the pending question without duplication, and resuming after
  restart.
- Migration tests assert schema version 10, the new table, and v9->v10
  preservation without retroactive interactions.
