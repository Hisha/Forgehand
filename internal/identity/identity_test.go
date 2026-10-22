package identity

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/auth"
	"github.com/Hisha/Forgehand/internal/state"
)

const testPassword = "correct horse battery staple"

type testHarness struct {
	service *identityService
	db      *state.Database
	master  auth.MasterKey
	now     time.Time
}

type identityService = Service

func newTestHarness(t *testing.T) *testHarness {
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

	harness := &testHarness{
		db:     db,
		master: master,
		now:    time.Now().UTC(),
	}

	throttle := auth.NewLoginThrottle()
	throttle.SetClock(func() time.Time { return harness.now })

	service, err := NewService(Options{
		DB:         db,
		MasterKey:  master,
		SessionTTL: time.Hour,
		Throttle:   throttle,
		Now:        func() time.Time { return harness.now },
	})
	if err != nil {
		t.Fatalf("NewService() returned error: %v", err)
	}
	harness.service = service

	return harness
}

// enroll creates an administrator and confirms the enrollment, returning the
// decoded one-time password secret.
func (h *testHarness) enroll(t *testing.T, username string) []byte {
	t.Helper()

	bootstrap, err := h.service.BootstrapAdministrator(
		context.Background(),
		username,
		"",
		testPassword,
	)
	if err != nil {
		t.Fatalf("BootstrapAdministrator() returned error: %v", err)
	}

	secret, err := auth.DecodeTOTPSecret(bootstrap.Secret)
	if err != nil {
		t.Fatalf("decode enrollment secret: %v", err)
	}

	code := auth.GenerateTOTP(secret, auth.TOTPAlgorithmSHA1, auth.DefaultTOTPDigits, auth.TOTPCounter(h.now, auth.DefaultTOTPPeriod))
	if _, err := h.service.ConfirmEnrollment(context.Background(), username, code); err != nil {
		t.Fatalf("ConfirmEnrollment() returned error: %v", err)
	}

	// The enrollment consumed the current one-time password. Move to the
	// next step so that a later login is not treated as a replay.
	h.now = h.now.Add(auth.DefaultTOTPPeriod)

	return secret
}

// code returns the current one-time password for a secret.
func (h *testHarness) code(secret []byte) string {
	return auth.GenerateTOTP(secret, auth.TOTPAlgorithmSHA1, auth.DefaultTOTPDigits, auth.TOTPCounter(h.now, auth.DefaultTOTPPeriod))
}

func TestBootstrapAdministratorRequiresNoExistingAccount(t *testing.T) {
	harness := newTestHarness(t)

	bootstrap, err := harness.service.BootstrapAdministrator(context.Background(), "alice", "Alice", testPassword)
	if err != nil {
		t.Fatalf("BootstrapAdministrator() returned error: %v", err)
	}
	if bootstrap.Account.Username != "alice" {
		t.Fatalf("username = %q, want %q", bootstrap.Account.Username, "alice")
	}
	if bootstrap.Account.Role != state.AccountRoleAdministrator {
		t.Fatalf("role = %q, want %q", bootstrap.Account.Role, state.AccountRoleAdministrator)
	}
	if bootstrap.Account.Status != state.AccountStatusPending {
		t.Fatalf("status = %q, want %q", bootstrap.Account.Status, state.AccountStatusPending)
	}
	if bootstrap.Secret == "" {
		t.Fatal("the bootstrap result carries no enrollment secret")
	}
	if len(bootstrap.RecoveryCodes) != auth.RecoveryCodeCount {
		t.Fatalf("issued %d recovery codes, want %d", len(bootstrap.RecoveryCodes), auth.RecoveryCodeCount)
	}

	// A second bootstrap must not create a hidden account.
	if _, err := harness.service.BootstrapAdministrator(context.Background(), "mallory", "", testPassword); !errors.Is(err, ErrAccountExists) {
		t.Fatalf("the second bootstrap returned %v, want ErrAccountExists", err)
	}
}

