package ipc

type Request struct {
	Command       string `json:"command"`
	Title         string `json:"title,omitempty"`
	SessionID     int64  `json:"session_id,omitempty"`
	Path          string `json:"path,omitempty"`
	ProjectID     int64  `json:"project_id,omitempty"`
	Action        string `json:"action,omitempty"`
	ExpectedState string `json:"expected_state,omitempty"`
	IncludeFiles  bool   `json:"include_files,omitempty"`
}

type Session struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

type Execution struct {
	ID          int64  `json:"id"`
	SessionID   int64  `json:"session_id"`
	Status      string `json:"status"`
	CurrentStep int    `json:"current_step"`
	TotalSteps  int    `json:"total_steps"`
}

type Response struct {
	OK         bool                `json:"ok"`
	Message    string              `json:"message,omitempty"`
	Version    string              `json:"version,omitempty"`
	PID        int                 `json:"pid,omitempty"`
	Session    *Session            `json:"session,omitempty"`
	Sessions   []Session           `json:"sessions,omitempty"`
	Execution  *Execution          `json:"execution,omitempty"`
	Project    *Project            `json:"project,omitempty"`
	Projects   []Project           `json:"projects,omitempty"`
	Snapshot   *RepositorySnapshot `json:"snapshot,omitempty"`
	Intake     *ProjectIntake      `json:"intake,omitempty"`
	Discovery  *ProjectDiscovery   `json:"discovery,omitempty"`
	Inventory  *Inventory          `json:"inventory,omitempty"`
	Inspection *Inspection         `json:"inspection,omitempty"`
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

// Inspection reports a stored inventory alongside its currency relative to the
// live repository HEAD. Status is one of "current", "stale", or "unavailable".
type Inspection struct {
	Inventory *Inventory `json:"inventory,omitempty"`
	Status    string     `json:"status"`
	Head      string     `json:"head,omitempty"`
	Dirty     bool       `json:"dirty"`
}
