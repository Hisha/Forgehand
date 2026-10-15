# Milestone 3A: Model-Backed Coding Worker

Milestone 3A introduces the first real model-backed AI coding worker that executes through the existing durable scheduler (dispatcher). It extends the session model with coding sessions tied to projects, builds bounded read-only repository context, calls an OpenAI-compatible provider (non-streaming) via standard net/http, persists model results in durable state, and exposes new CLI operations. The fake worker remains for deterministic tests. All repository access is read-only for 3A; no file editing, tool-calling loops, or multiple providers are introduced.

## Requirements Summary

- Exactly one scheduling path: new and resumed coding sessions go through the dispatcher (no second worker-management system). The fake worker remains for deterministic tests.
- Provider: OpenAI-compatible `POST /v1/chat/completions`, non-streaming only, standard `net/http`, no external SDK. Must send system+user messages, parse assistant responses, handle non-200, respect context cancellation, enforce timeouts, reject malformed/empty responses, and never log credentials.
- Repository context must be bounded (limits on file count, per-file size, total size) and skip `.git` internals, binaries, build output, dependency caches, credentials/secrets, large generated files. Repo text is untrusted and must not override system instructions.
- Session model extended with kind (`fake` or `coding`), associated `project_id`, `task`, and `model`/`provider` metadata.
- Migration v13 adds columns to `sessions` and `session_executions`, creates `model_results` table with unique constraint on `execution_id`.
- Execution completion for coding sessions writes the model result in one transaction with execution completion and the `model.response.completed` event (atomic). A failed/canceled model execution must not commit a result after cancellation and must release its worker slot.
- CLI: `session start --project <id> --task "..."` creates/queues a coding session; existing `session start <title>` still creates a fake session. Add `session status` fields and `session result <id>`. IPC extended with corresponding fields.
- Config env vars: `FORGEHAND_PROVIDER_BASE_URL`, `FORGEHAND_PROVIDER_MODEL`, `FORGEHAND_PROVIDER_API_KEY`, `FORGEHAND_PROVIDER_TIMEOUT`, `FORGEHAND_PROVIDER_MAX_TOKENS`.
- Isolated acceptance: everything under `/tmp`, no modifications to system paths or installed services; only kill exact verified disposable PIDs. No commit/push/install/deploy.
- Verification commands: `go test -count=1 ./...`, `go test -race -count=1 ./internal/state/... ./internal/daemon/...`, `go vet ./...`, `go build ./...`, `git diff --check`.

## Architecture

### Provider (internal/provider)

- Interface: `Provider` with `Complete(ctx context.Context, req ChatRequest) (ChatResponse, error)`.
- Types: `ChatRequest{System string, User string, Model string, MaxTokens int}`, `ChatResponse{Content string, Model string}`.
- OpenAI-compatible client using `net/http.Client` with configured base URL and timeout, posting to `baseURL + "/chat/completions"`. Accepts non-streaming JSON: send `messages=[{role:"system"},{role:"user"}]`, `model`, `max_tokens` (or equivalent), `stream=false`. Parse response for `choices[0].message.content`. Handle non-200 by returning error with status/body summary (no secrets). Never log credentials.
- Provider ID constant: `"openai-compatible"`.
- Do not implement streaming, tool-calling, retries beyond context/transport, multiple providers, or routing.

### Repository Context (internal/repocontext)

- Bounded builder using state DB (latest inventory + component observation) and `repository` (Observe, ReadBlob). Read-only.
- Deterministic file selection strategy (documentation category, component evidence files, task-token matching on paths/identities), explicit path denylist (skip `.git`, build outputs, dependencies, caches, binaries, credentials/secrets, large generated files).
- Enforce per-file size limit, total size limit, max file count. Record warnings/truncation when intelligence is missing or limits hit.
- System instructions must not be overridden by repo text; repo content is untrusted.
- Output context assembled into `system + user` messages with clear section headers; preferred model response organization (informational): Summary / Relevant Files / Proposed Changes / Risks / Recommended Validation (not enforced as strict JSON). Preserve original model response verbatim.

### State Schema (migration v13)

- `sessions` additions: `project_id INTEGER`, `task TEXT`, `kind TEXT NOT NULL DEFAULT 'fake'`, `provider TEXT`, `model TEXT`. Indexes on `project_id` if useful; `kind` tracked.
- `session_executions` additions: `kind TEXT` (or derive from session; include for claim/select convenience). 
- `model_results` table:
  - `id INTEGER PRIMARY KEY AUTOINCREMENT`
  - `session_id INTEGER NOT NULL`
  - `execution_id INTEGER NOT NULL UNIQUE`
  - `provider TEXT`
  - `model TEXT`
  - `content TEXT NOT NULL` (original model response)
  - `created_at TEXT NOT NULL`
  - FOREIGN KEY(session_id) REFERENCES sessions(id), FOREIGN KEY(execution_id) REFERENCES session_executions(id)
