# Investigation Topic Extraction Evaluation

**Date:** October 2026
**Scope:** Phase 3.3 — decide whether AI-assisted investigation topic extraction should replace, augment, or stay behind the deterministic investigation topic extractor.
**Result:** the deterministic extractor is better than or tied with every model configuration on every measured metric, and strictly better on concept recall, phrase recall, latency, and repeatability. The model-assisted path was **not** integrated.

This document records the question, the method, the observed numbers, the decision, and the evidence. The commands in [Reproduction](#12-reproduction) rerun the method; exact model outputs can vary because the harness intentionally measured the provider's configured defaults and did not set a sampling seed.

---

## 1. Question

Forgehand turns a natural-language investigation request into a bounded list of grounded concepts (`planner.ExtractInvestigationTopics`) that drives read-only repository discovery. That extractor is deterministic today. The question for this phase was narrow and concrete:

> Can a replaceable model extract investigation topics better than the deterministic extractor — enough better to justify putting a model on the critical path of every investigation request?

Everything outside that question was out of scope: no semantic repository search, no embeddings, no vector database, no new investigation engine, no change to the daemon, the scheduler, the persistence schema, or the TUI.

## 2. Method

### 2.1 Corpus

`internal/topiceval/corpus.go` holds 46 representative engineering investigation requests, each with human-reviewed expectations:

| Field | Meaning |
| --- | --- |
| `Request` | The engineering request as an operator would write it. |
| `Constraints` | Authorized-scope statements. They must never contribute a search topic. |
| `RequiredConcepts` | Concepts a useful extraction must surface. |
| `RequiredIdentifiers` | Language-level identifiers that must survive verbatim, compared case-sensitively. |
| `ForbiddenConcepts` | Request wording that must not become a topic. |
| `NoSubject` / `MaxTopics` | Requests that name no concrete subject and may produce no topics at all. |

The corpus covers daemon recovery and scheduling, configuration, authentication and authorization, database persistence and migrations, API routing, TUI behavior, build and deployment failures, C++ scoped identifiers, Go package-qualified symbols, C# namespaces, Python functions and modules, multi-concept and vague requests, requests carrying irrelevant instructions, requests with no subject, multi-language coordination, numeric noise, quoted subjects, and negative restrictions.

Language names (Go, C++, C#, Python) are deliberately **not** required concepts: they are request qualifiers, not investigation subjects. Language coverage is tested through `RequiredIdentifiers` instead.

### 2.2 Scorer

`internal/topiceval/score.go` scores an extraction against a case **without consulting any model**. No LLM judges any candidate, so a candidate is never graded by another model.

- The comparable vocabulary of an extraction is derived from the topic **terms** only (`planner.SplitIdentifierWords`), so two candidates are compared on the same footing; declared metadata is ignored because the term is the artifact under test.
- Light morphological folding (`wordForms`) treats `persistence`/`persisted` and `crash`/`crashes` as one concept.
- A concept counts as recalled when every one of its words is present in the extracted vocabulary. `PhraseRecall` additionally requires the concept to appear as one topic term, which measures multiword concept quality.
- Identifier recall compares topic terms case-sensitively against the required identifiers.
- Forbidden concepts are counted when their words appear in the extracted vocabulary.
- Ungrounded words are extracted words that do not occur in the request at all: unsupported invention.
- `NoSubject` cases pass only when the extraction stays inside the topic budget, invents nothing, and hits no forbidden concept.

### 2.3 Candidates

| Candidate | What it is | Status |
| --- | --- | --- |
| Deterministic extractor | `planner.ExtractInvestigationTopics`, rule based, no model | Baseline and incumbent |
| Qwen3.5-4B worker | `ryhme246/Qwen3.5-4B-Q4_K_M-GGUF:Q4_K_M`, the model the daemon's `worker` preset already loads | Evaluated, 4 prompt/token configurations |
| Qwen3-4B-Instruct-2507 | `unsloth/Qwen3-4B-Instruct-2507-GGUF` Q4_K_M, served locally on an isolated port | Evaluated |
| GLiNER2-base | `urchade/gliner2-base-v2.1` | **Not practical** — see [§6](#6-candidates-that-were-not-evaluated) |

The model-assisted candidate implementation is `internal/topiceval/candidate.go`. It is shaped like production code on purpose: a bounded capability contract, a strict JSON schema, grounding against the request text, bounded request/response sizes, and a deterministic fallback. It lives with the evaluation because the evaluation rejected it; it was never wired into the daemon, the provider capability allow-list, or the work-plan method table.

### 2.4 Prompts

Three prompt variants were evaluated so a candidate model was given a fair chance before the deterministic extractor was declared the winner (`internal/topiceval/prompts.go`):

- `production` — the bounded capability contract.
- `tight` — the same contract plus the two rules the deterministic extractor already follows: one topic per distinct concept, and short terms.
- `coverage` — the same contract plus an instruction to cover as many distinct concepts as the budget allows, to test whether terseness (not understanding) is what limits the model.

## 3. Measured results

All numbers are over the same 46-case corpus with the same scorer. `model-raw` is the model's own answer; a schema failure scores as zero topics. `model-production` is the hybrid path — the model answer when it is usable and the deterministic extractor otherwise — and must be read as a hybrid, not as a model score.

### 3.1 Headline table

| Candidate | Recall | Phrase recall | Identifier recall | Forbidden hits | Ungrounded | Schema failures | Perfect cases | Mean latency | Stability | Agreement |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| **Deterministic extractor** | **0.856** | **0.647** | **1.000** | **2** | **0** | **0** | **7** | **0.1 ms** | **1.000** | **1.000** |
| Qwen3.5-4B, production, 512 tok | 0.756 | 0.400 | 0.958 | 7 | 1 | 0 | 1 | 481 ms | 0.545 | 0.719 |
| Qwen3.5-4B, production, 1024 tok | 0.768 | 0.446 | 0.958 | 2 | 1 | 0 | 2 | 474 ms | 0.484 | 0.702 |
| Qwen3.5-4B, tight, 512 tok | 0.696 | 0.400 | 1.000 | 3 | 0 | 1 | 1 | 496 ms | 0.455 | 0.631 |
| Qwen3.5-4B, coverage, 512 tok | 0.771 | 0.452 | 1.000 | 7 | 1 | 2 | 3 | 561 ms | 0.245 | 0.413 |
| Qwen3-4B-Instruct-2507, production, 512 tok | 0.726 | 0.369 | 0.875 | 2 | 1 | 0 | 2 | 4905 ms | 0.876 | 0.947 |

Reading of the table:

- **Recall** — the deterministic extractor surfaces more of the concepts a reviewer expects (0.856 against 0.696–0.771). No model configuration came close.
- **Phrase recall** — the gap is largest here (0.647 against 0.369–0.452). The deterministic extractor forms adjacent domain phrases; the model mostly returns single words.
- **Identifier recall** — the deterministic extractor preserves every required identifier. Two model configurations tied on that metric and were still behind on recall, phrase recall, perfect cases, latency, and repeatability; the other three lost 4–12.5 % of the identifiers even when they answered with valid JSON.
- **Forbidden hits** — the models repeatedly turn request wording into topics. The deterministic extractor hit two, both in one deliberately vague case; the best models also hit two, the rest hit three to seven.
- **Schema failures** — the tight configuration produced one nine-topic answer against the eight-topic limit, and the coverage configuration produced two malformed JSON answers. The other three configurations produced no schema failures after the review corrected the candidate contract to accept the explicitly permitted empty list for a no-subject request.
- **Perfect cases** — fully correct extractions: 7 for the deterministic extractor, at most 3 for any model.
- **Latency** — 0.1 ms against 474–4905 ms per request.
- **Stability and agreement** — the deterministic extractor is byte-identical across runs. The models are not: the best model agreed with itself on 88 % of positions, and the coverage prompt agreed on only 25 %.

Review correction: the initial report counted `{"topics":[]}` as a schema failure even though every prompt explicitly required that answer when the request names no concrete subject. The parser now accepts an empty list, invalid output cannot count as a perfect case, and the table above was replayed from the stored raw responses. This changed schema-failure counts, but not recall, phrase recall, identifier recall, forbidden hits, ungrounded output, perfect cases, latency, stability, agreement, or the decision.

### 3.2 The hybrid path

| Candidate | Recall | Phrase recall | Identifier recall | Forbidden hits | Perfect cases |
| --- | --- | --- | --- | --- | --- |
| Deterministic extractor | 0.856 | 0.647 | 1.000 | 2 | 7 |
| Qwen3.5-4B, production, 512 tok | 0.734 | 0.400 | 0.958 | 5 | 1 |
| Qwen3.5-4B, production, 1024 tok | 0.768 | 0.446 | 0.958 | 2 | 2 |
| Qwen3.5-4B, tight, 512 tok | 0.718 | 0.411 | 1.000 | 3 | 1 |
| Qwen3.5-4B, coverage, 512 tok | 0.786 | 0.489 | 1.000 | 7 | 3 |
| Qwen3-4B-Instruct-2507, production, 512 tok | 0.729 | 0.386 | 0.958 | 2 | 3 |

The hybrid is a fallback, not a win. Tight, coverage, and Qwen3-Instruct score above their raw answers because the deterministic extractor replaces unusable output; the 1024-token production run is unchanged, and the 512-token production run is lower. The hybrid is partly the incumbent and still lands below the incumbent alone.

### 3.3 Where the models were strong

The models were not uniformly bad. Their best answers were phrase-shaped and precise: "provider capability allow-list", "systemd unit file", "SQLite WAL configuration", "continuous integration workflow". Where a request names a compound concept in one phrase, the model often returns it as one term, and the deterministic extractor sometimes splits it.

Their systematic weakness was **terseness**. The model often answered a request that names several distinct concepts with a single topic: `config-provider-timeout` (required "provider timeout" and "provider requests") became one topic, `FORGEHAND_PROVIDER_TIMEOUT`; `go-state-struct` (required "status transitions" and "persisted") became one topic, `state.WorkPlanTask`; `auth-project-ownership` became one topic, "project ownership". The `coverage` prompt was written to test exactly this and it raised raw recall to 0.771 — still below the deterministic 0.856, and it drove stability down to 0.245.

## 4. Failure analysis

Observed model failures, quoted from the raw responses stored in the evaluation artifacts:

1. **Malformed JSON.** The coverage prompt produced two answers with a trailing extra brace: `auth-peer-credentials` and `ui-session-refresh` both ended in `..."kind":"word"}}}`, which fails the strict schema.
2. **Empty answers exposed a contract defect.** Every model configuration correctly answered the no-subject request with `{"topics":[]}`. The initial parser rejected that answer despite the prompt requiring it. Review fixed the parser and hybrid path to accept the empty list. This also lets the two configurations that returned an empty list for the deliberately vague case avoid the deterministic extractor's junk topic "breaks sometimes".
3. **Over-long topic lists.** The tight prompt answered `auth-root-refusal` with nine topics against a limit of eight, padding the list with single generic words such as "run", "as", and "creates".
4. **Request wording leaking into topics**, in every configuration:
   - `numeric-noise` → "port 8080", "status code 200" (numbers the request mentions as data) in three of the five runs;
   - `negative-restriction` → "modifying files" (the *negated* clause) in the production and coverage runs;
   - `irrelevant-instructions` → "README", "linter", "team" in the coverage run; the other four runs answered that request with the subject only ("scheduler");
   - `ambiguous-vague` → "the thing that breaks sometimes", a phrase made entirely of vague words, in the production and tight runs.
5. **Non-repeatability.** The same request answered repeatedly produced materially different topic sets (positional stability 0.245–0.876, set agreement 0.413–0.947 over three runs per Qwen3.5-4B configuration and two runs for Qwen3-4B-Instruct-2507). A deterministic investigation needs a stable, auditable concept list.
6. **Terseness.** Requests that name several distinct concepts were answered with a single topic.
7. **Grounding trusted model metadata.** The initial hybrid accepted the reformatted `cancellationtoken` because its declared words occurred in the request, even though the required identifier was `CancellationToken`. Review now requires a contiguous request term and exact spelling for identifiers or code-like tokens. Replaying the stored responses improved the best affected hybrid but did not change the decision.

The deterministic extractor's own defects found by this evaluation were different in kind: it produced junk fragments from a negated restriction ("ing", "the config"), and it treated deliverable instructions as investigation subjects. Those were fixed in this phase (§7). Its two remaining forbidden hits are both in the deliberately vague `ambiguous-vague` case ("breaks", "sometimes"), which no rule-based extractor can resolve without guessing that the request has no subject.

## 5. Decision

**The deterministic extractor stays. The model-assisted path is not integrated.**

Reasons, in the order they mattered:

1. **It is not better.** The deterministic extractor is better on or tied with every model configuration on every metric that was measured, and strictly better on the ones that decide the question: concept recall, phrase recall, latency, and repeatability. There is no configuration in which a model is the better extractor, so there is no defensible integration.
2. **It is not cheaper.** The model path adds 474–4905 ms per investigation request and a provider dependency to a step that currently runs in 0.1 ms.
3. **It is not more auditable.** A model-backed topic list is not reproducible across runs, which conflicts with Forgehand's durable, evidence-backed investigation records.
4. **The fallback already exists.** The deterministic extractor *is* the fallback. Adding a model in front of it would only add a failure mode.

What this does **not** mean: that models are useless for investigation. It means that *this* step — turning a request into a short list of grounded search concepts — is deterministic work, and that a model on this path costs latency, reproducibility, and correctness without buying coverage. The models' phrase quality is a genuine signal that a model could help at a later stage, where a bounded model call adds semantic understanding rather than replacing a rule.

### 5.1 What was deliberately not built

Because no model was selected, none of the model-path integration was built: no `investigation_topic_extraction` provider capability, no configurable model assignment for it, no daemon dispatch path, no new work-plan method, no changes to the capability allow-list, no changes to the TUI, and no change to the investigation workflow. The candidate implementation exists only inside the evaluation package, together with the evidence that rejected it.

## 6. Candidates that were not evaluated

- **GLiNER2-base (`urchade/gliner2-base-v2.1`).** Not practical on this machine without disproportionate infrastructure. The model repository returned HTTP 401 for the base checkpoint; the multi checkpoint (`urchade/gliner_multi-v2.1`) was reachable but requires a PyTorch/Transformers/GLiNER runtime that is not installed. Standing one up would mean installing a new runtime and adding a second model stack purely to test a candidate that a 4B chat model already lost to a rule-based extractor. It was therefore not evaluated, and no result is claimed for it.
- **Larger models.** The remote router (`http://192.168.10.25:8000/v1`) is configured with `max_instances: 1`. Requesting a different model there would have evicted the running daemon's `worker` instance, which would have disrupted a live service. The evaluation deliberately stayed on the model the daemon already loads, and ran the additional candidate on an isolated local server instead.

## 7. Changes made to the deterministic extractor

The evaluation also measured the incumbent, and it had real defects. Three fixes were made, all in `internal/planner/investigation.go`, each covered by a regression test:

1. **Negated restrictions no longer leak fragments.** The negative-restriction pattern matched only the base verb, so `without modifying any files` left `ing` behind, and `do not automatically update the config` left `the config` and `output` behind. The pattern now covers the gerund forms (`brows(?:ing|e)`, `chang(?:ing|e)`, `execut(?:ing|e)`, `modif(?:ying|y)`, `remov(?:ing|e)`, `writ(?:ing|e)`).
2. **Polite and incidental wording is generic.** "thanks", "thank", and "also" no longer become topics.
3. **Instruction sentences are excluded from concept extraction.** A request mixes the subject of an investigation with instructions to the assistant. A sentence that carries no inquiry and only directs the assistant ("Also remember to update the README, run the linter, and tell the team.") no longer contributes search terms, while a sentence that carries an inquiry always contributes, and a follow-up statement that names the subject ("Recovery matters.") still does. This is implemented with sentence tracking in the tokenizer, an instruction vocabulary, and a scope that the candidate builder respects — including for quoted subjects.

Measured effect on the corpus: forbidden hits 7 → 2, ungrounded words 1 → 0, concept recall 0.834 → 0.856, phrase recall 0.626 → 0.647, perfect cases 6 → 7. Nothing regressed.

## 8. Regression evidence for the original daemon-recovery request

The request the daemon received before the extractor was corrected:

> Investigate how Forgehand handles daemon shutdown and recovery of interrupted sessions. Identify the components responsible for session persistence, execution scheduling, and recovery. Determine how the system prevents duplicate execution after a restart. Gather supporting source-code evidence with file paths and line numbers. Do not modify any files.

The stored topic payload of the earlier build was `["source-code","sessions.","recovery.","restart.","numbers.","files."]`. The current extractor returns:

```
daemon shutdown | interrupted sessions | execution scheduling | session persistence | duplicate execution | recovery | restart
```

Three of the six stored topics were request wording: "source-code" came from the deliverable instruction "Gather supporting source-code evidence", "numbers." from "line numbers", and "files." from the prohibition "Do not modify any files". The other three were genuine subject words, but carried with trailing punctuation and stripped of their compound form, so the stored list missed "daemon shutdown", "interrupted sessions", "session persistence", "execution scheduling", and "duplicate execution" — five of the eight concepts the request names. The new list names every required concept of that request and none of the forbidden wording. This comparison is locked in by `TestExtractInvestigationTopicsHandlesOriginalDaemonRecoveryRequest` in `internal/planner/investigation_test.go`.

## 9. What did not change

The only code this phase changed is the deterministic extractor itself (§7) and the new `internal/topiceval` evaluation package. Everything below was already true before the evaluation and is still true after it:

- The investigation workflow: authorization assessment, clarification handling, the deterministic discovery task, and the semantic-interpretation task are untouched.
- The daemon, the scheduler, the persistence schema, the IPC protocol, and the TUI are untouched by the topic-extraction work.
- Provider configuration and the capability allow-list are untouched — there is no `investigation_topic_extraction` capability to configure.
- No new daemon, scheduler, database service, or infrastructure was created.
- The daemon was not restarted and no service was disrupted. The evaluation used the router's already-loaded `worker` model and one isolated local server on `127.0.0.1:8081` that was stopped afterwards.

## 10. Risks and remaining uncertainty

- The corpus is 46 hand-written cases. It is representative of the request style this repository produces, but it is not a production request log. A different corpus could shift individual numbers. The deterministic extractor was ahead of every model configuration on concept recall, phrase recall, perfect cases, latency, and repeatability, and was tied on identifier recall, forbidden hits, and schema validity in some configurations, so the decision is unlikely to flip.
- The corpus was also used to diagnose and fix the deterministic extractor and to compare prompt variants; there is no held-out set. The numbers therefore establish the ordering on these reviewed cases, not an unbiased estimate for all future requests.
- Recall is measured against reviewer-authored concepts. Forbidden hits and ungrounded words catch known leakage and invention, but there is no general precision judgment for every extra grounded topic. The deterministic output still contains occasional low-value terms, and “perfect” means perfect under this rubric rather than an ideal minimal extraction.
- The harness sends no temperature, top-p, or seed, so repeatability describes the tested endpoints under their configured sampling defaults. The artifact records the `worker` alias and endpoint, not a cryptographic identity of the checkpoint behind that alias.
- The models are small local 4B models. A much larger model might close the phrase-quality gap. That would change the *quality* of a model-assisted answer, not the fact that the deterministic extractor is faster, reproducible, and already better on this corpus.
- The corpus cannot measure whether the extracted topics lead to a better *investigation outcome*. That would need an end-to-end study of discovery quality, which is outside this phase's scope and outside the topic-extraction boundary.
- On an end-to-end run of the original daemon-recovery request against Forgehand's committed tree, the improved topics surfaced relevant daemon ownership, interrupted-session, queue, execution-state, and restart evidence. Broad one-word topics such as `recovery` and `restart` also admitted unrelated service and TUI matches, and the global candidate cap can leave a narrower topic without a stored excerpt even when coverage reports matches. Fixing that would be repository-search ranking work, not topic extraction, and remains outside this decision.

## 11. Verification

Recorded in [§12](#12-reproduction). The change is covered by:

- `internal/planner/investigation_test.go` — deterministic extractor regressions (restriction fragments, instruction sentences, follow-up statements, no-subject requests, the original daemon-recovery request).
- `internal/topiceval/candidate_test.go` — candidate regressions (strict schema, grounding, deterministic fallback, cancellation, determinism).
- `internal/topiceval/topiceval_test.go` — the scored deterministic baseline over the whole corpus.

The commands below were run on the final tree (Go 1.26.8, Linux/amd64). Every one of them succeeded:

| Command | Result |
| --- | --- |
| `go build ./...` | clean, no output |
| `go vet ./...` | clean, no output |
| `go test -count=1 ./...` | all 19 packages `ok`, 2 without test files, no failures |
| `go test -race -count=1 ./internal/planner/... ./internal/topiceval/... ./internal/daemon/...` | `ok` for all three packages, no data races reported |
| `git diff --check` | clean, no whitespace errors |

The `internal/topiceval` package is green in the offline test run: the live model evaluation is skipped unless `TOPICEVAL_MODEL` names a model, so `go test ./...` needs no provider endpoint.

## 12. Reproduction

```bash
# deterministic baseline over the corpus
go test ./internal/topiceval/ -run TestDeterministicBaselineReport -v

# live model evaluation (requires an OpenAI-compatible endpoint)
FORGEHAND_PROVIDER_BASE_URL=http://192.168.10.25:8000/v1 \
TOPICEVAL_MODEL=worker \
TOPICEVAL_PROMPT=production \
TOPICEVAL_REPEAT=3 \
TOPICEVAL_MAX_TOKENS=512 \
TOPICEVAL_REPORT=/tmp/opencode/topic-eval/worker.json \
go test ./internal/topiceval/ -run TestModelExtractionEvaluation -v -timeout 3000s
```

Every evaluated configuration is the same command with a different endpoint, prompt, repeat count, and token budget:

| Artifact | Endpoint | Model | Prompt | Repeats | Tokens |
| --- | --- | --- | --- | --- | --- |
| `worker.json` | `http://192.168.10.25:8000/v1` | `worker` | `production` | 3 | 512 |
| `worker-t1024.json` | `http://192.168.10.25:8000/v1` | `worker` | `production` | 3 | 1024 |
| `worker-tight.json` | `http://192.168.10.25:8000/v1` | `worker` | `tight` | 3 | 512 |
| `worker-coverage.json` | `http://192.168.10.25:8000/v1` | `worker` | `coverage` | 3 | 512 |
| `instruct-2507.json` | `http://127.0.0.1:8081/v1` | `qwen3-4b-instruct-2507` | `production` | 2 | 512 |

Evaluation artifacts (aggregate scores, per-case results, raw model responses, prompt variant, and token budget) are written by `TOPICEVAL_REPORT`. The original stored artifacts predate the prompt/token metadata fields, so their filenames and the table above carry those two settings; newly generated artifacts are self-describing. The Qwen3-4B-Instruct-2507 candidate was served by a local `llama-server` on `127.0.0.1:8081` that was stopped after the run; the remote router and the `:8080` proxy were left untouched.

The evaluation never mutates production state: it only calls the provider, it writes to the report path, and it does not touch the daemon, the scheduler, or the work-plan database.

## 13. Files

| File | Role |
| --- | --- |
| `internal/topiceval/corpus.go` | The 46-case corpus with human-reviewed expectations. |
| `internal/topiceval/score.go` | The model-independent scorer. |
| `internal/topiceval/candidate.go` | The evaluated model-assisted candidate. |
| `internal/topiceval/prompts.go` | The evaluated prompt variants. |
| `internal/topiceval/model_test.go` | The live evaluation harness. |
| `internal/topiceval/candidate_test.go` | Candidate regressions. |
| `internal/topiceval/topiceval_test.go` | The deterministic baseline report. |
| `internal/planner/investigation.go` | The deterministic extractor, including this phase's fixes. |
