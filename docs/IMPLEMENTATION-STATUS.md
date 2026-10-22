# Forgehand Capability Status Matrix

**Status:** October 2026  
**Basis:** Source inspection, automated verification, and isolated local-model acceptance through commit `0696dc4` (October 21, 2026).

**Key:** I=Implemented, P=Partial, N=Not Implemented, U=Unverified

## 1. Foundation & Runtime

| Capability | Status | Evidence | Notes |
|---|---|---|---|
| Go CLI (`forgehand`) | I | `cmd/forgehand/main.go`, `cmd/forgehand/*.go` | Commands: project, inspect, query, investigate, status, session, sessions; CLI tests exist. |
| Unix domain socket IPC | I | `internal/ipc/protocol.go`, daemon accepts/encodes JSON | Versioned protocol; the CLI/TUI rejects incompatible daemons. |
| Daemon (long-running) | I | `internal/daemon/daemon.go` | Runs, handles commands, manages workers, graceful SIGTERM, recovers ownership. |
| Non-root enforcement | I | `internal/daemon/daemon.go:55-66` (`TestRootDaemonRefusalOccursBeforeFilesystemChanges`) | Refuses real/effective UID 0 before filesystem changes. |
| Socket/ownership locking | I | `internal/daemon/ownership_linux.go`, `internal/daemon/daemon_integration_test.go` | Single-daemon via advisory lock on state dir; rejects duplicates; recovers after crash. |
| SQLite persistence | I | `internal/state/database.go`, migrations v1-21 | Migrations tested; foreign keys enabled. Model results include durable token and latency metrics. |
| System service template (systemd) | I | `packaging/systemd/forgehand@.service`, `internal/config/*systemd*` | Template provided; config/systemd validation/tests. |
| Linux installer (Go) | I | `packaging/linux/installer/*.go`, tests | Commands install/upgrade/status/uninstall; wrapper `install.sh`; packaged `forgehand-installer` binary present. |
| Linux user service | I | `internal/service`, `forgehand service ...`, `docs/USER-SERVICE.md` | Unprivileged install/update/uninstall/start/stop/restart/status; preserves state and refuses unmanaged unit overwrite. |

## 2. Git Projects & Repository State
| Capability | Status | Evidence | Notes |
|---|---|---|---|
| Persistent projects | I | `internal/state/projects.go`, tests | Create/list/get with duplicate root rejection. |
| Git project intake (add) | I | `internal/daemon/project_intake.go`, `cmd/forgehand/project add`, `internal/repository/intake*` | Interactively resolves dirty working tree (commit/discard/cancel); verifies expected state. |
| Repository snapshots | I | `internal/state/repository_snapshots.go`, `internal/repository/snapshot*` | Records HEAD/branch/detached/dirty/counts; tests cover detached head, unborn, dirty cases. |
| Deterministic discovery (committed tree) | I | `internal/discovery/*`, `internal/daemon/project_discovery*` | Evidence-backed discovery; committed-tree only. |
| Repository inventory (committed files) | I | `internal/inventory/*`, `internal/daemon/project_inventory*`, migrations v7 | File catalog, categories/languages/build indicators, size/BlobHash/mode; engine v1; truncated budget. |
| Component/dependency mapping (Go AST) | I | `internal/components/*`, migration v8, `internal/daemon/project_analysis*` | Identifies modules/packages/commands/build/config; internal/external/stdlib/unresolved deps; evidence with line/declaration/blob. |

## 3. Queries & Investigation Context
| Capability | Status | Evidence | Notes |
|---|---|---|---|
| Evidence-backed queries (components/deps/dependents/evidence/changes) | I | `internal/query/query.go`, `internal/query/query_test.go`, `internal/daemon/project_query*`, CLI `query` | Read-only, deterministic ordering, provenance preserved. |
| Deterministic investigation context (4B.4) | I | `internal/query/investigation.go`, `internal/query/investigation_test.go`, `internal/daemon/investigation.go`, `cmd/forgehand/investigate*` | Historical selection, suggested files with reasons (declaration/outgoing/incoming), limits/truncation metadata, total counts, warnings, freshness status set by daemon; no working-tree reads; no mutations. |

