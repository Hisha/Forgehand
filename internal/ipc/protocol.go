package ipc

import "encoding/json"

type Request struct {
	Command             string `json:"command"`
	Title               string `json:"title,omitempty"`
	SessionID           int64  `json:"session_id,omitempty"`
	Path                string `json:"path,omitempty"`
	ProjectID           int64  `json:"project_id,omitempty"`
	Action              string `json:"action,omitempty"`
	ExpectedState       string `json:"expected_state,omitempty"`
	IncludeFiles        bool   `json:"include_files,omitempty"`
	IncludeComponents   bool   `json:"include_components,omitempty"`
	IncludeDependencies bool   `json:"include_dependencies,omitempty"`
	IncludeEvidence     bool   `json:"include_evidence,omitempty"`
	AnalysisID          int64  `json:"analysis_id,omitempty"`
	FromAnalysisID      int64  `json:"from_analysis_id,omitempty"`
	ToAnalysisID        int64  `json:"to_analysis_id,omitempty"`
	Kind                string `json:"kind,omitempty"`
	Identity            string `json:"identity,omitempty"`
	ComponentPath       string `json:"component_path,omitempty"`
	Contains            string `json:"contains,omitempty"`
	Limit               int    `json:"limit,omitempty"`
	Analysis            int64  `json:"analysis,omitempty"`
	Format              string `json:"format,omitempty"`
	AfterEventID        int64  `json:"after_event_id,omitempty"`
	InteractionID       int64  `json:"interaction_id,omitempty"`
	Answer              string `json:"answer,omitempty"`
	Reason              string `json:"reason,omitempty"`
	// Task and Model configure a coding session created through session-start.
	Task  string `json:"task,omitempty"`
	Model string `json:"model,omitempty"`
}

type Session struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	ProjectID *int64 `json:"project_id,omitempty"`
	Task      string `json:"task,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
}

// ModelResult is the durable model response for a coding session execution.
type ModelResult struct {
	SessionID   int64  `json:"session_id"`
	ExecutionID int64  `json:"execution_id"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	Content     string `json:"content"`
	CreatedAt   string `json:"created_at"`
}

// TaskChange is a single proposed or applied file change, without content.
type TaskChange struct {
	Op   string `json:"op"`
	Path string `json:"path"`
}

// TaskRun is the durable record of a controlled coding task, exposed over IPC.
type TaskRun struct {
	ID            int64  `json:"id"`
	SessionID     int64  `json:"session_id"`
	ExecutionID   int64  `json:"execution_id"`
	ProjectID     int64  `json:"project_id"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	BaseCommit    string `json:"base_commit,omitempty"`
	Phase         string `json:"phase"`
	Applied       bool   `json:"applied"`
	Summary       string `json:"summary,omitempty"`
	ProposalHash  string `json:"proposal_hash,omitempty"`
	WorkspaceDiff string `json:"workspace_diff,omitempty"`
	Error         string `json:"error,omitempty"`
	// ValidationStatus is one of skipped, passed, failed, error, or empty when
	// the post-apply validation stage has not run.
	ValidationStatus string `json:"validation_status,omitempty"`
	// Validation is the persisted detail of the validation stage (commands,
	// exit codes, bounded output). It is present when ValidationStatus is set.
	Validation json.RawMessage `json:"validation,omitempty"`
	Changes    []TaskChange    `json:"changes,omitempty"`
	CleanedAt  *string         `json:"cleaned_at,omitempty"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
}

// TaskOperation is one recorded inspect step of a task run.
type TaskOperation struct {
	Sequence  int    `json:"sequence"`
	Kind      string `json:"kind"`
	Path      string `json:"path,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at"`
}

type Execution struct {
	ID           int64   `json:"id"`
	SessionID    int64   `json:"session_id"`
	Status       string  `json:"status"`
	CurrentStep  int     `json:"current_step"`
	TotalSteps   int     `json:"total_steps"`
	CanceledAt   *string `json:"canceled_at,omitempty"`
	CancelReason *string `json:"cancel_reason,omitempty"`
	// QueueSeq is the durable FIFO sequence assigned on enqueue. It is retained
	// after dispatch.
	QueueSeq *int64 `json:"queue_seq,omitempty"`
	// QueuePosition is the 1-based position among currently QUEUED executions;
	// it is 0 when the execution is not queued.
	QueuePosition int    `json:"queue_position,omitempty"`
	Kind          string `json:"kind,omitempty"`
}

