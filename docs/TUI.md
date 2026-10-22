# Terminal interface

Run `forgehand` with no arguments (or run `forgehand tui`) to open the terminal
interface. The TUI is a Bubble Tea client of the existing Unix-socket IPC API;
it does not contain a scheduler, worker, model client, or execution engine.

## Dashboard

Forgehand opens to the project dashboard. It lists every registered project
once, grouped by what the project is doing right now:

| Group | Meaning |
| --- | --- |
| `NEEDS ATTENTION` | A session is waiting for the user: a question, an authorization, or an approval. |
| `WORKING` | A session is running, queued, or ready to start. |
| `IDLE` | No active work and nothing waiting for the user. |

A project that has both a waiting session and a running session appears only
under `NEEDS ATTENTION`: the user's reply is the only thing that can move the
project forward. All three headings are always shown, so an empty group reads
as "nothing here" instead of "unknown".

The dashboard is deliberately about projects, not sessions. Session history is
one keystroke away (`s`) or one level down (open a project and read its recent
sessions), so a long session log never pushes the projects out of view.

### Keys

| Key | Action |
| --- | --- |
| `↑`/`↓`, `k`/`j` | Move the selection |
| `enter` | Open the selected project |
| `a` | Register a Git repository |
| `s` | List every durable session |
| `m` | Model management |
| `r` | Refresh |
| `?` | Keyboard help for the current screen |
| `q` | Quit the frontend (the daemon keeps working) |

## Project screen

`enter` on the dashboard opens the selected project. The project screen shows
what the project needs, what it is doing, what it has done, and whether its
repository observations are current:

- `PENDING INPUT` — the questions and approvals that are waiting for an answer.
- `ACTIVE WORK` — running and queued sessions.
- `RECENT SESSIONS` — the most recent finished sessions.
- `MAINTENANCE` — the repository readiness summary.

Press `n` to start a new work plan request for the project, and `b`/`esc` to
return to the dashboard.

## Repository readiness

The dashboard shows a `!` at the right edge of a project row when the daemon
reports a repository readiness other than `current`:

| Status | Meaning |
| --- | --- |
| `current` | Every stored observation matches the live repository revision. |
| `stale` | A stored observation describes an older revision than the live one. |
| `unobserved` | The project has no stored inventory or analysis yet. |
| `unavailable` | The live repository revision could not be determined. |

Readiness is advisory. It never blocks opening a project and never changes
which group a project belongs to: activity, repository health, and the
prerequisites of an individual operation are three separate things. The daemon
computes readiness at startup and whenever the TUI asks for it, without
building an inventory, running analysis, or modifying stored state, so the
first paint is never delayed by a repository check.

## Workflow

1. Open Forgehand and pick a project on the dashboard.
2. Press `n` and type a free-form investigation question, then press `enter`.
3. Forgehand creates a persistent work plan session and queues it on the
   daemon.
4. The session screen polls durable status and shows queue/execution progress,
   findings, and evidence file/source references.
5. Press `q` to exit. This closes only the frontend; the daemon and work keep
   running.
6. Launch `forgehand` again, open the project, and reopen the investigation.

No project ID or session ID is displayed or entered in this workflow.

Press `a` on the dashboard to register a Git repository. Clean repositories are
registered immediately. When changes are present, the TUI shows them and offers
explicit commit, confirmed discard, or cancel actions using the daemon's
existing project-intake protocol.

## Daemon recovery

If the socket is unavailable or the daemon protocol is incompatible, the TUI
shows a recovery screen rather than a raw IPC error. Press `s` to start an
already installed user service or `r` to retry. If no user service is
installed, exit and run:

```sh
forgehand service install
forgehand service start
```

An incompatible running manual daemon must be stopped by its owner. A managed
service can be brought to the current executable with `forgehand service
update`.

## Current limitations

- Status updates use one-second polling; IPC does not yet provide event
  streaming.
- Provider configuration remains daemon environment configuration. The TUI
  does not edit credentials or provider settings.
- Human-interaction questions are visible through session state, but answering
  them remains a CLI operation in this minimal frontend.
- The interface is keyboard-only and intentionally has no code editor.
- The dashboard shows the project list and its activity. A full project-detail
  redesign, a complete model-management interface, and session export are not
  part of this phase.
