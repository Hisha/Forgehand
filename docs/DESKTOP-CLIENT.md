# Forgehand Desktop Client (Avalonia, Phase 3)

The desktop client is a native Avalonia application for Linux and Windows. It
connects to the Forgehand daemon's authenticated remote API
(`docs/REMOTE-API-PHASE1.md`). The daemon remains the only backend. The client
has no database access, no Unix socket access, and never runs workers or
project commands.

Security requirements are defined in `docs/SECURITY-PRINCIPLES.md`, especially
section 14. Section 14.5's public-exposure release gate is **not** satisfied, so
the client is for loopback development use. See [Known limitations](#known-limitations).

## Status

The completed client provides:

- Server address configuration, validated and persisted without credentials.
- Password plus six-digit TOTP sign-in through `POST /api/v1/auth/login`.
- The project list from `GET /api/v1/projects`, showing only what the daemon returns.
- Sign-out through `POST /api/v1/auth/logout`, which returns to the login screen.
- Connection status from `GET /api/v1/health`.
- Explicit HTTP API compatibility status, including daemon version and build identity.
- Authorized project navigation and read-only session inspection.
- Session creation with the daemon-supported title and task fields.
- Explicit, confirmed execution submission through the daemon scheduler.
- Manual refresh of authoritative session state.

## Layout

```
client/
  Forgehand.Client.sln
  Directory.Build.props              shared target framework and language settings
  src/
    Forgehand.Client.Core/           no UI dependency; testable without Avalonia
      Api/                           HTTP client, DTOs (mirror internal/remoteapi), error mapping
      Configuration/                 ServerEndpoint validation, persisted client settings
      Session/                       in-memory session, ForgehandService, login validation
      ViewModels/                    Shell, Login, Projects view models (CommunityToolkit.Mvvm)
    Forgehand.Client/                Avalonia application
      Views/                         MainWindow, LoginView, ProjectsView (AXAML + code-behind)
      Styles/ForgehandTheme.axaml    dark theme styles; brushes are in App.axaml
      App.axaml(.cs), Program.cs     composition root
  tests/
    Forgehand.Client.Tests/          unit tests (xUnit 2) and opt-in live daemon tests
    Forgehand.Client.UiTests/        headless Avalonia tests of the real XAML (xUnit v3)
```

## Prerequisites

- .NET 10 SDK.
- Linux: an X11 or Wayland session to run the window. Building and the test suite do not need a display.
- A running daemon with the network API enabled on loopback:

  ```sh
  FORGEHAND_API_ADDRESS=127.0.0.1:8787 forgehand service start
  ```

  The daemon's listener is off unless this variable is set.

## Linux: build, test, and run

Run these from the repository root.

```sh
cd client
dotnet restore
dotnet build
dotnet test
dotnet run --project src/Forgehand.Client
```

Live checks against a running daemon are opt-in. They use no real credentials.
They check health, reject an unissued token on the project list and on logout,
and reject a login for an account that does not exist:

```sh
cd client
FORGEHAND_CLIENT_LIVE_ENDPOINT=http://127.0.0.1:8787 \
  dotnet test tests/Forgehand.Client.Tests --filter "FullyQualifiedName~LiveDaemonTests"
```

Without the variable, the live tests return immediately, so they count as passed
without contacting a daemon. Use the filter above to confirm the live path.

## Windows: build

Build on Windows, or cross-publish from Linux, with the .NET 10 SDK.
The following produces a single self-contained executable with no separate
.NET runtime installation:

```powershell
cd client
dotnet publish src\Forgehand.Client -c Release -r win-x64 --self-contained true -p:PublishSingleFile=true -o publish\win-x64
```

The output is `publish\win-x64\Forgehand.Client.exe`. For a framework-dependent
build instead, omit `--self-contained true` and `-p:PublishSingleFile=true`,
and install the .NET 10 Desktop Runtime on the target machine.

The publish command was verified to complete from Linux. The resulting Windows
executable has not been launched on Windows.

## Configuration

The server address is set in the **Server** bar and applied with **Apply**.
The default is `http://127.0.0.1:8787`.

Accepted forms:

| Address | Accepted |
| --- | --- |
| `http://127.0.0.1:8787`, `http://localhost:9000`, `http://[::1]:8787` | yes (loopback, explicit port) |
| `https://forgehand.example.com:8443` | yes (TLS, any host) |
| `http://192.168.1.10:8787`, `http://0.0.0.0:8787` | no (plain HTTP only for loopback) |
| a path, query, fragment, or user name in the address | no |
| an address without `http://` or `https://` | no |

The address is stored in `client-settings.json` under the .NET
`ApplicationData` folder, in a `Forgehand` subdirectory. That is `~/.config`
on Linux by default and `%APPDATA%` on Windows. The file holds the server address only.
If the saved value is invalid, the client does not use it silently. It falls
back to the default and shows a notice.

The server address cannot be changed while signed in. This keeps a bearer token
from being sent to a different server than the one that issued it.

## Security behavior

- **Credentials.** Passwords and TOTP codes are sent only to `/api/v1/auth/login`.
  Once a sign-in request has been sent, the password and code are cleared from
  the form whether it succeeds or fails. They are not stored, logged, or written
  to the settings file. Input that fails local validation is not sent and is
  not cleared, so the user can correct it.
- **Session token.** The bearer token is held in memory only. It is never written
  to disk, never placed in a URL, and is sent only in the `Authorization: Bearer`
  header. Its `ToString()` output is redacted.
- **Failure messages.** Error text is fixed and generated by the client from the
  error code. Server message text is never displayed. Invalid credentials give
  one generic message.
- **Sign-out.** The local session is cleared before the logout request. If the
  daemon does not confirm the logout, the client says so. The server session
  still expires on its own.
- **Expired session.** A `401` on a project request ends the session, returns to
  sign-in, and does not sign in again automatically.
- **Project data.** The list is only what `GET /api/v1/projects` returned. Nothing
  is discovered from the local filesystem. A project list that arrives after
  sign-out is discarded.
- **Transport.** Redirects are not followed, so the token cannot be sent to another
  location. System proxies are not used. TLS certificates are validated by the
  operating system. Validation is not disabled.
- **Limits.** Requests time out after 15 seconds. Responses are capped at 1 MiB.
- **Write isolation.** Creation and execution use only the authenticated HTTP
  API. The client never opens the repository, SQLite database, Unix socket, or
  a local worker process. Failed creation keeps the user's draft; navigation,
  revocation, expiry, and logout cancel outstanding operations and prevent late
  responses from restoring scoped data.
- **Compatibility.** The client sends `Forgehand-API-Version: v1` on every
  request and checks the health response before authentication. Missing or
  mismatched HTTP API metadata fails closed. Session creation and execution
  remain disabled until compatibility is confirmed, and a daemon-side
  `incompatible_api` rejection immediately disables them again.

## Compatibility and recovery

The status bar distinguishes disconnected, connected-compatible,
connected-incompatible, authentication-expired, and server-error states. For
a reachable daemon it shows the daemon application version, build identity
when available, HTTP API version, and compatibility result. A different build
identity is visible but is not itself an HTTP incompatibility; the explicit
HTTP API version is authoritative.

When the daemon is incompatible, the client preserves safe health and read
access, shows an explanation in the project workspace, and disables **New
Session** and **Run Session**. After the daemon is upgraded or restarted, use
**Check connection**. A matching health response re-enables those actions
without restarting the desktop application.

Use the existing local lifecycle commands on the daemon host:

```sh
forgehand service status
forgehand service update
forgehand service restart
```

The desktop application does not expose restart. The existing safe lifecycle
flow depends on local Unix peer identity, inspection of the managed systemd
user unit, confirmation that its PID matches the daemon, and checks for active
or queued work. Reproducing those guarantees as an authenticated remote
administrative operation requires a separately designed authorization scope,
reauthentication policy, active-work safety contract, and audit record.
Allowing the desktop client to execute `systemctl` or arbitrary commands would
bypass those boundaries, so restart is deliberately deferred.

## Session creation and execution

Select an authorized project and choose **New Session**. Title and task are
required. A successful create refreshes the project session list and selects
the new `READY` session; it does not execute it.

For a selected `READY` session, choose **Run Session**. The confirmation names
the project, session, and task and explains that a coding worker will start on
the Forgehand host. Only the confirmation submits
`POST /api/v1/sessions/{id}/run`. The client displays the daemon-confirmed
execution status and refreshes session details. A `409` means another caller or
state change made the session ineligible; refresh shows the authoritative state.

Progress observation is manual in this phase. **Refresh** reads existing session
endpoints; there is no terminal streaming, interactive input, WebSocket, or
client-side worker control.

## Known limitations

- **Plain HTTP is for development only.** The daemon serves no TLS. The client
  accepts plain HTTP only for loopback, but a reverse proxy in front of the
  daemon is the only route to a remote server, and that deployment is not yet
  reviewed. Do not expose the daemon to a network.
- **Recovery-code sign-in is not implemented.** The client sends the TOTP code
  only. Accounts that cannot complete MFA cannot sign in here.
- **Enrollment is not implemented.** An account that is still pending gets an
  `enrollment_required` message and must finish enrollment with the local tools.
- **No automatic session refresh or expiry timer.** Expiry is detected on the next
  request, not before.
- **Proxies are ignored.** Corporate HTTP proxies are not used. This is deliberate
  for loopback use and must be revisited before remote use.
- **Passwords stay in memory until garbage collection.** .NET strings cannot be
  zeroed, so the client clears its references but cannot erase the text.
- **Login cannot be cancelled by the user.** It ends after the 15-second timeout.
- **Verification scope.** The unit, headless UI, and live daemon tests were run on
  Linux. The Windows build and publish were verified, but the application was not
  launched on Windows. An interactive sign-in with a real account and TOTP code was
  not performed by the author. Doing so requires that account's credentials.

## Phase 3 limitations

- Progress is refreshed manually; there is no streaming or automatic polling.
- Creation uses the daemon's configured provider and default model. The client
  does not expose or invent provider/model catalogs.
- Revocation blocks future remote operations and visibility but does not cancel
  a worker already accepted by the daemon.
- Session editing, deletion, cancellation, approval, and interactive worker
  input remain out of scope.
