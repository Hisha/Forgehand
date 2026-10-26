// Package remoteapi implements the versioned, authenticated network API of
// the Forgehand daemon.
//
// The API is a separate transport from the local Unix socket. It exposes a
// small, explicitly defined set of operations and never exposes the internal
// IPC command set. Every operation that touches project or session data requires
// a session that was established with a password and a one-time password, and
// every project is checked against the account's explicit authorization
// before any data is returned.
package remoteapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Hisha/Forgehand/internal/identity"
	"github.com/Hisha/Forgehand/internal/state"
)

// Options configures a Server.
type Options struct {
	// Service performs authentication and authorization.
	Service *identity.Service
	// Version is the Forgehand release version reported by the health
	// endpoint.
	Version string
	// BuildIdentity is the Forgehand build identity reported by the health
	// endpoint.
	BuildIdentity string
	// Sessions invokes the daemon's existing persistence and scheduling path.
	// It may be nil in read-only embeddings; write routes then fail closed.
	Sessions SessionOperations
	// DaemonStatus reads operational state owned by the daemon. It may be nil
	// in embeddings that do not expose the operational dashboard.
	DaemonStatus DaemonStatusOperations
}

// Server serves the remote API.
type Server struct {
	service       *identity.Service
	version       string
	buildIdentity string
	sessions      SessionOperations
	daemonStatus  DaemonStatusOperations
	handler       http.Handler
}

// NewServer builds a Server with its routes and middleware installed.
func NewServer(options Options) (*Server, error) {
	if options.Service == nil {
		return nil, errors.New("remote API requires an identity service")
	}

	server := &Server{
		service:       options.Service,
		version:       options.Version,
		buildIdentity: options.BuildIdentity,
		sessions:      options.Sessions,
		daemonStatus:  options.DaemonStatus,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+APIBasePath+"/health", server.handleHealth)
	mux.HandleFunc("GET "+APIBasePath+"/daemon/status", server.handleDaemonStatus)
	mux.HandleFunc("POST "+APIBasePath+"/auth/login", server.handleLogin)
	mux.HandleFunc("POST "+APIBasePath+"/auth/logout", server.handleLogout)
	mux.HandleFunc("POST "+APIBasePath+"/auth/enroll/confirm", server.handleEnrollConfirm)
	mux.HandleFunc("GET "+APIBasePath+"/auth/me", server.handleMe)
	mux.HandleFunc("GET "+APIBasePath+"/projects", server.handleProjects)
	mux.HandleFunc("GET "+APIBasePath+"/projects/{projectID}/sessions", server.handleProjectSessions)
	mux.HandleFunc("POST "+APIBasePath+"/projects/{projectID}/sessions", server.handleCreateProjectSession)
	mux.HandleFunc("GET "+APIBasePath+"/sessions/{sessionID}", server.handleSession)
	mux.HandleFunc("GET "+APIBasePath+"/sessions/{sessionID}/executions", server.handleSessionExecutions)
	mux.HandleFunc("GET "+APIBasePath+"/sessions/{sessionID}/executions/{executionID}", server.handleSessionExecution)
	mux.HandleFunc("POST "+APIBasePath+"/sessions/{sessionID}/run", server.handleRunSession)
	mux.HandleFunc("/", server.handleNotFound)

	server.handler = server.securityHeaders(server.enforceCompatibility(mux))
	return server, nil
}

// handleDaemonStatus exposes read-only operational state to administrators.
func (s *Server) handleDaemonStatus(w http.ResponseWriter, r *http.Request) {
	if !requireEmptyBody(w, r) {
		return
	}
	principal, ok := s.principal(w, r)
	if !ok {
		return
	}
	if principal.Account.Role != state.AccountRoleAdministrator {
		writeError(w, http.StatusForbidden, "forbidden", "administrator access is required")
		return
	}
	if s.daemonStatus == nil {
		writeError(w, http.StatusServiceUnavailable, "status_unavailable", "daemon status is unavailable")
		return
	}

	status, err := s.daemonStatus.DaemonStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "status_unavailable", "daemon status is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// enforceCompatibility rejects state-changing requests whose client does not
// identify a supported HTTP API contract. Safe reads, including health, remain
// available so an incompatible client can diagnose and recover.
func (s *Server) enforceCompatibility(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(CompatibilityHeader, APIVersion)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			clientVersion := strings.TrimSpace(r.Header.Get(CompatibilityHeader))
			if clientVersion == "" || clientVersion != APIVersion {
				writeError(w, http.StatusUpgradeRequired, "incompatible_api", "a compatible Forgehand HTTP API client is required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Handler returns the HTTP handler of the API.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// securityHeaders applies the response headers required for a credential
// bearing, non-browser API.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Cache-Control", "no-store")
		header.Set("Content-Security-Policy", "default-src 'none'")
		// The API is consumed by native clients. No cross-origin policy is
		// advertised, so a browser cannot read a response.
		header.Set("Vary", "Origin")

		if r.Header.Get("Origin") != "" {
			// A browser supplied origin indicates a cross-site request. The
			// remote API has no browser client, so such a request is refused
			// rather than answered.
			writeError(w, http.StatusForbidden, "origin_not_allowed", "cross-origin requests are not accepted")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// handleNotFound answers requests for unknown paths.
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "the requested resource does not exist")
}

// handleHealth reports daemon liveness and version.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:        "ok",
		Version:       s.version,
		BuildIdentity: s.buildIdentity,
		APIVersion:    APIVersion,
		Time:          time.Now().UTC().Format(time.RFC3339),
	})
}