func TestBootstrapAdministratorRejectsWeakPasswordAndUsername(t *testing.T) {
	harness := newTestHarness(t)

	if _, err := harness.service.BootstrapAdministrator(context.Background(), "alice", "", "short"); !errors.Is(err, ErrValidation) {
		t.Fatalf("BootstrapAdministrator() returned %v, want ErrValidation", err)
	}
	if _, err := harness.service.BootstrapAdministrator(context.Background(), "A!", "", testPassword); !errors.Is(err, ErrValidation) {
		t.Fatalf("BootstrapAdministrator() returned %v, want ErrValidation", err)
	}
}

func TestPendingAccountCannotAuthenticate(t *testing.T) {
	harness := newTestHarness(t)

	bootstrap, err := harness.service.BootstrapAdministrator(context.Background(), "alice", "", testPassword)
	if err != nil {
		t.Fatalf("BootstrapAdministrator() returned error: %v", err)
	}

	secret, err := auth.DecodeTOTPSecret(bootstrap.Secret)
	if err != nil {
		t.Fatalf("decode enrollment secret: %v", err)
	}

	_, err = harness.service.Authenticate(
		context.Background(),
		"alice",
		testPassword,
		harness.code(secret),
		"",
		"192.0.2.10",
	)
	if !errors.Is(err, ErrEnrollmentRequired) {
		t.Fatalf("Authenticate() returned %v, want ErrEnrollmentRequired", err)
	}
}

func TestAuthenticateRequiresSecondFactor(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	_, err := harness.service.Authenticate(context.Background(), "alice", testPassword, "", "", "192.0.2.10")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() returned %v, want ErrInvalidCredentials", err)
	}

	// The failed attempt must not have created a session.
	account, err := harness.service.LookupAccount(context.Background(), "alice")
	if err != nil {
		t.Fatalf("LookupAccount() returned error: %v", err)
	}
	if account.Status != state.AccountStatusActive {
		t.Fatalf("status = %q, want %q", account.Status, state.AccountStatusActive)
	}
	_ = secret
}

func TestAuthenticateSucceedsWithPasswordAndOneTimePassword(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	result, err := harness.service.Authenticate(
		context.Background(),
		"alice",
		testPassword,
		harness.code(secret),
		"",
		"192.0.2.10",
	)
	if err != nil {
		t.Fatalf("Authenticate() returned error: %v", err)
	}
	if result.Token == "" {
		t.Fatal("no session token was issued")
	}
	if result.Account.Username != "alice" {
		t.Fatalf("username = %q, want %q", result.Account.Username, "alice")
	}
	if !result.ExpiresAt.After(harness.now) {
		t.Fatal("the issued session is already expired")
	}
}

