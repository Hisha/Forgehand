// Package identity implements the transport-independent account and session
// logic of the Forgehand remote API.
//
// It is deliberately free of any HTTP or IPC concerns so that the local Unix
// socket administrative commands and the network API enforce exactly the same
// authentication and authorization rules.
package identity

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Hisha/Forgehand/internal/auth"
	"github.com/Hisha/Forgehand/internal/state"
)

// Authentication and authorization failures. Callers map these onto their
// transport specific status codes. The messages never disclose which part of
// a credential pair was wrong.
var (
	// ErrInvalidCredentials means the presented credentials did not match.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrAccountLocked means the account is locked after repeated failures.
	ErrAccountLocked = errors.New("account is temporarily locked")
	// ErrThrottled means the calling network peer exceeded its budget.
	ErrThrottled = errors.New("too many authentication attempts")
	// ErrEnrollmentRequired means the account has not completed one-time
	// password enrollment and may not use ordinary operations.
	ErrEnrollmentRequired = errors.New("one-time password enrollment is not complete")
	// ErrAccountDisabled means the account has been revoked.
	ErrAccountDisabled = errors.New("account is disabled")
	// ErrAccountExists means an account already exists, so the first
	// administrator bootstrap must not run again.
	ErrAccountExists = errors.New("an account already exists")
	// ErrAccountNotFound means the account does not exist.
	ErrAccountNotFound = errors.New("account does not exist")
	// ErrUnauthorized means the request carried no valid session.
	ErrUnauthorized = errors.New("authentication is required")
	// ErrForbidden means the authenticated identity is not authorized for the
	// requested resource.
	ErrForbidden = errors.New("access is not authorized")
	// ErrNotFound means the requested resource does not exist.
	ErrNotFound = errors.New("resource does not exist")
	// ErrValidation means the request was malformed.
	ErrValidation = errors.New("request is invalid")
)

const (
	// MaxAccountFailures is the number of consecutive failed logins that lock
	// an account. The lock is persisted so that restarting the daemon cannot
	// reset it.
	MaxAccountFailures = 5

	// AccountLockout is how long an account stays locked.
	AccountLockout = 15 * time.Minute

	// DefaultIdleTimeout is the maximum time a remote session may go unused
	// before it stops authenticating requests.
	DefaultIdleTimeout = time.Hour
)

// usernamePattern constrains account names to a conservative, unambiguous
// character set.
var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)

// Service performs account authentication, session issuance, and project
// authorization.
type Service struct {
	db          *state.Database
	masterKey   auth.MasterKey
	throttle    *auth.LoginThrottle
	sessionTTL  time.Duration
	idleTimeout time.Duration
	now         func() time.Time
}

// Options configures a Service.
type Options struct {
	// DB is the persistent state database.
	DB *state.Database
	// MasterKey protects TOTP secrets at rest.
	MasterKey auth.MasterKey
	// SessionTTL is the absolute lifetime of an issued session.
	SessionTTL time.Duration
	// IdleTimeout is the maximum time a session may go unused. A value of
	// zero disables the idle limit.
	IdleTimeout time.Duration
	// Throttle bounds repeated login attempts from one network peer.
	Throttle *auth.LoginThrottle
	// Now overrides the time source. It exists for tests.
	Now func() time.Time
}