// handleLogin authenticates an interactive user with a password and a second
// factor and issues a session.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if !decodeJSON(w, r, &request) {
		return
	}

	if request.Username == "" || request.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "username and password are required")
		return
	}
	if strings.TrimSpace(request.TOTPCode) == "" && strings.TrimSpace(request.RecoveryCode) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "a one-time password or a recovery code is required")
		return
	}
	if strings.TrimSpace(request.TOTPCode) != "" && strings.TrimSpace(request.RecoveryCode) != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "present only one second factor")
		return
	}

	result, err := s.service.Authenticate(
		r.Context(),
		request.Username,
		request.Password,
		request.TOTPCode,
		request.RecoveryCode,
		remoteAddress(r),
	)
	if err != nil {
		writeIdentityError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, sessionResponse{
		Token:         result.Token,
		ExpiresAt:     result.ExpiresAt.UTC().Format(time.RFC3339),
		Account:       toAccountResponse(result.Account),
		RecoveryCodes: result.RecoveryCodes,
	})
}

// handleEnrollConfirm completes one-time password enrollment for a pending
// account. The password is required in addition to the one-time code, so no
// password-only route exists.
func (s *Server) handleEnrollConfirm(w http.ResponseWriter, r *http.Request) {
	var request enrollRequest
	if !decodeJSON(w, r, &request) {
		return
	}

	if request.Username == "" || request.Password == "" || strings.TrimSpace(request.TOTPCode) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "username, password, and a one-time password are required")
		return
	}

	result, err := s.service.EnrollRemote(
		r.Context(),
		request.Username,
		request.Password,
		request.TOTPCode,
		remoteAddress(r),
	)
	if err != nil {
		writeIdentityError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, sessionResponse{
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt.UTC().Format(time.RFC3339),
		Account:   toAccountResponse(result.Account),
	})
}

// handleLogout revokes the session that presented the bearer token.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "a valid session is required")
		return
	}

	if err := s.service.RevokeSession(r.Context(), token, remoteAddress(r)); err != nil {
		writeIdentityError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
}

// handleMe reports the authenticated identity.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.principal(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toAccountResponse(identity.View(principal.Account)))
}

