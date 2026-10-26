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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/auth"
	"github.com/Hisha/Forgehand/internal/identity"
	"github.com/Hisha/Forgehand/internal/state"
)

const testPassword = "correct horse battery staple"

type harness struct {
	server     *httptest.Server
	client     *http.Client
	db         *state.Database
	service    *identity.Service
	operations *testSessionOperations
	now        *time.Time
	secret     []byte
	master     auth.MasterKey
}

type testDaemonStatus struct {
	response DaemonStatusResponse
	err      error
}

func (s *testDaemonStatus) DaemonStatus(context.Context) (DaemonStatusResponse, error) {
	return s.response, s.err
}

type testSessionOperations struct {
	db          *state.Database
	queueCalls  atomic.Int64
	createCalls atomic.Int64
}

func (o *testSessionOperations) CreateCodingSession(
	ctx context.Context,
	projectID int64,
	title, task string,
) (state.Session, error) {
	o.createCalls.Add(1)
	return o.db.CreateCodingSession(ctx, title, projectID, task, "test-provider", "test-model")
}

func (o *testSessionOperations) QueueSessionExecution(
	ctx context.Context,
	sessionID int64,
) (state.SessionExecution, error) {
	o.queueCalls.Add(1)
	execution, err := o.db.QueueSessionExecution(ctx, sessionID, 5)
	if err == nil {
		return execution, nil
	}
	session, readErr := o.db.GetSession(ctx, sessionID)
	if readErr == nil && session.State != state.SessionStateReady {
		return state.SessionExecution{}, fmt.Errorf("%w: %s", ErrSessionConflict, session.State)
	}
	return state.SessionExecution{}, err
}

func (o *testSessionOperations) ListSessionExecutionRecords(
	ctx context.Context,
	sessionID int64,
	limit int,
) ([]state.SessionExecutionRecord, bool, error) {
	return o.db.ListSessionExecutionRecords(ctx, sessionID, limit)
}

func (o *testSessionOperations) GetSessionExecutionRecord(
	ctx context.Context,
	sessionID, executionID int64,
	contentOffset, contentLimit int,
) (state.SessionExecutionRecord, error) {
	return o.db.GetSessionExecutionRecord(ctx, sessionID, executionID, contentOffset, contentLimit)
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

	operations := &testSessionOperations{db: db}
	statusOperations := &testDaemonStatus{response: DaemonStatusResponse{
		Status: "ok", ObservedAt: "2026-10-22T12:00:00Z",
		Identity:   DaemonIdentityResponse{Version: "test-version", BuildIdentity: "test-build", APIVersion: APIVersion, StartedAt: "2026-10-22T11:00:00Z", UptimeSeconds: 3600},
		Activity:   DaemonActivityResponse{WorkerCapacity: testIntPointer(4), ActiveWorkers: testIntPointer(1), RunningSessions: testIntPointer(1), QueuedExecutions: testIntPointer(2)},
		Executable: ExecutableResponse{Comparison: "match", Reason: "same_file_identity", CheckedAt: "2026-10-22T12:00:00Z"},
		Health:     DaemonHealthResponse{Database: "ok", Dispatcher: "ok", Workers: "ok"},
	}}
	api, err := NewServer(Options{
		Service:       service,
		Version:       "test-version",
		BuildIdentity: "test-build",
		Sessions:      operations,
		DaemonStatus:  statusOperations,
	})
	if err != nil {
		t.Fatalf("NewServer() returned error: %v", err)
	}

	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)

	return &harness{
		server:     server,
		client:     server.Client(),
		db:         db,
		service:    service,
		operations: operations,
		now:        &now,
		master:     master,
	}
}

func testIntPointer(value int) *int { return &value }

