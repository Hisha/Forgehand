package remoteapi

// healthResponse reports the daemon's liveness and version. It deliberately
// exposes no account, project, or session information and therefore needs no
// authentication.
type healthResponse struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	BuildIdentity string `json:"build_identity"`
	APIVersion    string `json:"api_version"`
	Time          string `json:"time"`
}

// loginRequest carries the credentials for an interactive login. Exactly one
// second factor must be supplied: totp_code or recovery_code.
type loginRequest struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	TOTPCode     string `json:"totp_code"`
	RecoveryCode string `json:"recovery_code"`
}

// enrollRequest carries the credentials that complete one-time password
// enrollment for a pending account.
type enrollRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTPCode string `json:"totp_code"`
}

// accountResponse is the non-secret projection of an account.
type accountResponse struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name,omitempty"`
	Role         string `json:"role"`
	Status       string `json:"status"`
	TOTPEnrolled bool   `json:"totp_enrolled"`
}

// sessionResponse carries an issued session credential.
type sessionResponse struct {
	Token         string          `json:"token"`
	ExpiresAt     string          `json:"expires_at"`
	Account       accountResponse `json:"account"`
	RecoveryCodes []string        `json:"recovery_codes,omitempty"`
}

// projectResponse describes a project the account is authorized to access.
type projectResponse struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	RootPath string `json:"root_path"`
}

// sessionSummaryResponse describes a persisted session.
type sessionSummaryResponse struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	ProjectID int64  `json:"project_id"`
	Task      string `json:"task,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type createSessionRequest struct {
	Title string `json:"title"`
	Task  string `json:"task"`
}

type executionResponse struct {
	ID            int64  `json:"id"`
	SessionID     int64  `json:"session_id"`
	Status        string `json:"status"`
	QueuePosition int    `json:"queue_position,omitempty"`
}

type runSessionResponse struct {
	Session   sessionSummaryResponse `json:"session"`
	Execution executionResponse      `json:"execution"`
}

type executionDetailResponse struct {
	ID               int64   `json:"id"`
	SessionID        int64   `json:"session_id"`
	Status           string  `json:"status"`
	CurrentStep      int     `json:"current_step"`
	TotalSteps       int     `json:"total_steps"`
	StartedAt        string  `json:"started_at"`
	CompletedAt      *string `json:"completed_at,omitempty"`
	CanceledAt       *string `json:"canceled_at,omitempty"`
	CancelReason     *string `json:"cancel_reason,omitempty"`
	FailureKind      string  `json:"failure_kind,omitempty"`
	FailureReason    string  `json:"failure_reason,omitempty"`
	Kind             string  `json:"kind,omitempty"`
	ResultAvailable  bool    `json:"result_available"`
	Provider         string  `json:"provider,omitempty"`
	Model            string  `json:"model,omitempty"`
	ResultCreatedAt  string  `json:"result_created_at,omitempty"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	TotalTokens      int     `json:"total_tokens,omitempty"`
	LatencyMS        int64   `json:"latency_ms,omitempty"`
}

type executionHistoryResponse struct {
	Executions []executionDetailResponse `json:"executions"`
	Truncated  bool                      `json:"truncated"`
}

type modelResultResponse struct {
	ID               int64  `json:"id"`
	ExecutionID      int64  `json:"execution_id"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Content          string `json:"content"`
	CreatedAt        string `json:"created_at"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	LatencyMS        int64  `json:"latency_ms"`
	ContentOffset    int    `json:"content_offset"`
	NextOffset       int    `json:"next_offset,omitempty"`
	TotalCharacters  int    `json:"total_characters"`
	Complete         bool   `json:"complete"`
}

type executionRecordResponse struct {
	Execution executionDetailResponse `json:"execution"`
	Result    *modelResultResponse    `json:"result,omitempty"`
}

// errorResponse is the single error envelope used by every endpoint.
type errorResponse struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