// handleProjects lists the projects the authenticated account may access.
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.principal(w, r)
	if !ok {
		return
	}

	projects, err := s.service.AuthorizedProjects(r.Context(), principal)
	if err != nil {
		writeIdentityError(w, err)
		return
	}

	response := make([]projectResponse, 0, len(projects))
	for _, project := range projects {
		response = append(response, projectResponse{
			ID:       project.ID,
			Name:     project.Name,
			RootPath: project.RootPath,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

// handleProjectSessions lists the sessions of an authorized project.
func (s *Server) handleProjectSessions(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.principal(w, r)
	if !ok {
		return
	}

	projectID, ok := parseIdentifier(w, r.PathValue("projectID"))
	if !ok {
		return
	}

	sessions, err := s.service.ListProjectSessions(r.Context(), principal, projectID)
	if err != nil {
		writeIdentityError(w, err)
		return
	}

	response := make([]sessionSummaryResponse, 0, len(sessions))
	for _, session := range sessions {
		response = append(response, toSessionResponse(session))
	}
	writeJSON(w, http.StatusOK, response)
}

// handleCreateProjectSession creates a READY coding session through the
// daemon-owned session engine. Creation never queues or starts work.
func (s *Server) handleCreateProjectSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.principal(w, r)
	if !ok {
		return
	}

	projectID, ok := parseIdentifier(w, r.PathValue("projectID"))
	if !ok {
		return
	}

	var request createSessionRequest
	if !decodeJSON(w, r, &request) {
		s.auditSessionOperation(r, principal, state.AuthEventSessionCreated, "invalid", fmt.Sprintf("project %d", projectID))
		return
	}
	request.Title = strings.TrimSpace(request.Title)
	request.Task = strings.TrimSpace(request.Task)
	if request.Title == "" || request.Task == "" {
		s.auditSessionOperation(r, principal, state.AuthEventSessionCreated, "invalid", fmt.Sprintf("project %d", projectID))
		writeError(w, http.StatusBadRequest, "invalid_request", "title and task are required")
		return
	}
	if len(request.Title) > MaxSessionTitleLength || len(request.Task) > MaxSessionTaskLength {
		s.auditSessionOperation(r, principal, state.AuthEventSessionCreated, "invalid", fmt.Sprintf("project %d", projectID))
		writeError(w, http.StatusBadRequest, "invalid_request", "title or task exceeds the supported length")
		return
	}

	authorized, err := s.service.CanAccessProject(r.Context(), principal, projectID)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	if !authorized {
		s.auditSessionOperation(r, principal, state.AuthEventSessionCreated, "forbidden", fmt.Sprintf("project %d", projectID))
		writeIdentityError(w, identity.ErrForbidden)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "execution_unavailable", "session creation is unavailable")
		return
	}

	session, err := s.sessions.CreateCodingSession(r.Context(), projectID, request.Title, request.Task)
	if err != nil {
		s.auditSessionOperation(r, principal, state.AuthEventSessionCreated, "failed", fmt.Sprintf("project %d", projectID))
		writeOperationError(w, err)
		return
	}
	s.auditSessionOperation(r, principal, state.AuthEventSessionCreated, "created", fmt.Sprintf("project %d session %d", projectID, session.ID))
	writeJSON(w, http.StatusCreated, toSessionResponse(session))
}

// handleSession returns the persisted details of one authorized session.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.principal(w, r)
	if !ok {
		return
	}

	sessionID, ok := parseIdentifier(w, r.PathValue("sessionID"))
	if !ok {
		return
	}

	session, err := s.service.GetSession(r.Context(), principal, sessionID)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSessionResponse(session))
}

// handleSessionExecutions lists persisted attempts for one authorized session.
func (s *Server) handleSessionExecutions(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.principal(w, r)
	if !ok {
		return
	}
	sessionID, ok := parseIdentifier(w, r.PathValue("sessionID"))
	if !ok {
		return
	}
	if _, err := s.service.GetSession(r.Context(), principal, sessionID); err != nil {
		writeIdentityError(w, err)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "execution_unavailable", "execution history is unavailable")
		return
	}
	records, truncated, err := s.sessions.ListSessionExecutionRecords(r.Context(), sessionID, MaxExecutionHistory)
	if err != nil {
		writeOperationError(w, err)
		return
	}
	items := make([]executionDetailResponse, 0, len(records))
	for _, record := range records {
		items = append(items, toExecutionDetailResponse(record))
	}
	writeJSON(w, http.StatusOK, executionHistoryResponse{Executions: items, Truncated: truncated})
}

// handleSessionExecution returns metadata and a bounded page of result text.
func (s *Server) handleSessionExecution(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.principal(w, r)
	if !ok {
		return
	}
	sessionID, ok := parseIdentifier(w, r.PathValue("sessionID"))
	if !ok {
		return
	}
	executionID, ok := parseIdentifier(w, r.PathValue("executionID"))
	if !ok {
		return
	}
	if _, err := s.service.GetSession(r.Context(), principal, sessionID); err != nil {
		writeIdentityError(w, err)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "execution_unavailable", "execution result is unavailable")
		return
	}
	offset, ok := parseNonNegativeQuery(w, r, "offset", 0)
	if !ok {
		return
	}
	limit, ok := parsePositiveQuery(w, r, "limit", DefaultResultContentCharacters, MaxResultContentCharacters)
	if !ok {
		return
	}
	record, err := s.sessions.GetSessionExecutionRecord(r.Context(), sessionID, executionID, offset, limit)
	if errors.Is(err, state.ErrSessionExecutionNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "the requested resource does not exist")
		return
	}
	if err != nil {
		writeOperationError(w, err)
		return
	}
	response := executionRecordResponse{Execution: toExecutionDetailResponse(record)}
	if record.Result != nil {
		if offset > record.ResultTotalCharacters {
			writeError(w, http.StatusBadRequest, "invalid_request", "result offset exceeds the content length")
			return
		}
		contentCharacters := len([]rune(record.Result.Content))
		end := offset + contentCharacters
		result := toModelResultResponse(*record.Result)
		result.Content = record.Result.Content
		result.ContentOffset = offset
		result.TotalCharacters = record.ResultTotalCharacters
		result.Complete = end == record.ResultTotalCharacters
		if !result.Complete {
			result.NextOffset = end
		}
		response.Result = &result
	}
	writeJSON(w, http.StatusOK, response)
}