func (h *harness) createUserAndLogin(t *testing.T, username string) string {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	secret, _, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("generate TOTP secret: %v", err)
	}
	ciphertext, err := h.master.EncryptSecret(username, secret)
	if err != nil {
		t.Fatalf("encrypt TOTP secret: %v", err)
	}
	account, err := h.db.CreateAccount(context.Background(), username, "", state.AccountRoleUser, hash.Encode(), string(auth.TOTPAlgorithmSHA1), ciphertext)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := h.db.ActivateAccount(context.Background(), account.ID, -1); err != nil {
		t.Fatalf("activate user: %v", err)
	}
	return h.login(t, username, testPassword, h.code(secret))
}

func (h *harness) authorizeProject(t *testing.T, projectID int64) {
	t.Helper()
	accounts, err := h.db.ListAccounts(context.Background())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("load enrolled account: accounts=%d err=%v", len(accounts), err)
	}
	if err := h.db.GrantAccountProject(context.Background(), accounts[0].ID, projectID); err != nil {
		t.Fatalf("grant project: %v", err)
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

	*h.now = h.now.Add(auth.DefaultTOTPPeriod)
	h.secret = secret
	return secret
}

func (h *harness) code(secret []byte) string {
	return auth.GenerateTOTP(secret, auth.TOTPAlgorithmSHA1, auth.DefaultTOTPDigits, auth.TOTPCounter(*h.now, auth.DefaultTOTPPeriod))
}

// do performs an HTTP request against the API.
func (h *harness) do(t *testing.T, method, path, token string, body any) (int, []byte) {
	return h.doWithCompatibility(t, method, path, token, body, APIVersion)
}

func (h *harness) doWithCompatibility(t *testing.T, method, path, token string, body any, apiVersion string) (int, []byte) {
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
	if apiVersion != "" {
		request.Header.Set(CompatibilityHeader, apiVersion)
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

func TestDaemonStatusRequiresAdministratorAndReturnsOperationalSnapshot(t *testing.T) {
	harness := newHarness(t)

	status, payload := harness.do(t, http.MethodGet, APIBasePath+"/daemon/status", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401 (body %s)", status, payload)
	}

	userToken := harness.createUserAndLogin(t, "operator")
	status, payload = harness.do(t, http.MethodGet, APIBasePath+"/daemon/status", userToken, nil)
	if status != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want 403 (body %s)", status, payload)
	}

	adminHarness := newHarness(t)
	secret := adminHarness.enroll(t, "admin")
	token := adminHarness.login(t, "admin", testPassword, adminHarness.code(secret))
	status, payload = adminHarness.do(t, http.MethodGet, APIBasePath+"/daemon/status", token, nil)
	if status != http.StatusOK {
		t.Fatalf("admin status = %d, want 200 (body %s)", status, payload)
	}
	var response DaemonStatusResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if response.Identity.APIVersion != APIVersion || response.Identity.UptimeSeconds != 3600 {
		t.Fatalf("unexpected identity: %#v", response.Identity)
	}
	if response.Activity.ActiveWorkers == nil || *response.Activity.ActiveWorkers != 1 || response.Activity.QueuedExecutions == nil || *response.Activity.QueuedExecutions != 2 {
		t.Fatalf("unexpected activity: %#v", response.Activity)
	}
	if response.Executable.Comparison != "match" || response.Health.Database != "ok" {
		t.Fatalf("unexpected operational status: %#v", response)
	}
}

func TestStateChangesRequireKnownCompatibleAPIVersion(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")
	project, err := harness.db.CreateProject(context.Background(), "allowed", t.TempDir())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	harness.authorizeProject(t, project.ID)
	session, err := harness.db.CreateCodingSession(context.Background(), "Ready", project.ID, "Task", "test", "test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))

	for _, clientVersion := range []string{"", "v2", "unknown"} {
		status, payload := harness.doWithCompatibility(
			t, http.MethodPost, fmt.Sprintf("%s/projects/%d/sessions", APIBasePath, project.ID), token,
			map[string]string{"title": "Rejected", "task": "Must not persist"}, clientVersion)
		if status != http.StatusUpgradeRequired || !strings.Contains(string(payload), `"code":"incompatible_api"`) {
			t.Fatalf("create with API %q returned %d, body %s", clientVersion, status, payload)
		}

		status, payload = harness.doWithCompatibility(
			t, http.MethodPost, fmt.Sprintf("%s/sessions/%d/run", APIBasePath, session.ID), token, nil, clientVersion)
		if status != http.StatusUpgradeRequired || !strings.Contains(string(payload), `"code":"incompatible_api"`) {
			t.Fatalf("run with API %q returned %d, body %s", clientVersion, status, payload)
		}
	}

	if harness.operations.createCalls.Load() != 0 || harness.operations.queueCalls.Load() != 0 {
		t.Fatalf("incompatible requests reached operations: create=%d queue=%d", harness.operations.createCalls.Load(), harness.operations.queueCalls.Load())
	}
	persisted, err := harness.db.GetSession(context.Background(), session.ID)
	if err != nil || persisted.State != state.SessionStateReady {
		t.Fatalf("rejected run changed session: state=%q err=%v", persisted.State, err)
	}
}

func TestDifferentBuildIdentitiesRemainCompatibleWhenAPIVersionMatches(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")
	project, err := harness.db.CreateProject(context.Background(), "allowed", t.TempDir())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	harness.authorizeProject(t, project.ID)
	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))

	status, payload := harness.do(t, http.MethodPost, fmt.Sprintf("%s/projects/%d/sessions", APIBasePath, project.ID), token, map[string]string{
		"title": "Compatible client",
		"task":  "The client build identity is deliberately absent from the HTTP contract.",
	})
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", status, payload)
	}
}

