# Forgehand Initial Roadmap

This roadmap is intentionally short. Later phases should be created from evidence gathered in earlier ones.

**Related:** [Security Principles](SECURITY-PRINCIPLES.md)

## Phase 0 — Design baseline

- Record architectural invariants.
- Establish terminology.
- Establish research questions.
- Keep implementation choices explicitly undecided.

**Exit condition:** the repository contains a coherent design baseline that can be reviewed and changed through Git.

## Phase 1 — Technical spikes

Research and prototype:

- implementation language candidates;
- OpenCode architecture/reuse;
- daemon/client boundary;
- durable session persistence;
- structured knowledge + SQLite indexing;
- repository discovery primitives.

**Exit condition:** enough evidence exists to select an initial implementation stack and persistence approach.

## Phase 2 — Persistent skeleton

Build a minimal Forgehand engine using fake workers.

Required behavior:

- initialize/open a Git project;
- create a session;
- execute a fake long-running worker;
- detach and reattach a client;
- persist events/state;
- enter WAITING_FOR_USER;
- accept user input and resume;
- manage at least two sessions;
- schedule them conservatively;
- recover meaningfully after daemon interruption.

No real coding model is required for this phase.

**Exit condition:** the orchestration architecture works without AI.

## Phase 3 — Project knowledge prototype

Add an initial cataloger for a deliberately limited project type.

- deterministic repository discovery;
- durable structured knowledge;
- narrative Markdown generation where useful;
- generated SQLite index;
- evidence/provenance;
- Git-aware staleness/update behavior.

**Exit condition:** Forgehand can answer useful project questions from its maintained knowledge and show where those answers came from.

Incremental implementation status:

- deterministic committed-tree discovery is implemented for registered
  projects;
- discovery records exact commit provenance, aggregate language counts, and
  evidence paths for a limited set of build-system indicators;
- historical discovery observations are retained in SQLite without copying the
  full Git file inventory.

This is the first deterministic layer of project knowledge. Phase 3 remains in
progress: semantic cataloging, durable Git-friendly knowledge, broader
provenance, and staleness/update behavior are not yet complete.

Infrastructure foundation implemented alongside the prototype:

- non-root Linux daemon invariant and capability-free service execution;
- explicit service state/socket configuration and single-daemon state locking;
- local Unix peer identity capture and a non-installed systemd service
  template.

This foundation does not implement multi-user authorization, project ACLs,
remote APIs, or isolated workers, and does not complete Phase 2 or Phase 3.

## Phase 4 — Investigation

Introduce the first reasoning worker.

Given a specific engineering question, it should retrieve relevant project knowledge and targeted source evidence, then produce an evidence-backed diagnosis without editing code.

**Exit condition:** investigation quality is demonstrably better than asking the same model to explore the repository from scratch.

## Phase 5 — Implementation and verification

Connect bounded coding and verification workers, including functional and security verification.

**Exit condition:** Forgehand can carry one real change from user objective through investigation, plan, implementation, functional verification, security verification, and promotion decision while preserving a durable session history.

## Beyond

Only after the above works should Forgehand consider:

- richer resource scheduling;
- remote/distributed workers;
- additional language intelligence;
- desktop GUI;
- broad cross-platform support;
- more autonomous project planning.
- security verification engine (scanners, evidence, findings, fail-closed promotion gate).
- project authorization, worker isolation, remote authentication, and audit logging.
- Linux installer/upgrade workflow (implemented in this milestone) extended to additional packaging targets (Debian/RPM/container) as future work.
