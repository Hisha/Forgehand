# Forgehand — Phase 2 Persistent Session Engine Acceptance Audit

**Date:** October 2026
**Repository:** `/home/smithkt/git/Forgehand`
**Commit audited:** `cfddc01` (clean working tree at audit start)
**Nature:** Acceptance audit only. No production code, schema, or dependency changes were made.

**Objective:** Determine, with evidence, how much of the original Phase 2 persistent
session engine actually works before implementing additional features.

**Method:** Source inspection + existing unit tests + isolated end-to-end acceptance
testing against a disposable binary, disposable `FORGEHAND_STATE_DIR`, isolated
`FORGEHAND_SOCKET_PATH`, a disposable Git repository, and an isolated foreground
daemon running as the current non-root user (`smithkt`, UID 1000). The installed
`/usr/bin/forgehand`, `/var/lib/forgehand`, `/run/forgehand/forgehand.sock`, and the
`forgehand@...` systemd service were not touched.

---

## 1. Evidence-Backed Capability Matrix

**Status key:** PASS · PARTIAL · FAIL · NOT IMPLEMENTED · NOT TESTED
**Evidence key:** SRC = source inspection, UNIT = existing Go unit/integration tests,
E2E = isolated end-to-end acceptance run performed for this audit.

| # | Capability | Status | Evidence | Notes |
|---|-----------|--------|----------|-------|
| 1 | Persistent session creation and retrieval | PASS | SRC + UNIT + E2E | `CreateSession` / `ListSessions` persist to SQLite; `session start`, `sessions` verified. Retrieval is list-only; no get-by-id IPC. |
| 2 | Fake-worker execution and checkpoint persistence | PASS | SRC + UNIT + E2E | 5-step fake worker advances `current_step` and persists each checkpoint; completion sets `COMPLETED` + `completed_at`. |
| 3 | Multiple sessions operating independently | **FAIL** | SRC + E2E | Two sessions can exist, but concurrent worker advancement reliably fails with `SQLITE_BUSY`; the worker aborts silently and the execution is left permanently `RUNNING`. |
| 4 | Client disconnect and reconnect without terminating work | PARTIAL | SRC + E2E | Disconnect-safe: `session run` returns immediately while the daemon-side worker continues (verified). No attach/reconnect or event subscription exists; "reconnect" is only a new request/response. |
| 5 | Daemon interruption detection and recovery | PASS | SRC + UNIT + E2E | Unclean `daemon_runs` row detected on restart; `InterruptRunningExecutions` marks orphaned executions/sessions `INTERRUPTED` and preserves `current_step`. Verified after `SIGKILL`. |
| 6 | Resume without repeating completed checkpoints | PASS | SRC + UNIT + E2E | `ResumeSessionExecution` continues from the recorded `current_step`. Verified resume from step 3 → 5 without redoing steps 1–3. |
| 7 | Durable `WAITING_FOR_USER` requests and responses | NOT IMPLEMENTED | SRC | No waiting state, request/response types, persistence, or CLI surface exist anywhere in `internal/` or `cmd/`. |
| 8 | Priority scheduling and resource allocation | NOT IMPLEMENTED | SRC | No scheduler component, no priority/resource columns, no resource claims. `sessions` has only `id,title,state,created_at,updated_at`. |
| 9 | Safe cancellation and shutdown | PARTIAL | SRC + E2E | Graceful `SIGTERM` stops workers and records a clean daemon run (verified). No user-initiated cancellation of a session, and worker advance errors leave executions stuck `RUNNING` with no failure transition. |
| 10 | Persistent session event history and visibility | NOT IMPLEMENTED | SRC + E2E (DB) | No events/history table; each advance overwrites `current_step` in place. Only final execution row and state survive. CLI exposes session state only, not checkpoint progress. |

**Phase 2 original acceptance list mapping:**

| Original criterion | Result |
|---|---|
| initialize/open a Git project | Already implemented (out of session-engine scope); not re-tested here |
| create a durable engineering session | PASS |
| run a fake long-running worker | PASS |
| detach and reattach a client without ending work | PARTIAL (detach-safe; no reattach/subscription) |
| persist state and events | PARTIAL (state yes; events no) |
| enter `WAITING_FOR_USER`, accept an answer, resume | NOT IMPLEMENTED |
| manage at least two sessions | FAIL under concurrency (creation/list PASS; independent execution FAIL) |
| schedule conservatively at safe checkpoints with resource claims | NOT IMPLEMENTED (step checkpoints exist; no scheduler/resource claims) |
| recover meaningfully after abrupt daemon interruption | PASS |

