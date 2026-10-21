# Semantic Work Planner

Forgehand converts repository requests into durable, dependency-ordered work
through the existing session queue, scheduler, and SQLite task graph. The
planner does not scan a repository through a model. Repository facts come from
commit-pinned deterministic operations and stored project analysis.

## Phase 3.3 implementation audit

The deterministic investigation bootstrap reuses the following existing path:

- work-plan sessions are persisted in `work_plans`, `work_plan_tasks`, and task
  evidence records, then dispatched by the ordinary session execution queue;
- repository inventory is commit-pinned and stores path, blob identity, size,
  and category, while committed content is read by blob identity rather than
  from the mutable working tree;
- literal search and source-excerpt tasks already establish path, blob, and line
  citations, and component/dependency queries read cached commit-pinned analysis;
- semantic planning already receives completed task results and evidence, and
  accepted revisions append tasks without replacing completed work; and
- the Work Plan TUI already renders task methods, dependencies, results,
  evidence, and blocked reasons through its scrolling view.

Before Phase 3.3, the deterministic grammar could only inventory files, search
one quoted literal, or query one named component relationship. Every other
request was replaced at daemon intake by a semantic bootstrap task. When no
qualified planning capability was configured, that task began blocked, so no
repository evidence was gathered. The missing facility was therefore a
conservative request classifier and a bounded orchestration operation over the
existing inventory/blob/analysis/evidence path—not another scheduler or
repository investigation subsystem.

## Planner contract

`internal/planner.Decomposer` accepts an objective and explicit constraints,
the selected project, existing repository knowledge, the deterministic
operation allow-list, completed task evidence, and qualified model
capabilities. It returns a structured proposal with tasks, dependencies,
required evidence, completion criteria, and unresolved questions. The
interface is provider-independent.

The built-in grammatical interpreter still handles these requests without a
model:

- `list files` or `list files under "path"`
- `find "literal text"` or `find "literal text" in "path"`
- `show dependencies of "component identity"`
- `show dependents of "component identity"`

Non-investigation requests outside that grammar create a
`planning.semantic_interpretation` task. When a
qualified planning capability is configured (include `planning` in
`FORGEHAND_PROVIDER_CAPABILITIES`), that task runs through the same
scheduler and asks for one strict JSON proposal. Without it, the task is
persistently blocked with a reason.

### Deterministic investigation bootstrap

Requests that explicitly ask to investigate, diagnose, trace, explain,
identify, locate, or analyze a concrete repository subject are eligible for a
read-only bootstrap. Forgehand extracts at most eight grounded topics, makes one
bounded pass over the commit-pinned inventory and blobs, ranks matching files
by deterministic role, path, content, and coverage scores, captures cited
excerpts, and adds matching components and direct relationships from cached
analysis for the same commit. The result is an ordinary Work Plan task with
ordinary persisted evidence; it does not create an investigation queue or
scheduler.

The package labels candidate-file relevance as inferred, while literal source
matches, component declarations, and relationships are labelled discovered and
carry path/blob/line evidence. It also records the original request, topics,
missing data, unresolved semantic work, and recommended next steps. A qualified
planning capability receives this completed package in its normal planning
request. Without that capability, deterministic discovery completes and remains
inspectable while the dependent semantic task is visibly blocked awaiting
capability or operator direction.

Eligibility is intentionally conservative. Negative restrictions such as
read-only, do not modify, do not execute commands, do not deploy, and no
external access are honored. A request that positively combines investigation
with mutation, command execution, deployment, installation, or external access
stops for clarification. Text without explicit investigation intent does not
gain investigation authority and continues through the existing semantic entry
path.

One bootstrap scans at most 5,000 inventory entries, accepts at most eight
grounded topics, records at most 300 content matches, ranks at most 20 candidate
files, stores at most 30 five-line excerpts and 24,000 excerpt characters,
includes at most 10 components and 20 direct dependencies, and persists at most
64 evidence items. Files larger than 1 MiB and binary blobs are not searched. A
work plan contains one bootstrap round; the existing 24-task and three
semantic-revision limits remain in force. Recovered execution replaces the
deterministic task's evidence atomically rather than appending duplicate
evidence.

#### Topic extraction

Topics are extracted from the request text only. Constraints such as
"read-only" or "do not modify files" never contribute a search term. The
extractor is deterministic and produces at most eight topics, each with a term,
the words it covers, and a kind:

- `identifier` — a language-level name such as `WorldSession::IsHeadless`,
  `session_status`, `FORGEHAND_PROVIDER_MODEL`, `mod-native-hunts`, or
  `config.json`. Identifiers are matched literally, never as loose substrings.
- `phrase` — two or three adjacent domain words from the same clause, such as
  `daemon shutdown` or `session persistence`. A phrase matches literally or
  when every constituent word is present.
- `word` — a single domain word such as `recovery` or `restart`. A word
  matches only as a whole word, so `test` never matches `latest`.