func TestAuthenticateRejectsWrongPassword(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	_, err := harness.service.Authenticate(
		context.Background(),
		"alice",
		"wrong password value",
		harness.code(secret),
		"",
		"192.0.2.10",
	)
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() returned %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticateRejectsExpiredOneTimePassword(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	expired := auth.GenerateTOTP(secret, auth.TOTPAlgorithmSHA1, auth.DefaultTOTPDigits, auth.TOTPCounter(harness.now.Add(-10*time.Minute), auth.DefaultTOTPPeriod))

	_, err := harness.service.Authenticate(context.Background(), "alice", testPassword, expired, "", "192.0.2.10")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() returned %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticateRejectsReplayedOneTimePassword(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	code := harness.code(secret)

	first, err := harness.service.Authenticate(context.Background(), "alice", testPassword, code, "", "192.0.2.10")
	if err != nil {
		t.Fatalf("Authenticate() returned error: %v", err)
	}
	if first.Token == "" {
		t.Fatal("no session token was issued")
	}

	_, err = harness.service.Authenticate(context.Background(), "alice", testPassword, code, "", "192.0.2.10")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() returned %v, want ErrInvalidCredentials", err)
	}
}

func TestRecoveryCodeIsSingleUse(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	bootstrap, err := harness.service.BootstrapAdministrator(context.Background(), "bob", "", testPassword)
	if !errors.Is(err, ErrAccountExists) {
		t.Fatalf("BootstrapAdministrator() returned %v, want ErrAccountExists", err)
	}
	_ = bootstrap

	account, err := harness.service.LookupAccount(context.Background(), "alice")
	if err != nil {
		t.Fatalf("LookupAccount() returned error: %v", err)
	}

	codes, err := auth.GenerateRecoveryCodes(1)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes() returned error: %v", err)
	}
	if err := harness.db.ReplaceRecoveryCodes(context.Background(), account.ID, []string{codes[0].Hash}); err != nil {
		t.Fatalf("ReplaceRecoveryCodes() returned error: %v", err)
	}

	result, err := harness.service.Authenticate(
		context.Background(),
		"alice",
		testPassword,
		"",
		codes[0].Code,
		"192.0.2.10",
	)
	if err != nil {
		t.Fatalf("Authenticate() returned error: %v", err)
	}
	if result.Token == "" {
		t.Fatal("the recovery code did not issue a session")
	}
	if len(result.RecoveryCodes) != auth.RecoveryCodeCount {
		t.Fatalf("issued %d replacement recovery codes, want %d", len(result.RecoveryCodes), auth.RecoveryCodeCount)
	}

	_, err = harness.service.Authenticate(
		context.Background(),
		"alice",
		testPassword,
		"",
		codes[0].Code,
		"192.0.2.10",
	)
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() returned %v, want ErrInvalidCredentials", err)
	}
	_ = secret
}

func TestRecoveryCodeCannotBeUsedWithoutThePassword(t *testing.T) {
	harness := newTestHarness(t)
	harness.enroll(t, "alice")

	account, err := harness.service.LookupAccount(context.Background(), "alice")
	if err != nil {
		t.Fatalf("LookupAccount() returned error: %v", err)
	}

	codes, err := auth.GenerateRecoveryCodes(1)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes() returned error: %v", err)
	}
	if err := harness.db.ReplaceRecoveryCodes(context.Background(), account.ID, []string{codes[0].Hash}); err != nil {
		t.Fatalf("ReplaceRecoveryCodes() returned error: %v", err)
	}

	_, err = harness.service.Authenticate(context.Background(), "alice", "wrong password value", "", codes[0].Code, "192.0.2.10")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() returned %v, want ErrInvalidCredentials", err)
	}
}

func TestRepeatedFailuresLockTheAccount(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	// Every attempt comes from a different caller so that the account
	// budget, not the caller budget, is what stops the login.
	for attempt := 0; attempt < MaxAccountFailures; attempt++ {
		_, err := harness.service.Authenticate(
			context.Background(),
			"alice",
			"wrong password value",
			harness.code(secret),
			"",
			fmt.Sprintf("192.0.2.%d", attempt+1),
		)
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d returned %v, want ErrInvalidCredentials", attempt, err)
		}
	}

	_, err := harness.service.Authenticate(context.Background(), "alice", testPassword, harness.code(secret), "", "192.0.2.200")
	if !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("Authenticate() returned %v, want ErrAccountLocked", err)
	}
}

func TestPeerThrottleRefusesRepeatedFailures(t *testing.T) {
	harness := newTestHarness(t)
	harness.enroll(t, "alice")

	// An unknown account cannot lock an account, so only the caller budget
	// is consumed.
	for attempt := 0; attempt < 25; attempt++ {
		_, err := harness.service.Authenticate(
			context.Background(),
			"nobody",
			"wrong password value",
			"000000",
			"",
			"192.0.2.99",
		)
		if attempt < 20 && errors.Is(err, ErrThrottled) {
			t.Fatalf("the caller was throttled after %d attempts", attempt+1)
		}
		if attempt >= 20 && !errors.Is(err, ErrThrottled) {
			t.Fatalf("attempt %d returned %v, want ErrThrottled", attempt+1, err)
		}
	}
}