// handleRunSession atomically queues an eligible persisted session and wakes
// the existing daemon dispatcher. It never waits for worker completion.
func (s *Server) handleRunSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.principal(w, r)
	if !ok {
		return
	}

	sessionID, ok := parseIdentifier(w, r.PathValue("sessionID"))
	if !ok {
		return
	}
	if !requireEmptyBody(w, r) {
		return
	}

	session, err := s.service.GetSession(r.Context(), principal, sessionID)
	if err != nil {
		s.auditSessionOperation(r, principal, state.AuthEventSessionRunSubmitted, "denied", fmt.Sprintf("session %d", sessionID))
		writeIdentityError(w, err)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "execution_unavailable", "session execution is unavailable")
		return
	}

	execution, err := s.sessions.QueueSessionExecution(r.Context(), sessionID)
	if err != nil {
		s.auditSessionOperation(r, principal, state.AuthEventSessionRunSubmitted, "failed", fmt.Sprintf("project %d session %d", *session.ProjectID, sessionID))
		writeOperationError(w, err)
		return
	}

	updated, err := s.service.GetSession(r.Context(), principal, sessionID)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	s.auditSessionOperation(r, principal, state.AuthEventSessionRunSubmitted, "queued", fmt.Sprintf("project %d session %d execution %d", *session.ProjectID, sessionID, execution.ID))
	writeJSON(w, http.StatusAccepted, runSessionResponse{
		Session: toSessionResponse(updated),
		Execution: executionResponse{
			ID:            execution.ID,
			SessionID:     execution.SessionID,
			Status:        execution.Status,
			QueuePosition: execution.QueuePosition,
		},
	})
}

func requireEmptyBody(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "the request body is too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "the request body could not be read")
		return false
	}
	if strings.TrimSpace(string(payload)) != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "this operation does not accept request options")
		return false
	}
	return true
}

func (s *Server) auditSessionOperation(
	r *http.Request,
	principal identity.Principal,
	eventType, outcome, detail string,
) {
	_ = s.service.RecordSessionOperation(r.Context(), principal, eventType, outcome, detail, remoteAddress(r))
}

func writeOperationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrSessionConflict):
		writeError(w, http.StatusConflict, "session_conflict", "the session is not eligible for execution")
	case errors.Is(err, ErrExecutionUnavailable):
		writeError(w, http.StatusServiceUnavailable, "execution_unavailable", "the coding worker is unavailable")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "the daemon could not complete the request")
	}
}

// principal resolves the bearer token of a request into an authenticated
// identity. It writes the failure response itself when the token is missing
// or invalid.
func (s *Server) principal(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
	token, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "a valid session is required")
		return identity.Principal{}, false
	}

	principal, err := s.service.AuthenticateToken(r.Context(), token)
	if err != nil {
		writeIdentityError(w, err)
		return identity.Principal{}, false
	}
	return principal, true
}

// bearerToken extracts the session credential from the Authorization header.
// The credential is never accepted from a query parameter or from any other
// header.
func bearerToken(r *http.Request) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return "", false
	}
	scheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}

// remoteAddress returns the network address of the caller.
//
// Only the transport level address is used. Forwarding and identity headers
// such as X-Forwarded-For, X-Real-IP, and Forwarded are deliberately ignored
// because the daemon cannot verify who set them.
func remoteAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// parseIdentifier parses a positive integer identifier from a URL path
// segment.
func parseIdentifier(w http.ResponseWriter, raw string) (int64, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > MaxIdentifierLength {
		writeError(w, http.StatusBadRequest, "invalid_request", "the identifier is not valid")
		return 0, false
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || value <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "the identifier is not valid")
		return 0, false
	}
	return value, true
}

func parseNonNegativeQuery(w http.ResponseWriter, r *http.Request, name string, fallback int) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", name+" is not valid")
		return 0, false
	}
	return value, true
}

func parsePositiveQuery(w http.ResponseWriter, r *http.Request, name string, fallback, maximum int) (int, bool) {
	value, ok := parseNonNegativeQuery(w, r, name, fallback)
	if !ok {
		return 0, false
	}
	if value <= 0 || value > maximum {
		writeError(w, http.StatusBadRequest, "invalid_request", name+" is not valid")
		return 0, false
	}
	return value, true
}

