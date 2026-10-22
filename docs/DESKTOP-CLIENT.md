# Forgehand Desktop Client (Avalonia, Phase 1)

The desktop client is a native Avalonia application for Linux and Windows. It
connects to the Forgehand daemon's authenticated remote API
(`docs/REMOTE-API-PHASE1.md`). The daemon remains the only backend. The client
has no database access, no Unix socket access, and never runs workers or
project commands.

Security requirements are defined in `docs/SECURITY-PRINCIPLES.md`, especially
section 14. Section 14.5's public-exposure release gate is **not** satisfied, so
the client is for loopback development use. See [Known limitations](#known-limitations).

## Status

Phase 1 provides:

- Server address configuration, validated and persisted without credentials.
- Password plus six-digit TOTP sign-in through `POST /api/v1/auth/login`.
- The project list from `GET /api/v1/projects`, showing only what the daemon returns.
- Sign-out through `POST /api/v1/auth/logout`, which returns to the login screen.
- Connection status from `GET /api/v1/health`.

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

## Next milestone

Phase 2 should add read-only views of the existing authorized endpoints:
`GET /api/v1/projects/{id}/sessions` and `GET /api/v1/sessions/{id}`. It should
also add the enrollment confirmation flow (`POST /api/v1/auth/enroll/confirm`) so
that a pending account can finish setup without the local CLI. Both need UI and
security review before implementation. Worker execution, session creation, and
any write operation remain out of scope until the daemon's sensitive-operation
reauthentication exists.