func TestMissingCompatibilityMetadataRejectsAuthenticationMutation(t *testing.T) {
	harness := newHarness(t)

	status, payload := harness.doWithCompatibility(t, http.MethodPost, APIBasePath+"/auth/login", "", map[string]string{
		"username": "alice", "password": testPassword, "totp_code": "000000",
	}, "")
	if status != http.StatusUpgradeRequired || !strings.Contains(string(payload), `"code":"incompatible_api"`) {
		t.Fatalf("status = %d, body = %s", status, payload)
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

func TestAuthorizedSessionCreationPersistsReadyCodingSession(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")
	project, err := harness.db.CreateProject(context.Background(), "allowed", t.TempDir())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	harness.authorizeProject(t, project.ID)
	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))

	status, payload := harness.do(t, http.MethodPost, fmt.Sprintf("%s/projects/%d/sessions", APIBasePath, project.ID), token, map[string]string{
		"title": "Fix queue race",
		"task":  "Find and repair the duplicate scheduling race.",
	})
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", status, payload)
	}
	var response sessionSummaryResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	persisted, err := harness.db.GetSession(context.Background(), response.ID)
	if err != nil {
		t.Fatalf("read created session: %v", err)
	}
	if persisted.State != state.SessionStateReady || persisted.ProjectID == nil || *persisted.ProjectID != project.ID {
		t.Fatalf("created session = %+v", persisted)
	}
	if persisted.Task != "Find and repair the duplicate scheduling race." || persisted.Kind != "coding" {
		t.Fatalf("created session fields = %+v", persisted)
	}
	events, err := harness.db.ListAuthEvents(context.Background(), 20)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	foundAudit := false
	for _, event := range events {
		if event.EventType == state.AuthEventSessionCreated && event.Outcome == "created" {
			foundAudit = true
			if strings.Contains(event.Detail, persisted.Task) {
				t.Fatalf("audit event contains task content: %+v", event)
			}
		}
	}
	if !foundAudit {
		t.Fatal("session creation audit event was not recorded")
	}
}