// decodeJSON reads a bounded, strict JSON request body.
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "the request body must be application/json")
		return false
	}
	mediaType := strings.TrimSpace(strings.Split(contentType, ";")[0])
	if !strings.EqualFold(mediaType, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "the request body must be application/json")
		return false
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(target); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "the request body is too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
		return false
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "the request body must contain a single JSON document")
		return false
	}
	return true
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(true)
	_ = encoder.Encode(payload)
}

// writeError writes an error response.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{
		Error: errorDetail{
			Code:    code,
			Message: message,
		},
	})
}

// writeIdentityError maps an identity failure onto an HTTP status. Internal
// failures are reported as a generic server error so that no internal detail
// reaches the caller.
func writeIdentityError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "the credentials were not accepted")
	case errors.Is(err, identity.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "unauthorized", "a valid session is required")
	case errors.Is(err, identity.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "the account is not authorized for this resource")
	case errors.Is(err, identity.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "the requested resource does not exist")
	case errors.Is(err, identity.ErrAccountLocked):
		writeError(w, http.StatusTooManyRequests, "account_locked", "the account is temporarily locked")
	case errors.Is(err, identity.ErrThrottled):
		writeError(w, http.StatusTooManyRequests, "throttled", "too many authentication attempts")
	case errors.Is(err, identity.ErrEnrollmentRequired):
		writeError(w, http.StatusForbidden, "enrollment_required", "one-time password enrollment is not complete")
	case errors.Is(err, identity.ErrAccountDisabled):
		writeError(w, http.StatusForbidden, "account_disabled", "the account is disabled")
	case errors.Is(err, identity.ErrAccountNotFound):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "the credentials were not accepted")
	case errors.Is(err, identity.ErrAccountExists):
		writeError(w, http.StatusConflict, "already_exists", "the resource already exists")
	case errors.Is(err, identity.ErrValidation):
		writeError(w, http.StatusBadRequest, "invalid_request", "the request is not valid")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "the request could not be completed")
	}
}

// toAccountResponse projects an account view onto the wire format.
func toAccountResponse(view identity.AccountView) accountResponse {
	return accountResponse{
		ID:           view.ID,
		Username:     view.Username,
		DisplayName:  view.DisplayName,
		Role:         view.Role,
		Status:       view.Status,
		TOTPEnrolled: view.TOTPEnrolled,
	}
}

// toSessionResponse projects a persisted session onto the wire format.
func toSessionResponse(session state.Session) sessionSummaryResponse {
	response := sessionSummaryResponse{
		ID:        session.ID,
		Title:     session.Title,
		State:     session.State,
		Task:      session.Task,
		Kind:      session.Kind,
		Provider:  session.Provider,
		Model:     session.Model,
		CreatedAt: session.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: session.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if session.ProjectID != nil {
		response.ProjectID = *session.ProjectID
	}
	return response
}

func toExecutionDetailResponse(record state.SessionExecutionRecord) executionDetailResponse {
	execution := record.Execution
	response := executionDetailResponse{
		ID: execution.ID, SessionID: execution.SessionID, Status: execution.Status,
		CurrentStep: execution.CurrentStep, TotalSteps: execution.TotalSteps,
		StartedAt:   execution.StartedAt.UTC().Format(time.RFC3339Nano),
		FailureKind: execution.FailureKind, FailureReason: execution.FailureReason,
		Kind: execution.Kind, ResultAvailable: record.Result != nil,
	}
	if execution.CompletedAt != nil {
		value := execution.CompletedAt.UTC().Format(time.RFC3339Nano)
		response.CompletedAt = &value
	}
	if execution.CanceledAt != nil {
		value := execution.CanceledAt.UTC().Format(time.RFC3339Nano)
		response.CanceledAt = &value
	}
	response.CancelReason = execution.CancelReason
	if record.Result != nil {
		response.Provider = record.Result.Provider
		response.Model = record.Result.Model
		response.ResultCreatedAt = record.Result.CreatedAt.UTC().Format(time.RFC3339Nano)
		response.PromptTokens = record.Result.PromptTokens
		response.CompletionTokens = record.Result.CompletionTokens
		response.TotalTokens = record.Result.TotalTokens
		response.LatencyMS = record.Result.LatencyMS
	}
	return response
}

func toModelResultResponse(result state.ModelResult) modelResultResponse {
	return modelResultResponse{
		ID: result.ID, ExecutionID: result.ExecutionID, Provider: result.Provider,
		Model: result.Model, CreatedAt: result.CreatedAt.UTC().Format(time.RFC3339Nano),
		PromptTokens: result.PromptTokens, CompletionTokens: result.CompletionTokens,
		TotalTokens: result.TotalTokens, LatencyMS: result.LatencyMS,
	}
}
