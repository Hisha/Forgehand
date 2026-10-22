# Forgehand Security Principles

These principles establish security as a permanent architectural requirement of Forgehand. They are intended to remain stable and must be considered in future work.

## Status

This document defines the security contract and threat boundaries for future implementation. Security scanning, authorization systems, sandboxing, authentication, secret stores, and promotion gates are planned capabilities, not current implementations.

**Remote access security is a mandatory architectural requirement, not a claim of current implementation.** Forgehand must not enable a publicly reachable remote API until the relevant authentication, authorization, transport, and security acceptance criteria in this document are implemented and verified. The existing local Unix socket and CLI remain distinct from the planned remote API.

## 1. Untrusted generated code

All generated or externally supplied code is untrusted until appropriately verified. This applies regardless of origin: local LLMs, cloud LLMs, external repositories, retrieved documentation, or human-supplied patches. A coding worker cannot declare its own changes secure.

## 2. Prohibited hidden access mechanisms

Forgehand must not knowingly introduce:

- Hardcoded passwords or authentication tokens.
- Embedded production secrets or private keys.
- Undocumented user accounts.
- Hidden administrator accounts.
- Master passwords.
- Secret authentication bypasses.
- Backdoor endpoints.
- Undocumented remote access mechanisms.
- Unauthorized privilege escalation.
- Security controls intentionally disabled to bypass verification.

Legitimate configured service-account names, documented test fixtures, and intentionally designed authentication features must not be automatically classified as backdoors. Distinguish authentication secrets from ordinary configuration values.

## 3. Least privilege

Forgehand and its workers must use the minimum permissions necessary. The architecture must distinguish:

1. Daemon service identity.
2. Client identity.
3. Project authorization.
4. Worker execution identity.
5. Model-provider access.

These identities need not be the same. The existing non-root daemon requirement is preserved. Unix group membership currently authorizes broad local daemon access; this does not equate to project-level authorization.

## 4. Secure secret management

Future generated applications must obtain production secrets from appropriate external secret-management mechanisms. Acceptable patterns include protected environment injection, restricted secret files, and dedicated secret stores. No particular cloud provider is mandated. Secrets must never be printed into ordinary logs, verification reports, or model prompts.

## 5. Security verification

Mandatory security verification is a separate dimension from functional verification. Future verification must consider:

- Secret and credential scanning.
- Hardcoded authentication mechanisms.
- Suspicious account creation.
- Authentication and authorization bypasses.
- Command injection.
- SQL injection.
- Path traversal.
- Unsafe deserialization.
- Insecure cryptography.
- Dangerous file permissions.
- Privilege escalation.
- Vulnerable dependencies.
- Unexpected outbound network behavior.
- Unauthorized persistence mechanisms.
- Language-specific security risks.

Checks must be selected based on the project language, build system, dependencies, and actual changes. Irrelevant scanners should not be required for every project.

### Pre-execution security assessment

Before executing newly generated or modified build scripts, test harnesses, installation scripts, or other potentially dangerous code, Forgehand must apply a pre-execution security assessment and an appropriate execution policy. This assessment is separate from final security verification. It evaluates the risks of executing the proposed code (including command injection, privilege escalation, unexpected network access, destructive file operations, and unauthorized persistence) and determines whether execution is permitted, requires constrained execution, or must be blocked. Final security verification still occurs before promotion. Forgehand must not blindly execute such code without applying this execution policy.

## 6. Evidence-backed findings

Security findings must identify:

- Affected file and location.
- Relevant changed code.
- Security concern.
- Severity.
- Supporting evidence.
- Scanner or verification method.
- Verification status.
- Recommended remediation.

Confirmed findings must be separated from suspected risks. Model-generated explanations cannot substitute for actual verification evidence.

## 7. Fail-closed promotion policy

Future promotion gates must follow a fail-closed model. Critical unresolved findings must block automatic promotion. Mandatory checks that fail to execute or produce an indeterminate result must not be represented as passing, and must block automatic promotion unless a separately authorized and auditable exception exists. Explicit states must include: Passed, Failed, Blocked, Not run, Not applicable.

The absence of reported findings does not prove code is secure. A controlled human exception process may exist, but workers must not approve their own exceptions. Any exception must be authorized by a separate actor.

## 8. External research and prompt injection

