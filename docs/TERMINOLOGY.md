# Forgehand Terminology

These terms provide a common vocabulary. Names may change as prototypes reveal better abstractions.

## Project

A Git repository managed by Forgehand.

## Project knowledge

Durable facts, interpretations, architecture, workflows, and other information Forgehand maintains about a project.

## Session

A durable engineering objective and its history/state. A session exists independently of any attached TUI.

## Client

A user-facing interface to Forgehand. The initial client is a TUI.

## Worker

A bounded executor used by Forgehand. A worker may use an LLM, deterministic tooling, or both.

## Cataloger

A worker/process responsible for discovering and maintaining project knowledge.

## Investigator

A worker responsible for understanding a specific behavior, bug, or question and producing an evidence-backed diagnosis.

## Planner

A worker responsible for turning an understood objective into bounded implementation work.

## Coder

A worker responsible for implementing a bounded coding task.

## Verifier

A worker/process responsible for establishing whether work produced the intended result.

## Human request

A durable request for user input that may block a session without terminating it.

## Checkpoint

A persisted safe boundary at which session state is coherent and scheduling/recovery decisions can be made.

## Resource

A capability with limited availability required by an operation, such as a model slot or heavy local execution slot.

## Structured knowledge

Git-friendly machine-readable durable project data. The concrete serialization format is not yet selected.

## Narrative knowledge

Markdown documentation intended to preserve coherent explanations useful to humans and models.

## Knowledge index

A generated SQLite database optimized for retrieval. It is disposable and rebuildable from durable project knowledge.
