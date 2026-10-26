# Phase 5A: Daemon Operational Visibility

Phase 5A adds a read-only operational snapshot to the authenticated remote API
and an administrator dashboard to the Avalonia client. It does not add daemon
lifecycle controls: start, stop, restart, upgrade, scheduling, and execution
semantics are unchanged.

## Authoritative sources

The daemon composes the snapshot from existing owners of state:

- `internal/version` supplies release, build, and build-time identity.
- The current daemon process supplies its captured start time and uptime.
- `workerGroup` supplies active worker ownership; this is used for both active
  workers and actively running sessions, rather than trusting possibly stale
  persisted `RUNNING` rows.
- The dispatcher supplies configured worker capacity and availability.
- SQLite supplies queued executions and durable pending/completed/failed
  session counts through indexed status columns.

Unknown or unavailable values are represented explicitly. Numeric values are
not fabricated when their source cannot be read.

## API

`GET /api/v1/daemon/status` requires a valid bearer session whose account role
is `administrator`. An unauthenticated request receives `401`; an authenticated
non-administrator receives `403`. The route is read-only and accepts no body.

The response contains daemon/build identity, HTTP API version, start time and
uptime; worker and session activity; running-versus-installed executable
identity; and database, dispatcher, and worker-subsystem availability.

The public `/api/v1/health` route remains the minimal unauthenticated liveness
check and does not expose these operational details.

## Executable replacement detection

At daemon startup, Forgehand captures the running executable's device/inode,
size, and modification time. Status refreshes compare that baseline and the
running Linux image (`/proc/self/exe`) with the file currently present at the
captured installed path. This is a file-identity check, not a pathname or Git
commit comparison, and it performs no repeated full-file hashing.

The comparison is `match`, `mismatch`, or `unknown`. A restart is recommended
only for a confirmed mismatch. Missing files or inaccessible metadata produce
`unknown`, not a false mismatch.

## Desktop dashboard

After an administrator signs in, **Daemon Status** appears beside **Projects**.
The dashboard refreshes on entry and on explicit **Refresh**. It shows a local
last-successful-refresh time, loading and safe error states, explicit unknowns,
and a restart recommendation only for a confirmed executable mismatch. Sign-out
cancels/invalidates an in-flight refresh and clears the snapshot from memory.

The dashboard never restarts, shuts down, upgrades, reconfigures, or submits
work to the daemon.
