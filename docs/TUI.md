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

`enter` on the dashboard opens the selected project. The project screen is a
conversation-first engineering workspace:

- a fixed application header with the product name and what is open
- a fixed session selector with every session of the project
- a conversation timeline built from the selected session's persisted state
- a contextual sidebar with project identity, session state, and repository or
  worker context
- a composer anchored at the bottom of the workspace

The timeline is a conversation, not a report. Each entry names the voice that
produced it:

| Voice | Meaning |
| --- | --- |
| `● You` | The original request the session was started with. |
| `◆ Forgehand` | A plan, the model response, or the evidence a response rests on. |
| `• Worker` | The state of the session's execution. |
| `• Activity` | The session events the daemon recorded, in order. |

The timeline only shows what the backend has persisted. A session that has no
model response, no evidence, and no recorded events shows the request and the
worker state alone; nothing is filled in to make the conversation look
complete.

### Keys

| Key | Action |
| --- | --- |
| any text | Type into the composer |
| `enter` | Send the request and show the new conversation |
| `shift+enter`, `ctrl+j` | Insert a newline |
| `tab` | Move focus to the session selector |
| `↑`/`↓` | Scroll the conversation (move the caret in a multiline request) |
| `pgup`/`pgdown` | Scroll the conversation by a screen |
| `esc` | Clear the composer, or leave the workspace |
| `b` | Back to the dashboard (from the session selector) |
| `?` | Keyboard help (from the session selector) |
| `q` | Quit the frontend (from the session selector) |

In session-selector focus, `↑`/`↓` choose another session of the project and
load its persisted details. The selector slides so the selected session stays
visible, and every tab carries the state of its session.

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
2. Type a free-form investigation question into the composer and press `enter`.
3. Forgehand creates a persistent work plan session and queues it on the
   daemon.
4. The workspace polls durable status and shows the conversation as it grows:
   the request, the worker's state, the plan, and the findings with their
   evidence.
5. Press `q` to exit. This closes only the frontend; the daemon and work keep
   running.
6. Launch `forgehand` again, open the project, and reopen the investigation
   from the session selector.

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
- The conversation is built from the persisted state of a single session. The
  backend records one request per session, so the timeline shows that request
  and the answers the workers produced for it. It is not a multi-turn
  transcript, and no turn is invented to make it look like one.
- Provider configuration remains daemon environment configuration. The TUI
  does not edit credentials or provider settings.
- Human-interaction questions are visible through session state, but answering
  them remains a CLI operation in this minimal frontend.
- The interface is keyboard-only and intentionally has no code editor.
- The dashboard shows the project list and its activity. A complete
  model-management interface and session export are not part of this phase.

## Application shell

Forgehand opens as a full-screen application shell rather than a scrolling
command transcript:

- A fixed header spans the top of the window with the product name, what is
  open, and the current view.
- The main workspace occupies the left portion of the terminal and holds the
  current view (dashboard, project, session, models, ...). It scrolls
  independently of the composer and the sidebar.
- On sufficiently wide terminals a contextual sidebar appears on the right
  (~25-30% of the width). Its contents follow the current view and selection:
  project identity, repository readiness, worker activity, pending decisions,
  or session identity, progress, and provider information.
- A persistent composer sits at the bottom of the main workspace. On the
  dashboard it shows `Select a project to begin...` and does not submit work
  against an arbitrary project. When a project is open, type directly into the
  composer; `tab` switches focus between the composer and the session
  selector.
- A compact status bar spans the bottom of the window and reports daemon
  connection status, worker activity, the current project context, and the
  most relevant keyboard shortcuts. Shortcuts are dropped rather than
  truncated when the terminal is too narrow for them.

The shell adapts to the actual terminal size: the sidebar narrows and then
collapses on small terminals, while navigation and input remain available.
The interface uses the terminal's own color capabilities: a restrained
lavender accent is used where the terminal can show it, and the interface
falls back to the 16-color palette, or to no color at all, where it cannot.