**Bottom line: Phase 2 is not yet met.** Session persistence, fake-worker checkpointing,
crash detection, and checkpoint resume work. The concurrency, human-input, scheduling,
cancellation, and event-history criteria do not.

---

## 2. Acceptance-Test Procedure and Results

All tests were run against a disposable environment (`/tmp`), never production.

### Environment setup

```bash
AUDIT=/tmp/forgehand-acceptance
rm -rf "$AUDIT"; mkdir -p "$AUDIT/state" "$AUDIT/run" "$AUDIT/repo"
cd /home/smithkt/git/Forgehand
go build -o "$AUDIT/forgehand" ./cmd/forgehand

export FORGEHAND_STATE_DIR="$AUDIT/state"
export FORGEHAND_SOCKET_PATH="$AUDIT/run/forgehand.sock"

# Disposable Git repo
cd "$AUDIT/repo" && git init -q && git config user.email a@b.invalid && git config user.name Audit
printf '# disposable project\n' > README.md
mkdir pkg && printf 'package pkg\n\nfunc Hello() string { return "hi" }\n' > pkg/hello.go
git add -A && git commit -qm "initial commit"

# Runtime dir must deny world access for a 0600 socket
chmod 700 "$AUDIT/run"
cd "$AUDIT" && nohup ./forgehand daemon > daemon.log 2>&1 & echo $! > daemon.pid
```

> Operational note: the daemon refuses a socket parent directory with world access
> (`internal/daemon/runtime.go:43`). A `mkdir`-created directory is `0755`, so the
> runtime directory must be `chmod 700` (or `770` for a `0660` socket). This is a
> deliberate security invariant, not a defect.

### T1 — Session creation and listing

```bash
./forgehand session start "audit-session-A"   # Created session 1, State: READY
./forgehand session start "audit-session-B"   # Created session 2, State: READY
./forgehand sessions
```
Result: both sessions returned `READY` and were listed. **PASS.**

### T2 — Fake-worker execution and checkpoint persistence (client detached)

```bash
./forgehand session run 1   # returns immediately: Started session 1 / Execution: 1 / Progress: 0/5
./forgehand session run 2
# client processes exit at once; worker continues inside the daemon
sqlite3 state/forgehand.db "SELECT id,session_id,status,current_step FROM session_executions;"
```
Observed progression while **no client was attached** (`06:54:56` → `06:55:14`):

```
exec 2 RUNNING 1/5 → 2/5 → 3/5 → 4/5 → COMPLETED 5/5
```
Result: the worker executes in the daemon after the CLI exits and persists each
checkpoint. **PASS** for execution/checkpointing; **PASS** for disconnect-safety.

### T3 — Concurrent sessions (multiple sessions operating independently) — FAILED

Running `session run 1` and `session run 2` together caused a write-lock collision.
The daemon log recorded:

```
forgehand: execution 1 step 4: read execution 1: database is locked (5) (SQLITE_BUSY)
```

Execution 1 was **permanently stuck** at `RUNNING 3/5`; session 1 stayed `RUNNING`.
Reproduced with a second pair (sessions 4 and 5):

```
started 06:56:35
4:RUNNING:0  5:RUNNING:0
4:RUNNING:1  5:RUNNING:1
4:RUNNING:2  5:RUNNING:2
4:RUNNING:2  5:RUNNING:3
4:RUNNING:2  5:RUNNING:4
4:RUNNING:2  5:COMPLETED:5
forgehand: execution 4 step 3: read execution 4: database is locked (5) (SQLITE_BUSY)
```

Result: **FAIL.** Concurrent fake workers do not operate independently. See §4.3
for root cause and required work.

### T4 — Unexpected daemon termination (SIGKILL)

Pre-crash state: execution 1 `RUNNING 3/5`, execution 2 `COMPLETED 5/5`;
`daemon_runs` row 1 `shutdown_clean=0`.

```bash
kill -9 $(cat daemon.pid)
# restart with the same state dir and socket path
nohup ./forgehand daemon > daemon.log 2>&1 &
```
Daemon log after restart:

```
Previous daemon run 1 (PID 576750) did not shut down cleanly
Marked 1 orphaned execution(s) as interrupted
```
Database after restart: session 1 `INTERRUPTED`, execution 1 `INTERRUPTED 3/5`
(checkpoint preserved); session 2 `COMPLETED` untouched. **PASS.**

### T5 — Resume from recorded checkpoint

```bash
./forgehand session resume 1   # Resumed session 1 / Execution: 1 / Progress: 3/5
```
Observed: `3/5 → RUNNING 4/5 → COMPLETED 5/5`; only steps 4–5 ran. Steps 1–3 were
**not** repeated. **PASS.**

### T6 — Graceful shutdown (`SIGTERM`) and cancellation

```bash
./forgehand session run 3   # step 1 reached
kill -TERM $(cat daemon.pid)
```
Daemon log: `Execution 3 stopped at 1/5` / `Forgehand daemon stopped`;
`daemon_runs` row 2 `shutdown_clean=1`. However, session 3 and execution 3 remained
`RUNNING` in the database until the next daemon start reconciled them to
`INTERRUPTED`. There is **no** user-initiated cancel command for a session.
Result: graceful shutdown **PASS**; cancellation **NOT IMPLEMENTED**. Overall PARTIAL.

### T7 — Completed history remains available

After all sequences: sessions 1–3 `COMPLETED` with `completed_at` timestamps; final
execution rows retained. Only the *final* step value is retained per execution —
there is no record of when each of steps 1–5 was reached. **PARTIAL** (final state
history persists; step-event history does not).

### T8 — Disposable SQLite inspection

```bash
sqlite3 state/forgehand.db ".tables"
# component_observations  components  daemon_runs  dependencies
# discovery_observations  inventory_files  project_inventories
# projects  repository_snapshots  schema_version  session_executions  sessions
sqlite3 state/forgehand.db "SELECT * FROM schema_version;"   # 8
```
Session-engine tables: `daemon_runs`, `sessions`, `session_executions`. There is no
events/history table. Persistent state confirmed to survive daemon restart and crash.

---

## 3. Verified Existing Functionality

Independently confirmed by source inspection, unit tests, and/or E2E:

- **Durable sessions** — `CreateSession`/`ListSessions` (`internal/state/sessions.go`);
  `sessions` table (migration v2).
- **Fake-worker lifecycle** — `StartSessionExecution`, `AdvanceSessionExecution`,
  `CompleteSessionExecution` (`internal/state/executions.go`); `runFakeWorker`
  (`internal/daemon/daemon.go:673`); 5 steps × 3s.
- **Checkpoint persistence** — `current_step` committed per step; resume continues
  from it.
- **Crash detection/reconciliation** — `daemon_runs.shutdown_clean`
  (`internal/state/database.go`) + `InterruptRunningExecutions`
  (`internal/state/executions.go:402`), invoked at startup (`internal/daemon/daemon.go:138`).
- **Checkpoint resume without repeat** — `ResumeSessionExecution`
  (`internal/state/executions.go:550`).
- **Disconnect-safe execution** — workers run as daemon goroutines; CLI is
  request/response and exits immediately.
- **Graceful shutdown** — `SIGTERM` cancels the daemon context, waits for workers,
  and records a clean run (`internal/daemon/daemon.go:150-182`).
- **Single-daemon ownership** — advisory `flock` on the state dir
  (`internal/daemon/ownership_linux.go`), owner-independent of socket path.
- **Non-root invariant** — daemon refuses root before touching filesystem.

Existing tests backing these: `internal/state/executions_test.go`,
`internal/state/sessions_test.go`,
`internal/daemon/daemon_integration_test.go`
(`TestConfiguredDaemonStartupSocketAndCleanSIGTERM`,
`TestDaemonRecoversOwnershipAfterCrash`,
`TestDaemonPreservesExistingSessionRecovery`),
`internal/state/database_test.go`.

---

## 4. Missing or Incomplete Functionality

For every PARTIAL/FAIL/NOT IMPLEMENTED result.

### 4.1 Multiple sessions operating independently — FAIL

- **Exists:** `sessions`/`session_executions` tables; `session run` spawns one
  goroutine per execution; two sessions can be created, listed, and (when not
  colliding) completed.
