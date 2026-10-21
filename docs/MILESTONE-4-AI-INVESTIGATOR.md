# Milestone 4: First Evidence-Backed AI Investigator

## Outcome

Forgehand can complete and persist one real, read-only AI investigation against a registered Git repository. The worker reuses the existing SQLite session/execution records, durable FIFO dispatcher, commit-pinned repository inventory, OpenAI-compatible provider, CLI, and TUI. It introduces no second scheduler, provider framework, daemon, database, or autonomous tool loop.

Phase 4 remains partial. The demonstrated question-only baseline could not inspect the repository, and the assisted run improved answerability rather than token or latency efficiency.

## User workflow

Configure an OpenAI-compatible local provider and explicitly qualify its model for investigation:

```sh
export FORGEHAND_PROVIDER_BASE_URL=http://127.0.0.1:8000/v1
export FORGEHAND_PROVIDER_MODEL=worker
export FORGEHAND_PROVIDER_CAPABILITIES=investigation
export FORGEHAND_PROVIDER_TIMEOUT=5m
export FORGEHAND_PROVIDER_MAX_TOKENS=4096
export FORGEHAND_PROVIDER_MAX_CONTEXT_CHARS=28000
```

Then use the existing daemon and session commands:

```sh
forgehand project add /path/to/repository
forgehand session start --investigate --project 1 --task "How does recovery work?"
forgehand session run 1
forgehand session status 1
forgehand session result 1
```

The TUI uses the same IPC commands to create investigation sessions and render their persisted reports/evidence. A provider without the explicit `investigation` capability is rejected before session creation.

## Implementation

The investigator:

1. records the original question in an `investigation` session;
2. reuses the deterministic investigation bootstrap to rank committed files and exact excerpts;
3. follows a bounded number of relevant qualified calls to their declarations;
4. pins repository revision, inventory, Git blob hashes, source ranges, and relevance explanations;
5. makes one cancellable, timeout-bounded provider call using a strict JSON-schema response request;
6. separates observed facts, reasoned conclusions, uncertainty, and next investigation steps;
7. rejects malformed reports, removes unsupported citations, and demotes claims left without valid citations to uncertainty;
8. atomically persists the normalized report, completion event, token counts, latency, and terminal session state.

Repository text is framed as untrusted evidence. The model cannot request shell execution or source edits. Provider failure, timeout, cancellation, malformed JSON, or a report with no supported facts/conclusions fails durably and does not fabricate a fallback result.

## Acceptance investigation

The isolated acceptance run used Forgehand itself at revision `0696dc462e07c210dbd2b5e523d6f78bd0336853` and asked:

> How does Forgehand recover interrupted sessions after daemon shutdown, and what prevents duplicate execution when the daemon restarts?

Provider/model actually tested:

- OpenAI-compatible router: `http://192.168.10.25:8000/v1`
- model alias: `worker`
- llama.cpp preset: `ryhme246/Qwen3.5-4B-Q4_K_M-GGUF:Q4_K_M`
- context size: 32,768; reasoning disabled; response cap: 4,096 tokens
- no provider installation, model installation, service restart, or deployment was performed

Persisted assisted-run metrics:

- prompt remained within the 28,000-character evidence/context bound;
- 6,321 prompt tokens + 1,946 completion tokens = 8,267 total;
- provider latency: 20,236 ms;
- 35 citations validated; 13 unsupported citations removed/demoted;
- the report was retrieved successfully after stopping and restarting only the disposable development daemon with the same SQLite state.

The report correctly identified:

- startup reconciliation calls `InterruptRunningExecutions`, `RecoverRunningWorkPlanTasks`, and `ReconcileInterruptedQualificationJobs`;
- `InterruptSessionExecution` preserves the last committed checkpoint;
- work-plan task recovery changes orphaned `RUNNING` tasks to `READY`;
- the dispatcher is a single scheduling loop and uses `ClaimNextQueuedExecution`;
- `ClaimNextQueuedExecution` atomically changes an eligible durable `QUEUED` execution to `RUNNING`;
- `workerGroup.owners` prevents two live workers from owning one execution within one daemon run but is intentionally in-memory and is not a cross-restart lease.

The report did not establish automatic resumption or exactly-once execution across arbitrary multi-daemon/crash scenarios. It retained those as uncertainty. Direct source review additionally shows that interrupted sessions require the existing resume path; they are not silently fabricated or automatically reported as completed.

Representative persisted conclusion:

```text
Cross-restart duplicate execution is prevented by durable atomic state transitions
(e.g. RUNNING->INTERRUPTED), but in-memory worker ownership is lost and does not
protect against duplicates across restarts.
```

This is intentionally narrower than a claim of globally exactly-once execution.

## Same-model baseline

Procedure: one non-streaming `/chat/completions` call to the same `worker` alias, the same 4,096-token response cap, no tools, no repository files, and an instruction not to invent paths, symbols, or citations. The baseline therefore could not inspect the repository; this limitation is not evidence that Forgehand would outperform a repository-aware agent.

| Measure | Forgehand-assisted | Question-only baseline |
|---|---:|---:|
| Repository access | Bounded commit-pinned package | None |
| Correct components identified | Startup reconciliation, interruption, work-plan recovery, dispatcher, atomic queue claim, in-memory owner guard | None; correctly declined |
| Citations | 35 validated, 13 rejected/demoted | None |
| Prompt tokens | 6,321 | 89 |
| Completion tokens | 1,946 | 124 |
| Total tokens | 8,267 | 213 |
| Latency | 20.236 s | 1.379 s |

The assisted run was substantially more informative and independently reviewable, but materially more expensive and slower. Model output varied across repeated development runs; deterministic evidence selection and strict validation were reproducible, while generation itself was not seeded. A fair Phase 4 exit evaluation still needs a baseline with comparable repository inspection ability.

## Verification

Completed on the implementation before documentation-only changes:

```text
go test -count=1 ./...                                                      PASS
go test -race -count=1 ./internal/daemon ./internal/state ./internal/provider PASS
go vet ./...                                                               PASS
go build ./...                                                             PASS
git diff --check                                                           PASS
```

The end-to-end run used a clean clone, a disposable `/tmp` state directory, a development daemon, and the configured local provider. The installed Forgehand service was not modified, restarted, or deployed.

## Known limitations and next milestone

- Single-pass investigation only; no model-directed retrieval loop or clarification turn.
- Citation validation proves that a cited path/line was supplied, not that the prose semantically entails every word of the claim.
- Retrieval is language-independent text matching plus bounded call following; component analysis remains strongest for Go.
- Response quality varies with the local model; a 2,048-token cap produced a safely rejected truncated JSON response, while 4,096 succeeded.
- No distributed lease or globally exactly-once execution guarantee is claimed.
- SQLite remains the durable store; Git-friendly narrative knowledge is still open.

Recommended next: evaluate semantic claim-to-evidence entailment and a repository-aware baseline under equal token/tool budgets, then improve retrieval precision and latency before adding multi-pass tools or any coding capability.
