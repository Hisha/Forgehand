package identity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/auth"
	"github.com/Hisha/Forgehand/internal/state"
)

const sensitiveTestAddress = "192.0.2.44"

func sensitiveAdmin(t *testing.T) (*testHarness, Principal, []byte) {
	t.Helper()
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")
	result, err := harness.service.Authenticate(
		context.Background(),
		"alice",
		testPassword,
		harness.code(secret),
		"",
		sensitiveTestAddress,
	)
	if err != nil {
		t.Fatalf("Authenticate() returned error: %v", err)
	}
	principal, err := harness.service.AuthenticateToken(context.Background(), result.Token)
	if err != nil {
		t.Fatalf("AuthenticateToken() returned error: %v", err)
	}
	harness.now = harness.now.Add(auth.DefaultTOTPPeriod)
	return harness, principal, secret
}

func activePrincipalForRole(
	t *testing.T,
	harness *testHarness,
	username string,
	role string,
) (Principal, []byte) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword() returned error: %v", err)
	}
	secret, _, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() returned error: %v", err)
	}
	ciphertext, err := harness.master.EncryptSecret(username, secret)
	if err != nil {
		t.Fatalf("EncryptSecret() returned error: %v", err)
	}
	account, err := harness.db.CreateAccount(
		context.Background(),
		username,
		"",
		role,
		hash.Encode(),
		string(auth.TOTPAlgorithmSHA1),
		ciphertext,
	)
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}
	counter := auth.TOTPCounter(harness.now, auth.DefaultTOTPPeriod)
	if err := harness.db.ActivateAccount(context.Background(), account.ID, counter-1); err != nil {
		t.Fatalf("ActivateAccount() returned error: %v", err)
	}
	account, err = harness.db.GetAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("GetAccount() returned error: %v", err)
	}
	token, err := auth.GenerateSessionToken()
	if err != nil {
		t.Fatalf("GenerateSessionToken() returned error: %v", err)
	}
	session, err := harness.db.CreateAccountSession(context.Background(), account.ID, token.Hash, harness.now.Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateAccountSession() returned error: %v", err)
	}
	return Principal{Account: account, Session: session}, secret
}

func authorizeRestart(
	t *testing.T,
	harness *testHarness,
	principal Principal,
	secret []byte,
) SensitiveAuthorization {
	t.Helper()
	authorization, err := harness.service.AuthorizeSensitiveOperation(
		context.Background(),
		principal,
		testPassword,
		harness.code(secret),
		SensitiveOperationDaemonRestart,
		sensitiveTestAddress,
	)
	if err != nil {
		t.Fatalf("AuthorizeSensitiveOperation() returned error: %v", err)
	}
	return authorization
}

func TestConcurrentLoginCannotConsumeSameTOTPCodeTwice(t *testing.T) {
	harness := newTestHarness(t)
	secret := harness.enroll(t, "alice")
	code := harness.code(secret)

	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for index := range 2 {
		go func() {
			ready.Done()
			<-start
			_, err := harness.service.Authenticate(
				context.Background(),
				"alice",
				testPassword,
				code,
				"",
				"192.0.2."+string(rune('1'+index)),
			)
			results <- err
		}()
	}
	ready.Wait()
	close(start)

	succeeded := 0
	rejected := 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrInvalidCredentials):
			rejected++
		default:
			t.Fatalf("Authenticate() returned unexpected error: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent authentication results: %d succeeded, %d rejected; want 1 and 1", succeeded, rejected)
	}
}

func TestAuthorizeSensitiveOperationForAdministrator(t *testing.T) {
	harness, principal, secret := sensitiveAdmin(t)
	authorization := authorizeRestart(t, harness, principal, secret)
	if authorization.Token == "" {
		t.Fatal("sensitive authorization token is empty")
	}
	if authorization.Purpose != SensitiveOperationDaemonRestart {
		t.Fatalf("purpose = %q, want %q", authorization.Purpose, SensitiveOperationDaemonRestart)
	}
	if got := authorization.ExpiresAt.Sub(harness.now); got != DefaultSensitiveAuthorizationTTL {
		t.Fatalf("authorization lifetime = %s, want %s", got, DefaultSensitiveAuthorizationTTL)
	}
}