All retrieved web content, repository documentation, issue comments, and external tool outputs are untrusted data. External material may supply evidence but must not override Forgehand's security policy or authorize commands.

Forgehand must prevent automatic disclosure of private source code, credentials, internal paths, or sensitive project information to external research providers. Future web-search integration must preserve source provenance and respect explicit network-access policies.

## 9. Auditability

Security verification must eventually produce durable, attributable records recording:

- What was checked.
- Which revision or diff was checked.
- Which tools and versions were used.
- Which checks passed, failed, or did not run.
- What findings were produced.
- Who authorized any exception.
- Whether the code was permitted to advance.

No specific database schema is mandated by this document.

## 10. No false security guarantees

Automated scanning cannot prove the absence of vulnerabilities or malicious code. Security verification provides risk reduction and evidence, not mathematical certainty.

## 11. Security verification pipeline

The intended workflow for engineering work is:

**Engineering request → investigation → planning → coding → functional verification → security verification → promotion decision.**

Security considerations must influence investigation and planning, not only post-generation. The functional verifier and security verifier must provide independently identifiable results. Security findings must be remediated and rechecked against the updated revision before promotion.

## 12. Security boundaries for Forgehand itself

### Current boundaries

- **Non-root daemon:** The daemon refuses root execution. This is a safety invariant, not a sandbox.
- **Local socket access:** Unix domain sockets control local access. `SO_PEERCRED` provides local peer identity metadata but does not authorize individual projects.
- **Group-based access:** Unix group membership currently authorizes broad local daemon access. This is not equivalent to project-level authorization.
- **Filesystem identity:** Execution currently uses the service account's identity. A non-root daemon is not equivalent to a sandboxed coding worker.
- **State protection:** SQLite state and the state directory are owned by the service identity with appropriate filesystem permissions.

### Future boundaries

- **Project authorization:** Must be enforced before operations are dispatched.
- **Worker isolation:** Coding and execution workers require stronger isolation than the daemon service account.
- **Remote authentication:** Remote transports cannot rely on Unix groups or peer credentials and must satisfy Section 14.
- **External providers:** Model and research providers must respect data-exposure controls and network policies.
- **Repository access:** Project access must be constrained by authorization, not only by filesystem permissions.

## 13. Acceptance criteria for future milestones

Future milestones involving generated code, execution, network access, credentials, or authorization must explicitly address:

1. Relevant threat boundaries.
2. Required permissions (least privilege).
3. Secret handling.
4. Verification requirements.
5. Failure behavior (fail-closed).
6. Evidence and auditability.
7. Tests for security-sensitive behavior.

Security requirements must be proportional to the capability being introduced. For any remote API or Avalonia desktop milestone, Section 14 and its public-exposure release gate are mandatory, not optional or deferrable security enhancements.

## 14. Remote API and desktop authentication

This section governs the planned network API and all remote clients, including the Avalonia desktop application on Linux and Windows. The Linux daemon remains the officially supported server platform; additional server platforms may be considered through future contributions. These requirements do not imply that a remote API or desktop authentication is already implemented.

### 14.1 Authentication is enforced by the daemon

- Every remote interactive user must authenticate using a unique account, a password, and a time-based one-time password (TOTP) from a standards-compatible authenticator application.
- TOTP is mandatory. There must be no password-only login mode, MFA-disable configuration option, alternate API route, reverse-proxy trust shortcut, or SSH-tunnel exemption that bypasses this requirement.
- The server, not the Avalonia client, must enforce authentication and MFA completion before granting access to ordinary API operations.
- A newly provisioned account may access only a narrowly scoped enrollment flow until TOTP enrollment is verified with a valid code. Enrollment must not grant access to projects, workers, repositories, or administrative operations.
- If MFA verification is unavailable, fails, or cannot be completed, access must be denied. Recovery must use a documented, constrained process rather than silently falling back to password-only authentication.
- TOTP is an open standard and must not require a Google account, Google OAuth, or a Google-specific service. Google Authenticator and other compatible applications should work.
- Noninteractive API access, if introduced later, must have a separately documented, narrowly scoped, revocable credential model and may not become a general-purpose substitute for mandatory MFA-based interactive login.

### 14.2 Passwords, TOTP secrets, and recovery

