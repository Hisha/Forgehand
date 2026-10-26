package remoteapi

import "context"

// DaemonStatusOperations is the read-only daemon-owned status boundary used by
// the remote API. Implementations must not mutate lifecycle or execution state.
type DaemonStatusOperations interface {
	DaemonStatus(ctx context.Context) (DaemonStatusResponse, error)
}

// DaemonStatusResponse is the authenticated operational status contract.
// Pointer counts are null when their authoritative source is unavailable.
type DaemonStatusResponse struct {
	Status     string                 `json:"status"`
	ObservedAt string                 `json:"observed_at"`
	Identity   DaemonIdentityResponse `json:"identity"`
	Activity   DaemonActivityResponse `json:"activity"`
	Executable ExecutableResponse     `json:"executable"`
	Health     DaemonHealthResponse   `json:"health"`
}

type DaemonIdentityResponse struct {
	Version        string `json:"version"`
	BuildIdentity  string `json:"build_identity"`
	BuildTimestamp string `json:"build_timestamp,omitempty"`
	APIVersion     string `json:"api_version"`
	StartedAt      string `json:"started_at"`
	UptimeSeconds  int64  `json:"uptime_seconds"`
}

type DaemonActivityResponse struct {
	WorkerCapacity    *int `json:"worker_capacity"`
	ActiveWorkers     *int `json:"active_workers"`
	RunningSessions   *int `json:"running_sessions"`
	QueuedExecutions  *int `json:"queued_executions"`
	PendingSessions   *int `json:"pending_sessions"`
	CompletedSessions *int `json:"completed_sessions"`
	FailedSessions    *int `json:"failed_sessions"`
}

type ExecutableResponse struct {
	RunningFileIdentity   string `json:"running_file_identity,omitempty"`
	InstalledFileIdentity string `json:"installed_file_identity,omitempty"`
	Comparison            string `json:"comparison"`
	Reason                string `json:"reason"`
	CheckedAt             string `json:"checked_at"`
	RestartRecommended    bool   `json:"restart_recommended"`
}

type DaemonHealthResponse struct {
	Database   string `json:"database"`
	Dispatcher string `json:"dispatcher"`
	Workers    string `json:"workers"`
}