- **Missing:** reliable concurrent write path. `OpenAt` uses the DSN
  `forgehand.db?_pragma=foreign_keys(1)` (`internal/state/database.go:41`) with no
  `busy_timeout` and no WAL/serialized writer. `database/sql` uses multiple pooled
  connections, so two workers advancing near-simultaneously produce
  `SQLITE_BUSY`. `runFakeWorker` (`internal/daemon/daemon.go:673`) treats the error
  as fatal for that worker and `return`s without updating any state.
- **Impact:** an execution is left permanently `RUNNING`; the session cannot be
  resumed (resume requires `INTERRUPTED`) and only a daemon restart reconciles it.
- **Files/functions:** `internal/state/database.go:34-59` (`OpenAt` DSN);
  `internal/daemon/daemon.go:673-738` (`runFakeWorker`).
- **Required work:** set a `busy_timeout` and/or serialize writes (single writer
  goroutine or `_txlock`/WAL); on advance/complete error, transition the execution
  to a defined terminal/interrupted state instead of abandoning it.
- **Dependencies:** none beyond `state`/`daemon`.
- **Suggested acceptance tests:** start N≥3 sessions concurrently and assert all
  reach `COMPLETED` (or a defined failure state) with no execution left `RUNNING`;
  inject a busy error and assert a deterministic failure transition.

### 4.2 Client disconnect and reconnect — PARTIAL

- **Exists:** daemon-side workers continue after client exit (verified).
- **Missing:** any client attach/reconnect concept, session event stream, or
  incremental progress subscription. IPC is one `Request` → one `Response` per
  connection (`internal/daemon/daemon.go:206-671`); `ipc.Request`/`Response`
  (`internal/ipc/protocol.go`) have no stream/subscribe fields.
- **Impact:** a reconnecting client cannot observe progress since last attach and
  cannot "reattach" to a running session as a first-class action.
- **Files/functions:** `internal/ipc/protocol.go`; `handleConnection`
  (`internal/daemon/daemon.go:206`).
- **Required work:** add an event/observation endpoint (poll or subscribe) and a
  per-session progress query; later a TUI attach flow.
- **Dependencies:** depends on 4.3 (event history) for meaningful reattach.
- **Suggested acceptance tests:** disconnect mid-run, reconnect, and retrieve all
  events since a cursor.

### 4.3 Priority scheduling and resource allocation — NOT IMPLEMENTED

- **Exists:** step-boundary checkpointing only; no scheduler.
- **Missing:** eligibility/priority/readiness logic; resource claims such as
  `local-model/...`, `build/...`, `execution/...`; `WAITING_FOR_RESOURCE`; safe
  yield policy. No priority/resource columns in any migration.
- **Files/functions:** `internal/state/migrations.go` (v2/v3 session tables);
  no scheduler package exists.
- **Required work:** add a scheduler that selects eligible work at safe checkpoints
  and honors resource claims.
- **Dependencies:** depends on 4.1 (reliable worker dispatch) and 4.4 (queued state).
- **Suggested acceptance tests:** two sessions requesting a single-exclusive
  resource must not run that critical section concurrently; higher priority is
  selected only at a checkpoint, never mid-step.

### 4.4 Durable `WAITING_FOR_USER` requests and responses — NOT IMPLEMENTED

- **Exists:** nothing.
- **Missing:** a `WAITING_FOR_USER` session state, a persisted human-request
  record (question, why, evidence, options, consequences, recommendation, blocking),
  and an answer command that transitions back to runnable.
- **Files/functions:** `internal/state/sessions.go:10-15` (only READY/RUNNING/
  COMPLETED/INTERRUPTED); `internal/ipc/protocol.go`; `internal/daemon/daemon.go`.
- **Required work:** new migration + state transitions + IPC command + worker
  wait/answer handshake.
- **Dependencies:** depends on 4.1 and 4.5 (event/history visibility).
- **Suggested acceptance tests:** worker enters `WAITING_FOR_USER`, value survives
  restart, answer resumes at the correct checkpoint, resources released while waiting.

### 4.5 Persistent session event history and visibility — NOT IMPLEMENTED

- **Exists:** `session_executions.current_step` (single mutable value),
  `started_at`/`completed_at`, `daemon_runs`; daemon stdout progress lines.
