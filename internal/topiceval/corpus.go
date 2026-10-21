// Package topiceval contains the representative evaluation corpus for
// investigation topic extraction and the model-independent scoring rules used
// to compare extraction methods.
//
// The corpus is a review artifact: every case records the engineering concepts
// a human reviewer expects a useful extraction to surface, the identifiers that
// must survive extraction unchanged, and the request wording that must not
// become a topic. Scoring never consults a language model, so a candidate
// extractor is never judged by another model.
package topiceval

// Case is one representative engineering investigation request together with
// the human-reviewed expectations used to score an extraction.
type Case struct {
	// Name identifies the case in reports.
	Name string
	// Request is the natural-language engineering investigation request.
	Request string
	// Constraints are the authorized-scope statements supplied with the
	// request. They must never contribute search topics.
	Constraints []string
	// RequiredConcepts are the engineering concepts a useful extraction must
	// surface. Multiword concepts are matched against the union of extracted
	// topic words, so a concept counts as covered when every one of its words
	// is present.
	RequiredConcepts []string
	// RequiredIdentifiers are language-level identifiers that must be
	// preserved as a topic term, compared case-sensitively.
	RequiredIdentifiers []string
	// ForbiddenConcepts are request words that must not become a topic
	// because they describe the request rather than the investigation subject.
	ForbiddenConcepts []string
	// NoSubject marks a request that names no concrete investigation subject.
	// Such a request is allowed to produce no topics at all.
	NoSubject bool
	// MaxTopics optionally bounds the acceptable topic count for a case.
	MaxTopics int
}