func TestSessionExpires(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	result, err := harness.service.Authenticate(context.Background(), "alice", testPassword, harness.code(secret), "", "192.0.2.10")
	if err != nil {
		t.Fatalf("Authenticate() returned error: %v", err)
	}

	if _, err := harness.service.AuthenticateToken(context.Background(), result.Token); err != nil {
		t.Fatalf("AuthenticateToken() rejected a fresh token: %v", err)
	}

	harness.now = harness.now.Add(2 * time.Hour)
	if _, err := harness.service.AuthenticateToken(context.Background(), result.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("AuthenticateToken() returned %v, want ErrUnauthorized", err)
	}
}

func TestUnusedSessionExpires(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	result, err := harness.service.Authenticate(context.Background(), "alice", testPassword, harness.code(secret), "", "192.0.2.10")
	if err != nil {
		t.Fatalf("Authenticate() returned error: %v", err)
	}

	// The absolute lifetime is one hour in the harness, so the idle limit
	// has to be exercised with a shorter absolute lifetime.
	harness.now = harness.now.Add(30 * time.Minute)
	if _, err := harness.service.AuthenticateToken(context.Background(), result.Token); err != nil {
		t.Fatalf("AuthenticateToken() rejected a token inside the idle window: %v", err)
	}

	harness.now = harness.now.Add(2 * time.Hour)
	if _, err := harness.service.AuthenticateToken(context.Background(), result.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("AuthenticateToken() returned %v, want ErrUnauthorized", err)
	}
}

func TestSessionCanBeRevoked(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	result, err := harness.service.Authenticate(context.Background(), "alice", testPassword, harness.code(secret), "", "192.0.2.10")
	if err != nil {
		t.Fatalf("Authenticate() returned error: %v", err)
	}

	if err := harness.service.RevokeSession(context.Background(), result.Token, "192.0.2.10"); err != nil {
		t.Fatalf("RevokeSession() returned error: %v", err)
	}

	if _, err := harness.service.AuthenticateToken(context.Background(), result.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("AuthenticateToken() returned %v, want ErrUnauthorized", err)
	}
}

func TestDisablingAnAccountRevokesItsSessions(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	result, err := harness.service.Authenticate(context.Background(), "alice", testPassword, harness.code(secret), "", "192.0.2.10")
	if err != nil {
		t.Fatalf("Authenticate() returned error: %v", err)
	}

	account, err := harness.service.LookupAccount(context.Background(), "alice")
	if err != nil {
		t.Fatalf("LookupAccount() returned error: %v", err)
	}

	if err := harness.service.DisableAccount(context.Background(), account.ID); err != nil {
		t.Fatalf("DisableAccount() returned error: %v", err)
	}

	if _, err := harness.service.AuthenticateToken(context.Background(), result.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("AuthenticateToken() returned %v, want ErrUnauthorized", err)
	}

	_, err = harness.service.Authenticate(context.Background(), "alice", testPassword, harness.code(secret), "", "192.0.2.10")
	if !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("Authenticate() returned %v, want ErrAccountDisabled", err)
	}
}

