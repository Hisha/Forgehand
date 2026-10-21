# Forgehand Roadmap and Current Status

**Updated:** October 2026
**Product:** An independent, Linux-first, TUI-first, persistent AI coding environment. OpenCode is a design reference and a tool used to develop Forgehand, **not** a Forgehand runtime dependency.

This roadmap preserves the original phase goals and exit criteria. The 4B.x labels used for recent repository-intelligence work are *implementation milestone identifiers*, not proof that original Phase 4 (AI investigation) or Phase 2 (orchestration) is complete. Do not renumber past commits or declare a phase finished merely because a related command exists.

## Core delivery sequence

1. Establish and validate durable orchestration with **fake workers**.
2. Connect the existing repository intelligence to Forgehand's **own** internal worker tasks.
3. Provide a usable minimal **TUI** to control and reattach to sessions.
4. Add replaceable **model-provider adapters** and a real evidence-backed AI investigator.
5. Add bounded coding, pre-execution safety controls, functional and independent security verification, and a human-controlled promotion decision.

These are dependencies, not a promise of particular implementation packages. The scheduler and TUI can be developed in thin vertical slices rather than as large standalone subsystems.

## Phase 0 — Design baseline

Architectural invariants, terminology, research questions, and security boundaries documented. **Baseline established;** amend with implementation evidence rather than silently changing product direction.

## Phase 1 — Technical spikes

Go, SQLite, Linux daemon/Unix IPC, and installer have been selected/implemented. OpenCode may be studied for interaction and provider patterns only. Remaining spikes should focus on safe fake-worker orchestration, provider streaming/tool calls, TUI reconnect/event consumption, and worker execution policy.

**Exit condition:** implementation choices are backed by small experiments, with alternatives and limitations recorded.

## Phase 2 — Persistent skeleton (NEXT PRIORITY: acceptance gap audit)

Original acceptance criteria remain authoritative:

- initialize/open a Git project;
- create a durable engineering session;
- run a fake long-running worker;
- detach and reattach a client without ending work;
- persist state and events;
- enter `WAITING_FOR_USER`, accept an answer, and resume;
- manage at least two sessions;
- schedule conservatively at safe checkpoints with resource claims;
- recover meaningfully after abrupt daemon interruption.

**Status:** partial infrastructure and earlier fake-worker/session recovery mechanisms exist, but the full acceptance matrix has **not been reverified** against current code. First audit what is already implemented; do not rebuild working mechanisms blindly. No real model is needed to pass Phase 2. Milestone 2D now implements and tests the `WAITING_FOR_USER`/answer/resume criterion (see `docs/MILESTONE-2D-HUMAN-INTERACTION.md`); Milestone 2E adds durable session cancellation with safe worker termination (see `docs/MILESTONE-2E-SESSION-LIFECYCLE.md`); Milestone 2F adds a durable FIFO queue with bounded worker concurrency and a daemon dispatcher (see `docs/MILESTONE-2F-WORKER-SCHEDULING.md`); Milestone 2G adds exclusive named resource claims with atomic all-or-nothing acquisition (see `docs/MILESTONE-2G-RESOURCE-SCHEDULING.md`). Detach/reattach and full orchestration recovery remain open; priority scheduling remains out of scope.

**Exit condition:** demonstrate the entire list against an isolated daemon and disposable state, including a crash/restart scenario, without depending on OpenCode.

## Phase 3 — Project knowledge prototype (SUBSTANTIAL PARTIAL IMPLEMENTATION)

Completed and independently exercised in the recent 4B.x work:

- **4B.1** committed-tree file inventory (`6cd68da`);
- **4B.2** Go AST component/dependency mapping (`d77b856`);
- **4B.3** evidence-backed repository queries (`e25e5c5`);
- **4B.4** deterministic investigation context (`b481347`, reported local commit), with historical selection, freshness/truncation metadata, and suggested files.

These are deterministic persisted observations and retrieval tools, not a semantic reasoning worker. **Still open:** Git-friendly structured and narrative durable knowledge that can rebuild the disposable index; semantic cataloging where useful; broader language coverage and invalidation/update policies. Avoid expanding these areas until Phase 2 is stable unless a concrete worker need justifies it.