## 4. Sessions, Execution & Recovery (Phase 2)
| Capability | Status | Evidence | Notes |
|---|---|---|---|
| Persistent sessions (create/list) | I | `internal/state/sessions.go`, migrations v2, `cmd/forgehand session start`, `sessions` | States: READY/RUNNING/WAITING_FOR_USER/COMPLETED/INTERRUPTED/CANCELED (per schema/tests). |
| Fake worker execution lifecycle | I | `internal/state/executions.go`, migrations v3, daemon `session-run`, `session-resume`, `runFakeWorker` | Start advances session to RUNNING; stepwise advance; completion sets COMPLETED; 5 steps, 3s delay (configurable); optional deterministic pause for human input (`FORGEHAND_FAKE_WORKER_PAUSE_STEP`). |
| Resume interrupted execution (checkpoint) | I | `TestResumeSessionExecutionPreservesCheckpoint`, `ResumeSessionExecution` in state, CLI `session resume` | Preserves current step; transitions back to RUNNING. |
| Interrupt running executions on crash/start | I | `InterruptRunningExecutions`, daemon startup checks previous run; integration `TestDaemonPreservesExistingSessionRecovery` | Marks orphaned RUNNING executions as INTERRUPTED and sessions to INTERRUPTED on unclean shutdown detection. |
| Multi-session management | I | Sessions table, durable FIFO queue, `internal/daemon/dispatcher.go`, `internal/daemon/dispatcher_test.go` | Dispatcher schedules multiple sessions across bounded worker capacity (default 2); capacity/FIFO verified by unit and isolated E2E tests. |
| Bounded worker concurrency / FIFO queue | I | `internal/state/queue.go`, migration v12, `internal/daemon/dispatcher.go`, `FORGEHAND_MAX_CONCURRENT_WORKERS`, `docs/MILESTONE-2F-WORKER-SCHEDULING.md` | Durable QUEUED state with monotonic `queue_seq`; single dispatcher enforces max concurrent workers (default 2, env-configurable, min 1); FIFO dispatch; wake on enqueue/completion; queue positions reported in `status`/CLI. |
| Detach/reattach client | I | `internal/tui`, persistent daemon sessions | TUI exits independently and reopens durable sessions; status uses polling because IPC has no event stream. |
| WAITING_FOR_USER / human input | I | `internal/state/interactions.go`, migration v10, `internal/daemon/daemon.go`, CLI `session questions`/`session answer`, `docs/MILESTONE-2D-HUMAN-INTERACTION.md` | Durable pending question; atomic RUNNING->WAITING_FOR_USER; idempotent request/answer; resume after answer preserving checkpoint; survives graceful and unclean restart without auto-resume. |
| Explicit session cancellation | I | `internal/state/executions.go` (`CancelSessionExecution`), migration v11, daemon `session-cancel`, CLI `session cancel`, `docs/MILESTONE-2E-SESSION-LIFECYCLE.md` | Terminal CANCELED from RUNNING/WAITING_FOR_USER/INTERRUPTED; idempotent; rejects COMPLETED/FAILED; atomic event; daemon-local worker ownership stops live workers safely; survives restart. |
| Safe scheduling checkpoints | I | Step-based checkpointing in fake worker; dispatcher (2F) start/capacity/FIFO; resource claims (2G) in `internal/state/queue.go`, `internal/daemon/daemon.go` worker group, migration v16, `docs/MILESTONE-2G-RESOURCE-SCHEDULING.md` | Checkpoints at step boundaries; queued executions declare exclusive named resources; all-or-nothing acquisition at dispatch; FIFO among eligible with skip-ahead past blocked work; release on completion/failure/cancel/human-wait; no stale ownership after restart. No priority policy (out of scope). |
| Recovery after daemon interruption | P | Crash recovery marks interrupted executions/sessions; queued executions survive restart and are re-dispatched; fake worker stops on context cancel | State-level recovery implemented, including queue recovery; full orchestration recovery (e.g., detach/reattach during run) not demonstrated. |

## 5. Workers, TUI, Providers, AI Investigation (later phases)
| Capability | Status | Evidence | Notes |
|---|---|---|---|
| Independent TUI (client of engine) | I | `internal/tui`, `forgehand`/`forgehand tui` | Bubble Tea client opens to a project dashboard grouped by activity (NEEDS ATTENTION/WORKING/IDLE), with independent repository-readiness indicators, registers repositories, starts work plans, polls progress, and renders findings/evidence. Sessions are reachable from the dashboard or a project, not from the home list. |
| Model provider interfaces/adapters | I | `internal/provider/provider.go` | Single OpenAI-compatible adapter with optional JSON-schema response format and usage parsing; no routing/multiple providers, streaming, or tool calls. |
| Real AI investigator worker | I | `internal/daemon/investigation_worker.go`, `internal/daemon/investigation_report.go`, CLI `session start --investigate`, `session result` | Single-pass, read-only worker with commit-pinned evidence, strict JSON request, citation validation, durable normalized report, explicit failure, and provider qualification. Broader evaluation remains Phase 4 work. |
| Coding worker / planner / verifier roles | N | None | Planned (Phase 5). |
| Secure execution isolation / sandboxing | N | Template non-root but execution identity is service account | Non-root is safety invariant, not isolation/sandbox. |
| Pre-execution security assessment / execution policy | N | Documented in SECURITY-PRINCIPLES.md as planned | Not implemented. |
| Functional/security verification pipeline with promotion gates | N | Documented as planned | Not implemented. |
| Git-friendly durable knowledge independent of SQLite | N | Structured knowledge/narrative knowledge not implemented; SQLite is generated index for observations | Phase 3 open. |

