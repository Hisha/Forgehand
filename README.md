# Forgehand

Forgehand is a persistent, local-first AI software engineering system for understanding, planning, building, and verifying code over long-running development sessions.

## Quick start

After building Forgehand, install and start its unprivileged user service, then
launch the terminal interface:

```sh
forgehand service install
forgehand service start
forgehand
```

The TUI lists projects and persistent investigations without requiring database
IDs. See the [terminal interface guide](docs/TUI.md).

## Core idea

Modern coding models can be effective workers when given bounded, well-understood tasks. The harder problem is maintaining an accurate understanding of a software project, diagnosing what actually needs to change, coordinating work over long periods, and preserving state when models, terminals, or machines come and go.

Forgehand is being designed around that problem.

The system will maintain durable project knowledge, coordinate specialized workers, schedule work against available resources, pause safely for human decisions, and allow long-running engineering sessions to survive client disconnects.

## Design documents

- [Architecture](docs/ARCHITECTURE.md)
- [Design Principles](docs/DESIGN-PRINCIPLES.md)
- [Security Principles](docs/SECURITY-PRINCIPLES.md)
- [Terminology](docs/TERMINOLOGY.md)
- [Research Agenda](docs/RESEARCH.md)
- [Roadmap](docs/ROADMAP.md)
- [Linux system-service foundation](docs/SYSTEM-SERVICE.md)
- [Linux user-service lifecycle](docs/USER-SERVICE.md)
- [Terminal interface](docs/TUI.md)
- [Deterministic work planner](docs/WORK-PLANNER.md)
- [Linux Installation Guide](docs/LINUX-INSTALLATION.md)

## Security

Forgehand treats security as a core architectural requirement. All generated and externally supplied code is untrusted until verified; security verification is separate from functional verification and operates under a fail-closed model. See [Security Principles](docs/SECURITY-PRINCIPLES.md) for details. Planned security capabilities (scanning, authorization, sandboxing, etc.) are documented there as future work, not current implementations.

## License

MIT