Request wording that describes the request rather than the repository is
filtered out (for example `source-code`, `file paths`, `line numbers`,
`evidence`). Capitalized proper nouns are used only as a fallback when no
other candidate exists. Selection maximizes specificity plus newly covered
concepts, skips candidates that repeat already covered vocabulary, and breaks
ties by specificity, then first appearance, then lexicographic order, so the
same request always produces the same topics.

A model-assisted extractor was evaluated against this one and rejected. Over a
46-case reviewed corpus with a model-independent scorer, the deterministic
extractor scored concept recall 0.856 against 0.696–0.771 for every model
configuration, preserved every required identifier, produced no schema failures,
answered in 0.1 ms, and returned identical results on every run, while the
models were non-repeatable (positional stability 0.245–0.876) and repeatedly
turned request wording into topics. No `investigation_topic_extraction`
capability, model assignment, daemon path, or TUI change was added as a result.
See `docs/INVESTIGATION-TOPIC-EXTRACTION-EVALUATION.md`.

#### Candidate ranking

Every committed file is given a role before it is scored. Roles come from the
existing inventory classification plus generic markers, so no repository layout
is assumed: `implementation`, `test`, `configuration`, `documentation`, `build`,
`data`, `script`, and `other`. The request also determines an intent —
`behavior`, `documentation`, `configuration`, or `test` — from keyword evidence,
with ties keeping the default `behavior` intent.

The role weight for the detected intent decides which role is expected to
answer the question:

| role | behavior | documentation | configuration | test |
| --- | --- | --- | --- | --- |
| implementation | 300 | 120 | 200 | 260 |
| test | 260 | 110 | 170 | 300 |
| script | 240 | 100 | 180 | 180 |
| configuration | 200 | 150 | 300 | 190 |
| build | 180 | 120 | 220 | 160 |
| other | 180 | 120 | 160 | 160 |
| data | 170 | 110 | 200 | 150 |
| documentation | 120 | 300 | 140 | 120 |

A role weight is only added to a file that matched at least one topic, so it
expresses relevance rather than presence. Documentation is never excluded from
the candidate list in any intent.

Each candidate's score is the sum of five bounded parts:

```text
score = role weight (0-300)
      + path score (0-40)
      + content score (0-90)
      + coverage bonus (0-30)
      + component bonus (0-10)
```

The path score rewards a topic term in the file name (20) or its directory
(10), a topic word in the file name (5, or 3 for an identifier, whose words
are only a hint), a topic word inside a file name (3), and a topic word in the
directory (1). The path score is capped at 40, so path evidence can never
outrank content evidence. The content score rewards a literal identifier
match (25), a literal phrase match (18), a phrase matched by all of its words
(10), and a word match (12); each topic contributes a fixed maximum and the
total is capped at 90, so one repeated topic cannot dominate. The coverage
bonus adds 10 per additional distinct topic the file supports, capped at 30,
and the component bonus adds 10 when the file is described by the cached
component analysis for the same commit.

These scores are deterministic relevance heuristics used to order candidates.
They are not probabilities, confidences, or estimates of correctness, and they
are not comparable across requests.

#### Scan order and evidence distribution

Scanning is bounded to 300 committed blobs. Files of the intent's primary role
are scanned first, and every other role keeps a reserved share of the scan in
a three-to-one ratio, so documentation, configuration, and test files are not
crowded out by the number of implementation files. Within each group, files
are ordered by path score and then by path, so the scan is repeatable. One file
contributes at most three recorded matches, which prevents a single verbose
file from consuming the global match budget.

Excerpts are allocated round-robin across the topic list, so one topic or one
file cannot consume the whole excerpt budget. A file contributes at most three
excerpts, a topic at most eight, and each excerpt spans at most five lines.

Every evidence record names the topics it supports. The stored package also
reports, for each topic, how many committed lines matched and how many
scanned files the topic reached, and it names the topics that produced no
committed content at all.

#### Component-analysis availability

The bootstrap never creates or refreshes component analysis. It reports why
the cached analysis could or could not be used:

- `available` — a commit-pinned analysis exists for the investigated commit.
- `never-performed` — no cached component analysis is available.
- `stale` — the cached analysis belongs to a different commit.
- `unreadable` — the cached analysis could not be read.
- `partial` — the cached analysis is incomplete, so component and relationship
  coverage may be limited.
- `no-match` — the cached analysis contains no component matching the topics.

#### Discovery bounds

| bound | value |
| --- | --- |
| inventory entries considered | 5,000 |
| committed blobs scanned | 300 |
| grounded topics | 8 |
| recorded content matches | 300 |
| recorded matches per file | 3 |
| ranked candidate files | 20 |
| stored excerpts | 30 |
| excerpts per file | 3 |
| excerpts per topic | 8 |
| excerpt lines | 5 |
| excerpt characters | 24,000 |
| cited components | 10 |
| cited direct dependencies | 20 |
| component and dependency citations | 12 |
| persisted evidence items | 64 |
| path / content / coverage / component score caps | 40 / 90 / 30 / 10 |

Reaching a bound is reported in the package as missing information rather than
hidden.

## Validation and capability boundaries