func TestExecutionHistoryAndChunkedResultRequireSessionAuthorization(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")
	allowed, _ := harness.db.CreateProject(context.Background(), "allowed", t.TempDir())
	forbidden, _ := harness.db.CreateProject(context.Background(), "forbidden", t.TempDir())
	harness.authorizeProject(t, allowed.ID)
	allowedSession, _ := harness.db.CreateCodingSession(context.Background(), "Allowed", allowed.ID, "task", "provider", "model")
	forbiddenSession, _ := harness.db.CreateCodingSession(context.Background(), "Forbidden", forbidden.ID, "task", "provider", "model")
	execution, err := harness.db.StartSessionExecution(context.Background(), allowedSession.ID, 1)
	if err != nil {
		t.Fatalf("start execution: %v", err)
	}
	content := "ab😀cd\nfinal"
	if err := harness.db.CompleteModelExecutionWithMetrics(context.Background(), execution.ID, "actual-provider", "actual-model", content, state.ModelMetrics{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7, LatencyMS: 25}); err != nil {
		t.Fatalf("complete execution: %v", err)
	}
	forbiddenExecution, err := harness.db.StartSessionExecution(context.Background(), forbiddenSession.ID, 1)
	if err != nil {
		t.Fatalf("start forbidden execution fixture: %v", err)
	}
	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))

	historyPath := fmt.Sprintf("%s/sessions/%d/executions", APIBasePath, allowedSession.ID)
	status, payload := harness.do(t, http.MethodGet, historyPath, token, nil)
	if status != http.StatusOK {
		t.Fatalf("history status=%d body=%s", status, payload)
	}
	var history executionHistoryResponse
	if err := json.Unmarshal(payload, &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if history.Truncated || len(history.Executions) != 1 || !history.Executions[0].ResultAvailable || history.Executions[0].Provider != "actual-provider" {
		t.Fatalf("history=%+v", history)
	}
	if strings.Contains(string(payload), content) {
		t.Fatal("history response included sensitive result content")
	}

	detailPath := fmt.Sprintf("%s/sessions/%d/executions/%d?offset=0&limit=4", APIBasePath, allowedSession.ID, execution.ID)
	status, payload = harness.do(t, http.MethodGet, detailPath, token, nil)
	if status != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", status, payload)
	}
	var detail executionRecordResponse
	if err := json.Unmarshal(payload, &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Result == nil || detail.Result.Content != "ab😀c" || detail.Result.Complete || detail.Result.NextOffset != 4 || detail.Result.TotalCharacters != 11 {
		t.Fatalf("first result page=%+v", detail.Result)
	}
	status, payload = harness.do(t, http.MethodGet, fmt.Sprintf("%s/sessions/%d/executions/%d?offset=4&limit=20", APIBasePath, allowedSession.ID, execution.ID), token, nil)
	if status != http.StatusOK {
		t.Fatalf("second page status=%d body=%s", status, payload)
	}
	if err := json.Unmarshal(payload, &detail); err != nil || detail.Result == nil || detail.Result.Content != "d\nfinal" || !detail.Result.Complete {
		t.Fatalf("second result page=%+v err=%v", detail.Result, err)
	}

	forbiddenPath := fmt.Sprintf("%s/sessions/%d/executions", APIBasePath, forbiddenSession.ID)
	if status, _ := harness.do(t, http.MethodGet, forbiddenPath, token, nil); status != http.StatusNotFound {
		t.Fatalf("unauthorized history status=%d, want 404", status)
	}
	crossPath := fmt.Sprintf("%s/sessions/%d/executions/%d", APIBasePath, allowedSession.ID, forbiddenExecution.ID)
	if status, _ := harness.do(t, http.MethodGet, crossPath, token, nil); status != http.StatusNotFound {
		t.Fatalf("missing execution status=%d, want 404", status)
	}
}

