package state

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCreateAccountStartsPendingAndWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	account, err := db.CreateAccount(ctx, "Alice", "Alice Example", AccountRoleAdministrator, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	if account.ID == 0 {
		t.Fatal("the account was not assigned an identifier")
	}
	if account.Username != "alice" {
		t.Fatalf("username = %q, want %q", account.Username, "alice")
	}
	if account.Status != AccountStatusPending {
		t.Fatalf("status = %q, want %q", account.Status, AccountStatusPending)
	}
	if account.TOTPConfirmed {
		t.Fatal("a new account is reported as enrolled")
	}
	if account.TOTPLastCounter != -1 {
		t.Fatalf("last counter = %d, want -1", account.TOTPLastCounter)
	}
	if account.ActivatedAt != nil {
		t.Fatal("a new account reports an activation time")
	}
}

func TestCreateAccountRejectsDuplicateUsername(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	if _, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext"); err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	if _, err := db.CreateAccount(ctx, "ALICE", "", AccountRoleUser, "hash", "SHA1", "ciphertext"); err == nil {
		t.Fatal("CreateAccount() accepted a duplicate username in a different case")
	}
}

func TestGetAccountByUsernameIsCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	created, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	found, err := db.GetAccountByUsername(ctx, "ALICE")
	if err != nil {
		t.Fatalf("GetAccountByUsername() returned error: %v", err)
	}
	if found.ID != created.ID {
		t.Fatalf("account ID = %d, want %d", found.ID, created.ID)
	}
}

func TestGetAccountByUsernameReportsMissingAccount(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	_, err := db.GetAccountByUsername(ctx, "nobody")
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("GetAccountByUsername() returned %v, want ErrAccountNotFound", err)
	}
}

func TestActivateAccountMarksAccountActive(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	account, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	if err := db.ActivateAccount(ctx, account.ID, 42); err != nil {
		t.Fatalf("ActivateAccount() returned error: %v", err)
	}

	updated, err := db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccount() returned error: %v", err)
	}
	if updated.Status != AccountStatusActive {
		t.Fatalf("status = %q, want %q", updated.Status, AccountStatusActive)
	}
	if !updated.TOTPConfirmed {
		t.Fatal("the activated account is not marked as enrolled")
	}
	if updated.TOTPLastCounter != 42 {
		t.Fatalf("last counter = %d, want 42", updated.TOTPLastCounter)
	}
	if updated.ActivatedAt == nil {
		t.Fatal("the activated account has no activation time")
	}
}

func TestConsumeAccountTOTPCounterIsAtomic(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	account, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	const counter = int64(42)
	start := make(chan struct{})
	results := make(chan bool, 2)
	errors := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			consumed, consumeErr := db.ConsumeAccountTOTPCounter(ctx, account.ID, counter)
			results <- consumed
			errors <- consumeErr
		}()
	}
	ready.Wait()
	close(start)

	successes := 0
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatalf("ConsumeAccountTOTPCounter() returned error: %v", err)
		}
		if <-results {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent counter consumption succeeded %d times, want 1", successes)
	}

	for _, replay := range []int64{counter - 1, counter} {
		consumed, err := db.ConsumeAccountTOTPCounter(ctx, account.ID, replay)
		if err != nil {
			t.Fatalf("ConsumeAccountTOTPCounter(%d) returned error: %v", replay, err)
		}
		if consumed {
			t.Fatalf("ConsumeAccountTOTPCounter(%d) accepted an old or replayed counter", replay)
		}
	}

	consumed, err := db.ConsumeAccountTOTPCounter(ctx, account.ID, counter+1)
	if err != nil {
		t.Fatalf("ConsumeAccountTOTPCounter(fresh) returned error: %v", err)
	}
	if !consumed {
		t.Fatal("ConsumeAccountTOTPCounter(fresh) rejected a newer counter")
	}
}

func TestActivateAccountDoesNotRegressConsumedTOTPCounter(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	account, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}
	consumed, err := db.ConsumeAccountTOTPCounter(ctx, account.ID, 43)
	if err != nil || !consumed {
		t.Fatalf("ConsumeAccountTOTPCounter() = (%t, %v), want (true, nil)", consumed, err)
	}
	if err := db.ActivateAccount(ctx, account.ID, 42); err != nil {
		t.Fatalf("ActivateAccount() returned error: %v", err)
	}
	updated, err := db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccount() returned error: %v", err)
	}
	if updated.TOTPLastCounter != 43 {
		t.Fatalf("last counter = %d, want 43", updated.TOTPLastCounter)
	}
}

func TestRecordAccountLoginFailureLocksTheAccount(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	account, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		locked, err := db.RecordAccountLoginFailure(ctx, account.ID, 3, time.Hour)
		if err != nil {
			t.Fatalf("RecordAccountLoginFailure() returned error: %v", err)
		}
		if locked != (attempt == 3) {
			t.Fatalf("attempt %d reported locked = %t", attempt, locked)
		}
	}

	updated, err := db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccount() returned error: %v", err)
	}
	if updated.LockedUntil == nil {
		t.Fatal("the locked account has no lockout time")
	}
	if !updated.IsLocked(time.Now()) {
		t.Fatal("the account is not reported as locked")
	}
}