// NewService builds a Service.
func NewService(options Options) (*Service, error) {
	if options.DB == nil {
		return nil, errors.New("identity service requires a state database")
	}
	if len(options.MasterKey) != auth.MasterKeyLength {
		return nil, errors.New("identity service requires a valid master key")
	}
	if options.SessionTTL <= 0 {
		options.SessionTTL = 12 * time.Hour
	}
	if options.IdleTimeout <= 0 {
		options.IdleTimeout = DefaultIdleTimeout
	}
	if options.Throttle == nil {
		options.Throttle = auth.NewLoginThrottle()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Service{
		db:          options.DB,
		masterKey:   options.MasterKey,
		throttle:    options.Throttle,
		sessionTTL:  options.SessionTTL,
		idleTimeout: options.IdleTimeout,
		now:         options.Now,
	}, nil
}

// Principal is an authenticated identity together with its session.
type Principal struct {
	Account state.Account
	Session state.AccountSession
}

// SessionResult is the outcome of a successful authentication.
type SessionResult struct {
	Token     string
	ExpiresAt time.Time
	Account   AccountView
	// RecoveryCodes is populated only when a recovery code was redeemed. The
	// codes are shown exactly once.
	RecoveryCodes []string
}

// AccountView is the non-secret projection of an account.
type AccountView struct {
	ID          int64
	Username    string
	DisplayName string
	Role        string
	Status      string
	// TOTPEnrolled reports whether a one-time password is enrolled and
	// confirmed.
	TOTPEnrolled bool
}

// View projects an account onto its non-secret fields.
func View(account state.Account) AccountView {
	return AccountView{
		ID:           account.ID,
		Username:     account.Username,
		DisplayName:  account.DisplayName,
		Role:         account.Role,
		Status:       account.Status,
		TOTPEnrolled: account.TOTPConfirmed,
	}
}

// BootstrapResult carries the one-time enrollment material of a newly created
// account. The secret is shown exactly once and is never logged.
type BootstrapResult struct {
	Account         AccountView
	Secret          string
	ProvisioningURI string
	RecoveryCodes   []string
}

// ValidateUsername checks an account name and returns its canonical form.
func ValidateUsername(username string) (string, error) {
	canonical := strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(canonical) {
		return "", fmt.Errorf(
			"%w: username must be 3 to 64 characters of lowercase letters, digits, dot, underscore, or hyphen",
			ErrValidation,
		)
	}
	return canonical, nil
}

// BootstrapAdministrator creates the first administrator account.
//
// It refuses to run when an account already exists, so it cannot be used to
// add a hidden account to an installation that already has identities. The
// caller is responsible for proving that the request arrived over an
// authorized local channel.
func (s *Service) BootstrapAdministrator(
	ctx context.Context,
	username string,
	displayName string,
	password string,
) (BootstrapResult, error) {
	canonical, err := ValidateUsername(username)
	if err != nil {
		return BootstrapResult{}, err
	}
	if err := auth.ValidatePasswordStrength(password); err != nil {
		return BootstrapResult{}, fmt.Errorf("%w: %s", ErrValidation, err)
	}

	count, err := s.db.CountAccounts(ctx)
	if err != nil {
		return BootstrapResult{}, err
	}
	if count > 0 {
		return BootstrapResult{}, ErrAccountExists
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("%w: %s", ErrValidation, err)
	}

	secret, encoded, err := auth.GenerateTOTPSecret()
	if err != nil {
		return BootstrapResult{}, err
	}

	ciphertext, err := s.masterKey.EncryptSecret(canonical, secret)
	if err != nil {
		return BootstrapResult{}, err
	}

	account, err := s.db.CreateAccount(
		ctx,
		canonical,
		displayName,
		state.AccountRoleAdministrator,
		hash.Encode(),
		string(auth.TOTPAlgorithmSHA1),
		ciphertext,
	)
	if err != nil {
		return BootstrapResult{}, err
	}

	codes, err := auth.GenerateRecoveryCodes(auth.RecoveryCodeCount)
	if err != nil {
		return BootstrapResult{}, err
	}
	hashes := make([]string, 0, len(codes))
	plaintext := make([]string, 0, len(codes))
	for _, code := range codes {
		hashes = append(hashes, code.Hash)
		plaintext = append(plaintext, code.Code)
	}
	if err := s.db.ReplaceRecoveryCodes(ctx, account.ID, hashes); err != nil {
		return BootstrapResult{}, err
	}

	if err := s.recordEvent(ctx, state.AuthEvent{
		EventType: state.AuthEventAccountCreated,
		AccountID: &account.ID,
		Username:  account.Username,
		Outcome:   "enrollment_pending",
		Detail:    "first administrator provisioned through the local administrative channel",
	}); err != nil {
		return BootstrapResult{}, err
	}

	return BootstrapResult{
		Account:         View(account),
		Secret:          encoded,
		ProvisioningURI: auth.ProvisioningURI("Forgehand", account.Username, encoded, auth.TOTPAlgorithmSHA1),
		RecoveryCodes:   plaintext,
	}, nil
}

// ConfirmEnrollment proves possession of the authenticator and activates the
// account. The caller must already be authorized; the local administrative
// path relies on the Unix socket peer check and the remote path supplies the
// account password.
func (s *Service) ConfirmEnrollment(
	ctx context.Context,
	username string,
	totpCode string,
) (state.Account, error) {
	canonical, err := ValidateUsername(username)
	if err != nil {
		return state.Account{}, err
	}

	account, err := s.db.GetAccountByUsername(ctx, canonical)
	if err != nil {
		if errors.Is(err, state.ErrAccountNotFound) {
			return state.Account{}, ErrAccountNotFound
		}
		return state.Account{}, err
	}
	if account.Status == state.AccountStatusActive {
		return state.Account{}, fmt.Errorf("%w: account is already active", ErrValidation)
	}
	if account.Status == state.AccountStatusDisabled {
		return state.Account{}, ErrAccountDisabled
	}

	counter, err := s.verifyTOTP(ctx, account, totpCode)
	if err != nil {
		return state.Account{}, err
	}

	if err := s.db.ActivateAccount(ctx, account.ID, counter); err != nil {
		return state.Account{}, err
	}

	if err := s.recordEvent(ctx, state.AuthEvent{
		EventType: state.AuthEventEnrollmentConfirmed,
		AccountID: &account.ID,
		Username:  account.Username,
		Outcome:   "activated",
		Detail:    "one-time password enrollment confirmed",
	}); err != nil {
		return state.Account{}, err
	}

	return s.db.GetAccount(ctx, account.ID)
}

// EnrollRemote completes enrollment for a pending account over the remote
// API. It requires the account password in addition to a valid one-time code,
// so there is no password-only route anywhere in the system.
func (s *Service) EnrollRemote(
	ctx context.Context,
	username string,
	password string,
	totpCode string,
	remoteAddress string,
) (SessionResult, error) {
	if err := s.throttle.Allow(remoteAddress); err != nil {
		s.auditRemote(ctx, state.AuthEventLoginThrottled, username, remoteAddress, "throttled", "enrollment attempt exceeded the caller failure budget")
		return SessionResult{}, ErrThrottled
	}

	canonical, err := ValidateUsername(username)
	if err != nil {
		s.throttle.RecordFailure(remoteAddress)
		return SessionResult{}, ErrInvalidCredentials
	}

	account, err := s.db.GetAccountByUsername(ctx, canonical)
	if err != nil {
		if errors.Is(err, state.ErrAccountNotFound) {
			s.dummyPasswordWork()
			s.throttle.RecordFailure(remoteAddress)
			s.auditRemote(ctx, state.AuthEventLoginFailed, canonical, remoteAddress, "failed", "unknown account")
			return SessionResult{}, ErrInvalidCredentials
		}
		return SessionResult{}, err
	}

	if account.IsLocked(s.now()) {
		s.auditRemote(ctx, state.AuthEventLoginThrottled, account.Username, remoteAddress, "locked", "account is locked after repeated failures")
		return SessionResult{}, ErrAccountLocked
	}

	if err := auth.VerifyPassword(account.PasswordHash, password); err != nil {
		s.recordFailure(ctx, account, remoteAddress)
		return SessionResult{}, ErrInvalidCredentials
	}

	if account.Status == state.AccountStatusActive {
		return SessionResult{}, fmt.Errorf("%w: account is already active", ErrValidation)
	}
	if account.Status == state.AccountStatusDisabled {
		return SessionResult{}, ErrAccountDisabled
	}

	counter, err := s.verifyTOTP(ctx, account, totpCode)
	if err != nil {
		s.recordFailure(ctx, account, remoteAddress)
		return SessionResult{}, err
	}

	if err := s.db.ActivateAccount(ctx, account.ID, counter); err != nil {
		return SessionResult{}, err
	}

	if err := s.db.ClearAccountLoginFailures(ctx, account.ID); err != nil {
		return SessionResult{}, err
	}
	s.throttle.RecordSuccess(remoteAddress)

	if err := s.recordEvent(ctx, state.AuthEvent{
		EventType:     state.AuthEventEnrollmentConfirmed,
		AccountID:     &account.ID,
		Username:      account.Username,
		RemoteAddress: remoteAddress,
		Outcome:       "activated",
		Detail:        "one-time password enrollment confirmed over the remote API",
	}); err != nil {
		return SessionResult{}, err
	}

	account, err = s.db.GetAccount(ctx, account.ID)
	if err != nil {
		return SessionResult{}, err
	}
	return s.issueSession(ctx, account, remoteAddress, nil)
}

// Authenticate verifies a password and a second factor and issues a session.
//
// Exactly one second factor must be presented: a one-time password or a
// single-use recovery code. There is no password-only path.
func (s *Service) Authenticate(
	ctx context.Context,
	username string,
	password string,
	totpCode string,
	recoveryCode string,
	remoteAddress string,
) (SessionResult, error) {
	if err := s.throttle.Allow(remoteAddress); err != nil {
		s.auditRemote(ctx, state.AuthEventLoginThrottled, username, remoteAddress, "throttled", "login attempt exceeded the caller failure budget")
		return SessionResult{}, ErrThrottled
	}

	canonical, err := ValidateUsername(username)
	if err != nil {
		s.throttle.RecordFailure(remoteAddress)
		return SessionResult{}, ErrInvalidCredentials
	}

	account, err := s.db.GetAccountByUsername(ctx, canonical)
	if err != nil {
		if errors.Is(err, state.ErrAccountNotFound) {
			// Spend the same work as a real verification so that a missing
			// account is not distinguishable by response time.
			s.dummyPasswordWork()
			s.throttle.RecordFailure(remoteAddress)
			s.auditRemote(ctx, state.AuthEventLoginFailed, canonical, remoteAddress, "failed", "unknown account")
			return SessionResult{}, ErrInvalidCredentials
		}
		return SessionResult{}, err
	}

	if account.IsLocked(s.now()) {
		s.auditRemote(ctx, state.AuthEventLoginThrottled, account.Username, remoteAddress, "locked", "account is locked after repeated failures")
		return SessionResult{}, ErrAccountLocked
	}

	if err := auth.VerifyPassword(account.PasswordHash, password); err != nil {
		s.recordFailure(ctx, account, remoteAddress)
		return SessionResult{}, ErrInvalidCredentials
	}

	if account.Status == state.AccountStatusDisabled {
		s.auditRemote(ctx, state.AuthEventLoginFailed, account.Username, remoteAddress, "disabled", "account is disabled")
		return SessionResult{}, ErrAccountDisabled
	}
	if account.Status == state.AccountStatusPending {
		s.auditRemote(ctx, state.AuthEventLoginFailed, account.Username, remoteAddress, "enrollment_required", "one-time password enrollment is not complete")
		return SessionResult{}, ErrEnrollmentRequired
	}

	redeemed := false
	if strings.TrimSpace(recoveryCode) != "" {
		if strings.TrimSpace(totpCode) != "" {
			return SessionResult{}, fmt.Errorf("%w: present only one second factor", ErrValidation)
		}
		if err := s.redeemRecoveryCode(ctx, account, recoveryCode, remoteAddress); err != nil {
			s.recordFailure(ctx, account, remoteAddress)
			return SessionResult{}, err
		}
		redeemed = true
	} else {
		if _, err := s.verifyTOTP(ctx, account, totpCode); err != nil {
			s.recordFailure(ctx, account, remoteAddress)
			return SessionResult{}, err
		}
	}

	if err := s.db.ClearAccountLoginFailures(ctx, account.ID); err != nil {
		return SessionResult{}, err
	}
	s.throttle.RecordSuccess(remoteAddress)

	account, err = s.db.GetAccount(ctx, account.ID)
	if err != nil {
		return SessionResult{}, err
	}

	var freshCodes []string
	if redeemed {
		freshCodes, err = s.rotateRecoveryCodes(ctx, account)
		if err != nil {
			return SessionResult{}, err
		}
	}

	return s.issueSession(ctx, account, remoteAddress, freshCodes)
}

// AuthenticateToken resolves a bearer token into an authenticated principal.
func (s *Service) AuthenticateToken(ctx context.Context, token string) (Principal, error) {
	if strings.TrimSpace(token) == "" {
		return Principal{}, ErrUnauthorized
	}

	session, err := s.db.GetAccountSessionByTokenHash(ctx, auth.HashSessionToken(token))
	if err != nil {
		return Principal{}, ErrUnauthorized
	}

	now := s.now()
	if session.RevokedAt != nil {
		return Principal{}, ErrUnauthorized
	}
	if !now.Before(session.ExpiresAt) {
		s.auditRemote(ctx, state.AuthEventSessionRevoked, "", "", "expired", "session expired")
		return Principal{}, ErrUnauthorized
	}
	if s.idleTimeout > 0 && session.LastSeenAt != nil && now.Sub(*session.LastSeenAt) > s.idleTimeout {
		s.auditRemote(ctx, state.AuthEventSessionRevoked, "", "", "idle_expired", "session expired after a period of inactivity")
		return Principal{}, ErrUnauthorized
	}

	account, err := s.db.GetAccount(ctx, session.AccountID)
	if err != nil {
		return Principal{}, ErrUnauthorized
	}
	if account.Status != state.AccountStatusActive {
		return Principal{}, ErrUnauthorized
	}

	// Record session activity at most once a minute so that a burst of
	// requests cannot turn into a burst of database writes.
	if session.LastSeenAt == nil || now.Sub(*session.LastSeenAt) >= time.Minute {
		if err := s.db.TouchAccountSession(ctx, session.ID); err != nil {
			return Principal{}, err
		}
	}

	return Principal{Account: account, Session: session}, nil
}

// RevokeSession invalidates the session that matches a bearer token.
func (s *Service) RevokeSession(ctx context.Context, token string, remoteAddress string) error {
	if strings.TrimSpace(token) == "" {
		return ErrUnauthorized
	}
	session, err := s.db.GetAccountSessionByTokenHash(ctx, auth.HashSessionToken(token))
	if err != nil {
		return ErrUnauthorized
	}
	if session.RevokedAt != nil {
		return nil
	}
	if err := s.db.RevokeAccountSession(ctx, session.ID, "signed out"); err != nil {
		return err
	}
	return s.recordEvent(ctx, state.AuthEvent{
		EventType:     state.AuthEventSessionRevoked,
		AccountID:     &session.AccountID,
		RemoteAddress: remoteAddress,
		Outcome:       "revoked",
		Detail:        "session signed out",
	})
}

// RevokeAccountSessions invalidates every session of an account. It is used
// when an account is disabled or when a local administrator revokes access.
func (s *Service) RevokeAccountSessions(ctx context.Context, accountID int64, reason string) (int64, error) {
	revoked, err := s.db.RevokeAccountSessions(ctx, accountID, reason)
	if err != nil {
		return 0, err
	}
	return revoked, s.recordEvent(ctx, state.AuthEvent{
		EventType: state.AuthEventSessionRevoked,
		AccountID: &accountID,
		Outcome:   "revoked",
		Detail:    reason,
	})
}

// AuthorizedProjects returns the projects an account is authorized to access.
func (s *Service) AuthorizedProjects(ctx context.Context, principal Principal) ([]state.Project, error) {
	identifiers, err := s.db.ListAccountProjectIDs(ctx, principal.Account.ID)
	if err != nil {
		return nil, err
	}
	if len(identifiers) == 0 {
		return nil, nil
	}

	projects, err := s.db.ListProjects(ctx)
	if err != nil {
		return nil, err
	}

	authorized := make(map[int64]struct{}, len(identifiers))
	for _, identifier := range identifiers {
		authorized[identifier] = struct{}{}
	}

	result := make([]state.Project, 0, len(identifiers))
	for _, project := range projects {
		if _, ok := authorized[project.ID]; ok {
			result = append(result, project)
		}
	}
	return result, nil
}

// CanAccessProject reports whether an account is authorized for a project.
func (s *Service) CanAccessProject(ctx context.Context, principal Principal, projectID int64) (bool, error) {
	return s.db.AccountCanAccessProject(ctx, principal.Account.ID, projectID)
}

// ListProjectSessions returns the sessions of a project the account is
// authorized to access.
func (s *Service) ListProjectSessions(
	ctx context.Context,
	principal Principal,
	projectID int64,
) ([]state.Session, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("%w: project identifier must be positive", ErrValidation)
	}
	authorized, err := s.CanAccessProject(ctx, principal, projectID)
	if err != nil {
		return nil, err
	}
	if !authorized {
		return nil, ErrForbidden
	}
	return s.db.ListSessionsForProject(ctx, projectID)
}

