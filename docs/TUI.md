# Terminal interface

Run `forgehand` with no arguments (or run `forgehand tui`) to open the terminal
interface. The TUI is a Bubble Tea client of the existing Unix-socket IPC API;
it does not contain a scheduler, worker, model client, or execution engine.

## Workflow

The home screen lists registered projects and durable sessions by name. Use the
arrow keys and Tab to select a section:

1. Select a project and press Enter.
2. Type a free-form investigation question and press Enter.
3. Forgehand creates a persistent investigation session and queues it on the
   daemon.
4. The session screen polls durable status and shows queue/execution progress,
   findings, and evidence file/source references.
5. Press `q` to exit. This closes only the frontend; the daemon and work keep
   running.
6. Launch `forgehand` again, switch to sessions, and reopen the investigation.

No project ID or session ID is displayed or entered in this workflow.

Press `a` on the home screen to register a Git repository. Clean repositories
are registered immediately. When changes are present, the TUI shows them and
offers explicit commit, confirmed discard, or cancel actions using the daemon's
existing project-intake protocol.

## Daemon recovery

If the socket is unavailable or the daemon protocol is incompatible, the TUI
shows a recovery screen rather than a raw IPC error. Press `s` to start an
already installed user service or `r` to retry. If no user service is installed,
exit and run:

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
