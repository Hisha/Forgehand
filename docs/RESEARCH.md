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

Use fake workers before real models.

Prove:

- multiple sessions;
- READY/RUNNING/WAITING_USER behavior;
- priorities;
- resource claims;
- safe checkpoint yielding;
- no work loss on client disconnect.

Do not build an advanced scheduler until simple scheduling produces a concrete limitation.

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