func TestProjectAuthorizationBoundary(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	allowed, err := harness.db.CreateProject(context.Background(), "allowed", t.TempDir())
	if err != nil {
		t.Fatalf("CreateProject() returned error: %v", err)
	}
	forbidden, err := harness.db.CreateProject(context.Background(), "forbidden", t.TempDir())
	if err != nil {
		t.Fatalf("CreateProject() returned error: %v", err)
	}

	account, err := harness.service.LookupAccount(context.Background(), "alice")
	if err != nil {
		t.Fatalf("LookupAccount() returned error: %v", err)
	}

	if err := harness.service.GrantProject(context.Background(), account.ID, allowed.ID); err != nil {
		t.Fatalf("GrantProject() returned error: %v", err)
	}

	result, err := harness.service.Authenticate(context.Background(), "alice", testPassword, harness.code(secret), "", "192.0.2.10")
	if err != nil {
		t.Fatalf("Authenticate() returned error: %v", err)
	}
	principal, err := harness.service.AuthenticateToken(context.Background(), result.Token)
	if err != nil {
		t.Fatalf("AuthenticateToken() returned error: %v", err)
	}

	projects, err := harness.service.AuthorizedProjects(context.Background(), principal)
	if err != nil {
		t.Fatalf("AuthorizedProjects() returned error: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != allowed.ID {
		t.Fatalf("authorized projects = %v, want only %d", projects, allowed.ID)
	}

	if _, err := harness.service.ListProjectSessions(context.Background(), principal, forbidden.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ListProjectSessions() returned %v, want ErrForbidden", err)
	}
}

func TestSessionWithoutProjectIsNotReadable(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	session, err := harness.db.CreateSession(context.Background(), "no project")
	if err != nil {
		t.Fatalf("CreateSession() returned error: %v", err)
	}

	result, err := harness.service.Authenticate(context.Background(), "alice", testPassword, harness.code(secret), "", "192.0.2.10")
	if err != nil {
		t.Fatalf("Authenticate() returned error: %v", err)
	}
	principal, err := harness.service.AuthenticateToken(context.Background(), result.Token)
	if err != nil {
		t.Fatalf("AuthenticateToken() returned error: %v", err)
	}

	if _, err := harness.service.GetSession(context.Background(), principal, session.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSession() returned %v, want ErrNotFound", err)
	}
}

func TestAuthenticationFailuresAreAudited(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")

	if _, err := harness.service.Authenticate(context.Background(), "alice", "wrong password value", harness.code(secret), "", "192.0.2.10"); err == nil {
		t.Fatal("the failed authentication unexpectedly succeeded")
	}

	events, err := harness.db.ListAuthEvents(context.Background(), 50)
	if err != nil {
		t.Fatalf("ListAuthEvents() returned error: %v", err)
	}

	var found bool
	for _, event := range events {
		if event.EventType == state.AuthEventLoginFailed {
			found = true
			if event.Username != "alice" {
				t.Fatalf("audit username = %q, want %q", event.Username, "alice")
			}
			if event.RemoteAddress != "192.0.2.10" {
				t.Fatalf("audit address = %q, want %q", event.RemoteAddress, "192.0.2.10")
			}
			for _, value := range []string{event.Detail, event.Outcome} {
				if value == testPassword {
					t.Fatal("the audit record contains the password")
				}
			}
		}
	}
	if !found {
		t.Fatal("no failed login audit record was written")
	}
}

func TestEnrollRemoteRequiresPasswordAndOneTimePassword(t *testing.T) {
	harness := newTestHarness(t)

	bootstrap, err := harness.service.BootstrapAdministrator(context.Background(), "alice", "", testPassword)
	if err != nil {
		t.Fatalf("BootstrapAdministrator() returned error: %v", err)
	}
	secret, err := auth.DecodeTOTPSecret(bootstrap.Secret)
	if err != nil {
		t.Fatalf("decode enrollment secret: %v", err)
	}

	if _, err := harness.service.EnrollRemote(context.Background(), "alice", testPassword, "", "192.0.2.10"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("EnrollRemote() returned %v, want ErrInvalidCredentials", err)
	}

	result, err := harness.service.EnrollRemote(context.Background(), "alice", testPassword, harness.code(secret), "192.0.2.10")
	if err != nil {
		t.Fatalf("EnrollRemote() returned error: %v", err)
	}
	if result.Token == "" {
		t.Fatal("EnrollRemote() issued no session")
	}
	if result.Account.Status != state.AccountStatusActive {
		t.Fatalf("status = %q, want %q", result.Account.Status, state.AccountStatusActive)
	}
}
