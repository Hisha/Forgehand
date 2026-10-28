package identity

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Hisha/Forgehand/internal/auth"
	"github.com/Hisha/Forgehand/internal/state"
)

const (
	// SensitiveOperationDaemonRestart is the only sensitive-operation purpose
	// supported in Phase 5B.1. It authorizes a future restart confirmation; it
	// does not itself perform or request a restart.
	SensitiveOperationDaemonRestart = "daemon.restart"

	// DefaultSensitiveAuthorizationTTL keeps step-up authorization deliberately
	// short lived.
	DefaultSensitiveAuthorizationTTL = 60 * time.Second
)

var (
	// ErrInvalidSensitivePurpose rejects unsupported attempts to turn the
	// step-up mechanism into a general authorization framework.
	ErrInvalidSensitivePurpose = errors.New("sensitive operation purpose is invalid")
	// ErrInvalidSensitiveAuthorization intentionally does not distinguish an
	// unknown, expired, revoked, consumed, or incorrectly bound authorization.
	ErrInvalidSensitiveAuthorization = errors.New("sensitive operation authorization is invalid")
)

// SensitiveAuthorization is an opaque, in-memory, single-use authorization.
// The plaintext value is returned once and must never be logged or persisted.
type SensitiveAuthorization struct {
	Token     string
	Purpose   string
	ExpiresAt time.Time
}

type sensitiveAuthorizationRecord struct {
	accountID int64
	sessionID int64
	purpose   string
	expiresAt time.Time
}

type sensitiveAuthorizationStore struct {
	mu      sync.Mutex
	records map[string]sensitiveAuthorizationRecord
}

func newSensitiveAuthorizationStore() *sensitiveAuthorizationStore {
	return &sensitiveAuthorizationStore{records: make(map[string]sensitiveAuthorizationRecord)}
}

func newSensitiveOperationThrottle() *auth.LoginThrottle {
	throttle := auth.NewLoginThrottle()
	throttle.MaxFailures = 5
	throttle.Window = 15 * time.Minute
	throttle.Lockout = 15 * time.Minute
	return throttle
}

// AuthorizeSensitiveOperation revalidates an existing account session and
// performs password-plus-fresh-TOTP step-up authentication for one supported
// administrative purpose. Recovery codes are deliberately not accepted.
func (s *Service) AuthorizeSensitiveOperation(
	ctx context.Context,
	principal Principal,
	password string,
	totpCode string,
	purpose string,
	remoteAddress string,
) (SensitiveAuthorization, error) {
	if purpose != SensitiveOperationDaemonRestart {
		s.auditSensitiveAttempt(ctx, principal, state.AuthEventSensitiveAuthorizationFailed, remoteAddress, "rejected", "unsupported sensitive operation purpose")
		return SensitiveAuthorization{}, ErrInvalidSensitivePurpose
	}
	if err := s.sensitiveThrottle.Allow(remoteAddress); err != nil {
		s.auditSensitiveAttempt(ctx, principal, state.AuthEventSensitiveAuthorizationThrottled, remoteAddress, "throttled", purpose+" step-up exceeded the caller failure budget")
		return SensitiveAuthorization{}, ErrThrottled
	}

	current, err := s.revalidatePrincipal(ctx, principal)
	if err != nil {
		s.auditRemote(ctx, state.AuthEventSensitiveAuthorizationFailed, "", remoteAddress, "failed", purpose+" step-up rejected an invalid session")
		return SensitiveAuthorization{}, ErrUnauthorized
	}
	if current.Account.Role != state.AccountRoleAdministrator {
		s.auditSensitiveAttempt(ctx, current, state.AuthEventSensitiveAuthorizationFailed, remoteAddress, "forbidden", purpose+" requires an administrator")
		return SensitiveAuthorization{}, ErrForbidden
	}
	if current.Account.IsLocked(s.now()) {
		s.auditSensitiveAttempt(ctx, current, state.AuthEventSensitiveAuthorizationThrottled, remoteAddress, "locked", purpose+" account is locked after repeated failures")
		return SensitiveAuthorization{}, ErrAccountLocked
	}

	if err := auth.VerifyPassword(current.Account.PasswordHash, password); err != nil {
		s.recordSensitiveFailure(ctx, current.Account, purpose, remoteAddress)
		return SensitiveAuthorization{}, ErrInvalidCredentials
	}
	if _, err := s.verifyTOTP(ctx, current.Account, totpCode); err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			s.recordSensitiveFailure(ctx, current.Account, purpose, remoteAddress)
			return SensitiveAuthorization{}, ErrInvalidCredentials
		}
		s.auditSensitiveAttempt(ctx, current, state.AuthEventSensitiveAuthorizationFailed, remoteAddress, "error", purpose+" step-up could not verify credentials")
		return SensitiveAuthorization{}, err
	}

	// The factor check may be expensive. Revalidate the session again before
	// issuing an authorization so a session revoked during verification cannot
	// receive a usable result.
	current, err = s.revalidatePrincipal(ctx, current)
	if err != nil {
		s.auditRemote(ctx, state.AuthEventSensitiveAuthorizationFailed, "", remoteAddress, "failed", purpose+" session ended during step-up")
		return SensitiveAuthorization{}, ErrUnauthorized
	}
	if err := s.db.ClearAccountLoginFailures(ctx, current.Account.ID); err != nil {
		return SensitiveAuthorization{}, err
	}

	token, err := auth.GenerateSessionToken()
	if err != nil {
		return SensitiveAuthorization{}, err
	}
	expiresAt := s.now().Add(s.sensitiveAuthorizationTTL)
	if err := s.recordEvent(ctx, state.AuthEvent{
		EventType:     state.AuthEventSensitiveAuthorizationSucceeded,
		AccountID:     &current.Account.ID,
		Username:      current.Account.Username,
		RemoteAddress: remoteAddress,
		Outcome:       "authorized",
		Detail:        purpose + " step-up authorized",
	}); err != nil {
		return SensitiveAuthorization{}, err
	}

	s.sensitiveAuthorizations.mu.Lock()
	s.sensitiveAuthorizations.records[token.Hash] = sensitiveAuthorizationRecord{
		accountID: current.Account.ID,
		sessionID: current.Session.ID,
		purpose:   purpose,
		expiresAt: expiresAt,
	}
	s.sensitiveAuthorizations.mu.Unlock()
	s.sensitiveThrottle.RecordSuccess(remoteAddress)

	return SensitiveAuthorization{Token: token.Token, Purpose: purpose, ExpiresAt: expiresAt}, nil
}

