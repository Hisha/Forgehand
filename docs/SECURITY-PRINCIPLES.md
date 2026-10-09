# Forgehand Security Principles

These principles establish security as a permanent architectural requirement of Forgehand. They are intended to remain stable and must be considered in future work.

## Status

This document defines the security contract and threat boundaries for future implementation. Security scanning, authorization systems, sandboxing, authentication, secret stores, and promotion gates are planned capabilities, not current implementations.

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
- **Remote authentication:** Remote transports cannot rely on Unix groups or peer credentials.
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

Security requirements must be proportional to the capability being introduced.