package remoteapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/auth"
	"github.com/Hisha/Forgehand/internal/identity"
	"github.com/Hisha/Forgehand/internal/state"
)

const testPassword = "correct horse battery staple"

type harness struct {
	server  *httptest.Server
	client  *http.Client
	db      *state.Database
	service *identity.Service
	now     time.Time
	secret  []byte
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	t.Setenv("FORGEHAND_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	db, err := state.Open(context.Background())
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close state database: %v", err)
		}
	})

	master, err := auth.LoadOrCreateMasterKey(t.TempDir())
	if err != nil {
		t.Fatalf("load master key: %v", err)
	}

	now := time.Now().UTC()
	throttle := auth.NewLoginThrottle()
	throttle.SetClock(func() time.Time { return now })

	service, err := identity.NewService(identity.Options{
		DB:         db,
		MasterKey:  master,
		SessionTTL: time.Hour,
		Throttle:   throttle,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewService() returned error: %v", err)
	}

	api, err := NewServer(Options{
		Service:       service,
		Version:       "test-version",
		BuildIdentity: "test-build",
	})
	if err != nil {
		t.Fatalf("NewServer() returned error: %v", err)
	}

	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)

	return &harness{
		server:  server,
		client:  server.Client(),
		db:      db,
		service: service,
		now:     now,
	}
}

// enroll creates an active administrator and returns its one-time secret.
func (h *harness) enroll(t *testing.T, username string) []byte {
	t.Helper()

	bootstrap, err := h.service.BootstrapAdministrator(context.Background(), username, "", testPassword)
	if err != nil {
		t.Fatalf("bootstrap account: %v", err)
	}

	secret, err := auth.DecodeTOTPSecret(bootstrap.Secret)
	if err != nil {
		t.Fatalf("decode enrollment secret: %v", err)
	}

	code := h.code(secret)
	if _, err := h.service.ConfirmEnrollment(context.Background(), username, code); err != nil {
		t.Fatalf("activate account: %v", err)
	}

	h.now = h.now.Add(auth.DefaultTOTPPeriod)
	h.secret = secret
	return secret
}

func (h *harness) code(secret []byte) string {
	return auth.GenerateTOTP(secret, auth.TOTPAlgorithmSHA1, auth.DefaultTOTPDigits, auth.TOTPCounter(h.now, auth.DefaultTOTPPeriod))
}

// do performs an HTTP request against the API.
func (h *harness) do(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	request, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := h.client.Do(request)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return response.StatusCode, payload
}

// login authenticates over HTTP and returns the session token.
func (h *harness) login(t *testing.T, username, password, code string) string {
	t.Helper()

	status, payload := h.do(t, http.MethodPost, APIBasePath+"/auth/login", "", map[string]string{
		"username":  username,
		"password":  password,
		"totp_code": code,
	})
	if status != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", status, payload)
	}

	var response sessionResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if response.Token == "" {
		t.Fatal("the login response carries no token")
	}
	return response.Token
}

func TestHealthIsAvailableWithoutAuthentication(t *testing.T) {
	harness := newHarness(t)

	status, payload := harness.do(t, http.MethodGet, APIBasePath+"/health", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, payload)
	}

	var response healthResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if response.Status != "ok" {
		t.Fatalf("status = %q, want %q", response.Status, "ok")
	}
	if response.APIVersion != APIVersion {
		t.Fatalf("api version = %q, want %q", response.APIVersion, APIVersion)
	}
	if response.Version != "test-version" {
		t.Fatalf("version = %q, want %q", response.Version, "test-version")
	}
}

func TestProtectedOperationsRefuseUnauthenticatedRequests(t *testing.T) {
	harness := newHarness(t)

	paths := []string{
		APIBasePath + "/auth/me",
		APIBasePath + "/projects",
		APIBasePath + "/projects/1/sessions",
		APIBasePath + "/sessions/1",
	}

	for _, path := range paths {
		status, payload := harness.do(t, http.MethodGet, path, "", nil)
		if status != http.StatusUnauthorized {
			t.Fatalf("%s without a token returned %d, want 401 (body %s)", path, status, payload)
		}
	}
}