## 6. AI Workers (Phase 3/4 prelude)

| Capability | Status | Evidence | Notes |
|---|---|---|---|
| Model provider (OpenAI-compatible) | I | `internal/provider/provider.go`, config `FORGEHAND_PROVIDER_*` | Non-streaming chat completions, timeouts, context cancellation, error handling with non-200 body summary (no credentials logged). Regression tests in `internal/provider/provider_test.go`. |
| Bounded repository context builder | I | `internal/repocontext/builder.go` | Deterministic selection with denylist, per-file/file-count limits and a configurable prompt character budget (`FORGEHAND_PROVIDER_MAX_CONTEXT_CHARS`); reads the inventoried commit's blobs (never the dirty working tree); assembles untrusted system+user messages with structured response guidance. Tests in `internal/repocontext/content_test.go`. |
| Coding session model (schema v13) | I | `internal/state/migrations.go` (v13), `model_results` table, sessions/project/task/kind/provider/model | Migration v13 adds coding session fields and durable model_results with unique execution_id; event `model.response.completed`. Durable completion tested in `internal/state/model_results_test.go`. |
| Model worker and dispatcher branching | I | `internal/daemon/model_worker.go`, `internal/daemon/dispatcher.go`, kind from execution | Single scheduling path: coding executions branch to model worker, others to fake worker; completion via `CompleteModelExecution` is atomic (result + completion event + terminal state). Integration tests in `internal/daemon/model_worker_test.go`. |
| IPC/CLI for coding sessions | I | `internal/ipc/protocol.go` (Task/Model/Result), `cmd/forgehand/main.go`, daemon handlers (`session-start` coding branch, `session-result`) | `session start --project <id> --task "..." [--model <name>]`, `session status` shows project/kind/task/provider/model, `session result <id>` returns model result; backward compatible with existing fake sessions. Tests in `cmd/forgehand/main_coding_test.go`. |

Milestone 3A (see `docs/MILESTONE-3A-MODEL-WORKER.md`) is implemented and verified: `go test -count=1 ./...`, `go test -race -count=1 ./internal/state/... ./internal/daemon/...`, `go vet ./...`, and `go build ./...` pass, and disposable end-to-end acceptance (success, restart persistence, failing-provider queue continuation, in-flight cancellation with no commit) passed against an isolated daemon and a mock OpenAI-compatible provider. All repository access is read-only; no real local model endpoint was available for the optional scenario.

## 7. Work Plans & Investigation Bootstrap (Phase 3.3)

| Capability | Status | Evidence | Notes |
|---|---|---|---|
| Work-plan session and task graph | I | `internal/state/work_plans.go`, `internal/planner/planner.go`, `internal/daemon/work_plan_worker.go` | Durable plans, dependency-ordered tasks, per-task evidence, blocked reasons. |
| Deterministic investigation topic extraction | I | `internal/planner/investigation.go`, `internal/planner/investigation_test.go` | Rule-based, at most eight grounded topics per request; identical output for identical input; no provider call. |
| Deterministic investigation bootstrap | I | `internal/daemon/investigation_bootstrap.go`, `internal/daemon/investigation_bootstrap_test.go` | One bounded read-only pass over commit-pinned inventory and blobs; produces an ordinary work-plan task with ordinary persisted evidence; creates no separate queue or scheduler. |
| Model-assisted topic extraction | N | `internal/topiceval/candidate.go` (evaluation only) | Evaluated against the deterministic extractor and rejected on recall, phrase quality, latency, and repeatability; two configurations also had schema failures. Never wired into the daemon, the provider capability list, or the work-plan method table. See `docs/INVESTIGATION-TOPIC-EXTRACTION-EVALUATION.md`. |