// ConsumeSensitiveAuthorization atomically consumes a valid authorization.
// Exactly one concurrent caller can succeed.
func (s *Service) ConsumeSensitiveAuthorization(
	ctx context.Context,
	principal Principal,
	token string,
	purpose string,
) error {
	if token == "" || purpose != SensitiveOperationDaemonRestart {
		return ErrInvalidSensitiveAuthorization
	}
	current, err := s.revalidatePrincipal(ctx, principal)
	if err != nil {
		return ErrUnauthorized
	}

	hash := auth.HashSessionToken(token)
	now := s.now()
	s.sensitiveAuthorizations.mu.Lock()
	defer s.sensitiveAuthorizations.mu.Unlock()
	record, ok := s.sensitiveAuthorizations.records[hash]
	if !ok {
		return ErrInvalidSensitiveAuthorization
	}
	if !now.Before(record.expiresAt) {
		delete(s.sensitiveAuthorizations.records, hash)
		return ErrInvalidSensitiveAuthorization
	}
	if record.accountID != current.Account.ID ||
		record.sessionID != current.Session.ID ||
		record.purpose != purpose {
		return ErrInvalidSensitiveAuthorization
	}

	delete(s.sensitiveAuthorizations.records, hash)
	return nil
}

// RevokeSensitiveAuthorization invalidates one outstanding authorization.
// It returns false when the value was already expired, consumed, revoked, or
// unknown.
func (s *Service) RevokeSensitiveAuthorization(token string) bool {
	hash := auth.HashSessionToken(token)
	s.sensitiveAuthorizations.mu.Lock()
	defer s.sensitiveAuthorizations.mu.Unlock()
	if _, ok := s.sensitiveAuthorizations.records[hash]; !ok {
		return false
	}
	delete(s.sensitiveAuthorizations.records, hash)
	return true
}

// ClearSensitiveAuthorizations removes every in-memory authorization. Process
// exit naturally provides the same guarantee; this method supports explicit
// service teardown and deterministic tests.
func (s *Service) ClearSensitiveAuthorizations() {
	s.sensitiveAuthorizations.mu.Lock()
	defer s.sensitiveAuthorizations.mu.Unlock()
	s.sensitiveAuthorizations.records = make(map[string]sensitiveAuthorizationRecord)
}

func (s *Service) revalidatePrincipal(ctx context.Context, principal Principal) (Principal, error) {
	if principal.Account.ID <= 0 || principal.Session.ID <= 0 || principal.Session.TokenHash == "" {
		return Principal{}, ErrUnauthorized
	}
	session, err := s.db.GetAccountSessionByTokenHash(ctx, principal.Session.TokenHash)
	if err != nil || session.ID != principal.Session.ID || session.AccountID != principal.Account.ID {
		return Principal{}, ErrUnauthorized
	}
	return s.authenticateSession(ctx, session)
}

func (s *Service) recordSensitiveFailure(
	ctx context.Context,
	account state.Account,
	purpose string,
	remoteAddress string,
) {
	s.sensitiveThrottle.RecordFailure(remoteAddress)
	locked, err := s.db.RecordAccountLoginFailure(ctx, account.ID, MaxAccountFailures, AccountLockout)
	if err != nil {
		return
	}
	outcome := "failed"
	detail := purpose + " step-up rejected invalid credentials"
	if locked {
		outcome = "locked"
		detail = purpose + " account locked after repeated failures"
	}
	_ = s.recordEvent(ctx, state.AuthEvent{
		EventType:     state.AuthEventSensitiveAuthorizationFailed,
		AccountID:     &account.ID,
		Username:      account.Username,
		RemoteAddress: remoteAddress,
		Outcome:       outcome,
		Detail:        detail,
	})
}

func (s *Service) auditSensitiveAttempt(
	ctx context.Context,
	principal Principal,
	eventType string,
	remoteAddress string,
	outcome string,
	detail string,
) {
	var accountID *int64
	if principal.Account.ID > 0 {
		value := principal.Account.ID
		accountID = &value
	}
	_ = s.recordEvent(ctx, state.AuthEvent{
		EventType:     eventType,
		AccountID:     accountID,
		Username:      principal.Account.Username,
		RemoteAddress: remoteAddress,
		Outcome:       outcome,
		Detail:        detail,
	})
}
