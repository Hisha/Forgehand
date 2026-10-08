# Forgehand Architecture

## Status

Initial architecture. Component boundaries are conceptual and intentionally do not prescribe an implementation language, TUI framework, IPC mechanism, model provider, or structured-data format.

## System view

```text
                         Clients
                           |
                +----------+----------+
                |                     |
               TUI                Future UI/API
                |                     |
                +----------+----------+
                           |
                    Application Boundary
                           |
                  +--------+--------+
                  |  Session Engine |
                  +--------+--------+
                           |
          +----------------+----------------+
          |                |                |
      Scheduler       Project Knowledge   Workers
          |                |                |
     Resources       Git / text / DB     Models/tools
```

The core rule is that client presentation does not own engineering state.

## Major conceptual components

### Session Engine

Owns durable engineering sessions and their lifecycle.

Responsibilities are expected to include:

- session creation and restoration;
- objectives and current work;
- checkpoints;
- event/history persistence;
- waiting-for-human state;
- interruption and recovery;
- exposing session state to clients.

A session is not a terminal process and does not cease to exist when a client disconnects.

### Scheduler

Selects eligible work at safe checkpoints.

The scheduler is deterministic software, not an LLM.

It should eventually consider:

- session/task priority;
- readiness;
- required resources;
- resource availability;
- safe yield boundaries;
- human waits;
- failures and recovery.

The first implementation should remain intentionally simple.

### Project Knowledge

Provides durable understanding of a managed Git repository.

The planned layers are:

```text
Git repository
    |
    +-- structured knowledge     durable / Git-friendly
    +-- Markdown knowledge       durable / Git-friendly
    +-- generated SQLite index   disposable / rebuildable
```

Project knowledge should carry provenance and support invalidation or re-verification when relevant code changes.

### Cataloger

Bootstraps and updates knowledge for an existing project.

This should not be treated as a purely LLM-driven task. Deterministic discovery should establish facts wherever practical, with models used for semantic interpretation where useful.

Potential discovery areas include:

- languages and frameworks;
- repository structure;
- build systems;
- dependencies;
- symbols and references;
- configuration;
- commands;
- tests;
- CI;
- database migrations;
- major components;
- execution workflows.

What can be discovered reliably and portably is a research question, not yet an architectural promise.

### Investigator

Works from the project knowledge plus targeted source evidence to diagnose a problem or understand requested behavior.

Its goal is not to write code. It should produce a supported diagnosis that can be challenged or falsified.

### Planner

Converts a supported diagnosis or feature design into bounded implementation work with explicit constraints and expected outcomes.

Whether Investigator and Planner remain separate workers is intentionally undecided.

### Coder

Implements bounded changes.

Forgehand assumes coding itself can be delegated to suitable local models once the task is sufficiently constrained.

### Verifier

Evaluates whether completed work satisfies the plan and project requirements.

Verification should prefer executable evidence—tests, builds, static checks, diffs, and reproduction steps—over model confidence.

## Existing-project bootstrap

```text
Git repository
      |
 deterministic discovery
      |
 semantic cataloging
      |
 durable project knowledge
      |
 targeted investigation
      |
 diagnosis / plan
      |
 implementation
      |
 verification
```

### Project intake checkpoint

Before registering an existing project, the daemon requires a clean Git working
tree. A clean repository is registered without creating a commit. For a dirty
repository, clients present the daemon's structured change list and ask the user
to commit the current state, discard it, or cancel. The daemon alone performs
the selected Git operation, verifies that the result is clean, and registers the
project only after that verification succeeds.

The intake response includes an opaque expected-state fingerprint covering
HEAD, staged content, tracked working-tree content, and untracked paths and
content. The daemon checks it immediately before a commit or discard so that a
decision based on stale inspection data is rejected. This is optimistic
concurrency control, not filesystem locking: another process can still modify
the repository after verification and while the Git operation is running. The
post-operation cleanliness check detects many such races, but it cannot make a
multi-command Git operation atomic.

This checkpoint establishes a known source-control starting point. It does not
show that the project builds, passes tests, or is otherwise healthy.

### Deterministic repository discovery

The first implemented project-knowledge layer is deterministic discovery of a
specific committed Git tree. The daemon resolves a registered project ID to its
persisted repository root, captures the exact current HEAD commit, and asks the
discovery component to inspect that commit through Git tree primitives. The
live working directory is not used as the source inventory.

A discovery observation records aggregate tracked-file and language counts,
the number of files that remain unclassified, and build-system indicators with
their committed source paths as evidence. The classifier is an explicit,
limited table; ambiguous files such as `.h` remain unclassified rather than
being guessed. Indicator detection establishes only that a file exists. It does
not execute a build or establish that the indicated build system works.

Observations are stored historically in SQLite as one structured summary per
project and commit, rather than duplicating Git's complete file inventory as
database rows. This is deterministic observed evidence, not semantic analysis
or interpreted project knowledge. Semantic cataloging and a Git-friendly
durable knowledge format remain later work.

## New-project bootstrap

```text
User intent
    |
 requirements / constraints
    |
 architecture / planned project model
    |
 initial repository
    |
 implementation
    |
 verification
    |
 observed project knowledge
```

The two paths should converge onto the same ongoing project-knowledge model.

## Session lifecycle

Exact state names are not locked down, but the model must support concepts equivalent to:

```text
QUEUED
READY
RUNNING
WAITING_FOR_USER
WAITING_FOR_RESOURCE
PAUSED
COMPLETED
FAILED
CANCELLED
INTERRUPTED
RECOVERING
```

Not every state must exist in the first implementation.

## Persistence and recovery

Forgehand should checkpoint durable state throughout a session.

After an unclean shutdown, Forgehand must not blindly assume an interrupted external operation completed successfully. Recovery should validate relevant project and operation state before resuming or should request human intervention when the result is ambiguous.

## Resource model

Sessions do not own the whole machine.

Operations request capabilities/resources. A future resource pool might include:

```text
local-model/videoai
local-model/laptop
build/laptop
execution/laptop
network
```

Resource names and implementation are undecided.

The architectural requirement is simply that concurrency is governed by actual conflicting resource needs rather than by an assumption of one active project.

## Client boundary

The TUI should issue commands and consume state/events through a stable application boundary.

The transport is undecided. It could initially be in-process while preserving clean interfaces, or it could use local IPC from the beginning if daemon requirements justify it.

Do not introduce network/distributed complexity merely to preserve the possibility of a future GUI.

## Non-goals for the initial implementation

Forgehand v0 does not need to:

- provide a desktop GUI;
- support Windows;
- implement sophisticated distributed scheduling;
- support every programming language;
- autonomously resolve every ambiguity;
- replace Git;
- recreate tmux or screen;
- implement its own LLM;
- perform fully autonomous software development.