Semantic proposals are rejected before expansion when they contain unknown
JSON fields, unsupported operations or inputs, missing evidence/criteria,
unknown fact references, dependency cycles, lost user constraints, unqualified
model capabilities, more than 24 tasks, or more than three planning rounds.
Diagnostics identify the invalid schema path and reason.

The runtime exposes the configured model only as a **planning** capability.
Interpretation and coding are separate qualification labels and are not
implicitly granted to the provider or default model. Deterministic tools
establish facts. `human.clarification` and `capability.unsupported` explicitly
represent work that cannot proceed. Semantic work plans remain read-only and
never authorize autonomous code changes.

## Incremental revisions

A proposal may schedule one `planning.incremental_revision` task after a
deterministic evidence task. The next model call receives the completed task
results and evidence summaries. Accepted tasks are appended to the existing
plan; completed rows and their evidence are never replaced. Each revision is
recorded in `work_plan_revisions`.

Expansion is limited to 24 total tasks, three revisions, and one incremental
planning task per proposal. These bounds prevent recursive planning loops and
uncontrolled graph growth. Execution progress is adjusted on the active
session execution, so the existing scheduler remains the only execution path.

## Qualification harness

`planner.Qualify` repeatedly runs a `Decomposer` over
`RepresentativeQualificationCases` and records, per model and configuration:

- requirement and expected-information coverage;
- dependency and structured-output validity;
- supported operation selection;
- exact user-constraint preservation;
- unsupported-work recognition; and
- proposal consistency across repeated runs.

`WriteQualificationReport` writes the result as JSON. Reports are diagnostic
artifacts only: they do not promote a model or alter daemon configuration.

Run the harness without the daemon:

```bash
./forgehand model qualify \
  --model worker \
  --capability planning \
  --runs 10 \
  --output ~/forgehand-reports/worker-planning.json
```

The command uses the existing OpenAI-compatible provider settings but does not
require `planning` in `FORGEHAND_PROVIDER_CAPABILITIES`; qualification is an
evaluation, not an authorization change. `--runs` is the number of repetitions
per representative case. `--timeout` optionally sets the total evaluation
deadline (default 15 minutes, maximum one hour).

The terminal summary and JSON report distinguish invalid or inadequate model
plans (`NOT_QUALIFIED`, exit status 0) from provider, timeout, configuration,
and report-writing failures (`INCONCLUSIVE`, nonzero exit status). The report
contains a one-way provider-configuration fingerprint and never includes the
API key. A `QUALIFIED` recommendation means every case passed on every run;
different valid task graphs may pass when they satisfy the same requirements.

### Persistent qualification in the TUI

Launch Forgehand with `./forgehand`, use Tab to select **Models**, and press
Enter. The Models view shows the configured OpenAI-compatible provider
endpoint and default model without displaying credentials. From that view an
operator can edit the model identifier, choose the number of planning-case
repetitions, start a qualification, monitor persisted progress, open readable
case results, cancel queued or running work, and retry an interrupted job.

Qualification jobs use the existing daemon execution queue and worker-capacity
limit. Their lifecycle is `QUEUED`, `RUNNING`, and one of `COMPLETED`, `FAILED`,
`CANCELLED`, or `INTERRUPTED`. Each case result is committed before the next
provider request, so closing and reopening the TUI does not affect the job.
Daemon shutdown leaves active qualification work explicitly `INTERRUPTED`; it
is never silently restarted. Retrying reuses the committed evaluations and
continues at the next case.

`COMPLETED` means the evaluation ran to completion. Its recommendation may be
`QUALIFIED`, `NOT_QUALIFIED`, or `INCONCLUSIVE`. Invalid plans and missed
requirements are model results and normally produce `NOT_QUALIFIED` while the
job remains completed. Provider errors, request deadlines, persistence errors,
and daemon interruption are shown separately as infrastructure conditions and
do not count as evidence that the model lacks planning competence.

Qualification remains separate from authorization: it does not enable
`FORGEHAND_PROVIDER_CAPABILITIES`, change provider configuration, promote a
model, touch a project, or execute any proposed plan. The standalone
`forgehand model qualify` command remains available for daemon-independent
evaluation and JSON report generation.

## Evidence and TUI

Inventory and search work read committed Git blobs. Evidence records paths,
blob identities, line ranges, and bounded excerpts. Investigation evidence also
records the topics each item supports, and the Work Plan view renders the
task key, the topic list, and the per-evidence topic line. Component
operations use stored commit-pinned analysis. The TUI renders the revised
objective, constraints, unresolved questions, each task's dependencies and
execution method, and blocked or failed reasons.

## Current limits

Literal search is case-sensitive, skips binary and oversized files, and caps
matches. Bootstrap matching is case-insensitive but remains literal; it does not
perform stemming or semantic ranking. Candidate scores are deterministic
relevance heuristics, not probabilities, and they are only comparable within a
single request. Component analysis remains Go-focused and the bootstrap never
creates missing or stale analysis automatically. The semantic planner cannot
run arbitrary commands, modify code, deploy, install capabilities, or qualify
models automatically. A malformed or policy-invalid model response fails the
execution with validation diagnostics; it is not repaired or silently
accepted.
