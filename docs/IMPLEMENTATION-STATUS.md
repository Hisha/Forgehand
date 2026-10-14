# Forgehand Capability Status Matrix

**Status:** October 2026  
**Basis:** Source inspection of Go implementation, tests, migrations, CLI, daemon, git history (commits through b481347).

**Key:** I=Implemented, P=Partial, N=Not Implemented, U=Unverified

## 1. Foundation & Runtime

| Capability | Status | Evidence | Notes |
|---|---|---|---|
| Go CLI (`forgehand`) | I | `cmd/forgehand/main.go`, `cmd/forgehand/*.go` | Commands: project, inspect, query, investigate, status, session, sessions; CLI tests exist. |
| Unix domain socket IPC | I | `internal/ipc/protocol.go`, daemon accepts/encodes JSON | Protocol defined; request/response types exist. |
| Daemon (long-running) | I | `internal/daemon/daemon.go` | Runs, handles commands, manages workers, graceful SIGTERM, recovers ownership. |
| Non-root enforcement | I | `internal/daemon/daemon.go:55-66` (`TestRootDaemonRefusalOccursBeforeFilesystemChanges`) | Refuses real/effective UID 0 before filesystem changes. |
| Socket/ownership locking | I | `internal/daemon/ownership_linux.go`, `internal/daemon/daemon_integration_test.go` | Single-daemon via advisory lock on state dir; rejects duplicates; recovers after crash. |
| SQLite persistence | I | `internal/state/database.go`, migrations v1-10 | Migrations tested; foreign keys enabled. |
| System service template (systemd) | I | `packaging/systemd/forgehand@.service`, `internal/config/*systemd*` | Template provided; config/systemd validation/tests. |
| Linux installer (Go) | I | `packaging/linux/installer/*.go`, tests | Commands install/upgrade/status/uninstall; wrapper `install.sh`; packaged `forgehand-installer` binary present. |

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
| Persistent sessions (create/list) | I | `internal/state/sessions.go`, migrations v2, `cmd/forgehand session start`, `sessions` | States: READY/RUNNING/WAITING_FOR_USER/COMPLETED/INTERRUPTED (per schema/tests). |
| Fake worker execution lifecycle | I | `internal/state/executions.go`, migrations v3, daemon `session-run`, `session-resume`, `runFakeWorker` | Start advances session to RUNNING; stepwise advance; completion sets COMPLETED; 5 steps, 3s delay (configurable); optional deterministic pause for human input (`FORGEHAND_FAKE_WORKER_PAUSE_STEP`). |
| Resume interrupted execution (checkpoint) | I | `TestResumeSessionExecutionPreservesCheckpoint`, `ResumeSessionExecution` in state, CLI `session resume` | Preserves current step; transitions back to RUNNING. |
| Interrupt running executions on crash/start | I | `InterruptRunningExecutions`, daemon startup checks previous run; integration `TestDaemonPreservesExistingSessionRecovery` | Marks orphaned RUNNING executions as INTERRUPTED and sessions to INTERRUPTED on unclean shutdown detection. |
| Multi-session management | P | Sessions table persists multiple; no explicit multi-session scheduler test exercising concurrent dispatch | Can create/list multiple sessions; no demonstrated concurrent scheduling across sessions with resource claims. |
| Detach/reattach client | N | No client detach/reconnect event stream/state beyond process disconnect | IPC is request-response over Unix socket; no session attachment tracking or event subscription shown. |
| WAITING_FOR_USER / human input | I | `internal/state/interactions.go`, migration v10, `internal/daemon/daemon.go`, CLI `session questions`/`session answer`, `docs/MILESTONE-2D-HUMAN-INTERACTION.md` | Durable pending question; atomic RUNNING->WAITING_FOR_USER; idempotent request/answer; resume after answer preserving checkpoint; survives graceful and unclean restart without auto-resume. |
| Safe scheduling checkpoints | P | Step-based checkpointing in fake worker (advances after delay); no scheduler component with policy | Checkpoints exist at step boundaries; no general scheduler enforcing safe yield/resource claims. |
| Recovery after daemon interruption | P | Crash recovery marks interrupted executions/sessions; fake worker stops on context cancel; no end-to-end crash+resume of in-progress step demonstrated beyond state transitions | State-level recovery implemented; full orchestration recovery (e.g., detach/reattach during run) not demonstrated. |

## 5. Workers, TUI, Providers, AI Investigation (later phases)
| Capability | Status | Evidence | Notes |
|---|---|---|---|
| Independent TUI (client of engine) | N | No TUI implementation; CLI is simple command tool | Not present. |
| Model provider interfaces/adapters | N | No provider code | Not implemented. |
| Real AI investigator worker | N | Only deterministic investigation context/query | Deterministic `investigate` is read-only evidence assembly; not an AI worker. |
| Coding worker / planner / verifier roles | N | None | Planned (Phase 5). |
| Secure execution isolation / sandboxing | N | Template non-root but execution identity is service account | Non-root is safety invariant, not isolation/sandbox. |
| Pre-execution security assessment / execution policy | N | Documented in SECURITY-PRINCIPLES.md as planned | Not implemented. |
| Functional/security verification pipeline with promotion gates | N | Documented as planned | Not implemented. |
| Git-friendly durable knowledge independent of SQLite | N | Structured knowledge/narrative knowledge not implemented; SQLite is generated index for observations | Phase 3 open. |