- Index on `session_id` for list-by-session.

New state methods:
- `CreateCodingSession(ctx, title, projectID, task, provider, model) (Session, error)` (creates READY session with kind `coding`).
- `CompleteModelExecution(ctx, executionID, provider, model, content) error` — atomic: insert into `model_results`, append `model.response.completed` event, transition execution to COMPLETED and session to COMPLETED, all in one transaction. Only permitted if execution is RUNNING; prevents committing result after cancel/fail.
- Add `Kind` to `Session` and `SessionExecution` structs. 

`QueueSessionExecution` signature remains unchanged (calls pass totalSteps). Derive execution `kind` from `session.kind` inside the enqueue transaction (read session row) to avoid changing call sites.
- Event constant `EventTypeModelResponseCompleted = "model.response.completed"` (or reuse existing naming convention per existing events). Add to events.

### Daemon/Worker

- Dispatcher remains single scheduling path; in `dispatch()` when claiming next queued execution, inspect `execution.Kind` (or load session kind) — if `coding`, launch model worker; else launch fake worker. Keep existing fake worker unchanged for tests.
- Model worker: build bounded repo context for execution's session/project/task; call provider with context cancellation and timeout; on success call `CompleteModelExecution` atomically; on provider error call `FailSessionExecution`; on context cancel (worker stop / cancellation signal) do not call CompleteModelExecution — rely on `handleWorkerStop` to reconcile (if still RUNNING, interrupt/fail per existing logic). A canceled execution must never commit a model result.
- WorkerGroup/ownership same: claim before goroutine start, release on exit, `workers.cancel()` signals in-flight work on session cancel.
- No new worker-management system.

### IPC/CLI

- Request additions: `Task string`, `Model string` (and existing ProjectID/Title/SessionID used).
- Session response: add fields `ProjectID`, `Task`, `Kind`, `Provider`, `Model`.
- Add `ModelResult` type and include in response for `session result`: `ModelResult{SessionID, ExecutionID, Provider, Model, Content, CreatedAt}`.
- Daemon handlers:
  - `session-start` extended: if `ProjectID` and `Task` provided (or `request` carries coding params), create coding session via `CreateCodingSession`. Otherwise preserve behavior (fake session with just Title).
  - `session-result <id>` (new command): get latest execution's model result by session ID (load model_results for most recent execution or by session).
- CLI:
  - `session start --project <id> --task "..."` (optionally `--model <name>`, `--provider openai-compatible` default). Title may be derived or optional; maintain backward compatibility: `session start <title>` unchanged.
  - `session status <id>` shows new fields when present (Project, Task, Kind, Provider, Model).
  - `session result <id>` prints result metadata and content (truncate reasonably if very large, but preserve content; use tabular/sections per existing CLI style).
- Keep existing commands unchanged in semantics for fake sessions.

### Config

Add env vars in `internal/config`:
- `FORGEHAND_PROVIDER_BASE_URL` (required for coding sessions)
- `FORGEHAND_PROVIDER_MODEL` (default or required)
- `FORGEHAND_PROVIDER_API_KEY` (optional; send as Authorization Bearer if set)
- `FORGEHAND_PROVIDER_TIMEOUT` (duration or seconds; default reasonable, e.g. 120s)
- `FORGEHAND_PROVIDER_MAX_TOKENS` (int, default reasonable)
Follow existing validation style (absolute paths, format checks). Never log these values.

### Testing Constraints

- Tests remain isolated under `/tmp` as in existing integration tests. Fake worker path unchanged.
- Provider may be mocked in unit tests (interface); integration tests for model worker use a local mock HTTP server if needed, never external network.
- Acceptance scenarios A–E as specified (A successful coding task; B restart persistence; C failing provider releases capacity + queue continues; D cancel in-flight with no commit; E optional real llama.cpp endpoint — do not install/start, report if unavailable).

## Implementation Order

1. Migration v13 + state types/methods (sessions/executions/events/model_results, CreateCodingSession, CompleteModelExecution, event constant)
2. Provider package (interface + OpenAI-compatible client)
3. Repocontext package (bounded builder with limits/denylist)
4. Daemon model worker + dispatcher kind branch
5. IPC protocol + daemon handlers
6. Config env vars
7. CLI updates (session start flags, status, result)
8. Tests (unit + integration following existing patterns)
9. Docs updates (ROADMAP.md, IMPLEMENTATION-STATUS.md), write this milestone doc, verification

## Verification

Run: `go test -count=1 ./...`, `go test -race -count=1 ./internal/state/... ./internal/daemon/...`, `go vet ./...`, `go build ./...`, `git diff --check`. Also verify acceptance per scenarios with isolated disposable setup.
