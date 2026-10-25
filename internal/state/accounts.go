package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrAccountNotFound is returned when an account does not exist.
var ErrAccountNotFound = errors.New("account does not exist")

// ErrAccountSessionNotFound is returned when a session does not exist.
var ErrAccountSessionNotFound = errors.New("account session does not exist")

// Account lifecycle states.
const (
	// AccountStatusPending marks an account whose TOTP enrollment has not
	// been confirmed. A pending account cannot use ordinary API operations.
	AccountStatusPending = "pending"
	// AccountStatusActive marks a fully enrolled account.
	AccountStatusActive = "active"
	// AccountStatusDisabled marks an account that has been revoked by a local
	// administrator. A disabled account can never authenticate.
	AccountStatusDisabled = "disabled"
)

// Account roles.
const (
	AccountRoleAdministrator = "administrator"
	AccountRoleUser          = "user"
)

// Authentication audit event types. These records are durable, attributable,
// and never contain a credential.
const (
	AuthEventAccountCreated      = "auth.account.created"
	AuthEventAccountActivated    = "auth.account.activated"
	AuthEventAccountDisabled     = "auth.account.disabled"
	AuthEventEnrollmentConfirmed = "auth.enrollment.confirmed"
	AuthEventLoginSucceeded      = "auth.login.succeeded"
	AuthEventLoginFailed         = "auth.login.failed"
	AuthEventLoginThrottled      = "auth.login.throttled"
	AuthEventRecoveryCodeUsed    = "auth.recovery.used"
	AuthEventSessionIssued       = "auth.session.issued"
	AuthEventSessionRevoked      = "auth.session.revoked"
	AuthEventProjectGranted      = "auth.project.granted"
	AuthEventProjectRevoked      = "auth.project.revoked"
	AuthEventSessionCreated      = "remote.session.created"
	AuthEventSessionRunSubmitted = "remote.session.run_submitted"
)

// Account is a remote-access identity. It carries no secret material: the
// password hash and the encrypted TOTP secret are stored separately and are
// never returned by the remote API.
type Account struct {
	ID                   int64
	Username             string
	DisplayName          string
	Role                 string
	Status               string
	PasswordHash         string
	TOTPAlgorithm        string
	TOTPSecretCiphertext string
	TOTPConfirmed        bool
	TOTPLastCounter      int64
	FailedAttempts       int
	LockedUntil          *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ActivatedAt          *time.Time
}

// IsLocked reports whether the account is currently locked out because of
// repeated authentication failures.
func (a Account) IsLocked(now time.Time) bool {
	return a.LockedUntil != nil && now.Before(*a.LockedUntil)
}

// AccountSession is an issued remote session. Only the digest of the bearer
// token is stored.
type AccountSession struct {
	ID            int64
	AccountID     int64
	TokenHash     string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	LastSeenAt    *time.Time
	RevokedAt     *time.Time
	RevokedReason string
}

// IsUsable reports whether the session may still authenticate a request.
func (s AccountSession) IsUsable(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}
	return now.Before(s.ExpiresAt)
}

// AuthEvent is a durable authentication audit record.
type AuthEvent struct {
	ID            int64
	EventType     string
	AccountID     *int64
	Username      string
	RemoteAddress string
	Outcome       string
	Detail        string
	CreatedAt     time.Time
}

