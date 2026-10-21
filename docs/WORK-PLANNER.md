# Semantic Work Planner

Forgehand converts repository requests into durable, dependency-ordered work
through the existing session queue, scheduler, and SQLite task graph. The
planner does not scan a repository through a model. Repository facts come from
commit-pinned deterministic operations and stored project analysis.

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

Other requests create a `planning.semantic_interpretation` task. When a
qualified planning capability is configured (include `planning` in
`FORGEHAND_PROVIDER_CAPABILITIES`), that task runs through the same
scheduler and asks for one strict JSON proposal. Without it, the task is
persistently blocked with a reason.

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
blob identities, line ranges, and bounded excerpts. Component operations use
stored commit-pinned analysis. The TUI renders the revised objective,
constraints, unresolved questions, each task's dependencies and execution
method, and blocked or failed reasons.

## Current limits

Literal search is case-sensitive, skips binary and oversized files, and caps
matches. Component analysis remains Go-focused. The semantic planner cannot
run arbitrary commands, modify code, deploy, install capabilities, or qualify
models automatically. A malformed or policy-invalid model response fails the
execution with validation diagnostics; it is not repaired or silently
accepted.