// GetSession returns a persisted session the account is authorized to read.
//
// A session that is not bound to a project has no authorization basis and is
// therefore not readable through the remote API.
func (s *Service) GetSession(
	ctx context.Context,
	principal Principal,
	sessionID int64,
) (state.Session, error) {
	if sessionID <= 0 {
		return state.Session{}, fmt.Errorf("%w: session identifier must be positive", ErrValidation)
	}
	session, err := s.db.GetSession(ctx, sessionID)
	if err != nil {
		return state.Session{}, ErrNotFound
	}
	if session.ProjectID == nil {
		return state.Session{}, ErrNotFound
	}
	authorized, err := s.CanAccessProject(ctx, principal, *session.ProjectID)
	if err != nil {
		return state.Session{}, err
	}
	if !authorized {
		return state.Session{}, ErrNotFound
	}
	return session, nil
}

// GrantProject authorizes an account to access a project.
func (s *Service) GrantProject(ctx context.Context, accountID, projectID int64) error {
	if _, err := s.db.GetAccount(ctx, accountID); err != nil {
		return err
	}
	if _, err := s.db.GetProject(ctx, projectID); err != nil {
		return err
	}
	if err := s.db.GrantAccountProject(ctx, accountID, projectID); err != nil {
		return err
	}
	return s.recordEvent(ctx, state.AuthEvent{
		EventType: state.AuthEventProjectGranted,
		AccountID: &accountID,
		Outcome:   "granted",
		Detail:    fmt.Sprintf("project %d", projectID),
	})
}