func TestClearAccountLoginFailuresRemovesTheLock(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	account, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	if _, err := db.RecordAccountLoginFailure(ctx, account.ID, 1, time.Hour); err != nil {
		t.Fatalf("RecordAccountLoginFailure() returned error: %v", err)
	}
	if err := db.ClearAccountLoginFailures(ctx, account.ID); err != nil {
		t.Fatalf("ClearAccountLoginFailures() returned error: %v", err)
	}

	updated, err := db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetAccount() returned error: %v", err)
	}
	if updated.LockedUntil != nil {
		t.Fatal("the lockout was not cleared")
	}
	if updated.FailedAttempts != 0 {
		t.Fatalf("failed attempts = %d, want 0", updated.FailedAttempts)
	}
}

func TestRecoveryCodeCanBeConsumedOnlyOnce(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	account, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	if err := db.ReplaceRecoveryCodes(ctx, account.ID, []string{"hash-one", "hash-two"}); err != nil {
		t.Fatalf("ReplaceRecoveryCodes() returned error: %v", err)
	}

	consumed, err := db.ConsumeRecoveryCode(ctx, account.ID, "hash-one")
	if err != nil {
		t.Fatalf("ConsumeRecoveryCode() returned error: %v", err)
	}
	if !consumed {
		t.Fatal("the first use of the recovery code was rejected")
	}

	consumed, err = db.ConsumeRecoveryCode(ctx, account.ID, "hash-one")
	if err != nil {
		t.Fatalf("ConsumeRecoveryCode() returned error: %v", err)
	}
	if consumed {
		t.Fatal("the recovery code was accepted twice")
	}

	consumed, err = db.ConsumeRecoveryCode(ctx, account.ID, "hash-two")
	if err != nil {
		t.Fatalf("ConsumeRecoveryCode() returned error: %v", err)
	}
	if !consumed {
		t.Fatal("the second recovery code was rejected")
	}
}

func TestConsumeRecoveryCodeRejectsAnotherAccount(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	first, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}
	second, err := db.CreateAccount(ctx, "bob", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	if err := db.ReplaceRecoveryCodes(ctx, first.ID, []string{"shared-hash"}); err != nil {
		t.Fatalf("ReplaceRecoveryCodes() returned error: %v", err)
	}

	consumed, err := db.ConsumeRecoveryCode(ctx, second.ID, "shared-hash")
	if err != nil {
		t.Fatalf("ConsumeRecoveryCode() returned error: %v", err)
	}
	if consumed {
		t.Fatal("a recovery code was consumed by a different account")
	}
}

func TestInvalidateRecoveryCodesMarksRemainingCodesUsed(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	account, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	if err := db.ReplaceRecoveryCodes(ctx, account.ID, []string{"one", "two", "three"}); err != nil {
		t.Fatalf("ReplaceRecoveryCodes() returned error: %v", err)
	}
	if err := db.InvalidateRecoveryCodes(ctx, account.ID); err != nil {
		t.Fatalf("InvalidateRecoveryCodes() returned error: %v", err)
	}

	for _, hash := range []string{"one", "two", "three"} {
		consumed, err := db.ConsumeRecoveryCode(ctx, account.ID, hash)
		if err != nil {
			t.Fatalf("ConsumeRecoveryCode() returned error: %v", err)
		}
		if consumed {
			t.Fatalf("the invalidated recovery code %q was still usable", hash)
		}
	}
}

func TestAccountSessionExpiresAndCanBeRevoked(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	account, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	expiresAt := time.Now().Add(time.Hour)
	session, err := db.CreateAccountSession(ctx, account.ID, "token-hash", expiresAt)
	if err != nil {
		t.Fatalf("CreateAccountSession() returned error: %v", err)
	}
	if !session.IsUsable(time.Now()) {
		t.Fatal("a fresh session is not usable")
	}
	if session.IsUsable(expiresAt.Add(time.Second)) {
		t.Fatal("an expired session is still usable")
	}

	if err := db.RevokeAccountSession(ctx, session.ID, "signed out"); err != nil {
		t.Fatalf("RevokeAccountSession() returned error: %v", err)
	}

	reloaded, err := db.GetAccountSessionByTokenHash(ctx, "token-hash")
	if err != nil {
		t.Fatalf("GetAccountSessionByTokenHash() returned error: %v", err)
	}
	if reloaded.IsUsable(time.Now()) {
		t.Fatal("a revoked session is still usable")
	}
	if reloaded.RevokedReason != "signed out" {
		t.Fatalf("revocation reason = %q, want %q", reloaded.RevokedReason, "signed out")
	}
}

func TestRevokeAccountSessionsRevokesEverySession(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	account, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	for _, hash := range []string{"one", "two"} {
		if _, err := db.CreateAccountSession(ctx, account.ID, hash, time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("CreateAccountSession() returned error: %v", err)
		}
	}

	revoked, err := db.RevokeAccountSessions(ctx, account.ID, "disabled")
	if err != nil {
		t.Fatalf("RevokeAccountSessions() returned error: %v", err)
	}
	if revoked != 2 {
		t.Fatalf("revoked %d sessions, want 2", revoked)
	}
}