func TestProtectedOperationsRefuseAnInvalidToken(t *testing.T) {
	harness := newHarness(t)

	status, payload := harness.do(t, http.MethodGet, APIBasePath+"/projects", "not-a-real-token", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", status, payload)
	}
}

func TestLoginRequiresASecondFactor(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")

	status, payload := harness.do(t, http.MethodPost, APIBasePath+"/auth/login", "", map[string]string{
		"username": "alice",
		"password": testPassword,
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, payload)
	}
}

func TestLoginRefusesPasswordOnly(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")

	// A login without a one-time password is refused even when the password
	// is correct.
	status, payload := harness.do(t, http.MethodPost, APIBasePath+"/auth/login", "", map[string]string{
		"username": "alice",
		"password": testPassword,
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, payload)
	}
}

func TestLoginWithWrongOneTimePasswordIsRefused(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")

	status, payload := harness.do(t, http.MethodPost, APIBasePath+"/auth/login", "", map[string]string{
		"username":  "alice",
		"password":  testPassword,
		"totp_code": "000000",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", status, payload)
	}
}

func TestAuthenticatedIdentityIsReported(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")

	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))

	status, payload := harness.do(t, http.MethodGet, APIBasePath+"/auth/me", token, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, payload)
	}

	var response accountResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode identity response: %v", err)
	}
	if response.Username != "alice" {
		t.Fatalf("username = %q, want %q", response.Username, "alice")
	}
	if !response.TOTPEnrolled {
		t.Fatal("the identity response does not report the enrolled one-time password")
	}
}

func TestLogoutRevokesTheSession(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")

	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))

	status, payload := harness.do(t, http.MethodPost, APIBasePath+"/auth/logout", token, map[string]string{})
	if status != http.StatusOK {
		t.Fatalf("logout status = %d, body = %s", status, payload)
	}

	status, payload = harness.do(t, http.MethodGet, APIBasePath+"/auth/me", token, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", status, payload)
	}
}

func TestAuthorizedProjectsAreListed(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")

	project, err := harness.db.CreateProject(context.Background(), "visible", t.TempDir())
	if err != nil {
		t.Fatalf("CreateProject() returned error: %v", err)
	}
	if _, err := harness.db.CreateProject(context.Background(), "hidden", t.TempDir()); err != nil {
		t.Fatalf("CreateProject() returned error: %v", err)
	}

	accounts, err := harness.db.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts() returned error: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected one account, got %d", len(accounts))
	}
	if err := harness.db.GrantAccountProject(context.Background(), accounts[0].ID, project.ID); err != nil {
		t.Fatalf("GrantAccountProject() returned error: %v", err)
	}

	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))

	status, payload := harness.do(t, http.MethodGet, APIBasePath+"/projects", token, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, payload)
	}

	var projects []projectResponse
	if err := json.Unmarshal(payload, &projects); err != nil {
		t.Fatalf("decode projects response: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("returned %d projects, want 1", len(projects))
	}
	if projects[0].ID != project.ID {
		t.Fatalf("project ID = %d, want %d", projects[0].ID, project.ID)
	}
}