- **Missing:** an append-only event/checkpoint history (what happened, when, in
  what order), durable across restarts, and any CLI/IPC read path for it. The CLI
  exposes only session `state`; progress is only visible in daemon stdout or by
  reading SQLite directly.
- **Files/functions:** `internal/state/migrations.go` (no events table);
  `internal/state/executions.go` (overwrites `current_step`);
  `cmd/forgehand/main.go:199-234` (`listSessions`).
- **Required work:** add an append-only event table (or equivalent) written on each
  checkpoint, and a query to read it.
- **Dependencies:** independent; feeds 4.2 and 4.4.
- **Suggested acceptance tests:** after a multi-step run, the full ordered event
  sequence is retrievable and identical before and after a daemon restart.

### 4.6 Safe cancellation — NOT IMPLEMENTED (safe shutdown PASS)

- **Exists:** graceful `SIGTERM` shutdown, worker context cancellation, clean run
  record.
- **Missing:** a user-initiated `session cancel`; a `CANCELLED` state; and a
  failure transition when a worker step errors (see 4.1).
- **Files/functions:** `internal/daemon/daemon.go:150-182`;
  `internal/ipc/protocol.go`.
- **Required work:** cancellation command + `CANCELLED` state + applying the same
  state machine to internal worker errors.
- **Dependencies:** depends on 4.1.
- **Suggested acceptance tests:** cancel a running session; assert it becomes
  `CANCELLED`, the worker stops at a checkpoint, and no execution remains `RUNNING`.

---

## 5. Recommended Next Implementation Milestone

### Milestone 2B — Reliable concurrent fake-worker execution and failure-safe execution states

