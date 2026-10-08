# Linux system-service foundation

Forgehand includes a systemd **system service template** at
`packaging/systemd/forgehand@.service`. It is an example deployment artifact,
not an installer. The repository does not create accounts or groups, install or
enable the unit, change group membership, or alter repository permissions.

## Identity boundaries

Forgehand distinguishes four identities and relationships:

- **Service identity:** the non-root Linux account running the daemon. The
  daemon refuses real or effective UID 0 and rejects effective Linux
  capabilities because it requires none.
- **Client identity:** for local Unix socket connections, the daemon captures
  kernel-provided PID, UID, and GID using `SO_PEERCRED`. These values are
  transport metadata and are never accepted from request JSON.
- **Project ownership:** not implemented. Membership in the `forgehand` group
  grants access to the local daemon, not authorization to an individual
  project.
- **Execution identity:** currently the service identity. Git operations,
  builds, tests, and future workers therefore use the service account's Linux
  filesystem permissions. Execution isolation and impersonation are not
  implemented.

These identities happen to be related in a single-user deployment, but the
architecture does not assume they are permanently identical. Root refusal is a
safety invariant; it is not a complete security sandbox.

## Configuration

Configuration is intentionally small and environment-based so the daemon and
CLI can share socket selection without an interactive login.

State directory resolution, in order:

1. `FORGEHAND_STATE_DIR`;
2. `$XDG_STATE_HOME/forgehand`;
3. `$HOME/.local/state/forgehand`.

Socket path resolution, in order:

1. `FORGEHAND_SOCKET_PATH`;
2. `$XDG_RUNTIME_DIR/forgehand/forgehand.sock`.

`FORGEHAND_SOCKET_MODE` accepts only `0600` or `0660` and defaults to `0600`.
Paths must be absolute; the filesystem root and a socket directly under `/` are
rejected. Runtime directories must deny world access, and a `0660` socket
requires group traversal on its directory.

The service template explicitly selects `/var/lib/forgehand` and
`/run/forgehand/forgehand.sock`; these are deployment values, not universal
application defaults. Existing development state is not migrated or deleted.
To ensure a development CLI does not connect to an installed daemon, unset the
`FORGEHAND_*` variables and use a distinct `XDG_RUNTIME_DIR`, or explicitly set
`FORGEHAND_SOCKET_PATH` to the desired development socket.

Service configuration, client connection configuration, and future
project-specific policy remain separate concerns. There is no generic
configuration framework or project authorization configuration yet.

## Daemon ownership and recovery

The daemon opens `daemon.lock` in its configured state directory and holds a
non-blocking Linux advisory file lock for its process lifetime. The lock is
acquired before SQLite is opened or migrations run. Consequently, changing the
socket path cannot start a second daemon against the same state directory.

The lock file is not a PID file and is not removed during normal operation.
Kernel advisory ownership is released on clean exit or process death, so a
crash does not leave a permanent lock and Forgehand never removes another live
process's ownership marker. Existing daemon-run and interrupted-session
recovery remains authoritative. SIGTERM produces the normal clean-shutdown
record after active fake workers stop.

## Socket access

The system service runs with `Group=forgehand`, a `0770` runtime directory, and
a `0660` socket. The service account owns the socket and can connect without
group membership. Other local CLI users generally need membership in the
`forgehand` group. Until application-level authorization exists, group members
must be treated as trusted daemon operators.

Group membership does not grant access to registered repositories. Repository
read and write access is governed independently by ordinary Linux permissions
of the service account. Forgehand does not chown, chmod, relocate, or add ACLs
to repositories.

`SO_PEERCRED` applies only to local Unix sockets. A future remote transport must
use its own authenticated identity and must perform authorization before
dispatching project operations; Unix groups cannot authorize remote clients.

## Example deployments

### Personal laptop

Use the existing non-root account as the systemd template instance, for example
`forgehand@ACCOUNT.service`, with group `forgehand`. Repositories under that
account's home remain accessible through its normal permissions. Replace
`ACCOUNT` with the actual configured account; no username is hardcoded in the
unit.

### Dedicated server

Use `forgehand@forgehand.service` after an administrator separately provisions
the `forgehand` user and group. Repositories might live under
`/srv/forgehand/projects`, with permissions granted through normal system
administration. Forgehand does not perform that provisioning.

This `forgehand@forgehand.service` instance is the default dedicated-service
model; the `%i` instance value keeps the service account configurable.

Both deployments run the same daemon. The service refuses root even if the unit
is incorrectly configured with `User=root`.

## systemd behavior and hardening

The template uses `StateDirectory=forgehand` and
`RuntimeDirectory=forgehand`, restarts after unexpected failure, sends SIGTERM
for graceful shutdown, and writes stdout/stderr to journald by default. It has
no network-online dependency. Useful diagnostics include:

```sh
systemctl status forgehand@ACCOUNT.service
journalctl -u forgehand@ACCOUNT.service
```

`NoNewPrivileges`, an empty capability set, and `ProtectSystem=full` are safe
for the current daemon. `ProtectHome` and `PrivateTmp` are deliberately disabled
because they can hide authorized home-directory repositories or previously
registered temporary repositories. Stronger filesystem isolation is deferred
until repository and worker access can be expressed explicitly.

The service account needs read access for discovery and read/write access for
project intake, commits, discards, and future coding/build operations. The
`forgehand` group alone does not provide those permissions.

## Development execution

Direct development remains supported:

```sh
export XDG_RUNTIME_DIR="$(mktemp -d)"
export XDG_STATE_HOME="$(mktemp -d)"
./forgehand daemon
```

Development defaults retain a `0700` runtime directory and `0600` socket and do
not require the `forgehand` group or systemd. `dev-build.sh` never installs,
enables, or restarts a system service.

## Future multi-user boundary

This milestone deliberately adds no users, roles, ACLs, ownership columns,
remote listeners, privilege switching, or worker sandboxing. Future work must
preserve these boundaries:

- local group membership controls daemon access, not project permissions;
- Unix peer credentials identify a local process but do not authorize a
  project;
- service-account filesystem permissions currently determine repository
  access;
- project authorization must run before an operation is dispatched;
- execution isolation is independent of model-provider selection;
- remote clients require transport-appropriate authentication and cannot rely
  on Unix group membership;
- no public TCP or HTTP listener is provided.