func TestAuthorizeSensitiveOperationRejectsNonAdministrator(t *testing.T) {
	harness := newTestHarness(t)
	principal, secret := activePrincipalForRole(t, harness, "regular-user", state.AccountRoleUser)
	_, err := harness.service.AuthorizeSensitiveOperation(
		context.Background(),
		principal,
		testPassword,
		harness.code(secret),
		SensitiveOperationDaemonRestart,
		sensitiveTestAddress,
	)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("AuthorizeSensitiveOperation() returned %v, want ErrForbidden", err)
	}
}

func TestAuthorizeSensitiveOperationUsesGenericCredentialFailure(t *testing.T) {
	harness, principal, secret := sensitiveAdmin(t)
	wrongCode := "000000"
	if wrongCode == harness.code(secret) {
		wrongCode = "111111"
	}

	_, passwordErr := harness.service.AuthorizeSensitiveOperation(
		context.Background(), principal, "incorrect password value", harness.code(secret),
		SensitiveOperationDaemonRestart, "192.0.2.45",
	)
	_, totpErr := harness.service.AuthorizeSensitiveOperation(
		context.Background(), principal, testPassword, wrongCode,
		SensitiveOperationDaemonRestart, "192.0.2.46",
	)
	if !errors.Is(passwordErr, ErrInvalidCredentials) || !errors.Is(totpErr, ErrInvalidCredentials) {
		t.Fatalf("factor failures = (%v, %v), want generic ErrInvalidCredentials", passwordErr, totpErr)
	}
}

func TestAuthorizeSensitiveOperationRejectsRecoveryCode(t *testing.T) {
	harness, principal, _ := sensitiveAdmin(t)
	codes, err := auth.GenerateRecoveryCodes(1)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes() returned error: %v", err)
	}
	if err := harness.db.ReplaceRecoveryCodes(context.Background(), principal.Account.ID, []string{codes[0].Hash}); err != nil {
		t.Fatalf("ReplaceRecoveryCodes() returned error: %v", err)
	}
	_, err = harness.service.AuthorizeSensitiveOperation(
		context.Background(), principal, testPassword, codes[0].Code,
		SensitiveOperationDaemonRestart, sensitiveTestAddress,
	)
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("AuthorizeSensitiveOperation() returned %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthorizeSensitiveOperationRevalidatesBearerSession(t *testing.T) {
	t.Run("revoked", func(t *testing.T) {
		harness, principal, secret := sensitiveAdmin(t)
		if err := harness.db.RevokeAccountSession(context.Background(), principal.Session.ID, "test revocation"); err != nil {
			t.Fatalf("RevokeAccountSession() returned error: %v", err)
		}
		_, err := harness.service.AuthorizeSensitiveOperation(
			context.Background(), principal, testPassword, harness.code(secret),
			SensitiveOperationDaemonRestart, sensitiveTestAddress,
		)
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("AuthorizeSensitiveOperation() returned %v, want ErrUnauthorized", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		harness, principal, secret := sensitiveAdmin(t)
		harness.now = harness.now.Add(2 * time.Hour)
		_, err := harness.service.AuthorizeSensitiveOperation(
			context.Background(), principal, testPassword, harness.code(secret),
			SensitiveOperationDaemonRestart, sensitiveTestAddress,
		)
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("AuthorizeSensitiveOperation() returned %v, want ErrUnauthorized", err)
		}
	})
}