// CreateAccount inserts a new account. The username is stored lower-cased so
// that lookups are case-insensitive and cannot be used to create two
// accounts that differ only by case.
func (d *Database) CreateAccount(
	ctx context.Context,
	username string,
	displayName string,
	role string,
	passwordHash string,
	totpAlgorithm string,
	totpSecretCiphertext string,
) (Account, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	displayName = strings.TrimSpace(displayName)
	role = strings.TrimSpace(role)

	if username == "" {
		return Account{}, fmt.Errorf("account username must not be empty")
	}
	if passwordHash == "" {
		return Account{}, fmt.Errorf("account password hash must not be empty")
	}
	if role == "" {
		role = AccountRoleUser
	}
	if role != AccountRoleAdministrator && role != AccountRoleUser {
		return Account{}, fmt.Errorf("unknown account role %q", role)
	}
	if totpAlgorithm == "" {
		totpAlgorithm = "SHA1"
	}

	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)

	result, err := d.db.ExecContext(ctx, `
INSERT INTO accounts (
	username,
	display_name,
	role,
	status,
	password_hash,
	totp_algorithm,
	totp_secret_ciphertext,
	totp_confirmed,
	totp_last_counter,
	failed_attempts,
	locked_until,
	created_at,
	updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, -1, 0, NULL, ?, ?)
`,
		username,
		displayName,
		role,
		AccountStatusPending,
		passwordHash,
		totpAlgorithm,
		totpSecretCiphertext,
		nowText,
		nowText,
	)
	if err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Account{}, fmt.Errorf("read account id: %w", err)
	}

	return Account{
		ID:                   id,
		Username:             username,
		DisplayName:          displayName,
		Role:                 role,
		Status:               AccountStatusPending,
		PasswordHash:         passwordHash,
		TOTPAlgorithm:        totpAlgorithm,
		TOTPSecretCiphertext: totpSecretCiphertext,
		TOTPConfirmed:        false,
		TOTPLastCounter:      -1,
		CreatedAt:            now,
		UpdatedAt:            now,
	}, nil
}

const accountColumns = `
	id,
	username,
	display_name,
	role,
	status,
	password_hash,
	totp_algorithm,
	totp_secret_ciphertext,
	totp_confirmed,
	totp_last_counter,
	failed_attempts,
	locked_until,
	created_at,
	updated_at,
	activated_at
`

func scanAccount(scanner interface{ Scan(dest ...any) error }) (Account, error) {
	var (
		account     Account
		confirmed   int
		lockedUntil sql.NullString
		createdText string
		updatedText string
		activatedAt sql.NullString
	)

	err := scanner.Scan(
		&account.ID,
		&account.Username,
		&account.DisplayName,
		&account.Role,
		&account.Status,
		&account.PasswordHash,
		&account.TOTPAlgorithm,
		&account.TOTPSecretCiphertext,
		&confirmed,
		&account.TOTPLastCounter,
		&account.FailedAttempts,
		&lockedUntil,
		&createdText,
		&updatedText,
		&activatedAt,
	)
	if err != nil {
		return Account{}, err
	}

	account.TOTPConfirmed = confirmed != 0

	parsed, err := time.Parse(time.RFC3339Nano, createdText)
	if err != nil {
		return Account{}, fmt.Errorf("parse account created_at: %w", err)
	}
	account.CreatedAt = parsed

	parsed, err = time.Parse(time.RFC3339Nano, updatedText)
	if err != nil {
		return Account{}, fmt.Errorf("parse account updated_at: %w", err)
	}
	account.UpdatedAt = parsed

	if lockedUntil.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, lockedUntil.String)
		if parseErr != nil {
			return Account{}, fmt.Errorf("parse account locked_until: %w", parseErr)
		}
		account.LockedUntil = &value
	}

	if activatedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, activatedAt.String)
		if parseErr != nil {
			return Account{}, fmt.Errorf("parse account activated_at: %w", parseErr)
		}
		account.ActivatedAt = &value
	}

	return account, nil
}

// GetAccount returns an account by identifier.
func (d *Database) GetAccount(ctx context.Context, id int64) (Account, error) {
	if id <= 0 {
		return Account{}, fmt.Errorf("account ID must be positive")
	}
	row := d.db.QueryRowContext(ctx, `
SELECT `+accountColumns+`
FROM accounts
WHERE id = ?
`, id)
	account, err := scanAccount(row)
	if err == sql.ErrNoRows {
		return Account{}, fmt.Errorf("account %d: %w", id, ErrAccountNotFound)
	}
	if err != nil {
		return Account{}, fmt.Errorf("get account %d: %w", id, err)
	}
	return account, nil
}