// Interaction is a durable worker-to-human question. Answer is populated only
// once the interaction has been answered.
type Interaction struct {
	ID          int64          `json:"id"`
	SessionID   int64          `json:"session_id"`
	ExecutionID int64          `json:"execution_id"`
	Question    string         `json:"question"`
	Context     map[string]any `json:"context,omitempty"`
	Status      string         `json:"status"`
	CreatedAt   string         `json:"created_at"`
	Answer      *string        `json:"answer,omitempty"`
	AnsweredAt  *string        `json:"answered_at,omitempty"`
}

type Response struct {
	OK             bool                   `json:"ok"`
	Message        string                 `json:"message,omitempty"`
	Version        string                 `json:"version,omitempty"`
	PID            int                    `json:"pid,omitempty"`
	Session        *Session               `json:"session,omitempty"`
	Sessions       []Session              `json:"sessions,omitempty"`
	Execution      *Execution             `json:"execution,omitempty"`
	Project        *Project               `json:"project,omitempty"`
	Projects       []Project              `json:"projects,omitempty"`
	Snapshot       *RepositorySnapshot    `json:"snapshot,omitempty"`
	Intake         *ProjectIntake         `json:"intake,omitempty"`
	Discovery      *ProjectDiscovery      `json:"discovery,omitempty"`
	Inventory      *Inventory             `json:"inventory,omitempty"`
	Inspection     *Inspection            `json:"inspection,omitempty"`
	Analysis       *ComponentAnalysis     `json:"analysis,omitempty"`
	Query          *QueryResponse         `json:"query,omitempty"`
	Investigation  *InvestigationResponse `json:"investigation,omitempty"`
	Events         []SessionEvent         `json:"events,omitempty"`
	NextEventID    int64                  `json:"next_event_id,omitempty"`
	Interaction    *Interaction           `json:"interaction,omitempty"`
	Interactions   []Interaction          `json:"interactions,omitempty"`
	Result         *ModelResult           `json:"result,omitempty"`
	TaskRun        *TaskRun               `json:"task_run,omitempty"`
	TaskOperations []TaskOperation        `json:"task_operations,omitempty"`
	// Diff holds a rendered unified diff for the task-diff command. DiffSource
	// reports whether it is a proposed preview or the applied workspace diff.
	Diff          string `json:"diff,omitempty"`
	DiffSource    string `json:"diff_source,omitempty"`
	AnswerApplied bool   `json:"answer_applied,omitempty"`
	Canceled      bool   `json:"canceled,omitempty"`
	CancelOutcome string `json:"cancel_outcome,omitempty"`
	// WorkerCapacity, ActiveWorkers, and QueuedExecutions report daemon
	// scheduling state for the status command.
	WorkerCapacity   int `json:"worker_capacity,omitempty"`
	ActiveWorkers    int `json:"active_workers,omitempty"`
	QueuedExecutions int `json:"queued_executions,omitempty"`
}

type Project struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	RootPath string `json:"root_path"`
}