// RevokeProject removes an account's authorization for a project.
func (s *Service) RevokeProject(ctx context.Context, accountID, projectID int64) error {
	if err := s.db.RevokeAccountProject(ctx, accountID, projectID); err != nil {
		return err
	}
	return s.recordEvent(ctx, state.AuthEvent{
		EventType: state.AuthEventProjectRevoked,
		AccountID: &accountID,
		Outcome:   "revoked",
		Detail:    fmt.Sprintf("project %d", projectID),
	})
}

// DisableAccount revokes an account and every session it owns.
func (s *Service) DisableAccount(ctx context.Context, accountID int64) error {
	if err := s.db.SetAccountStatus(ctx, accountID, state.AccountStatusDisabled); err != nil {
		return err
	}
	if _, err := s.RevokeAccountSessions(ctx, accountID, "account disabled"); err != nil {
		return err
	}
	return s.recordEvent(ctx, state.AuthEvent{
		EventType: state.AuthEventAccountDisabled,
		AccountID: &accountID,
		Outcome:   "disabled",
		Detail:    "account disabled through the local administrative channel",
	})
}

// LookupAccount returns an account by username without its secrets.
func (s *Service) LookupAccount(ctx context.Context, username string) (state.Account, error) {
	canonical, err := ValidateUsername(username)
	if err != nil {
		return state.Account{}, err
	}
	account, err := s.db.GetAccountByUsername(ctx, canonical)
	if err != nil {
		if errors.Is(err, state.ErrAccountNotFound) {
			return state.Account{}, ErrAccountNotFound
		}
		return state.Account{}, err
	}
	return account, nil
}