**Topic extraction decision (Phase 3.3, October 2026):** an AI-assisted investigation topic extractor was evaluated against the deterministic extractor over a 46-case reviewed corpus with a model-independent scorer, and it lost on concept recall, phrase recall, latency, and repeatability; two configurations also had schema failures. The deterministic extractor stays on the critical path and no model capability was added for it. The evaluation is recorded in `docs/INVESTIGATION-TOPIC-EXTRACTION-EVALUATION.md`; the spike record is in `docs/RESEARCH.md` §9. This closes one question, not Phase 3.

**Exit condition:** Forgehand answers useful project questions with provenance, and its intended durable knowledge survives loss/rebuild of the generated index.

## Phase 4 — AI investigation (PARTIAL: FIRST VERTICAL SLICE IMPLEMENTED)

Introduce a real Forgehand-owned investigator that consumes evidence and repository knowledge, uses a replaceable model provider, requests targeted tools or human clarification as needed, and produces a falsifiable, evidence-backed diagnosis **without editing code**.

**Status:** partial. The first evidence-backed investigator vertical slice is implemented and acceptance-tested (see `docs/MILESTONE-4-AI-INVESTIGATOR.md`). It reuses the durable session/dispatcher/provider path, gathers commit-pinned deterministic evidence, makes one bounded structured model call, validates citations, and persists the normalized report plus token/latency metrics. The CLI and TUI can start and retrieve investigations. It is read-only and has no autonomous tool loop.

The same-model, question-only baseline had no repository access and correctly declined to diagnose the code. The Forgehand-assisted run identified the relevant startup reconciliation, interrupted-state persistence, work-plan recovery, dispatcher, and atomic queue claim, but used more tokens and latency and retained uncertainty around automatic resume and cross-restart guarantees. That demonstrates the first functional slice and a quality advantage over this limited baseline, not the full phase exit criterion or an efficiency advantage.

**Exit condition:** demonstrate better, more efficient investigations than the same model exploring from scratch, with durable sessions and provenance. A deterministic `investigate` command alone does not meet this criterion. **Still open:** compare against a baseline that can inspect the repository under comparable retrieval/tool constraints, reduce prompt cost/latency, and exercise broader repositories/questions before declaring Phase 4 complete.

## Phase 5 — Implementation and verification (PLANNED)

Forgehand-owned planner/coder/verifier roles execute bounded work. Include tool permissions, execution identity and isolation, pre-execution assessment, functional checks, independent security verification, human review and fail-closed promotion. Workers cannot approve their own security exceptions.

**Exit condition:** one real change proceeds from user objective through investigation, plan, code, verification, and promotion decision, with a recoverable audit trail.

## Near-term proposed implementation checkpoints (not yet committed design)

1. **Phase 2 audit:** map current job/session/event/worker/recovery code to each original acceptance criterion and identify missing tests.
2. **Durable job vertical slice:** implement only proven gaps in queue, worker handoff, human wait, multi-session resource scheduling, and restart recovery using fake workers.
3. **Minimal TUI vertical slice:** show jobs/events, submit an objective, detach/reattach, answer a human request; no full-screen design expansion until these flows work.
4. **Internal task/context contract:** link existing investigation context to Forgehand-owned jobs and worker roles, with bounded evidence and explicit provenance.
5. **Provider spike:** local llama.cpp/OpenAI-compatible streaming and tool-call behavior, cancellation, errors, and model switching without losing session state.
6. **AI investigator vertical slice:** one evidence-backed investigation in a durable session, no source modification.

The exact checkpoint numbering should be assigned after the Phase 2 audit; do not automatically label these `4C.x` or `4B.5`.

## Beyond

More advanced resource scheduling, remote/distributed workers, additional language intelligence, GUI, Windows, broader automation, project authorization, and remote authentication follow demonstrated needs. The Linux installer is **already implemented**; additional packaging targets remain future work. Security verification and isolation are requirements for applicable code-execution milestones, **not** optional features to postpone until after autonomous coding.

## Release/acceptance discipline

- Keep development acceptance isolated from `/usr/bin/forgehand`, `/var/lib/forgehand`, `/run/forgehand/forgehand.sock`, and the installed `forgehand@...` systemd service.
- Distinguish **implemented**, **tested**, **locally committed**, **pushed**, and **deployed**. Do not infer later stages from earlier ones.
- Record actual test commands and results; keep unrelated working-tree changes out of milestone commits.
- Review this roadmap when a milestone changes the product boundary or when evidence disproves an assumption.
