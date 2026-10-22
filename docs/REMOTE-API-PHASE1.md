# Phase 1: Remote API Foundation and Mandatory TOTP

This document records the Phase 1 work that prepares the Forgehand daemon for
native desktop clients: a versioned, authenticated network API and the
authentication foundations it depends on.

`docs/SECURITY-PRINCIPLES.md` remains the authoritative security contract.
Nothing in this document relaxes it. Section 14.5 of that document defines a
public-exposure release gate; **that gate is not satisfied by this phase** and
the network listener is therefore restricted to the loopback interface.

## 1. Architecture findings

The inspection of the existing system produced the following findings. They
determined what could be reused and what had to be introduced.

### 1.1 Daemon lifecycle

`internal/daemon.Run` refuses to run as root and rejects unexpected effective
capabilities (`internal/daemon/identity_linux.go`). `runConfigured` acquires an
exclusive lock on the state directory (`internal/daemon/ownership_linux.go`),
opens the SQLite state database, creates the Unix socket, reconciles
interrupted executions, records a daemon run, starts the dispatcher, and then
serves the socket until the listener is closed.

The shutdown path cancels the daemon context, stops and waits for the worker
group, joins the dispatcher, and records a clean daemon run. **Reusable:** the
listener was started as a separate, self-contained step with its own shutdown
hook, so the network listener was added as a sibling of the Unix listener
rather than as a rewrite.

### 1.2 Unix socket transport and dispatch

The Unix socket is a newline-delimited JSON request/response protocol
(`internal/ipc/protocol.go`). `handleConnection` decodes one `ipc.Request`,
validates the protocol version and the build identity, and dispatches on
`request.Command` through a single large switch. The socket mode is `0600` or
`0660`; a `0660` socket intentionally authorizes broad *group* access.

**Reusable:** the request/response conventions, the protocol-version check,
and the state accessors. **Introduced:** the network API deliberately does
*not* reuse `ipc.Request`/`ipc.Response` and does not reuse the command
switch. It has its own DTOs, so the internal IPC command set (which includes
`session-run`, `task-approve`, `project-add`, and `task-cleanup`) is not
reachable over the network.

### 1.3 Existing authentication and identity

There was none. The only identity mechanism was the Unix socket peer
credential (`SO_PEERCRED`, `internal/daemon/peer_identity_linux.go`), which
identifies the local UID/GID of a socket peer but does not authorize an
individual project or user, and which has no meaning for a network client.

**Introduced:** a complete account, credential, session, and audit model.

### 1.4 SQLite schema and migration conventions

The state database is a single SQLite file managed by an ordered list of
versioned migrations in `internal/state/migrations.go`. Each migration runs in
its own transaction and records the new `schema_version`. The existing tests
assert the latest schema version.

**Reusable:** the migration mechanism, the `time.RFC3339Nano` timestamp
convention, the `Context`-taking method style, and the `database/sql` usage.
**Introduced:** migration 22, which adds `accounts`, `account_recovery_codes`,
`account_sessions`, `account_projects`, and `auth_audit_events`. SQLite was
not replaced and the existing schema was not modified.

### 1.5 Project and session authorization boundaries

None existed. `state.ListProjects` and `state.ListSessions` returned
everything to any local socket peer. Sessions carry an optional `project_id`,
and a session without a project has no project-level authorization basis.

**Introduced:** an explicit `account_projects` grant table. Every remote
project or session read is checked against it. Administrators are not
implicitly granted access to anything; a local administrator grants access
explicitly. Sessions without a project are not readable over the remote API.

### 1.6 Configuration mechanisms

Configuration is environment-driven and fail-closed: an unset variable means
the default, and an invalid value is an error rather than a silent fallback
(`internal/config/config.go`).

**Reusable:** the exact pattern. `FORGEHAND_API_ADDRESS` and
`FORGEHAND_API_SESSION_TTL` follow it.

### 1.7 Test structure and security-relevant invariants

Tests are per-package Go tests with no external framework. Security-relevant
invariants that already existed and were preserved: the daemon refuses to run
as root; it refuses unexpected capabilities; it refuses to delete a socket it
does not own; the runtime directory must deny world access; a second daemon
cannot take over the state directory.

**Introduced:** a new test package for each new component, plus a test that
proves the ordinary Unix socket command set is unchanged.

### 1.8 Reuse summary