type RepositorySnapshot struct {
	ID             int64  `json:"id"`
	ProjectID      int64  `json:"project_id"`
	HeadCommit     string `json:"head_commit,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Detached       bool   `json:"detached"`
	Dirty          bool   `json:"dirty"`
	TrackedFiles   int    `json:"tracked_files"`
	UntrackedFiles int    `json:"untracked_files"`
}

type ProjectIntake struct {
	RootPath      string              `json:"root_path"`
	Changes       []WorkingTreeChange `json:"changes"`
	ExpectedState string              `json:"expected_state"`
}

type WorkingTreeChange struct {
	Path         string `json:"path"`
	OriginalPath string `json:"original_path,omitempty"`
	IndexStatus  string `json:"index_status,omitempty"`
	WorkStatus   string `json:"work_status,omitempty"`
	Untracked    bool   `json:"untracked"`
}

type ProjectDiscovery struct {
	ObservationID     int64                  `json:"observation_id"`
	Project           Project                `json:"project"`
	CommitHash        string                 `json:"commit_hash"`
	TrackedFiles      int                    `json:"tracked_files"`
	UnclassifiedFiles int                    `json:"unclassified_files"`
	Languages         []LanguageCount        `json:"languages"`
	BuildSystems      []BuildSystemIndicator `json:"build_systems"`
}

type LanguageCount struct {
	Language string `json:"language"`
	Count    int    `json:"count"`
}

type EvidenceReference struct {
	Path string `json:"path"`
}

type BuildSystemIndicator struct {
	Name     string            `json:"name"`
	Evidence EvidenceReference `json:"evidence"`
}

// Inventory is a commit-pinned, evidence-backed inventory of a project's
// committed tree.
type Inventory struct {
	ID            int64            `json:"id"`
	ProjectID     int64            `json:"project_id"`
	CommitHash    string           `json:"commit_hash"`
	HeadCommit    string           `json:"head_commit,omitempty"`
	IsDirty       bool             `json:"is_dirty"`
	EngineVersion int              `json:"engine_version"`
	TrackedFiles  int              `json:"tracked_files"`
	Truncated     bool             `json:"truncated"`
	ObservedAt    string           `json:"observed_at"`
	Summary       InventorySummary `json:"summary"`
	Files         []InventoryFile  `json:"files,omitempty"`
}

type InventorySummary struct {
	TrackedFiles      int                    `json:"tracked_files"`
	UnclassifiedFiles int                    `json:"unclassified_files"`
	Languages         []InventoryLanguage    `json:"languages"`
	BuildSystems      []InventoryBuildSystem `json:"build_systems"`
	Directories       []InventoryDirectory   `json:"directories"`
	Categories        []InventoryCategory    `json:"categories"`
	Truncated         bool                   `json:"truncated"`
}

type InventoryLanguage struct {
	Language string `json:"language"`
	Count    int    `json:"count"`
}

type InventoryBuildSystem struct {
	Name     string            `json:"name"`
	Evidence EvidenceReference `json:"evidence"`
}

type InventoryDirectory struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
}

type InventoryCategory struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}

type InventoryFile struct {
	Path     string `json:"path"`
	BlobHash string `json:"blob_hash"`
	Mode     string `json:"mode"`
	Size     int64  `json:"size"`
	Language string `json:"language,omitempty"`
	Category string `json:"category"`
}

// Inspection reports a project's latest stored observations alongside their
// currency relative to the live repository HEAD. Status describes the inventory
// only; Analysis carries its own independently evaluated Status. Inspection is
// read-only and never triggers analysis. Status is one of "current", "stale",
// or "unavailable".
type Inspection struct {
	Inventory *Inventory         `json:"inventory,omitempty"`
	Analysis  *ComponentAnalysis `json:"analysis,omitempty"`
	Status    string             `json:"status"`
	Head      string             `json:"head,omitempty"`
	Dirty     bool               `json:"dirty"`
}

type ComponentAnalysis struct {
	ID                 int64                 `json:"id"`
	ProjectID          int64                 `json:"project_id"`
	CommitHash         string                `json:"commit_hash"`
	HeadCommit         string                `json:"head_commit,omitempty"`
	IsDirty            bool                  `json:"is_dirty"`
	EngineVersion      int                   `json:"engine_version"`
	ObservedAt         string                `json:"observed_at"`
	Status             string                `json:"status"`
	Head               string                `json:"head,omitempty"`
	Dirty              bool                  `json:"dirty,omitempty"`
	Summary            AnalysisSummary       `json:"summary"`
	Components         []AnalysisComponent   `json:"components,omitempty"`
	Dependencies       []AnalysisDependency  `json:"dependencies,omitempty"`
	SkippedFiles       []AnalysisSkippedFile `json:"skipped_files,omitempty"`
	ModuleRequirements []ModuleRequirement   `json:"module_requirements,omitempty"`
}

type AnalysisSummary struct {
	Complete                bool                      `json:"complete"`
	Truncated               bool                      `json:"truncated"`
	Sources                 int                       `json:"sources"`
	Analyzed                int                       `json:"analyzed"`
	Skipped                 int                       `json:"skipped"`
	Modules                 int                       `json:"modules"`
	Packages                int                       `json:"packages"`
	Commands                int                       `json:"commands"`
	BuildComponents         int                       `json:"build_components"`
	ConfigurationComponents int                       `json:"configuration_components"`
	Dependencies            []AnalysisDependencyCount `json:"dependencies,omitempty"`
}

type AnalysisDependencyCount struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

type AnalysisComponent struct {
	Kind     string           `json:"kind"`
	Identity string           `json:"identity"`
	Path     string           `json:"path"`
	Language string           `json:"language,omitempty"`
	Internal bool             `json:"internal"`
	Evidence []SourceEvidence `json:"evidence,omitempty"`
}

type AnalysisDependency struct {
	Kind         string           `json:"kind"`
	Source       string           `json:"source"`
	Target       string           `json:"target"`
	TargetModule string           `json:"target_module,omitempty"`
	Language     string           `json:"language,omitempty"`
	Evidence     []SourceEvidence `json:"evidence,omitempty"`
}

type ModuleRequirement struct {
	Path     string `json:"path"`
	Version  string `json:"version,omitempty"`
	Indirect bool   `json:"indirect,omitempty"`
}

type AnalysisSkippedFile struct {
	Path     string `json:"path"`
	BlobHash string `json:"blob_hash,omitempty"`
	Reason   string `json:"reason"`
}

type SourceEvidence struct {
	Path        string `json:"path"`
	BlobHash    string `json:"blob_hash,omitempty"`
	Declaration string `json:"declaration,omitempty"`
	Line        int    `json:"line,omitempty"`
}

// QueryResponse is the result of a read-only query over persisted
// observations. It never triggers analysis and never mutates stored state.
type QueryResponse struct {
	ProjectID    int64                `json:"project_id"`
	Query        string               `json:"query"`
	Observation  *QueryObservation    `json:"observation,omitempty"`
	Components   []AnalysisComponent  `json:"components,omitempty"`
	Dependencies []AnalysisDependency `json:"dependencies,omitempty"`
	Dependents   []QueryDependent     `json:"dependents,omitempty"`
	Evidence     []SourceEvidence     `json:"evidence,omitempty"`
	Changes      *QueryChanges        `json:"changes,omitempty"`
	Total        int                  `json:"total"`
	Empty        bool                 `json:"empty"`
	Limited      bool                 `json:"limited,omitempty"`
	Warnings     []string             `json:"warnings,omitempty"`
}

// QueryObservation identifies the persisted analysis a query ran against and
// separates freshness (current/stale/unavailable) from completeness
// (complete/partial).
type QueryObservation struct {
	AnalysisID    int64  `json:"analysis_id"`
	ProjectID     int64  `json:"project_id"`
	Commit        string `json:"commit"`
	HeadCommit    string `json:"head_commit,omitempty"`
	IsDirty       bool   `json:"is_dirty"`
	EngineVersion int    `json:"engine_version"`
	ObservedAt    string `json:"observed_at"`
	Status        string `json:"status"`
	Head          string `json:"head,omitempty"`
	Complete      bool   `json:"complete"`
	Truncated     bool   `json:"truncated"`
	Sources       int    `json:"sources"`
	Analyzed      int    `json:"analyzed"`
	Skipped       int    `json:"skipped"`
}

// QueryDependent is a direct dependent of a component.
type QueryDependent struct {
	Kind     string           `json:"kind"`
	Source   string           `json:"source"`
	Target   string           `json:"target"`
	Language string           `json:"language,omitempty"`
	Evidence []SourceEvidence `json:"evidence,omitempty"`
}

// QueryChanges is the deterministic difference between two observations.
type QueryChanges struct {
	From                QueryObservation        `json:"from"`
	To                  QueryObservation        `json:"to"`
	AddedComponents     []AnalysisComponent     `json:"added_components,omitempty"`
	RemovedComponents   []AnalysisComponent     `json:"removed_components,omitempty"`
	AddedDependencies   []AnalysisDependency    `json:"added_dependencies,omitempty"`
	RemovedDependencies []AnalysisDependency    `json:"removed_dependencies,omitempty"`
	Reclassified        []QueryReclassification `json:"reclassified_dependencies,omitempty"`
}

// QueryReclassification records a dependency whose classification changed while
// its underlying relationship stayed the same.
type QueryReclassification struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	FromKind   string `json:"from_kind"`
	ToKind     string `json:"to_kind"`
	FromModule string `json:"from_module,omitempty"`
	ToModule   string `json:"to_module,omitempty"`
}

// InvestigationResponse is the result of an investigation request. It contains
// a deterministic, evidence-backed investigation context.
type InvestigationResponse struct {
	SchemaVersion  int                  `json:"schemaVersion"`
	ProjectID      int64                `json:"projectId"`
	AnalysisID     int64                `json:"analysisId"`
	Commit         string               `json:"commit"`
	Status         string               `json:"status"`
	Complete       bool                 `json:"complete"`
	Component      AnalysisComponent    `json:"component"`
	Dependencies   []AnalysisDependency `json:"dependencies"`
	Dependents     []QueryDependent     `json:"dependents"`
	Evidence       []SourceEvidence     `json:"evidence"`
	SuggestedFiles []InvestigationFile  `json:"suggestedFiles"`
	Warnings       []string             `json:"warnings"`
	Truncated      bool                 `json:"truncated,omitempty"`
	TruncatedBy    string               `json:"truncatedBy,omitempty"`
	TotalEvidence  int                  `json:"totalEvidence,omitempty"`
	TotalFiles     int                  `json:"totalFiles,omitempty"`
}

type SessionEvent struct {
	ID          int64          `json:"id"`
	SessionID   int64          `json:"session_id"`
	ExecutionID *int64         `json:"execution_id,omitempty"`
	Type        string         `json:"type"`
	Timestamp   string         `json:"timestamp"`
	Details     map[string]any `json:"details,omitempty"`
}

// InvestigationFile describes a suggested file with reasons for selection.
type InvestigationFile struct {
	Path    string   `json:"path"`
	Reasons []string `json:"reasons"`
}
