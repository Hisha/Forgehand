# Deterministic Work Planner

The first Forgehand work planner converts a deliberately limited set of
repository questions into durable, dependency-ordered tasks. Plans belong to
an existing project and session, run through the existing execution queue, and
survive daemon restarts in SQLite.

## Supported request forms

- `list files` or `list files under "path"`
- `find "literal text"` or `find "literal text" in "path"`
- `show dependencies of "component identity"`
- `show dependents of "component identity"`

Matching is grammatical and anchored rather than a general keyword classifier.
Modification requests and questions outside these forms produce a persistent
blocked semantic-planning task with a reason. Forgehand does not silently infer
repository facts or invoke a model for those tasks.

## Execution and evidence

The worker is read-only and uses the existing durable scheduler. Inventory and
literal-search work is pinned to a commit and reads Git blobs rather than the
working tree. Search evidence records the path, blob hash, and matching line;
the following excerpt task records an exact bounded line range. Component and
relationship requests read the existing commit-pinned component analysis and
block when that prerequisite is unavailable.

Task states are `PENDING`, `READY`, `RUNNING`, `COMPLETED`, `BLOCKED`, and
`FAILED`. A blocked or failed prerequisite blocks dependent tasks. An
interrupted deterministic task is reset to `READY` during daemon recovery and
can be resumed through the existing session lifecycle.

## Current limits

This milestone does not perform semantic question answering, source changes,
transitive dependency analysis, regex search, or model-based decomposition.
Literal search is case-sensitive, skips binary and oversized files, and caps
captured matches. The plan and its evidence remain available to a future
qualified semantic worker through the IPC representation.