| Area | Reused | Introduced |
| --- | --- | --- |
| Lifecycle | Listener start/stop pattern, context cancellation | `startRemoteAPI` sibling of the Unix listener |
| Transport | JSON conventions, protocol-version check | Separate API DTOs and routes |
| Persistence | Migration mechanism, `state.Database` methods | `internal/state/accounts.go` |
| Identity | Unix peer credentials | Accounts, Argon2id, TOTP, recovery codes, sessions |
| Authorization | `state.Project`, `state.Session` accessors | `account_projects` grants |
| Configuration | Environment variables, fail-closed parsing | `internal/config/api.go` |
| Auditing | `session_events` conventions | `auth_audit_events` table |

## 2. Network API

### 2.1 Shape

The API is a versioned JSON over HTTP under `/api/v1`. It is a separate
transport from the Unix socket and shares no command dispatch with it.

| Method | Path | Authentication | Effect |
| --- | --- | --- | --- |
| `GET` | `/api/v1/health` | none | Daemon liveness, version, build identity, API version |
| `POST` | `/api/v1/auth/login` | password **and** one-time password or recovery code | Issues a session token |
| `POST` | `/api/v1/auth/logout` | session | Revokes the session |
| `POST` | `/api/v1/auth/enroll/confirm` | password **and** one-time password | Completes enrollment for a pending account and issues a session |
| `GET` | `/api/v1/auth/me` | session | The authenticated account identity |
| `GET` | `/api/v1/projects` | session | The projects the account is authorized to access |
| `GET` | `/api/v1/projects/{id}/sessions` | session **and** project grant | The sessions of an authorized project |
| `GET` | `/api/v1/sessions/{id}` | session **and** project grant | The persisted details of one authorized session |

Every project and session value comes from the existing `state` package and
the existing session and project records. Nothing is synthesized.

### 2.2 Listener

* The listener is **disabled by default**. `FORGEHAND_API_ADDRESS` is unset
  in a default installation and the daemon then opens no network socket.
* When enabled, the address must resolve to the loopback interface
  (`127.0.0.0/8`, `::1`, or `localhost`). A routable address is refused with
  an error that names the unmet release gate. Host names are never resolved,
  so a DNS answer cannot move the listener onto a routable interface.
* A failure to bind is fatal: a requested but unavailable API never degrades
  into a partially running daemon.
* The listener serves plain HTTP on loopback. TLS termination belongs to a
  reverse proxy. This is the deployment shape required for a future Nginx
  deployment: Nginx terminates TLS on the public interface and proxies to
  `127.0.0.1`.

### 2.3 Request handling

* **Bounded bodies.** Request bodies are limited to 64 KiB with
  `http.MaxBytesReader`. A larger body is refused with `413`.
* **Strict JSON.** Decoding rejects unknown fields and trailing documents.
* **Content type.** A body must be `application/json`; anything else is
  refused with `415`.
* **Timeouts.** `ReadHeaderTimeout` 10 s, `ReadTimeout` 30 s, `WriteTimeout`
  60 s, `IdleTimeout` 120 s, `MaxHeaderBytes` 16 KiB.
* **Bounded identifiers.** Path identifiers are parsed as positive integers
  with a length bound; anything else is a `400`.
* **Safe errors.** A single JSON error envelope is used. Internal failures
  become a generic `500`; no internal detail reaches the caller.
* **No browser access.** A request carrying an `Origin` header is refused with
  `403`, and no cross-origin policy is advertised. The API has no browser
  client, and this removes the cross-site request forgery surface.
* **No trusted forwarding headers.** `X-Forwarded-For`, `X-Real-IP`, and
  `Forwarded` are ignored entirely. The caller address comes from the
  transport only. A deployment behind a proxy therefore sees the proxy
  address; that is the safe default.
* **Credentials are never in a URL.** The session credential is only accepted
  in the `Authorization: Bearer` header.

## 3. Authentication foundations

### 3.1 Passwords

Argon2id (`golang.org/x/crypto/argon2`) with `m = 19456 KiB`, `t = 2`,
`p = 1`, a 16-byte cryptographically random salt per hash, and a 32-byte
derived key. The parameters follow the OWASP guidance for Argon2id and are
recorded in every stored hash so they can be raised later. The stored form is
`$argon2id$v=19$m=...,t=...,p=...$salt$hash`; only the argon2id algorithm
and version 19 are accepted when parsing, so a weaker algorithm cannot be
introduced through a stored value. Passwords are never stored or logged in
plaintext.

### 3.2 One-time passwords

RFC 6238 with the interoperable defaults: HMAC-SHA-1, six digits, a 30 second
step. The implementation is verified against the RFC 6238 appendix B test
vectors. SHA-256 and SHA-512 are supported for provisioning.

* Secrets are 20 bytes from `crypto/rand` and are handed to the
  authenticator application as base32 through an `otpauth://` URI. No Google
  account or Google-specific service is involved.