// GetAccountByUsername returns an account by username. The lookup is
// case-insensitive because usernames are stored lower-cased.
func (d *Database) GetAccountByUsername(ctx context.Context, username string) (Account, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return Account{}, fmt.Errorf("account username must not be empty")
	}
	row := d.db.QueryRowContext(ctx, `
SELECT `+accountColumns+`
FROM accounts
WHERE username = ?
`, username)
	account, err := scanAccount(row)
	if err == sql.ErrNoRows {
		return Account{}, fmt.Errorf("account %q: %w", username, ErrAccountNotFound)
	}
	if err != nil {
		return Account{}, fmt.Errorf("get account %q: %w", username, err)
	}
	return account, nil
}

// ListAccounts returns every account ordered by identifier.
func (d *Database) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT `+accountColumns+`
FROM accounts
ORDER BY id
`)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()

	var accounts []Account
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	return accounts, nil
}

// CountAccounts returns the number of accounts that exist.
func (d *Database) CountAccounts(ctx context.Context) (int, error) {
	var count int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count accounts: %w", err)
	}
	return count, nil
}

// SetAccountPassword replaces the stored password hash.
func (d *Database) SetAccountPassword(ctx context.Context, id int64, passwordHash string) error {
	if passwordHash == "" {
		return fmt.Errorf("account password hash must not be empty")
	}
	result, err := d.db.ExecContext(ctx, `
UPDATE accounts
SET password_hash = ?,
    updated_at = ?
WHERE id = ?
`, passwordHash, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("set account password: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set account password: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("account %d does not exist", id)
	}
	return nil
}

// ActivateAccount marks an account as enrolled and active. It is only called
// after a valid one-time code has been presented, which is what proves
// possession of the authenticator.
func (d *Database) ActivateAccount(ctx context.Context, id int64, counter int64) error {
	now := time.Now().UTC()
	result, err := d.db.ExecContext(ctx, `
UPDATE accounts
SET status = ?,
    totp_confirmed = 1,
    totp_last_counter = ?,
    failed_attempts = 0,
    locked_until = NULL,
    activated_at = ?,
    updated_at = ?
WHERE id = ?
`,
		AccountStatusActive,
		counter,
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return fmt.Errorf("activate account: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("activate account: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("account %d does not exist", id)
	}
	return nil
}

// SetAccountStatus changes the lifecycle status of an account.
func (d *Database) SetAccountStatus(ctx context.Context, id int64, status string) error {
	switch status {
	case AccountStatusPending, AccountStatusActive, AccountStatusDisabled:
	default:
		return fmt.Errorf("unknown account status %q", status)
	}
	result, err := d.db.ExecContext(ctx, `
UPDATE accounts
SET status = ?,
    updated_at = ?
WHERE id = ?
`, status, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("set account status: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set account status: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("account %d does not exist", id)
	}
	return nil
}

// UpdateAccountTOTPCounter records the highest accepted one-time code
// counter. It is used to reject a replayed code.
func (d *Database) UpdateAccountTOTPCounter(ctx context.Context, id int64, counter int64) error {
	result, err := d.db.ExecContext(ctx, `
UPDATE accounts
SET totp_last_counter = ?,
    updated_at = ?
WHERE id = ?
`, counter, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("update account one-time counter: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update account one-time counter: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("account %d does not exist", id)
	}
	return nil
}

// RecordAccountLoginFailure increments the account's failure counter and locks
// the account once the budget is exhausted. The lockout is persisted so that
// restarting the daemon cannot reset it.
func (d *Database) RecordAccountLoginFailure(
	ctx context.Context,
	id int64,
	maxAttempts int,
	lockout time.Duration,
) (locked bool, err error) {
	now := time.Now().UTC()

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin account failure record: %w", err)
	}
	defer tx.Rollback()

	var attempts int
	if err := tx.QueryRowContext(ctx,
		`SELECT failed_attempts FROM accounts WHERE id = ?`,
		id,
	).Scan(&attempts); err != nil {
		if err == sql.ErrNoRows {
			return false, fmt.Errorf("account %d does not exist", id)
		}
		return false, fmt.Errorf("read account failure count: %w", err)
	}

	attempts++
	lockedUntil := ""
	if maxAttempts > 0 && attempts >= maxAttempts {
		locked = true
		lockedUntil = now.Add(lockout).Format(time.RFC3339Nano)
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE accounts
SET failed_attempts = ?,
    locked_until = ?,
    updated_at = ?
WHERE id = ?
`, attempts, nullableText(lockedUntil), now.Format(time.RFC3339Nano), id); err != nil {
		return false, fmt.Errorf("update account failure count: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit account failure record: %w", err)
	}
	return locked, nil
}