func TestPurgeExpiredAccountSessionsRemovesOnlyExpiredSessions(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	account, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	if _, err := db.CreateAccountSession(ctx, account.ID, "expired", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("CreateAccountSession() returned error: %v", err)
	}
	if _, err := db.CreateAccountSession(ctx, account.ID, "current", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateAccountSession() returned error: %v", err)
	}

	purged, err := db.PurgeExpiredAccountSessions(ctx, time.Now())
	if err != nil {
		t.Fatalf("PurgeExpiredAccountSessions() returned error: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged %d sessions, want 1", purged)
	}

	if _, err := db.GetAccountSessionByTokenHash(ctx, "current"); err != nil {
		t.Fatalf("the current session was purged: %v", err)
	}
}

func TestAccountProjectGrantsAreScopedToTheAccount(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	account, err := db.CreateAccount(ctx, "alice", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}
	other, err := db.CreateAccount(ctx, "bob", "", AccountRoleUser, "hash", "SHA1", "ciphertext")
	if err != nil {
		t.Fatalf("CreateAccount() returned error: %v", err)
	}

	project, err := db.CreateProject(ctx, "fixture", t.TempDir())
	if err != nil {
		t.Fatalf("CreateProject() returned error: %v", err)
	}

	if err := db.GrantAccountProject(ctx, account.ID, project.ID); err != nil {
		t.Fatalf("GrantAccountProject() returned error: %v", err)
	}

	allowed, err := db.AccountCanAccessProject(ctx, account.ID, project.ID)
	if err != nil {
		t.Fatalf("AccountCanAccessProject() returned error: %v", err)
	}
	if !allowed {
		t.Fatal("the granted account cannot access the project")
	}

	allowed, err = db.AccountCanAccessProject(ctx, other.ID, project.ID)
	if err != nil {
		t.Fatalf("AccountCanAccessProject() returned error: %v", err)
	}
	if allowed {
		t.Fatal("an ungranted account can access the project")
	}

	identifiers, err := db.ListAccountProjectIDs(ctx, account.ID)
	if err != nil {
		t.Fatalf("ListAccountProjectIDs() returned error: %v", err)
	}
	if len(identifiers) != 1 || identifiers[0] != project.ID {
		t.Fatalf("project identifiers = %v, want [%d]", identifiers, project.ID)
	}
}

func TestRecordAuthEventStoresAttributableRecords(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	accountID := int64(7)
	if err := db.RecordAuthEvent(ctx, AuthEvent{
		EventType:     AuthEventLoginFailed,
		AccountID:     &accountID,
		Username:      "alice",
		RemoteAddress: "192.0.2.10",
		Outcome:       "failed",
		Detail:        "invalid credentials",
	}); err != nil {
		t.Fatalf("RecordAuthEvent() returned error: %v", err)
	}

	events, err := db.ListAuthEvents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAuthEvents() returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}

	event := events[0]
	if event.EventType != AuthEventLoginFailed {
		t.Fatalf("event type = %q, want %q", event.EventType, AuthEventLoginFailed)
	}
	if event.AccountID == nil || *event.AccountID != accountID {
		t.Fatalf("event account = %v, want %d", event.AccountID, accountID)
	}
	if event.Username != "alice" {
		t.Fatalf("event username = %q, want %q", event.Username, "alice")
	}
	if event.RemoteAddress != "192.0.2.10" {
		t.Fatalf("event address = %q, want %q", event.RemoteAddress, "192.0.2.10")
	}
	if event.CreatedAt.IsZero() {
		t.Fatal("the audit event has no timestamp")
	}
}

func TestListSessionsForProjectReturnsOnlyThatProject(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	first, err := db.CreateProject(ctx, "first", t.TempDir())
	if err != nil {
		t.Fatalf("CreateProject() returned error: %v", err)
	}
	second, err := db.CreateProject(ctx, "second", t.TempDir())
	if err != nil {
		t.Fatalf("CreateProject() returned error: %v", err)
	}

	if _, err := db.CreateSessionWithDetails(ctx, "in first", &first.ID, nil, "fake", "", ""); err != nil {
		t.Fatalf("CreateSessionWithDetails() returned error: %v", err)
	}
	if _, err := db.CreateSessionWithDetails(ctx, "in second", &second.ID, nil, "fake", "", ""); err != nil {
		t.Fatalf("CreateSessionWithDetails() returned error: %v", err)
	}
	if _, err := db.CreateSession(ctx, "without project"); err != nil {
		t.Fatalf("CreateSession() returned error: %v", err)
	}

	sessions, err := db.ListSessionsForProject(ctx, first.ID)
	if err != nil {
		t.Fatalf("ListSessionsForProject() returned error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("returned %d sessions, want 1", len(sessions))
	}
	if sessions[0].Title != "in first" {
		t.Fatalf("session title = %q, want %q", sessions[0].Title, "in first")
	}
}
