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

// errorResponse is the single error envelope used by every endpoint.
type errorResponse struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