func TestSessionsOfAnUnauthorizedProjectAreRefused(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")

	allowed, err := harness.db.CreateProject(context.Background(), "allowed", t.TempDir())
	if err != nil {
		t.Fatalf("CreateProject() returned error: %v", err)
	}
	forbidden, err := harness.db.CreateProject(context.Background(), "forbidden", t.TempDir())
	if err != nil {
		t.Fatalf("CreateProject() returned error: %v", err)
	}

	accounts, err := harness.db.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts() returned error: %v", err)
	}
	if err := harness.db.GrantAccountProject(context.Background(), accounts[0].ID, allowed.ID); err != nil {
		t.Fatalf("GrantAccountProject() returned error: %v", err)
	}

	allowedSession, err := harness.db.CreateSessionWithDetails(context.Background(), "allowed session", &allowed.ID, nil, "fake", "", "")
	if err != nil {
		t.Fatalf("CreateSessionWithDetails() returned error: %v", err)
	}
	forbiddenSession, err := harness.db.CreateSessionWithDetails(context.Background(), "forbidden session", &forbidden.ID, nil, "fake", "", "")
	if err != nil {
		t.Fatalf("CreateSessionWithDetails() returned error: %v", err)
	}

	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))

	status, payload := harness.do(t, http.MethodGet, fmt.Sprintf("%s/projects/%d/sessions", APIBasePath, allowed.ID), token, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, payload)
	}
	var sessions []sessionSummaryResponse
	if err := json.Unmarshal(payload, &sessions); err != nil {
		t.Fatalf("decode sessions response: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != allowedSession.ID {
		t.Fatalf("sessions = %v, want only %d", sessions, allowedSession.ID)
	}

	status, payload = harness.do(t, http.MethodGet, fmt.Sprintf("%s/projects/%d/sessions", APIBasePath, forbidden.ID), token, nil)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", status, payload)
	}

	status, payload = harness.do(t, http.MethodGet, fmt.Sprintf("%s/sessions/%d", APIBasePath, forbiddenSession.ID), token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", status, payload)
	}

	status, payload = harness.do(t, http.MethodGet, fmt.Sprintf("%s/sessions/%d", APIBasePath, allowedSession.ID), token, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, payload)
	}
}

