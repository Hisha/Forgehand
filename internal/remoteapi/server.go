// Package remoteapi implements the versioned, authenticated network API of
// the Forgehand daemon.
//
// The API is a separate transport from the local Unix socket. It exposes a
// small, read-only set of operations and never exposes the internal IPC
// command set. Every operation that touches project or session data requires
// a session that was established with a password and a one-time password, and
// every project is checked against the account's explicit authorization
// before any data is returned.
package remoteapi

import (
	"encoding/json"
	"errors"
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
}

// Server serves the remote API.
type Server struct {
	service       *identity.Service
	version       string
	buildIdentity string
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
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+APIBasePath+"/health", server.handleHealth)
	mux.HandleFunc("POST "+APIBasePath+"/auth/login", server.handleLogin)
	mux.HandleFunc("POST "+APIBasePath+"/auth/logout", server.handleLogout)
	mux.HandleFunc("POST "+APIBasePath+"/auth/enroll/confirm", server.handleEnrollConfirm)
	mux.HandleFunc("GET "+APIBasePath+"/auth/me", server.handleMe)
	mux.HandleFunc("GET "+APIBasePath+"/projects", server.handleProjects)
	mux.HandleFunc("GET "+APIBasePath+"/projects/{projectID}/sessions", server.handleProjectSessions)
	mux.HandleFunc("GET "+APIBasePath+"/sessions/{sessionID}", server.handleSession)
	mux.HandleFunc("/", server.handleNotFound)

	server.handler = server.securityHeaders(mux)
	return server, nil
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