func TestSensitiveOperationThrottleIsDedicated(t *testing.T) {
	harness, principal, secret := sensitiveAdmin(t)
	for attempt := 0; attempt < 5; attempt++ {
		_, err := harness.service.AuthorizeSensitiveOperation(
			context.Background(), principal, "incorrect password value", harness.code(secret),
			SensitiveOperationDaemonRestart, sensitiveTestAddress,
		)
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d returned %v, want ErrInvalidCredentials", attempt+1, err)
		}
	}
	_, err := harness.service.AuthorizeSensitiveOperation(
		context.Background(), principal, testPassword, harness.code(secret),
		SensitiveOperationDaemonRestart, sensitiveTestAddress,
	)
	if !errors.Is(err, ErrThrottled) {
		t.Fatalf("throttled attempt returned %v, want ErrThrottled", err)
	}
	account, err := harness.db.GetAccount(context.Background(), principal.Account.ID)
	if err != nil {
		t.Fatalf("GetAccount() returned error: %v", err)
	}
	if !account.IsLocked(harness.now) {
		t.Fatal("sensitive-operation failures did not preserve account lockout behavior")
	}
}

func TestSensitiveAuthorizationExpirationAndRevocation(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		harness, principal, secret := sensitiveAdmin(t)
		authorization := authorizeRestart(t, harness, principal, secret)
		harness.now = harness.now.Add(DefaultSensitiveAuthorizationTTL)
		err := harness.service.ConsumeSensitiveAuthorization(
			context.Background(), principal, authorization.Token, SensitiveOperationDaemonRestart,
		)
		if !errors.Is(err, ErrInvalidSensitiveAuthorization) {
			t.Fatalf("ConsumeSensitiveAuthorization() returned %v, want ErrInvalidSensitiveAuthorization", err)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		harness, principal, secret := sensitiveAdmin(t)
		authorization := authorizeRestart(t, harness, principal, secret)
		if !harness.service.RevokeSensitiveAuthorization(authorization.Token) {
			t.Fatal("RevokeSensitiveAuthorization() did not revoke the authorization")
		}
		err := harness.service.ConsumeSensitiveAuthorization(
			context.Background(), principal, authorization.Token, SensitiveOperationDaemonRestart,
		)
		if !errors.Is(err, ErrInvalidSensitiveAuthorization) {
			t.Fatalf("ConsumeSensitiveAuthorization() returned %v, want ErrInvalidSensitiveAuthorization", err)
		}
	})

	t.Run("cleared", func(t *testing.T) {
		harness, principal, secret := sensitiveAdmin(t)
		authorization := authorizeRestart(t, harness, principal, secret)
		harness.service.ClearSensitiveAuthorizations()
		err := harness.service.ConsumeSensitiveAuthorization(
			context.Background(), principal, authorization.Token, SensitiveOperationDaemonRestart,
		)
		if !errors.Is(err, ErrInvalidSensitiveAuthorization) {
			t.Fatalf("ConsumeSensitiveAuthorization() returned %v, want ErrInvalidSensitiveAuthorization", err)
		}
	})
}

func TestSensitiveAuthorizationIsSingleUse(t *testing.T) {
	harness, principal, secret := sensitiveAdmin(t)
	authorization := authorizeRestart(t, harness, principal, secret)
	if err := harness.service.ConsumeSensitiveAuthorization(
		context.Background(), principal, authorization.Token, SensitiveOperationDaemonRestart,
	); err != nil {
		t.Fatalf("first ConsumeSensitiveAuthorization() returned error: %v", err)
	}
	if err := harness.service.ConsumeSensitiveAuthorization(
		context.Background(), principal, authorization.Token, SensitiveOperationDaemonRestart,
	); !errors.Is(err, ErrInvalidSensitiveAuthorization) {
		t.Fatalf("second ConsumeSensitiveAuthorization() returned %v, want ErrInvalidSensitiveAuthorization", err)
	}
}