func TestOversizedRequestBodyIsRefused(t *testing.T) {
	harness := newHarness(t)

	body := `{"username":"` + strings.Repeat("a", MaxRequestBytes+1024) + `"}`
	request, err := http.NewRequest(
		http.MethodPost,
		harness.server.URL+APIBasePath+"/auth/login",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := harness.client.Do(request)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusRequestEntityTooLarge {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 413 (body %s)", response.StatusCode, payload)
	}
}

func TestMalformedRequestBodyIsRefused(t *testing.T) {
	harness := newHarness(t)

	request, err := http.NewRequest(
		http.MethodPost,
		harness.server.URL+APIBasePath+"/auth/login",
		strings.NewReader("{not json"),
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := harness.client.Do(request)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 400 (body %s)", response.StatusCode, payload)
	}
}

func TestUnknownRequestFieldsAreRefused(t *testing.T) {
	harness := newHarness(t)

	status, payload := harness.do(t, http.MethodPost, APIBasePath+"/auth/login", "", map[string]string{
		"username":    "alice",
		"password":    testPassword,
		"totp_code":   "000000",
		"is_admin":    "true",
		"extra_field": "value",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, payload)
	}
}

func TestNonJSONContentTypeIsRefused(t *testing.T) {
	harness := newHarness(t)

	request, err := http.NewRequest(
		http.MethodPost,
		harness.server.URL+APIBasePath+"/auth/login",
		strings.NewReader(`{"username":"alice"}`),
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "text/plain")

	response, err := harness.client.Do(request)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnsupportedMediaType {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 415 (body %s)", response.StatusCode, payload)
	}
}

func TestMissingContentTypeIsRefused(t *testing.T) {
	harness := newHarness(t)

	request, err := http.NewRequest(
		http.MethodPost,
		harness.server.URL+APIBasePath+"/auth/login",
		strings.NewReader(`{"username":"alice"}`),
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	response, err := harness.client.Do(request)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnsupportedMediaType {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 415 (body %s)", response.StatusCode, payload)
	}
}

func TestCrossOriginRequestsAreRefused(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")

	request, err := http.NewRequest(http.MethodGet, harness.server.URL+APIBasePath+"/projects", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Origin", "https://example.invalid")

	response, err := harness.client.Do(request)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusForbidden {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 403 (body %s)", response.StatusCode, payload)
	}
}

func TestUnknownRouteIsNotFound(t *testing.T) {
	harness := newHarness(t)

	status, payload := harness.do(t, http.MethodGet, "/api/v1/does-not-exist", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", status, payload)
	}
}

func TestPendingAccountCannotObtainASession(t *testing.T) {
	harness := newHarness(t)

	bootstrap, err := harness.service.BootstrapAdministrator(context.Background(), "alice", "", testPassword)
	if err != nil {
		t.Fatalf("BootstrapAdministrator() returned error: %v", err)
	}
	secret, err := auth.DecodeTOTPSecret(bootstrap.Secret)
	if err != nil {
		t.Fatalf("decode enrollment secret: %v", err)
	}

	// The account has a valid password and a valid one-time password, but
	// enrollment has not been confirmed. It must not receive a session.
	status, payload := harness.do(t, http.MethodPost, APIBasePath+"/auth/login", "", map[string]string{
		"username":  "alice",
		"password":  testPassword,
		"totp_code": harness.code(secret),
	})
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", status, payload)
	}
	if strings.Contains(string(payload), "\"token\"") {
		t.Fatalf("the refused login returned a session token: %s", payload)
	}
}

func TestEnrollmentConfirmationRequiresBothFactors(t *testing.T) {
	harness := newHarness(t)

	bootstrap, err := harness.service.BootstrapAdministrator(context.Background(), "alice", "", testPassword)
	if err != nil {
		t.Fatalf("BootstrapAdministrator() returned error: %v", err)
	}
	secret, err := auth.DecodeTOTPSecret(bootstrap.Secret)
	if err != nil {
		t.Fatalf("decode enrollment secret: %v", err)
	}

	// The password alone does not confirm enrollment.
	status, payload := harness.do(t, http.MethodPost, APIBasePath+"/auth/enroll/confirm", "", map[string]string{
		"username": "alice",
		"password": testPassword,
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, payload)
	}

	// A wrong password with a correct one-time password is refused.
	status, payload = harness.do(t, http.MethodPost, APIBasePath+"/auth/enroll/confirm", "", map[string]string{
		"username":  "alice",
		"password":  "wrong password value",
		"totp_code": harness.code(secret),
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", status, payload)
	}

	status, payload = harness.do(t, http.MethodPost, APIBasePath+"/auth/enroll/confirm", "", map[string]string{
		"username":  "alice",
		"password":  testPassword,
		"totp_code": harness.code(secret),
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, payload)
	}

	var response sessionResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode enrollment response: %v", err)
	}
	if response.Token == "" {
		t.Fatal("the enrollment response carries no session token")
	}
	if !response.Account.TOTPEnrolled {
		t.Fatal("the enrollment response does not report the enrolled one-time password")
	}
}

func TestInternalIpcCommandsAreNotReachable(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")

	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))

	// The internal IPC command set is not exposed over the network API.
	// These are the commands that start work, approve changes, or modify
	// repositories on the local socket.
	commands := []string{
		"session-run",
		"session-cancel",
		"task-start",
		"task-approve",
		"task-reject",
		"project-add",
		"interaction-answer",
	}

	for _, command := range commands {
		path := APIBasePath + "/" + command
		status, payload := harness.do(t, http.MethodPost, path, token, map[string]any{
			"command":    command,
			"session_id": 1,
		})
		if status != http.StatusNotFound {
			t.Fatalf("%s returned %d, want 404 (body %s)", path, status, payload)
		}
	}
}

func TestWrongMethodIsNotFound(t *testing.T) {
	harness := newHarness(t)

	status, payload := harness.do(t, http.MethodPost, APIBasePath+"/projects", "", map[string]string{})
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 404 or 405 (body %s)", status, payload)
	}
}

func TestResponsesCarryNoStoreAndNoSniffHeaders(t *testing.T) {
	harness := newHarness(t)

	request, err := http.NewRequest(http.MethodGet, harness.server.URL+APIBasePath+"/health", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := harness.client.Do(request)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	defer response.Body.Close()

	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", response.Header.Get("Cache-Control"), "no-store")
	}
	if response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want %q", response.Header.Get("X-Content-Type-Options"), "nosniff")
	}
}