* Validation accepts a window of exactly one step on either side, so a code
  is valid for at most 90 seconds.
* **Replay prevention.** The highest accepted counter is persisted per
  account. A code whose counter is not strictly greater is refused.
* A submission that is not exactly six digits is refused before any
  comparison is attempted.

### 3.3 Encrypted secrets at rest

TOTP secrets are encrypted with AES-256-GCM. The key is derived with
HKDF-SHA-256 from a daemon master key and the account name, and the account
name is bound in as additional authenticated data, so a ciphertext cannot be
moved to another account and cannot be modified without detection.

The master key is generated on first use into `<state dir>/auth-master.key`
with mode `0600`. The daemon refuses to use a master key file that is a
symbolic link, is not a regular file, or that permits group or world access.
No key is embedded in the binary.

### 3.4 Recovery codes

Ten codes are issued with each account, in the form `XXXXX-XXXXX` over an
ambiguity-free alphabet, generated from `crypto/rand` with rejection
sampling. Only the SHA-256 digest is stored. A redeemed code is marked used
by a conditional update, so two concurrent uses cannot both succeed. When a
code is redeemed, the remaining codes are invalidated and a fresh set is
issued and returned exactly once.

A recovery code is a *second factor alternative for one login*. It never
disables the mandatory one-time password: the account still requires a
one-time password for every subsequent login.

### 3.5 Throttling

* **Per account.** Five consecutive failures lock the account for fifteen
  minutes. The counter and the lock are persisted, so restarting the daemon
  cannot reset the budget.
* **Per caller.** Twenty failures from one network peer within fifteen
  minutes throttle that peer for fifteen minutes. This bounds password
  spraying across accounts from a single source.
* Both budgets are consulted before any password derivation, and both are
  audited.

### 3.6 Sessions

* A session token is 32 bytes from `crypto/rand`, base64url encoded. Only its
  SHA-256 digest is stored.
* A session has an absolute lifetime (12 hours by default,
  `FORGEHAND_API_SESSION_TTL`) and an idle limit (1 hour).
* Sign-out revokes the session. Disabling an account revokes every session
  it owns. The local administrative channel can revoke all sessions of an
  account.
* Session activity is recorded at most once a minute, so a burst of requests
  does not become a burst of database writes.

### 3.7 Audit records

`auth_audit_events` records account creation, enrollment confirmation,
login success, login failure, throttling, recovery-code redemption, session
issuance and revocation, account disabling, and project grants and revokes.
Each record carries the event type, the account, the username, the caller
address, the outcome, a fixed non-secret detail string, and a timestamp. No
credential, hash, secret, or token is ever written to an audit record, and the
audit table is not exposed by the remote API.

## 4. Account enrollment

There is no unauthenticated registration endpoint, and no first-user
registration route exists on the network API.

The first administrator is provisioned through the **existing local Unix
socket**:

1. `forgehand account bootstrap-admin --username <name>` prompts for the
   password with terminal echo disabled and sends it over the Unix socket.
2. The daemon accepts the command only when the connecting peer's UID is the
   daemon's own effective UID, checked through `SO_PEERCRED`. A peer that
   merely shares the socket's group is refused. This is the "authorized local
   access" the task requires, and it is strictly narrower than the socket's
   group access.
3. The bootstrap refuses to run when any account already exists, so it cannot
   be used to add a hidden account to an installation that already has
   identities.
4. The daemon returns the enrollment secret, the `otpauth://` URI, and ten
   recovery codes. The CLI prints them once. The secret is stored only in
   encrypted form.
5. The account is created in the `pending` state. A pending account cannot
   use any ordinary API operation.
6. `forgehand account activate --username <name>` prompts for a code from the
   authenticator application and sends it over the Unix socket. The daemon
   verifies it, records the accepted counter, and activates the account.

**Enrollment cannot bypass mandatory MFA.** A pending account has no session
and no project access. The remote enrollment route
`POST /api/v1/auth/enroll/confirm` requires the account password *and* a
valid one-time password, so there is no password-only route anywhere in the
system. After enrollment confirmation the account is `active` and every
login requires a password plus a one-time password (or a single-use recovery
code).

## 5. Minimal authenticated API

Phase 1 exposes five read-only capabilities:

* server health and version,
* the authenticated account identity,
* the authorized project list,
* the session list of an authorized project,
* the persisted details of one authorized session.

Remote worker execution, approvals, arbitrary shell commands, and repository
modification are deliberately **not** implemented. The internal IPC commands
that perform them have no network route; a request to them returns `404`.

Authorization is enforced by the daemon for every project and session read.
A session whose project is not granted to the account returns `404` rather
than `403`, so that the existence of another project's session is not
disclosed.