func TestSensitiveAuthorizationConcurrentConsumption(t *testing.T) {
	harness, principal, secret := sensitiveAdmin(t)
	authorization := authorizeRestart(t, harness, principal, secret)
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			results <- harness.service.ConsumeSensitiveAuthorization(
				context.Background(), principal, authorization.Token, SensitiveOperationDaemonRestart,
			)
		}()
	}
	ready.Wait()
	close(start)

	succeeded := 0
	rejected := 0
	for range 2 {
		err := <-results
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrInvalidSensitiveAuthorization) {
			rejected++
		} else {
			t.Fatalf("ConsumeSensitiveAuthorization() returned unexpected error: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent consumption results: %d succeeded, %d rejected; want 1 and 1", succeeded, rejected)
	}
}

func TestSensitiveAuthorizationBinding(t *testing.T) {
	harness, principal, secret := sensitiveAdmin(t)
	authorization := authorizeRestart(t, harness, principal, secret)

	otherAccount, _ := activePrincipalForRole(t, harness, "other-user", state.AccountRoleUser)
	if err := harness.service.ConsumeSensitiveAuthorization(
		context.Background(), otherAccount, authorization.Token, SensitiveOperationDaemonRestart,
	); !errors.Is(err, ErrInvalidSensitiveAuthorization) {
		t.Fatalf("wrong-account consumption returned %v, want ErrInvalidSensitiveAuthorization", err)
	}

	otherToken, err := auth.GenerateSessionToken()
	if err != nil {
		t.Fatalf("GenerateSessionToken() returned error: %v", err)
	}
	otherSession, err := harness.db.CreateAccountSession(
		context.Background(), principal.Account.ID, otherToken.Hash, harness.now.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("CreateAccountSession() returned error: %v", err)
	}
	otherPrincipal := Principal{Account: principal.Account, Session: otherSession}
	if err := harness.service.ConsumeSensitiveAuthorization(
		context.Background(), otherPrincipal, authorization.Token, SensitiveOperationDaemonRestart,
	); !errors.Is(err, ErrInvalidSensitiveAuthorization) {
		t.Fatalf("wrong-session consumption returned %v, want ErrInvalidSensitiveAuthorization", err)
	}
	if err := harness.service.ConsumeSensitiveAuthorization(
		context.Background(), principal, authorization.Token, "account.disable",
	); !errors.Is(err, ErrInvalidSensitiveAuthorization) {
		t.Fatalf("wrong-purpose consumption returned %v, want ErrInvalidSensitiveAuthorization", err)
	}
	if err := harness.service.ConsumeSensitiveAuthorization(
		context.Background(), principal, authorization.Token, SensitiveOperationDaemonRestart,
	); err != nil {
		t.Fatalf("correctly bound consumption returned error: %v", err)
	}
}

func TestSensitiveAuthorizationAuditRedactsSecrets(t *testing.T) {
	harness, principal, secret := sensitiveAdmin(t)
	code := harness.code(secret)
	wrongPassword := "incorrect sensitive password"
	if _, err := harness.service.AuthorizeSensitiveOperation(
		context.Background(), principal, wrongPassword, code,
		SensitiveOperationDaemonRestart, sensitiveTestAddress,
	); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("failed AuthorizeSensitiveOperation() returned %v, want ErrInvalidCredentials", err)
	}
	authorization := authorizeRestart(t, harness, principal, secret)

	events, err := harness.db.ListAuthEvents(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListAuthEvents() returned error: %v", err)
	}
	foundSuccess := false
	foundFailure := false
	for _, event := range events {
		if event.EventType == state.AuthEventSensitiveAuthorizationSucceeded {
			foundSuccess = true
		}
		if event.EventType == state.AuthEventSensitiveAuthorizationFailed {
			foundFailure = true
		}
		combined := event.Outcome + " " + event.Detail
		for _, secretValue := range []string{testPassword, wrongPassword, code, authorization.Token} {
			if strings.Contains(combined, secretValue) {
				t.Fatalf("audit event %q contains sensitive value", event.EventType)
			}
		}
	}
	if !foundSuccess || !foundFailure {
		t.Fatalf("sensitive audit events: success=%t failure=%t, want both", foundSuccess, foundFailure)
	}
}