// Cases is the representative corpus. It covers daemon recovery and
// scheduling, configuration, authentication and authorization, database
// persistence and migrations, API request handling, UI behavior, build and
// deployment failures, C++ scoped identifiers, Go package-qualified symbols,
// C# namespaces, Python functions and modules, ambiguous and multi-concept
// requests, requests carrying irrelevant instructions, and requests with no
// meaningful investigation subject.
var Cases = []Case{
	{
		Name:    "daemon-recovery",
		Request: "Investigate how Forgehand handles daemon shutdown and recovery of interrupted sessions. Identify the components responsible for session persistence, execution scheduling, and recovery. Determine how the system prevents duplicate execution after a restart. Gather supporting source-code evidence with file paths and line numbers. Do not modify any files.",
		RequiredConcepts: []string{
			"daemon", "shutdown", "recovery", "interrupted", "session persistence",
			"execution scheduling", "duplicate execution", "restart",
		},
		ForbiddenConcepts: []string{"source-code", "file paths", "line numbers", "modify", "files", "evidence"},
	},
	{
		Name:             "daemon-crash-restart",
		Request:          "Investigate why the daemon crashes on startup after a SIGKILL and how startup recovery reconciles orphaned executions.",
		RequiredConcepts: []string{"daemon", "crash", "startup", "recovery", "orphaned executions"},
	},
	{
		Name:             "worker-scheduling",
		Request:          "Investigate how the worker scheduler claims queued executions and enforces FIFO ordering.",
		RequiredConcepts: []string{"worker", "scheduler", "queued executions", "FIFO"},
	},
	{
		Name:             "resource-claims",
		Request:          "Explain how exclusive resource claims prevent two sessions from using the same local model at once.",
		RequiredConcepts: []string{"resource claims", "exclusive", "local model"},
	},
	{
		Name:             "checkpoint-yield",
		Request:          "Investigate where a running worker is interrupted at a safe checkpoint boundary.",
		RequiredConcepts: []string{"worker", "interrupted", "checkpoint"},
	},
	{
		Name:                "config-provider-timeout",
		Request:             "Investigate how FORGEHAND_PROVIDER_TIMEOUT is read and applied to provider requests.",
		RequiredConcepts:    []string{"provider timeout", "provider requests"},
		RequiredIdentifiers: []string{"FORGEHAND_PROVIDER_TIMEOUT"},
	},
	{
		Name:                "config-base-url",
		Request:             "Determine where FORGEHAND_PROVIDER_BASE_URL is defined and which components read it.",
		RequiredConcepts:    []string{"provider base url"},
		RequiredIdentifiers: []string{"FORGEHAND_PROVIDER_BASE_URL"},
	},
	{
		Name:                "config-state-dir",
		Request:             "Trace how FORGEHAND_STATE_DIR determines where the SQLite database and the socket live.",
		RequiredConcepts:    []string{"SQLite", "socket"},
		RequiredIdentifiers: []string{"FORGEHAND_STATE_DIR"},
	},
	{
		Name:                "config-capability-allowlist",
		Request:             "Investigate how the provider capability allow-list gates semantic planning.",
		RequiredConcepts:    []string{"capability allow-list", "semantic planning"},
		RequiredIdentifiers: []string{},
	},
	{
		Name:             "config-default-override",
		Request:          "Investigate how default configuration values are overridden by environment variables.",
		RequiredConcepts: []string{"configuration", "environment variables", "override"},
	},
	{
		Name:             "auth-peer-credentials",
		Request:          "Investigate how peer credentials are captured from the Unix socket and recorded before authorization.",
		RequiredConcepts: []string{"peer credentials", "socket", "authorization"},
	},
	{
		Name:             "auth-project-ownership",
		Request:          "Determine whether project ownership is enforced before an investigation is allowed to run.",
		RequiredConcepts: []string{"project ownership", "enforced", "investigation"},
	},
	{
		Name:             "auth-root-refusal",
		Request:          "Investigate the daemon refusal to run as root before it creates directories or opens the socket.",
		RequiredConcepts: []string{"root", "daemon", "directories", "socket"},
	},
	{
		Name:             "db-migrations",
		Request:          "Investigate how database migrations are applied and how the schema version is tracked.",
		RequiredConcepts: []string{"database migrations", "schema version"},
	},
	{
		Name:             "db-wal-concurrency",
		Request:          "Explain the SQLite WAL configuration and the concurrency handling around the shared database connection.",
		RequiredConcepts: []string{"SQLite", "WAL", "concurrency"},
	},
	{
		Name:             "db-evidence-persistence",
		Request:          "Trace how work evidence records are persisted together with their blob hash and line range.",
		RequiredConcepts: []string{"work evidence", "persisted", "blob hash", "line range"},
	},
	{
		Name:             "db-plan-revisions",
		Request:          "Investigate how work plan revisions are stored and replayed after a semantic expansion.",
		RequiredConcepts: []string{"work plan", "revisions", "semantic expansion"},
	},
	{
		Name:             "api-request-routing",
		Request:          "Investigate how the IPC request router dispatches commands and encodes the responses.",
		RequiredConcepts: []string{"IPC", "router", "dispatches", "responses"},
	},
	{
		Name:             "api-session-start",
		Request:          "Determine how a session start request validates the project identifier before a work plan is created.",
		RequiredConcepts: []string{"session start", "project identifier", "work plan", "validates"},
	},
	{
		Name:                "api-graceful-shutdown",
		Request:             "Investigate how the daemon handles SIGTERM and drains in-flight work before it exits.",
		RequiredConcepts:    []string{"SIGTERM", "in-flight"},
		RequiredIdentifiers: []string{"SIGTERM"},
	},
	{
		Name:             "ui-work-plan-render",
		Request:          "Investigate how the TUI renders work plan task status and blocked reasons.",
		RequiredConcepts: []string{"TUI", "work plan", "task status", "blocked reasons"},
	},
	{
		Name:             "ui-session-refresh",
		Request:          "Explain how the TUI refreshes the session list and detects a daemon version mismatch.",
		RequiredConcepts: []string{"TUI", "session list", "version mismatch"},
	},
	{
		Name:             "ui-detach-reattach",
		Request:          "Investigate how a detached client reattaches to a durable session without ending the work.",
		RequiredConcepts: []string{"detached client", "reattaches", "durable session"},
	},
	{
		Name:             "build-linker-failure",
		Request:          "Investigate why the build fails with an undefined symbol during linking.",
		RequiredConcepts: []string{"build", "undefined symbol", "linking"},
	},
	{
		Name:             "build-ci-workflow",
		Request:          "Determine which continuous integration workflow runs the test suite and how a failure is reported.",
		RequiredConcepts: []string{"continuous integration", "workflow", "test suite"},
	},
	{
		Name:             "deploy-systemd-unit",
		Request:          "Investigate how the systemd unit file starts the daemon with the expected environment.",
		RequiredConcepts: []string{"systemd", "unit file", "daemon", "environment"},
	},
	{
		Name:                "cpp-scoped-identifiers",
		Request:             "Investigate WorldSession::IsHeadless and SessionManager::Recover in the C++ engine.",
		RequiredConcepts:    []string{"engine"},
		RequiredIdentifiers: []string{"WorldSession::IsHeadless", "SessionManager::Recover"},
	},
	{
		Name:                "cpp-template-specialization",
		Request:             "Explain how the TemplateRegistry::Instantiate template specialization is resolved at compile time.",
		RequiredConcepts:    []string{"template specialization", "compile time"},
		RequiredIdentifiers: []string{"TemplateRegistry::Instantiate"},
	},
	{
		Name:                "go-package-qualified-symbols",
		Request:             "Investigate planner.ExtractInvestigationTopics and daemon.executeInvestigationBootstrap in the Go codebase.",
		RequiredConcepts:    []string{"codebase"},
		RequiredIdentifiers: []string{"planner.ExtractInvestigationTopics", "daemon.executeInvestigationBootstrap"},
	},
	{
		Name:                "go-state-struct",
		Request:             "Locate where state.WorkPlanTask status transitions are persisted in the Go state package.",
		RequiredConcepts:    []string{"status transitions", "persisted"},
		RequiredIdentifiers: []string{"state.WorkPlanTask"},
	},
	{
		Name:                "csharp-namespace-method",
		Request:             "Investigate SessionManager.RestoreAsync in the Forgehand.Worker namespace of the C# service.",
		RequiredConcepts:    []string{"namespace", "service"},
		RequiredIdentifiers: []string{"SessionManager.RestoreAsync", "Forgehand.Worker"},
	},
	{
		Name:                "csharp-cancellation-token",
		Request:             "Determine how CancellationToken is observed in the C# request handler.",
		RequiredConcepts:    []string{"request handler"},
		RequiredIdentifiers: []string{"CancellationToken"},
	},
	{
		Name:             "python-normalization",
		Request:          "Investigate how the Python extractor normalizes a request before it tokenizes it.",
		RequiredConcepts: []string{"extractor", "normalizes", "tokenizes"},
	},
	{
		Name:                "python-module-function",
		Request:             "Locate the parse_config function in the config_loader Python module.",
		RequiredConcepts:    []string{"module"},
		RequiredIdentifiers: []string{"parse_config", "config_loader"},
	},
	{
		Name:             "ambiguous-two-concepts",
		Request:          "Investigate the retry policy and the circuit breaker.",
		RequiredConcepts: []string{"retry policy", "circuit breaker"},
	},
	{
		Name:      "ambiguous-vague",
		Request:   "Investigate the thing that breaks sometimes.",
		NoSubject: true,
		MaxTopics: 1,
		ForbiddenConcepts: []string{
			"breaks", "sometimes", "thing",
		},
	},
	{
		Name:             "irrelevant-instructions",
		Request:          "Investigate the scheduler. Also remember to update the README, run the linter, and tell the team. Do not modify files.",
		RequiredConcepts: []string{"scheduler"},
		ForbiddenConcepts: []string{
			"README", "linter", "team", "remember", "update", "run", "tell", "modify", "files",
		},
	},
	{
		Name:      "no-subject-polite",
		Request:   "Could you please help me understand how this works? Thanks!",
		NoSubject: true,
		MaxTopics: 1,
		ForbiddenConcepts: []string{
			"please", "help", "understand", "works", "thanks",
		},
	},
	{
		Name:             "mixed-language-coordination",
		Request:          "Investigate how Python asyncio scheduling and Go errgroup coordinate worker shutdown.",
		RequiredConcepts: []string{"asyncio", "errgroup", "worker shutdown"},
	},
	{
		Name:             "domain-compaction",
		Request:          "Investigate how the WAL checkpoint and the vacuum job interact during compaction.",
		RequiredConcepts: []string{"WAL", "checkpoint", "vacuum", "compaction"},
	},
	{
		Name:              "constraint-echo",
		Request:           "Investigate the token refresh flow.",
		Constraints:       []string{"read only", "do not modify files", "do not execute commands"},
		RequiredConcepts:  []string{"token refresh"},
		ForbiddenConcepts: []string{"read", "files", "commands", "modify", "execute"},
	},
	{
		Name:                "identifier-heavy",
		Request:             "Investigate FORGEHAND_PROVIDER_MAX_TOKENS, FORGEHAND_PROVIDER_TIMEOUT, and FORGEHAND_PROVIDER_API_KEY.",
		RequiredConcepts:    []string{"provider"},
		RequiredIdentifiers: []string{"FORGEHAND_PROVIDER_MAX_TOKENS", "FORGEHAND_PROVIDER_TIMEOUT", "FORGEHAND_PROVIDER_API_KEY"},
	},
	{
		Name:             "multiword-concepts",
		Request:          "Investigate how session recovery and checkpoint restoration differ after an unclean shutdown.",
		RequiredConcepts: []string{"session recovery", "checkpoint restoration", "unclean shutdown"},
	},
	{
		Name:              "numeric-noise",
		Request:           "Investigate the health endpoint on port 8080 that returns status code 200 for liveness probes.",
		RequiredConcepts:  []string{"health endpoint", "liveness probes"},
		ForbiddenConcepts: []string{"8080", "200"},
	},
	{
		Name:             "quoted-subject",
		Request:          "Investigate \"session persistence\" and how it interacts with the queue.",
		RequiredConcepts: []string{"session persistence", "queue"},
	},
	{
		Name:              "negative-restriction",
		Request:           "Investigate how the daemon prevents duplicate execution without modifying any files.",
		RequiredConcepts:  []string{"daemon", "duplicate execution"},
		ForbiddenConcepts: []string{"modifying", "files"},
	},
}
