# Forgehand Research Agenda

Forgehand should resolve these questions through targeted research and small technical spikes before committing to a full implementation stack.

## 1. Implementation language

Compare at least:

- Go
- Rust
- .NET
- TypeScript / potential OpenCode reuse

Evaluate for Forgehand's actual needs:

- Linux-first distribution;
- long-running daemon/service behavior;
- subprocess and process-group control;
- async/concurrency ergonomics;
- SQLite support;
- Git integration;
- TUI ecosystem;
- filesystem watching;
- Tree-sitter/LSP integration;
- model-provider HTTP/streaming support;
- testing;
- crash behavior and recovery;
- single-binary/self-contained deployment;
- maintainability with AI coding workers.

Do not select a language based solely on familiarity or benchmark performance.

## 2. OpenCode architecture and reuse

**Decision clarification (October 2026):** Forgehand is an independent application. OpenCode is a useful reference for TUI, providers, tool permissions, and session UX, and may be used during Forgehand development. Integrating with, wrapping, or depending on OpenCode is not a product objective. Prior options below remain historical research categories, not an active integration plan. Go has been selected and is in use; do not reopen the language choice without evidence of a concrete blocker.


Study OpenCode to determine:

- how its TUI is structured;
- how sessions/messages/tool calls are represented;
- how model providers are abstracted;
- how tools and permissions are implemented;
- how repository context is gathered;
- what is reusable under its license;
- which architectural assumptions conflict with Forgehand.

Outcome categories should be explicit:

- reuse components;
- borrow design patterns;
- integrate with it;
- fork it;
- use only as reference.

A fork should not be the default assumption.

## 3. Daemon and client boundary

Prototype the smallest durable session service capable of:

1. starting a fake job;
2. streaming events to a client;
3. detaching the client;
4. continuing the job;
5. reconnecting;
6. requesting human input;
7. resuming after input;
8. completing.

Then terminate the daemon unexpectedly and determine what persistence is actually necessary for safe recovery.

## 4. Knowledge representation

Prototype a minimal project-knowledge model.

Test structured text plus generated SQLite rather than debating serialization abstractly.

Questions include:

- YAML vs JSON vs TOML or another format;
- stable IDs;
- evidence/provenance representation;
- commit association;
- inferred claims vs observed facts;
- stale-knowledge detection;
- regeneration of SQLite;
- merge behavior in Git.

## 5. Repository cataloging

Choose several real repositories of different shapes and determine what can be cataloged deterministically.

Forgehand itself should eventually be one subject, but testing only against Forgehand would bias the design.

Useful candidates should include at least:

- a .NET project;
- a C/C++ project;
- a project with SQL/configuration;
- a repository with a substantial test suite.

Measure usefulness, not merely quantity of extracted metadata.

## 6. Source intelligence

Investigate the roles of:

- Tree-sitter;
- language servers;
- compiler/build metadata;
- ripgrep/text search;
- Git;
- dependency manifests;
- test discovery;
- static-analysis tools.

Do not assume Forgehand needs to build its own universal call graph.

## 7. Model roles

Only after the project representation exists, test which local models are actually sufficient for:

- semantic cataloging;
- investigation;
- planning;
- coding;
- verification.

Avoid choosing permanent agent boundaries before measuring where models fail.

## 8. Scheduling prototype

**Immediate research task:** audit existing fake-worker/session and daemon-recovery implementation against the original Phase 2 acceptance matrix before proposing new scheduler code. Test with an isolated daemon, two sessions, a durable human wait, client detach/reattach, resource contention, and abrupt restart. Record observed results and missing behavior rather than assuming either success or absence.


Use fake workers before real models.

Prove:

- multiple sessions;
- READY/RUNNING/WAITING_USER behavior;
- priorities;
- resource claims;
- safe checkpoint yielding;
- no work loss on client disconnect.

Do not build an advanced scheduler until simple scheduling produces a concrete limitation.

## 9. Investigation topic extraction (spike record, October 2026)

**Question.** Can a replaceable model extract investigation topics better than the deterministic extractor — enough better to justify putting a model on the critical path of every investigation request?

**Hypothesis.** A qualified local model, asked for a bounded grounded concept list under a strict JSON schema, should beat the rule-based extractor on concept coverage, because it understands requests that name compound concepts in prose. A model that cannot beat the rules should not be integrated, however good its phrases look.

**Prototype.** `internal/topiceval` — a 46-case corpus of engineering investigation requests with human-reviewed required concepts, required identifiers, and forbidden concepts; a model-independent scorer (`internal/topiceval/score.go`, no LLM judge); a production-shaped model-assisted candidate with a bounded capability contract, strict schema, grounding, size limits, and a deterministic fallback (`internal/topiceval/candidate.go`); three prompt variants; and a live harness that scores a model, a hybrid, and repeatability, writing the raw responses to an artifact.

**Observed result.** Over the same corpus and scorer: the deterministic extractor scored concept recall 0.856, phrase recall 0.647, identifier recall 1.000, 2 forbidden hits, 0 ungrounded words, 0 schema failures, 7 perfect cases, 0.1 ms, repeatability 1.000. The best model configuration (Qwen3.5-4B, coverage prompt) scored 0.771 / 0.452 / 1.000 / 7 / 1 / 2 schema failures / 3 perfect cases / 561 ms / stability 0.245. Qwen3-4B-Instruct-2507 scored 0.726 / 0.369 / 0.875 / 2 / 1 / 0 / 2 / 4905 ms / 0.876. The hybrid path — model when usable, deterministic otherwise — never reached the incumbent alone (best hybrid 0.786 / 0.489). Full evidence in `docs/INVESTIGATION-TOPIC-EXTRACTION-EVALUATION.md`.

**Decision.** The deterministic extractor stays and the model path is not integrated. No `investigation_topic_extraction` capability, no configurable model assignment for it, no daemon dispatch path, no change to the work-plan method table, the capability allow-list, or the TUI. The evaluation also found real defects in the incumbent, which were fixed in `internal/planner/investigation.go` (negated-restriction fragments, generic politeness words, instruction sentences leaking into topics), raising corpus recall 0.834 → 0.856 and cutting forbidden hits 7 → 2.

**Rejected alternatives.** A hybrid that always prefers the model and falls back on failure — the fallback is the incumbent, so the hybrid can only lose the incumbent's accuracy or the model's reproducibility, and it did both. A coverage prompt that asks for as many concepts as the budget allows — it raised raw recall to 0.771 but pushed stability to 0.245 and forbidden hits to 7. GLiNER2-base as a named-entity extractor — the base checkpoint returned HTTP 401 and the multi checkpoint needs a PyTorch/Transformers runtime that is not installed, which is disproportionate infrastructure for a candidate that a 4B chat model already lost to a rule. A larger model on the shared router — the router is limited to one instance, and requesting another model would have evicted the running daemon's instance.

**Remaining uncertainty.** The corpus is 46 hand-written cases, not a production request log; a different corpus could shift individual numbers. A much larger model might close the phrase-quality gap, though that would change answer quality rather than the fact that the deterministic extractor is faster, reproducible, and already better on this corpus. Nothing here measures whether better topics lead to a better investigation outcome, which would need an end-to-end study outside the topic-extraction boundary.

## Research discipline

Each spike should record:

- question;
- hypothesis;
- prototype;
- observed result;
- decision;
- rejected alternatives;
- remaining uncertainty.

Forgehand should prefer evidence from small experiments over speculative architecture.