// ListAccounts returns every account without its secrets.
func (s *Service) ListAccounts(ctx context.Context) ([]AccountView, error) {
	accounts, err := s.db.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]AccountView, 0, len(accounts))
	for _, account := range accounts {
		views = append(views, View(account))
	}
	return views, nil
}

// verifyTOTP validates a one-time code and records the accepted counter so a
// replayed code is refused.
func (s *Service) verifyTOTP(
	ctx context.Context,
	account state.Account,
	totpCode string,
) (int64, error) {
	if strings.TrimSpace(totpCode) == "" {
		return 0, ErrInvalidCredentials
	}

	secret, err := s.masterKey.DecryptSecret(account.Username, account.TOTPSecretCiphertext)
	if err != nil {
		return 0, ErrInvalidCredentials
	}

	algorithm, err := auth.ParseTOTPAlgorithm(account.TOTPAlgorithm)
	if err != nil {
		return 0, ErrInvalidCredentials
	}

	counter, err := auth.ValidateTOTP(totpCode, secret, algorithm, account.TOTPLastCounter, s.now())
	if err != nil {
		if errors.Is(err, auth.ErrReplay) {
			return 0, ErrInvalidCredentials
		}
		return 0, ErrInvalidCredentials
	}

	if err := s.db.UpdateAccountTOTPCounter(ctx, account.ID, counter); err != nil {
		return 0, err
	}
	return counter, nil
}