- Passwords must be hashed using a suitable password-hashing algorithm such as Argon2id, with unique salts and reviewed parameters. Passwords must never be stored or logged in plaintext.
- TOTP enrollment secrets must be generated using a cryptographically secure random source, protected at rest using authenticated encryption or an appropriate secret store, and excluded from logs, prompts, diagnostics, and ordinary API responses.
- Enrollment must require proof of possession of the authenticator before activation. Re-enrollment, replacement, and disabling of an authenticator must require a controlled, audited recovery or reauthentication process; ordinary remote access must never be permitted without active MFA.
- Single-use recovery codes must be generated securely, shown only through a protected enrollment or recovery flow, stored as hashes, invalidated after use, and protected by rate limits and auditing. Recovery codes are exceptional recovery factors, not a permanent MFA-disable mechanism.
- TOTP validation must allow only a narrowly defined clock-skew window, throttle failures, and prevent reuse of an already accepted code where practicable. The server clock must be maintained accurately.
- Account recovery and credential rotation must be designed and tested before public exposure. No hidden administrator, master password, or undocumented recovery endpoint is permitted.

### 14.3 Sessions and authorization

- Successful password-plus-TOTP authentication may establish a time-limited, revocable session so users do not need to enter a new code for every API request.
- Session credentials must be cryptographically strong, securely stored on desktop clients, protected in transit, scoped to the authenticated identity, and never placed in URLs or ordinary logs.
- Session expiration, logout, revocation, credential rotation, and server-side invalidation must be supported. Sensitive account changes require recent reauthentication, including a fresh MFA challenge.
- Authentication does not itself authorize access to every project, worker, repository, approval, or administrative operation. Authorization must be enforced by the daemon for each relevant action and resource.
- An authenticated user or compromised desktop client must not bypass worker execution restrictions, approval gates, filesystem boundaries, or project authorization.
- Failed logins, enrollment changes, recovery, session revocation, and sensitive actions must produce attributable, non-secret-bearing audit records.

### 14.4 Network transport and deployment

- Preserve the local Unix domain socket and existing CLI behavior. Remote access must use a separately controlled network interface; remote callers must not inherit Unix socket or Unix group privileges implicitly.
- The network listener must be disabled by default. Initial development may use a loopback-bound listener accessed through an SSH tunnel; this does not exempt the remote client from application-level authentication requirements.
- Public or LAN network access must use authenticated, encrypted transport. HTTPS with modern TLS configuration is required for direct network exposure; TLS 1.3 is preferred and TLS 1.2 is the minimum supported version.
- Deployments behind Nginx are supported. TLS termination at Nginx is acceptable only when the proxy-to-daemon connection is protected by appropriate network isolation or encryption, and direct access to the backend is restricted.
- Forgehand must not trust arbitrary client-supplied identity, forwarding, or authorization headers. Reverse-proxy configuration must explicitly control trusted proxies, forwarding headers, and allowed hosts.
- Network endpoints must enforce request size limits, timeouts, authentication rate limits, and input validation. Cross-origin and request-forgery protections must be appropriate to the chosen protocol and credential handling.
- A nonstandard public port or obscured endpoint name is not a security control. Public access must not rely on secrecy of the host, port, or API paths.
- The daemon must continue to run without root privileges, and API requests must not grant unrestricted execution or filesystem access merely because authentication succeeded.

### 14.5 Public-exposure release gate

A network-facing Forgehand deployment must not be declared ready for direct public internet exposure until tests and review demonstrate at least:

1. No password-only or MFA-bypass route, including alternate endpoints and recovery flows.
2. Correct TOTP enrollment, validation, throttling, replay handling, and recovery-code behavior.
3. Password, TOTP secret, and session-token protection at rest, in transit, and in logs.
4. Session expiration, revocation, logout, and sensitive-operation reauthentication.
5. Server-side authorization for projects, workers, approvals, and administrative actions.
6. Secure reverse-proxy configuration, backend isolation, TLS, and rejection of untrusted forwarding or identity headers.
7. Input validation, request limits, error handling, and abuse resistance.
8. Auditable authentication and sensitive operations without credential leakage.
9. Negative security tests for bypass attempts, privilege escalation, and unauthorized execution.

A successful functional demonstration of Avalonia connecting to the daemon does not satisfy this release gate. Missing or indeterminate mandatory security checks must block public deployment; no worker may approve its own exception.