// ClearAccountLoginFailures resets the account's failure counter and lock.
func (d *Database) ClearAccountLoginFailures(ctx context.Context, id int64) error {
	if _, err := d.db.ExecContext(ctx, `
UPDATE accounts
SET failed_attempts = 0,
    locked_until = NULL,
    updated_at = ?
WHERE id = ?
`, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return fmt.Errorf("clear account failure count: %w", err)
	}
	return nil
}

// ReplaceRecoveryCodes replaces every recovery code of an account. The codes
// are stored only as digests.
func (d *Database) ReplaceRecoveryCodes(ctx context.Context, accountID int64, hashes []string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace recovery codes: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM account_recovery_codes WHERE account_id = ?`,
		accountID,
	); err != nil {
		return fmt.Errorf("clear recovery codes: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, hash := range hashes {
		if hash == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO account_recovery_codes (account_id, code_hash, used_at, created_at)
VALUES (?, ?, NULL, ?)
`, accountID, hash, now); err != nil {
			return fmt.Errorf("insert recovery code: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace recovery codes: %w", err)
	}
	return nil
}

// ConsumeRecoveryCode marks a recovery code as used. It reports whether the
// code existed for the account and had not already been used. The update is
// conditional so that two concurrent uses of the same code cannot both
// succeed.
func (d *Database) ConsumeRecoveryCode(ctx context.Context, accountID int64, hash string) (bool, error) {
	result, err := d.db.ExecContext(ctx, `
UPDATE account_recovery_codes
SET used_at = ?
WHERE account_id = ? AND code_hash = ? AND used_at IS NULL
`, time.Now().UTC().Format(time.RFC3339Nano), accountID, hash)
	if err != nil {
		return false, fmt.Errorf("consume recovery code: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("consume recovery code: %w", err)
	}
	return affected > 0, nil
}

// InvalidateRecoveryCodes marks every unused recovery code of an account as
// used. It is used after a recovery code has been redeemed so that the
// remaining codes cannot be reused.
func (d *Database) InvalidateRecoveryCodes(ctx context.Context, accountID int64) error {
	if _, err := d.db.ExecContext(ctx, `
UPDATE account_recovery_codes
SET used_at = ?
WHERE account_id = ? AND used_at IS NULL
`, time.Now().UTC().Format(time.RFC3339Nano), accountID); err != nil {
		return fmt.Errorf("invalidate recovery codes: %w", err)
	}
	return nil
}

// ListRecoveryCodeHashes returns the digests of every recovery code of an
// account.
func (d *Database) ListRecoveryCodeHashes(ctx context.Context, accountID int64) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT code_hash
FROM account_recovery_codes
WHERE account_id = ?
ORDER BY id
`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list recovery codes: %w", err)
	}
	defer rows.Close()

	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, fmt.Errorf("scan recovery code: %w", err)
		}
		hashes = append(hashes, hash)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recovery codes: %w", err)
	}
	return hashes, nil
}

// CreateAccountSession records an issued session. Only the token digest is
// stored.
func (d *Database) CreateAccountSession(
	ctx context.Context,
	accountID int64,
	tokenHash string,
	expiresAt time.Time,
) (AccountSession, error) {
	now := time.Now().UTC()
	result, err := d.db.ExecContext(ctx, `
INSERT INTO account_sessions (
	account_id,
	token_hash,
	created_at,
	expires_at,
	last_seen_at,
	revoked_at,
	revoked_reason
)
VALUES (?, ?, ?, ?, ?, NULL, '')
`, accountID, tokenHash, now.Format(time.RFC3339Nano), expiresAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return AccountSession{}, fmt.Errorf("create account session: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return AccountSession{}, fmt.Errorf("read account session id: %w", err)
	}
	return AccountSession{
		ID:         id,
		AccountID:  accountID,
		TokenHash:  tokenHash,
		CreatedAt:  now,
		ExpiresAt:  expiresAt.UTC(),
		LastSeenAt: &now,
	}, nil
}

// GetAccountSessionByTokenHash returns the session matching a token digest.
func (d *Database) GetAccountSessionByTokenHash(ctx context.Context, tokenHash string) (AccountSession, error) {
	row := d.db.QueryRowContext(ctx, `
SELECT
	id,
	account_id,
	token_hash,
	created_at,
	expires_at,
	last_seen_at,
	revoked_at,
	revoked_reason
FROM account_sessions
WHERE token_hash = ?
`, tokenHash)
	session, err := scanAccountSession(row)
	if err == sql.ErrNoRows {
		return AccountSession{}, ErrAccountSessionNotFound
	}
	if err != nil {
		return AccountSession{}, fmt.Errorf("get account session: %w", err)
	}
	return session, nil
}

func scanAccountSession(scanner interface{ Scan(dest ...any) error }) (AccountSession, error) {
	var (
		session     AccountSession
		createdText string
		expiresText string
		lastSeen    sql.NullString
		revokedAt   sql.NullString
	)

	err := scanner.Scan(
		&session.ID,
		&session.AccountID,
		&session.TokenHash,
		&createdText,
		&expiresText,
		&lastSeen,
		&revokedAt,
		&session.RevokedReason,
	)
	if err != nil {
		return AccountSession{}, err
	}

	created, err := time.Parse(time.RFC3339Nano, createdText)
	if err != nil {
		return AccountSession{}, fmt.Errorf("parse session created_at: %w", err)
	}
	session.CreatedAt = created

	expires, err := time.Parse(time.RFC3339Nano, expiresText)
	if err != nil {
		return AccountSession{}, fmt.Errorf("parse session expires_at: %w", err)
	}
	session.ExpiresAt = expires

	if lastSeen.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, lastSeen.String)
		if parseErr != nil {
			return AccountSession{}, fmt.Errorf("parse session last_seen_at: %w", parseErr)
		}
		session.LastSeenAt = &value
	}
	if revokedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, revokedAt.String)
		if parseErr != nil {
			return AccountSession{}, fmt.Errorf("parse session revoked_at: %w", parseErr)
		}
		session.RevokedAt = &value
	}
	return session, nil
}

// TouchAccountSession records that a session was used.
func (d *Database) TouchAccountSession(ctx context.Context, id int64) error {
	if _, err := d.db.ExecContext(ctx, `
UPDATE account_sessions
SET last_seen_at = ?
WHERE id = ?
`, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return fmt.Errorf("touch account session: %w", err)
	}
	return nil
}

// RevokeAccountSession revokes a single session.
func (d *Database) RevokeAccountSession(ctx context.Context, id int64, reason string) error {
	result, err := d.db.ExecContext(ctx, `
UPDATE account_sessions
SET revoked_at = ?,
    revoked_reason = ?
WHERE id = ? AND revoked_at IS NULL
`, time.Now().UTC().Format(time.RFC3339Nano), reason, id)
	if err != nil {
		return fmt.Errorf("revoke account session: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke account session: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("account session %d does not exist or is already revoked", id)
	}
	return nil
}

// RevokeAccountSessions revokes every session of an account.
func (d *Database) RevokeAccountSessions(ctx context.Context, accountID int64, reason string) (int64, error) {
	result, err := d.db.ExecContext(ctx, `
UPDATE account_sessions
SET revoked_at = ?,
    revoked_reason = ?
WHERE account_id = ? AND revoked_at IS NULL
`, time.Now().UTC().Format(time.RFC3339Nano), reason, accountID)
	if err != nil {
		return 0, fmt.Errorf("revoke account sessions: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("revoke account sessions: %w", err)
	}
	return affected, nil
}

// PurgeExpiredAccountSessions deletes sessions that expired before the given
// instant.
func (d *Database) PurgeExpiredAccountSessions(ctx context.Context, before time.Time) (int64, error) {
	result, err := d.db.ExecContext(ctx, `
DELETE FROM account_sessions
WHERE expires_at < ?
`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("purge expired account sessions: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge expired account sessions: %w", err)
	}
	return affected, nil
}

// GrantAccountProject authorizes an account to access a project.
func (d *Database) GrantAccountProject(ctx context.Context, accountID, projectID int64) error {
	if accountID <= 0 || projectID <= 0 {
		return fmt.Errorf("account and project identifiers must be positive")
	}
	if _, err := d.db.ExecContext(ctx, `
INSERT INTO account_projects (account_id, project_id, granted_at)
VALUES (?, ?, ?)
ON CONFLICT(account_id, project_id) DO NOTHING
`, accountID, projectID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("grant account project: %w", err)
	}
	return nil
}

// RevokeAccountProject removes an account's authorization for a project.
func (d *Database) RevokeAccountProject(ctx context.Context, accountID, projectID int64) error {
	if _, err := d.db.ExecContext(ctx, `
DELETE FROM account_projects
WHERE account_id = ? AND project_id = ?
`, accountID, projectID); err != nil {
		return fmt.Errorf("revoke account project: %w", err)
	}
	return nil
}

// ListAccountProjectIDs returns the identifiers of every project an account
// is authorized to access.
func (d *Database) ListAccountProjectIDs(ctx context.Context, accountID int64) ([]int64, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT project_id
FROM account_projects
WHERE account_id = ?
ORDER BY project_id
`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list account projects: %w", err)
	}
	defer rows.Close()

	var identifiers []int64
	for rows.Next() {
		var identifier int64
		if err := rows.Scan(&identifier); err != nil {
			return nil, fmt.Errorf("scan account project: %w", err)
		}
		identifiers = append(identifiers, identifier)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list account projects: %w", err)
	}
	return identifiers, nil
}