// redeemRecoveryCode consumes a single-use recovery code.
func (s *Service) redeemRecoveryCode(
	ctx context.Context,
	account state.Account,
	recoveryCode string,
	remoteAddress string,
) error {
	hash := auth.HashRecoveryCode(recoveryCode)
	consumed, err := s.db.ConsumeRecoveryCode(ctx, account.ID, hash)
	if err != nil {
		return err
	}
	if !consumed {
		return ErrInvalidCredentials
	}
	return s.recordEvent(ctx, state.AuthEvent{
		EventType:     state.AuthEventRecoveryCodeUsed,
		AccountID:     &account.ID,
		Username:      account.Username,
		RemoteAddress: remoteAddress,
		Outcome:       "redeemed",
		Detail:        "single-use recovery code redeemed",
	})
}

// rotateRecoveryCodes invalidates the remaining recovery codes and issues a
// fresh set, which is returned to the caller exactly once.
func (s *Service) rotateRecoveryCodes(ctx context.Context, account state.Account) ([]string, error) {
	if err := s.db.InvalidateRecoveryCodes(ctx, account.ID); err != nil {
		return nil, err
	}
	codes, err := auth.GenerateRecoveryCodes(auth.RecoveryCodeCount)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, 0, len(codes))
	plaintext := make([]string, 0, len(codes))
	for _, code := range codes {
		hashes = append(hashes, code.Hash)
		plaintext = append(plaintext, code.Code)
	}
	if err := s.db.ReplaceRecoveryCodes(ctx, account.ID, hashes); err != nil {
		return nil, err
	}
	return plaintext, nil
}