## 6. Verification

`go test ./...` and `go vet ./...` are the required checks. The security
relevant tests added in this phase are:

| Requirement | Test |
| --- | --- |
| Password hashing and verification | `internal/auth/password_test.go` |
| TOTP vectors, enrollment, validation | `internal/auth/totp_test.go` |
| Expired, invalid, replayed codes | `internal/auth/totp_test.go`, `internal/identity/identity_test.go` |
| Recovery code single use | `internal/identity/identity_test.go`, `internal/state/accounts_test.go` |
| Login rate limiting | `internal/auth/ratelimit_test.go`, `internal/identity/identity_test.go` |
| Session expiration and revocation | `internal/identity/identity_test.go`, `internal/state/accounts_test.go` |
| Unauthorized API access | `internal/remoteapi/server_test.go` |
| Cross-project authorization boundaries | `internal/remoteapi/server_test.go`, `internal/identity/identity_test.go` |
| Request size and malformed input | `internal/remoteapi/server_test.go` |
| Remote listener disabled by default | `internal/config/api_test.go`, `internal/daemon/remoteapi_test.go` |
| Unix socket compatibility | `internal/daemon/remoteapi_test.go`, `internal/daemon/daemon_integration_test.go` |
| Local administration authorization | `internal/daemon/accounts_test.go` |

## 7. Security limitations and remaining work

Passing unit tests is functional evidence, not proof of security. The
following are known gaps.

### 7.1 The public-exposure release gate is not satisfied

`docs/SECURITY-PRINCIPLES.md` section 14.5 is **not** met. The listener is
loopback-only, the transport is plain HTTP, and there is no reverse-proxy
configuration, no TLS configuration, and no trusted-proxy model in this
phase. The daemon must not be exposed to a public network. Deploying behind
Nginx with TLS termination and an isolated backend is the intended shape,
but that deployment has not been designed, reviewed, or tested here.

### 7.2 Remaining authentication work

* **Authenticator loss recovery is not implemented.** A user who loses their
  authenticator and every recovery code has no in-product recovery path. The
  intended path is a controlled, audited local administrative recovery, which
  does not exist yet. Until it does, the only recovery is to operate on the
  state database directly, which is outside the product's security model.
* **Credential rotation is not implemented.** There is no password change, no
  authenticator replacement, and no authenticator removal path. The security
  principles require these to be audited and to require a fresh
  multi-factor challenge; they are not built.
* **Sensitive-operation reauthentication is not implemented.** Phase 1 has no
  sensitive remote operation, so there is nothing to reauthenticate. The
  mechanism must be built before any write operation is exposed.
* **Account creation beyond the first administrator is not implemented.** The
  local administrative channel can grant and revoke project access and can
  disable accounts, but it cannot create a second account. There is therefore
  no way to give a second person access without creating a new installation.
* **Account lockout is a denial-of-service surface.** An attacker who knows a
  username can lock that account for fifteen minutes by failing five times.
  This is the standard tradeoff, but it is a real limitation.
* **The caller throttle is in memory.** A daemon restart resets the per-caller
  budget. The per-account budget is persisted and is not affected.
* **The caller address is the transport address.** Behind a reverse proxy
  every caller appears as the proxy, so the caller throttle applies to the
  proxy rather than to the client. This is the safe default, but a
  reverse-proxy deployment needs an explicit, reviewed trusted-proxy model
  before the throttle is meaningful.

### 7.3 Remaining transport and platform work

* No TLS support in the daemon. The daemon speaks plain HTTP on loopback and
  expects a reverse proxy to terminate TLS.
* No request signing, no replay protection for non-authentication requests,
  and no certificate-based client authentication.
* The daemon remains Linux-only. The Avalonia desktop clients do not exist
  yet.
* The remote API exposes no streaming or long-poll interface. A desktop client
  will need one, and it will need its own authorization review.

### 7.4 Verification that has not been performed

No independent security review, no dependency audit, no secret or credential
scan, and no penetration test has been performed on this change. The tests in
this repository are the only evidence produced here, and they are evidence of
behaviour, not of absence of defects.

## 8. Operating the new capability

```sh
# Provision the first administrator over the local Unix socket.
forgehand account bootstrap-admin --username alice
forgehand account activate --username alice

# Grant the account access to a project.
forgehand account grant --username alice --project 1

# Enable the network listener (loopback only).
FORGEHAND_API_ADDRESS=127.0.0.1:8787 forgehand service start
```

The listener is off unless `FORGEHAND_API_ADDRESS` is set. The systemd unit
does not set it, so a default installation has no network interface.
