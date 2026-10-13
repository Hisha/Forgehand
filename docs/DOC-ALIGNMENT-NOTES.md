# Documentation alignment notes — October 2026

This review uses the nine supplied Forgehand documents and recent 4B.1–4B.4 development/acceptance reports, **not** a fresh checkout or code audit.

## Direction restored

- Standalone Forgehand coding environment; OpenCode is only a reference/development tool.
- Durable session engine, deterministic scheduler, TUI client, internal worker roles, replaceable models.
- Repository intelligence is a foundation for internal workers, not a third-party integration product.
- Non-root service is not a worker sandbox; pre-execution assessment and independent security verification remain requirements.

## Edited

- `ROADMAP.md`: replaced stale progress notes with current status, phase exit conditions, Phase 2 priority, and proposed checkpoints.
- `ARCHITECTURE.md`: clarified ownership boundaries, current implementation, and incomplete capabilities.
- `DESIGN-PRINCIPLES.md`: added explicit product identity invariant.
- `RESEARCH.md`: documented Go/OpenCode decision and Phase 2 audit target.
- `TERMINOLOGY.md`: clarified worker, job/task, provider, and investigation context.
- `SYSTEM-SERVICE.md`: clarified relationship to existing installer.

## Preserved unchanged

- `SECURITY-PRINCIPLES.md`: security contract remains authoritative.
- `LINUX-INSTALLATION.md`: operational guidance retained; verify actual installed version before any deployment.

## Verification needed before code planning

1. Audit current source and tests for actual Phase 2 job/session functionality.
2. Verify whether 4B.4 local commit was pushed and whether its tests remain green at HEAD.
3. Decide job-vs-session semantics and resource ownership from existing code, not terminology alone.
4. Check whether `ARCHITECTURE.md` discovery section needs new implementation detail after source audit.
5. Confirm any future documentation statements against source, not just agent reports.

**No application code, installer, systemd service, production database, or repository was modified by this document package.**