// issueSession creates a session and returns the bearer token once.
func (s *Service) issueSession(
	ctx context.Context,
	account state.Account,
	remoteAddress string,
	recoveryCodes []string,
) (SessionResult, error) {
	token, err := auth.GenerateSessionToken()
	if err != nil {
		return SessionResult{}, err
	}

	expiresAt := s.now().Add(s.sessionTTL)
	session, err := s.db.CreateAccountSession(ctx, account.ID, token.Hash, expiresAt)
	if err != nil {
		return SessionResult{}, err
	}

	if err := s.recordEvent(ctx, state.AuthEvent{
		EventType:     state.AuthEventSessionIssued,
		AccountID:     &account.ID,
		Username:      account.Username,
		RemoteAddress: remoteAddress,
		Outcome:       "issued",
		Detail:        fmt.Sprintf("session %d", session.ID),
	}); err != nil {
		return SessionResult{}, err
	}

	return SessionResult{
		Token:         token.Token,
		ExpiresAt:     session.ExpiresAt,
		Account:       View(account),
		RecoveryCodes: recoveryCodes,
	}, nil
}

// recordFailure records a failed authentication attempt for an account and
// for the calling peer.
func (s *Service) recordFailure(
	ctx context.Context,
	account state.Account,
	remoteAddress string,
) {
	s.throttle.RecordFailure(remoteAddress)
	locked, err := s.db.RecordAccountLoginFailure(ctx, account.ID, MaxAccountFailures, AccountLockout)
	if err != nil {
		return
	}
	outcome := "failed"
	detail := "invalid credentials"
	if locked {
		outcome = "locked"
		detail = "account locked after repeated failures"
	}
	_ = s.recordEvent(ctx, state.AuthEvent{
		EventType:     state.AuthEventLoginFailed,
		AccountID:     &account.ID,
		Username:      account.Username,
		RemoteAddress: remoteAddress,
		Outcome:       outcome,
		Detail:        detail,
	})
}

// dummyPasswordWork performs an Argon2id derivation against a throwaway salt
// so that a request for an unknown account costs roughly the same as a
// request for a known account.
func (s *Service) dummyPasswordWork() {
	_, _ = auth.HashPassword("forgehand-timing-equalizer")
}

// recordEvent appends an audit record.
func (s *Service) recordEvent(ctx context.Context, event state.AuthEvent) error {
	return s.db.RecordAuthEvent(ctx, event)
}

// auditRemote appends an audit record for a request that had no resolved
// account.
func (s *Service) auditRemote(
	ctx context.Context,
	eventType string,
	username string,
	remoteAddress string,
	outcome string,
	detail string,
) {
	_ = s.recordEvent(ctx, state.AuthEvent{
		EventType:     eventType,
		Username:      username,
		RemoteAddress: remoteAddress,
		Outcome:       outcome,
		Detail:        detail,
	})
}