**Why this milestone next.** It is the smallest independently testable slice that
removes the defect currently blocking Phase 2 acceptance. Criterion 3 ("manage at
least two sessions") **fails today** because of a concrete, reproducible
`SQLITE_BUSY`/stuck-`RUNNING` bug; every later capability (scheduling, human waits,
TUI reattach, event history) depends on workers being able to run concurrently and
fail deterministically. It can be delivered without touching human-input handling,
scheduling policy, TUI development, or model integration — those remain separate
later milestones.

**Scope**
1. Make concurrent state writes safe: configure SQLite (`busy_timeout`, and/or
   WAL/single-writer serialization) in `internal/state/database.go`.
2. Give `runFakeWorker` a deterministic failure path: on advance/complete error,
   transition execution/session to a defined terminal state (`INTERRUPTED` or a new
   `FAILED`) and stop cleanly. Do not abandon an execution in `RUNNING`.
3. Add a minimal read path so a client can observe execution progress
   (expose `current_step`/`status` for a session via IPC/CLI).

**Explicitly out of scope:** `WAITING_FOR_USER`, scheduler/resource policy, the TUI,
and model providers. Event-history persistence is a strong candidate for the
*immediately following* milestone (see 4.5), which the read path in item 3 will make
trivial to extend.

**Acceptance criteria**
- Running ≥3 sessions concurrently in one daemon drives every execution to
  `COMPLETED`; none is left `RUNNING`; no `SQLITE_BUSY` abort occurs.
- A forced step error yields a deterministic terminal state (not `RUNNING`), and the
  session can be resumed or explicitly cancelled.
- A client can query a session's execution status/progress without reading SQLite.
- Existing tests continue to pass; new tests exercise concurrent workers and the
  error path.

---

## 6. Validation Results

Run from `/home/smithkt/git/Forgehand` after the audit:

| Command | Result |
|---|---|
| `go test -count=1 ./...` | **PASS** — all packages `ok` (e.g. `state 0.232s`, `daemon 2.208s`, `repository 0.346s`) |
| `go vet ./...` | **PASS** — no output, exit 0 |
| `go build ./...` | **PASS** — exit 0 |
| `git diff --check` | **PASS** — no whitespace errors, exit 0 |
| `go test -race -count=1 ./internal/state/ ./internal/daemon/` (extra) | **PASS** — `state 3.057s`, `daemon 10.684s` (note: existing tests do not exercise concurrent workers, so they do not catch the 4.1 defect) |

No failures were observed in the required validation commands.

---

## 7. Modified-File List

Only the audit report is added; the working tree is otherwise unchanged.

- **Added:** `docs/PHASE-2-ACCEPTANCE-AUDIT.md` (this file).
- **No production code, schema, migration, dependency, or test changes.**
- Disposable artifacts left under `/tmp/forgehand-acceptance/` (binary, isolated
  state DB, disposable Git repo, daemon log). The disposable daemon was stopped; the
  production service and `/usr/bin/forgehand`, `/var/lib/forgehand`, and
  `/run/forgehand/forgehand.sock` were not accessed or modified.

---

## 8. Milestone 2B Follow-Up — Implemented (October 13, 2026)

> This section is **appended**. The original audit above (commit `cfddc01`, including
> its failing T3 evidence and §4 findings) is preserved unedited as the historical
> record. Statuses below supersede the matrix **only** for the items noted.

**Objective:** implement Milestone 2B — reliable concurrent fake-worker execution,
deterministic worker failure handling, and a progress read path (see §5).

**Environment:** same isolation method as the original audit — disposable binary,
disposable `FORGEHAND_STATE_DIR`, isolated `FORGEHAND_SOCKET_PATH`, non-root
`smithkt` (UID 1000). The installed `/usr/bin/forgehand`, `/var/lib/forgehand`,
`/run/forgehand/forgehand.sock`, and the `forgehand@...` service were not accessed
or modified.

### 8.1 Root cause addressed

As diagnosed in §4.1, the state DSN was `forgehand.db?_pragma=foreign_keys(1)` with
no `busy_timeout`, no WAL, and deferred transactions. Two workers advancing
concurrently returned `SQLITE_BUSY` immediately; `runFakeWorker` logged the error
and `return`ed, leaving the execution permanently `RUNNING`.

### 8.2 Changes

- **`internal/state/database.go`** — concurrency-safe configuration. DSN now
  `...?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL&_txlock=immediate`,
  with `SetMaxOpenConns(8)`/`SetMaxIdleConns(8)`. The immediate transaction lock
  plus `busy_timeout` serializes writers instead of failing fast.
- **`internal/state/executions.go`** — added terminal `FAILED` transitions:
  `ExecutionStatusFailed`, `FailSessionExecution`, `InterruptSessionExecution`,
  a shared `transitionRunningExecution` helper, and a read-only
  `SessionExecutionStatus(ctx, sessionID)` returning the latest execution.
  `INTERRUPTED` remains recoverable/resumable; `FAILED` is terminal.
- **`internal/state/sessions.go`** — added `SessionStateFailed`.
- **`internal/daemon/daemon.go`** — `runFakeWorker(ctx, store, execution, delay)`
  now takes an `executionStore` interface: context cancellation →
  `InterruptSessionExecution` (checkpoint preserved); advance/complete error with a
  live context → `FailSessionExecution`; bounded `retryOnBusy` (5 attempts, 50 ms
  base backoff) retries `SQLITE_BUSY`/`SQLITE_LOCKED`. Added a `workerGroup`
  (mutex-guarded wait set) so workers cannot be registered concurrently with the
  shutdown wait (fixes a latent `WaitGroup` `Add`/`Wait` race). Added IPC command
  `session-status`.
- **`cmd/forgehand/main.go`** — added `session status <id>` and its usage line.
- **Tests (new/updated):** concurrent full lifecycles and interrupt/fail behavior in
  `internal/state/executions_test.go`; WAL/legacy-DB preservation in
  `internal/state/migrations_test.go`; `internal/daemon/session_worker_test.go`
  (4 concurrent workers all `COMPLETED`, injected failure leaves no `RUNNING`,
  cancellation preserves the checkpoint, resume continues, in-process
  `session-status` over `net.Pipe`); daemon socket `session-status` +
  shutdown-interrupt in `internal/daemon/daemon_integration_test.go`.

State remains a TEXT column; **no schema migration was added** (schema stays v8).

### 8.3 Validation (from `/home/smithkt/git/Forgehand`)

| Command | Result |
|---|---|
| `go test -count=1 ./...` | **PASS** — all packages `ok` |
| `go test -race -count=1 ./internal/state/ ./internal/daemon/` | **PASS** — repeated 5× (`-race`), no data races, no flakes |
| `go vet ./...` | **PASS** — exit 0 |
| `go build ./...` | **PASS** — exit 0 |
| `git diff --check` | **PASS** — no whitespace errors |

> Pre-existing `gofmt` clean but two unrelated, untouched files remain unformatted:
> `internal/query/investigation_test.go`, `cmd/forgehand/investigate_test.go`.

### 8.4 Isolated end-to-end acceptance (Milestone 2B)

Against a disposable binary under `/tmp/opencode/fh-m2b`:

- **Concurrent execution (original T3):** three sessions run concurrently in one
  daemon. All three executions advanced `0/5 → 5/5` and every session reached
  `COMPLETED`; `journal_mode=wal`; **0** `SQLITE_BUSY`/`database is locked` log
  lines; **0** executions left `RUNNING`. (Original T3 FAIL is now resolved.)
- **Progress read path:** `session status <id>` reports state, execution id, status,
  and `current_step/total_steps` while a run is in flight — no direct SQLite read.
- **Crash recovery (T4):** `SIGKILL` mid-run, then restart. Checkpoint preserved at
  step 0 and again at step 2; state reconciled to `INTERRUPTED`, then resumed to
  `COMPLETED`.
- **Graceful shutdown (T6):** `SIGTERM` produces `shutdown_clean=1`.
- Final DB: sessions 1–5 all `COMPLETED`; no `RUNNING` leftovers.

### 8.5 Superseded capability status

| # | Capability | Was | Now |
|---|-----------|-----|-----|
| 3 | Multiple sessions operating independently | FAIL | **PASS** — concurrent runs complete with no `SQLITE_BUSY`/stuck `RUNNING` |
| 9 | Safe cancellation and shutdown | PARTIAL | **PARTIAL (improved)** — graceful shutdown preserved; worker errors now reach a terminal `FAILED`/`INTERRUPTED` state instead of `RUNNING`. User-initiated `session cancel` still absent. |
| 10 | Persistent session event history and visibility | NOT IMPLEMENTED | **PARTIAL** — `session status` exposes execution progress/status via IPC/CLI; append-only event history/§4.5 still not implemented. |
| 8 | Priority scheduling and resource allocation | NOT IMPLEMENTED | **PASS (resource claims)** — Milestone 2G adds exclusive named resource claims (migration v16 `execution_resources`): atomic all-or-nothing acquisition at dispatch, FIFO among eligible with skip-ahead past blocked work, release on completion/failure/cancel/`WAITING_FOR_USER`, no stale ownership after `SIGKILL` + restart, blocker surfaced in `status`/`session status`. See `docs/MILESTONE-2G-RESOURCE-SCHEDULING.md`. Priority scheduling was scoped out as not required for acceptance; §4.3's `WAITING_FOR_RESOURCE` state is unneeded because blocked work stays `QUEUED`. |

**Remaining Phase 2 gaps:** `WAITING_FOR_USER` (§4.4), scheduler/resource
policy (§4.3 — resource claims now implemented, priority remains out of scope),
append-only event history (§4.5), client attach/reconnect (§4.2), and
user-initiated cancellation (§4.6). Milestone 2B's read path is the intended seam for
the event-history work next.

---

## Appendix A — Source Reference Index

| Concern | Location |
|---|---|
| Session state constants | `internal/state/sessions.go:10-15` |
| Create/list sessions | `internal/state/sessions.go:25`, `:66` |
| Start execution | `internal/state/executions.go:26` |
| Advance checkpoint | `internal/state/executions.go:150` |
| Complete execution | `internal/state/executions.go:259` |
| Interrupt running executions | `internal/state/executions.go:402` |
| Resume from checkpoint | `internal/state/executions.go:550` |
| SQLite DSN (no busy_timeout) | `internal/state/database.go:41` |
| Session/execution schema | `internal/state/migrations.go:28-60` |
| Fake worker loop (error path) | `internal/daemon/daemon.go:673-738` |
| `session-run` / `session-resume` handlers | `internal/daemon/daemon.go:576`, `:607` |
| Startup reconciliation | `internal/daemon/daemon.go:125-148` |
| Signal handling | `internal/daemon/daemon.go:150-158` |
| IPC request/response types | `internal/ipc/protocol.go:3-59` |
| CLI session commands | `cmd/forgehand/main.go:85-234` |
| Runtime-dir permission check | `internal/daemon/runtime.go:43` |
| Single-daemon ownership lock | `internal/daemon/ownership_linux.go` |
