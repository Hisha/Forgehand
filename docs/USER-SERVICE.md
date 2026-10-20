# Linux user-service lifecycle

The supported single-user lifecycle is built into the `forgehand` CLI and uses
the current user's systemd manager. It never invokes `sudo`, never writes
`/usr/bin/forgehand`, and never changes accounts, groups, repositories, SQLite
state, projects, or sessions.

## Install and start

Build or otherwise place the executable at its intended stable location, then
run it from that location:

```sh
go build -o "$HOME/.local/bin/forgehand" ./cmd/forgehand
"$HOME/.local/bin/forgehand" service install
"$HOME/.local/bin/forgehand" service start
```

`service install` writes and enables
`$XDG_CONFIG_HOME/systemd/user/forgehand.service` (normally
`~/.config/systemd/user/forgehand.service`). It records the canonical path of
the CLI that performed the installation and the resolved state-directory and
socket paths. Installation deliberately does not start the daemon.

The service uses the normal per-user state directory and runtime socket:

- `$XDG_STATE_HOME/forgehand`, or `~/.local/state/forgehand` when
  `XDG_STATE_HOME` is unset;
- `$XDG_RUNTIME_DIR/forgehand/forgehand.sock`.

If `FORGEHAND_STATE_DIR` or `FORGEHAND_SOCKET_PATH` is set during installation,
that explicit path is recorded in the unit. Use the same environment when
invoking the CLI, or reinstall without the override.

## Everyday operations

```sh
forgehand service status
forgehand service start
forgehand service stop
forgehand service restart
forgehand status
```

`forgehand status` reports daemon PID, executable, release/build identity,
socket, state directory, and IPC protocol. A daemon can be running yet be
reported as incompatible. Other CLI operations reject an incompatible daemon
instead of silently sending it work.

The daemon holds an advisory lock at `<state-dir>/daemon.lock` for its entire
lifetime. The lock, rather than its diagnostic PID text or the socket file, is
authoritative. A second daemon cannot open the same SQLite state through a
different socket.

## Update

First replace or build the executable at its stable location, or run a newer
Forgehand executable from its new intended location. Then run:

```sh
/path/to/new/forgehand service update
```

Update rewrites only a Forgehand-managed user unit, reloads the user manager,
and restarts the daemon only if it was already active. The state directory and
SQLite database are not replaced. Check the result with `forgehand status`.

## Uninstall

```sh
forgehand service uninstall
```

This stops and disables the user unit and removes only the managed unit file.
It preserves the executable, state directory, SQLite database, projects,
sessions, and repositories.

## Existing manual or system services

Forgehand does not assume an existing daemon is systemd-managed. Before moving
a manually started daemon to the user service, stop that exact process cleanly,
then run `forgehand service start`. Never delete `daemon.lock`: kernel lock
ownership is authoritative and disappears automatically when its owning
process exits.

The older administrator-managed `forgehand@.service` template and privileged
installer remain available for dedicated multi-user hosts. They are separate
from the recommended personal user service and must not run against the same
state directory at the same time.

If user services are unavailable (for example, a container without a systemd
user manager), direct `forgehand daemon` execution remains supported. In that
case process supervision is the operator's responsibility.