// AccountCanAccessProject reports whether an account is authorized to access
// a project.
func (d *Database) AccountCanAccessProject(ctx context.Context, accountID, projectID int64) (bool, error) {
	var authorized int
	err := d.db.QueryRowContext(ctx, `
SELECT 1
FROM account_projects
WHERE account_id = ? AND project_id = ?
`, accountID, projectID).Scan(&authorized)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check account project access: %w", err)
	}
	return true, nil
}

// RecordAuthEvent appends a durable authentication audit record. The record
// never contains a credential, a secret, or a token.
func (d *Database) RecordAuthEvent(ctx context.Context, event AuthEvent) error {
	if event.EventType == "" {
		return fmt.Errorf("audit event type must not be empty")
	}
	var accountID any
	if event.AccountID != nil {
		accountID = *event.AccountID
	}
	if _, err := d.db.ExecContext(ctx, `
INSERT INTO auth_audit_events (
	event_type,
	account_id,
	username,
	remote_address,
	outcome,
	detail,
	created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?)
`,
		event.EventType,
		accountID,
		event.Username,
		event.RemoteAddress,
		event.Outcome,
		event.Detail,
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("record authentication audit event: %w", err)
	}
	return nil
}

// ListAuthEvents returns the most recent authentication audit records.
func (d *Database) ListAuthEvents(ctx context.Context, limit int) ([]AuthEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT
	id,
	event_type,
	account_id,
	username,
	remote_address,
	outcome,
	detail,
	created_at
FROM auth_audit_events
ORDER BY id DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, fmt.Errorf("list authentication audit events: %w", err)
	}
	defer rows.Close()

	var events []AuthEvent
	for rows.Next() {
		var (
			event     AuthEvent
			accountID sql.NullInt64
			created   string
		)
		if err := rows.Scan(
			&event.ID,
			&event.EventType,
			&accountID,
			&event.Username,
			&event.RemoteAddress,
			&event.Outcome,
			&event.Detail,
			&created,
		); err != nil {
			return nil, fmt.Errorf("scan authentication audit event: %w", err)
		}
		if accountID.Valid {
			identifier := accountID.Int64
			event.AccountID = &identifier
		}
		timestamp, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, fmt.Errorf("parse authentication audit timestamp: %w", err)
		}
		event.CreatedAt = timestamp
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list authentication audit events: %w", err)
	}
	return events, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
