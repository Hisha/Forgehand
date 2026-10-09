# Linux Installation Guide

Forgehand provides a supported Linux installer for installing, upgrading, checking status, and uninstalling the systemd service. This replaces manual deployment steps while preserving the existing security architecture and application state.

## Implementation choice

The installer is implemented in Go (under `packaging/linux/installer`) rather than a pure shell script. Rationale:
- Strong typing and structured validation reduce shell injection risk.
- Easier to unit test with mocked system operations (no real system modifications).
- Consistent with existing Go codebase (daemon, CLI, tests).
- Better error handling and atomic file operations on Linux.
- Easier to enforce invariants (symlink rejection, path validation) safely.

A thin convenience wrapper (`packaging/linux/install.sh`) is provided for ergonomic CLI usage; it invokes a prebuilt installer executable at `packaging/linux/forgehand-installer`. The wrapper never compiles anything: it fails with a clear error if the executable is missing.

## Prerequisites

- Linux system with systemd.
- Root privileges (sudo) for install/upgrade/uninstall operations.
- Go toolchain available to build the daemon and the installer ahead of time (as used by `dev-build.sh`).
- Existing non-root account for personal mode, or ability to create service account for dedicated mode.

## Building Forgehand

Build the daemon binary:
```bash
cd /home/smithkt/git/Forgehand
./dev-build.sh
```
This produces `./forgehand`.

Build the installer executable (required by `install.sh`):
```bash
go build -o packaging/linux/forgehand-installer ./packaging/linux/installer
```

## Installation

### Personal mode (use existing account)
Do not let installer guess account; explicitly specify:
```bash
sudo ./packaging/linux/install.sh install --user <existing-username>
```
Notes:
- Uses existing account; does not create new user.
- Creates `forgehand` group if missing.
- Does not change primary group, home perms, or repository ownership.
- Adds CLI user access context via group membership only as needed (group creation only).

### Dedicated mode (create service account)
```bash
sudo ./packaging/linux/install.sh install --user forgehand --create-user
```
Notes:
- Creates non-login system account `forgehand` if it does not exist; verifies suitability if it exists.
- Creates `forgehand` group if missing.
- Runs service under that account.
- No sudo/capabilities/privileged groups; no hidden accounts.

### Common options
- `--source <path>`: Use explicit build artifact instead of building from repo (`./forgehand` by default).
- `--yes`/`--assume-yes`: Non-interactive mode (if implemented).
- `--help`: Show usage.

## Upgrading

```bash
sudo ./packaging/linux/install.sh upgrade --user <username>
```
Behavior:
- Preserves `/var/lib/forgehand`, SQLite DB, projects, sessions, execution history, repos, accounts/groups.
- Validates source, stages and atomically replaces binary after stopping service gracefully.
- Backs up current binary for rollback on failure.
- Reloads systemd, restarts service, verifies readiness with bounded retries.
- If startup fails after upgrade, attempts to restore previous binary/template and restart previous version. State is never destroyed; DB compatibility between versions is not guaranteed if migrations are incompatible (restoring older binary may not restore compatibility).

## Status

```bash
sudo ./packaging/linux/install.sh status --user <username>
```
Verifies:
- Service is active/enabled state as reported by systemd (where applicable)
- Daemon responds via `forgehand status` against installed socket
- Expected socket exists and has appropriate permissions
- Reports readiness clearly

## Uninstall

```bash
sudo ./packaging/linux/install.sh uninstall --user <username>
```
Behavior:
- Stops and disables selected instance (`forgehand@<user>.service`).
- Removes installed application components safely when no conflicting shared state requires preservation.
- Reloads systemd.
- Preserves application state (`/var/lib/forgehand`) by default, registered repositories, user accounts and groups by default.
- Never auto-deletes state or repos; refuses unsafe removal if multiple instances/conflicts detected.
- Destructive purge is not implemented in this milestone.

## Readiness verification

Daemon readiness is verified by invoking the existing CLI:
```bash
FORGEHAND_SOCKET_PATH=/run/forgehand/forgehand.sock /usr/bin/forgehand status
```
with bounded retries (exponential backoff with timeout) to tolerate startup delay. A clean `systemctl start` is not sufficient.

## Migration from manual install

Existing manually installed service/template is compatible. The installer:
- Validates and installs/updates template if needed.
- Detects conflicting active instances and refuses unsafe operations.
- Does not migrate or delete existing development state; preserves `/var/lib/forgehand`.

## Troubleshooting

- If another Forgehand instance owns same state dir/socket context, installer refuses unsafe ops (check `systemctl status forgehand@*`).
- For readiness failures, check logs: `journalctl -u forgehand@<user>.service`.
- Rollback: on upgrade failure, installer attempts to restore previous binary and template and restart; see limitations above if DB incompatible.

## Security notes

Follows [Security Principles](SECURITY-PRINCIPLES.md): non-root daemon, validate paths/names, reject symlink-based attacks on privileged dests, safe temp files, no execution of repo scripts as root, no embedded secrets, preserve least privilege, narrow privileged surface.