func TestAuthorizedRunQueuesExistingSession(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")
	project, err := harness.db.CreateProject(context.Background(), "allowed", t.TempDir())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	harness.authorizeProject(t, project.ID)
	session, err := harness.db.CreateCodingSession(context.Background(), "Run me", project.ID, "Do work", "test", "test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))

	status, payload := harness.do(t, http.MethodPost, fmt.Sprintf("%s/sessions/%d/run", APIBasePath, session.ID), token, nil)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", status, payload)
	}
	var response runSessionResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Session.State != state.SessionStateQueued || response.Execution.Status != state.ExecutionStatusQueued {
		t.Fatalf("run response = %+v", response)
	}
	events, err := harness.db.ListAuthEvents(context.Background(), 20)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	foundAudit := false
	for _, event := range events {
		if event.EventType == state.AuthEventSessionRunSubmitted && event.Outcome == "queued" {
			foundAudit = true
		}
	}
	if !foundAudit {
		t.Fatal("session run audit event was not recorded")
	}
}

func TestDuplicateRunReturnsConflictAndCreatesOneExecution(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")
	project, _ := harness.db.CreateProject(context.Background(), "allowed", t.TempDir())
	harness.authorizeProject(t, project.ID)
	session, _ := harness.db.CreateCodingSession(context.Background(), "Run once", project.ID, "Do work", "test", "test")
	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))
	path := fmt.Sprintf("%s/sessions/%d/run", APIBasePath, session.ID)

	firstStatus, firstPayload := harness.do(t, http.MethodPost, path, token, nil)
	secondStatus, secondPayload := harness.do(t, http.MethodPost, path, token, nil)
	if firstStatus != http.StatusAccepted {
		t.Fatalf("first status = %d, body %s", firstStatus, firstPayload)
	}
	if secondStatus != http.StatusConflict {
		t.Fatalf("second status = %d, want 409 (body %s)", secondStatus, secondPayload)
	}
	_, execution, err := harness.db.SessionExecutionStatus(context.Background(), session.ID)
	if err != nil || execution == nil || execution.Status != state.ExecutionStatusQueued {
		t.Fatalf("execution = %+v err=%v", execution, err)
	}
}

func TestConcurrentDuplicateRunAcceptsExactlyOneRequest(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")
	project, _ := harness.db.CreateProject(context.Background(), "allowed", t.TempDir())
	harness.authorizeProject(t, project.ID)
	session, _ := harness.db.CreateCodingSession(context.Background(), "Run once", project.ID, "Do work", "test", "test")
	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))
	path := fmt.Sprintf("%s/sessions/%d/run", APIBasePath, session.ID)

	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			status, _ := harness.do(t, http.MethodPost, path, token, nil)
			statuses <- status
		}()
	}
	close(start)
	wait.Wait()
	close(statuses)

	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusAccepted] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("statuses = %v, want one 202 and one 409", counts)
	}
}

func TestUnauthorizedAndRevokedWritesAreRefusedWithoutQueueing(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")
	project, _ := harness.db.CreateProject(context.Background(), "private", t.TempDir())
	session, _ := harness.db.CreateCodingSession(context.Background(), "Hidden", project.ID, "Do work", "test", "test")
	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))

	status, _ := harness.do(t, http.MethodPost, fmt.Sprintf("%s/projects/%d/sessions", APIBasePath, project.ID), token, map[string]string{"title": "No", "task": "No"})
	if status != http.StatusForbidden {
		t.Fatalf("unauthorized creation status = %d, want 403", status)
	}
	status, _ = harness.do(t, http.MethodPost, fmt.Sprintf("%s/sessions/%d/run", APIBasePath, session.ID), token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("unauthorized run status = %d, want 404", status)
	}
	if harness.operations.queueCalls.Load() != 0 {
		t.Fatalf("rejected requests made %d queue calls", harness.operations.queueCalls.Load())
	}

	harness.authorizeProject(t, project.ID)
	accounts, _ := harness.db.ListAccounts(context.Background())
	if err := harness.db.RevokeAccountProject(context.Background(), accounts[0].ID, project.ID); err != nil {
		t.Fatalf("revoke project: %v", err)
	}
	status, _ = harness.do(t, http.MethodPost, fmt.Sprintf("%s/sessions/%d/run", APIBasePath, session.ID), token, nil)
	if status != http.StatusNotFound || harness.operations.queueCalls.Load() != 0 {
		t.Fatalf("revoked run status=%d queueCalls=%d", status, harness.operations.queueCalls.Load())
	}
}

