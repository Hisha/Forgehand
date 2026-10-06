# Forgehand Design Principles

This document records the architectural invariants accepted during Forgehand's initial design. These are intended to remain stable unless implementation evidence demonstrates that one should change.

## 1. Git is required

Every Forgehand-managed project is a Git repository.

Git provides:

- source history and diffs;
- provenance for project knowledge;
- synchronization between machines;
- branches and rollback;
- a concrete basis for determining what changed since knowledge was last verified.

Forgehand does not initially need to support unmanaged source directories.

## 2. Project knowledge travels with the repository

Durable project knowledge belongs with the project rather than in a machine-specific Forgehand installation.

The intended knowledge stack is:

- **structured text** for machine-readable facts and relationships;
- **Markdown** for narrative architecture, workflows, decisions, build instructions, and other explanatory knowledge;
- **SQLite** as a generated, disposable retrieval/indexing layer.

SQLite must not be the only copy of durable project knowledge. Deleting the generated database must not destroy knowledge.

The exact structured-text format is not yet selected.

## 3. Existing and new projects have different bootstrap paths

Existing projects begin with discovery and cataloging:

`source code -> observed facts -> project model`

New projects begin with requirements and design:

`intent -> requirements -> planned project model -> source code`

After bootstrap, both use the same project-knowledge system.

## 4. Knowledge requires provenance

A model-generated statement must not silently become ground truth.

Knowledge records should distinguish observed facts from interpretations and should retain enough provenance to answer questions such as:

- Where did this fact come from?
- Which source files or symbols support it?
- At which Git commit was it verified?
- Is it still valid after subsequent changes?
- Is it deterministic evidence or an inferred claim?

The exact confidence/evidence model remains to be designed and tested.

## 5. Forgehand is headless and TUI-first

The primary interface is a terminal user interface suitable for Linux and SSH use.

The engineering engine must not depend on the TUI. Presentation-specific logic must remain outside the core engineering system so other clients, including a future graphical interface, can be added without redesigning the engine.

A GUI is not an initial requirement.

## 6. Sessions are durable

Engineering work belongs to Forgehand's persistent session engine, not to an attached terminal.

Disconnecting SSH, closing the TUI, or otherwise detaching a client must not terminate active work.

Sessions should persist enough state to support safe recovery after process or machine interruption.

## 7. Models are replaceable workers

Models perform bounded reasoning or implementation work. They do not own Forgehand's operating state.

Deterministic software owns:

- session lifecycle;
- persistence;
- scheduling;
- priorities;
- resource allocation;
- checkpoints;
- recovery;
- Git state tracking;
- human-input requests.

Forgehand should be able to replace or change a model without losing the engineering session.

## 8. Multiple sessions may coexist

Forgehand may manage multiple projects and multiple engineering sessions concurrently.

A session may be running, ready to run, waiting for a person, waiting for a resource, completed, failed, interrupted, or otherwise inactive without being destroyed.

The scheduler selects eligible work based on priority and available resources.

## 9. Scheduling occurs at safe boundaries

Forgehand must not arbitrarily interrupt an operation merely because higher-priority work becomes available.

Scheduling decisions occur at safe checkpoints such as:

- completion of a model turn;
- completion of a tool operation;
- completion of a task;
- completion of a test/build operation;
- an explicit wait for human input.

The exact checkpoint semantics will be refined during implementation.

## 10. Human interaction is first-class state

When safe progress requires a human decision, Forgehand should persist a structured request and allow the session to enter a waiting state.

A human request should eventually be capable of retaining:

- the question;
- why the decision is required;
- relevant evidence;
- available options when known;
- consequences when known;
- a recommendation when appropriate;
- whether the request blocks further work.

Waiting for a user must release resources that the session no longer needs.

## 11. Resources, not projects, constrain concurrency

Forgehand should not assume that only one project can perform work at a time.

Different operations may require different resources: model inference, CPU-heavy builds, local execution, network access, or other capabilities.

The first scheduler may be conservative, but the architecture must not prevent independent operations from proceeding concurrently when their resource requirements do not conflict.

## 12. Optimize for modest local models

Forgehand is specifically interested in making capable software engineering possible without requiring one frontier-scale model to understand and execute an entire project.

The system should reduce the scope of each model task through persistent knowledge, retrieval, decomposition, evidence, and verification.

A larger model may be usable, but the architecture must not depend on one.

## 13. Do not automate uncertainty away

When Forgehand cannot establish enough evidence to make a safe engineering decision, it should expose the uncertainty rather than manufacture confidence.

The objective is not maximum autonomy. The objective is reliable long-horizon engineering.
