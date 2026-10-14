# Milestone 2C: Durable Session Event History

## Schema

### session_events
- `id` (INTEGER PRIMARY KEY AUTOINCREMENT): Monotonically increasing event ID used as cursor. Deterministic ordering by ID.
- `session_id` (INTEGER NOT NULL): Foreign key to `sessions(id)`.
- `execution_id` (INTEGER): Foreign key to `session_executions(id)`, NULL when event applies to session lifecycle only.
- `event_type` (TEXT NOT NULL): Stable event type.
- `timestamp` (TEXT NOT NULL): UTC timestamp in RFC3339Nano format.
- `details_json` (TEXT NOT NULL DEFAULT '{}'): JSON-encoded structured details (versionable).
- Indexes: `(session_id, id)`, `(execution_id)`

Migration version 9 adds this table. Existing databases migrate forward without data loss. Historical events are not retroactively generated.

## Event Types
- `session.created`: Session created (READY). Details include title and initial state.
- `execution.started`: Execution started. Details include execution_id, total_steps, status.
- `execution.checkpoint`: Checkpoint completed. Details include execution_id, step, current_step, total_steps. Emitted on successful advance; not replayed on resume.
- `execution.completed`: Execution completed. Details include execution_id, current_step, total_steps, status.
- `execution.interrupted`: Execution interrupted (graceful interrupt or recovery). Details include execution_id, status, session_state; `recovery=true` when emitted during startup recovery.
- `execution.failed`: Execution failed. Details include execution_id, status, session_state.
- `execution.resumed`: Execution resumed. Details include execution_id, current_step, total_steps, status.

## Cursor Semantics
- `id` provides the exclusive cursor. The API accepts `after_event_id` and returns events with `id > after_event_id`.
- Results are ordered ascending by `id`.
- `NextEventID` in the response is the last returned event's ID. Clients can continue with `after_event_id = NextEventID`.
- Exclusive cursor prevents duplicates; successive requests retrieve complete history without gaps.

## Atomicity
- All state transitions that emit events occur within a single SQLite transaction.
- Events are appended via the same transaction as the corresponding INSERT/UPDATE.
- If event insertion fails, the transaction rolls back and the state transition does not commit.
- If the state transition fails, no event is emitted.
- Startup recovery (`InterruptRunningExecutions`) appends events for each execution successfully interrupted within that transaction.
- Event retrieval is read-only and does not modify state.

## Implementation Notes
- Event details are stored as JSON to allow versioning without schema migrations.
- Timestamps are stored in RFC3339Nano UTC; returned in RFC3339Nano UTC.
- Events are append-only through the application API.

## Verification
- Unit tests cover event creation, checkpoint behavior, pagination (including exclusive cursors and empty pages), persistence across close/reopen, concurrent workers, failure/interruption, resume behavior, migration from v8 preserving existing data without retroactive events, fault injection (event insertion failure rolls back state), API validation (unknown session errors, limit clamping), and daemon IPC events.