func TestSessionWriteValidationRejectsUnknownMissingAndUnsupportedFields(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")
	project, _ := harness.db.CreateProject(context.Background(), "allowed", t.TempDir())
	harness.authorizeProject(t, project.ID)
	session, _ := harness.db.CreateCodingSession(context.Background(), "Ready", project.ID, "Task", "test", "test")
	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))
	createPath := fmt.Sprintf("%s/projects/%d/sessions", APIBasePath, project.ID)

	for _, body := range []map[string]string{
		{"title": "", "task": "Task"},
		{"title": "Title", "task": ""},
		{"title": "Title", "task": "Task", "model": "unsupported"},
	} {
		status, payload := harness.do(t, http.MethodPost, createPath, token, body)
		if status != http.StatusBadRequest {
			t.Fatalf("body %v returned %d, want 400 (payload %s)", body, status, payload)
		}
	}
	status, payload := harness.do(t, http.MethodPost, createPath, token, map[string]string{
		"title": "Title",
		"task":  strings.Repeat("x", MaxRequestBytes+1),
	})
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized create returned %d, want 413 (payload %s)", status, payload)
	}

	status, payload = harness.do(t, http.MethodPost, fmt.Sprintf("%s/sessions/%d/run", APIBasePath, session.ID), token, map[string]string{"command": "sh"})
	if status != http.StatusBadRequest {
		t.Fatalf("run options returned %d, want 400 (payload %s)", status, payload)
	}
	if harness.operations.queueCalls.Load() != 0 {
		t.Fatalf("invalid requests made %d queue calls", harness.operations.queueCalls.Load())
	}
	if harness.operations.createCalls.Load() != 0 {
		t.Fatalf("invalid requests made %d create calls", harness.operations.createCalls.Load())
	}
}

func TestSessionWritesRequireValidUnexpiredAuthentication(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")
	project, _ := harness.db.CreateProject(context.Background(), "allowed", t.TempDir())
	harness.authorizeProject(t, project.ID)
	path := fmt.Sprintf("%s/projects/%d/sessions", APIBasePath, project.ID)
	body := map[string]string{"title": "Title", "task": "Task"}

	for _, token := range []string{"", "invalid"} {
		status, _ := harness.do(t, http.MethodPost, path, token, body)
		if status != http.StatusUnauthorized {
			t.Fatalf("token %q returned %d, want 401", token, status)
		}
		status, _ = harness.do(t, http.MethodPost, APIBasePath+"/sessions/1/run", token, nil)
		if status != http.StatusUnauthorized {
			t.Fatalf("run token %q returned %d, want 401", token, status)
		}
	}
	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))
	*harness.now = harness.now.Add(2 * time.Hour)
	status, _ := harness.do(t, http.MethodPost, path, token, body)
	if status != http.StatusUnauthorized {
		t.Fatalf("expired token returned %d, want 401", status)
	}
}

func TestInvalidSessionWriteIdentifiersAreRejected(t *testing.T) {
	harness := newHarness(t)
	harness.enroll(t, "alice")
	token := harness.login(t, "alice", testPassword, harness.code(harness.secret))

	for _, path := range []string{
		APIBasePath + "/projects/invalid/sessions",
		APIBasePath + "/projects/0/sessions",
		APIBasePath + "/sessions/-1/run",
	} {
		status, payload := harness.do(t, http.MethodPost, path, token, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("%s returned %d, want 400 (body %s)", path, status, payload)
		}
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
	request.Header.Set(CompatibilityHeader, APIVersion)

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
	request.Header.Set(CompatibilityHeader, APIVersion)

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
	request.Header.Set(CompatibilityHeader, APIVersion)

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
	request.Header.Set(CompatibilityHeader, APIVersion)

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
