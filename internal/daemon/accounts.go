package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Hisha/Forgehand/internal/auth"
	"github.com/Hisha/Forgehand/internal/identity"
	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

// accountManager lazily creates the identity service used by the local
// administrative commands and by the remote API.
//
// The master key that protects one-time password secrets at rest is created
// on first use inside the daemon state directory with owner-only permissions.
// It is never written to the binary and never logged.
type accountManager struct {
	mu         sync.Mutex
	stateDir   string
	database   *state.Database
	sessionTTL time.Duration
	service    *identity.Service
}

func newAccountManager(stateDir string, database *state.Database, sessionTTL time.Duration) *accountManager {
	if sessionTTL <= 0 {
		sessionTTL = defaultAccountSessionTTL
	}
	return &accountManager{
		stateDir:   stateDir,
		database:   database,
		sessionTTL: sessionTTL,
	}
}

// get returns the identity service, creating it on first use.
func (m *accountManager) get(ctx context.Context) (*identity.Service, error) {
	if m == nil {
		return nil, errors.New("account administration is not available")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.service != nil {
		return m.service, nil
	}

	masterKey, err := auth.LoadOrCreateMasterKey(m.stateDir)
	if err != nil {
		return nil, fmt.Errorf("load authentication master key: %w", err)
	}

	service, err := identity.NewService(identity.Options{
		DB:         m.database,
		MasterKey:  masterKey,
		SessionTTL: m.sessionTTL,
	})
	if err != nil {
		return nil, err
	}

	m.service = service
	return service, nil
}

// defaultAccountSessionTTL is the lifetime of a remote session when the
// daemon is started without an explicit configuration.
const defaultAccountSessionTTL = 12 * time.Hour

// requireLocalAdministrator reports whether the Unix socket peer is
// authorized to run account administration commands.
//
// The daemon refuses to run as root, so the daemon's own effective user is
// the service identity. Requiring the peer to share that identity means only
// the owner of the Forgehand installation can provision accounts, even when
// the socket is shared with a group.
func requireLocalAdministrator(client ClientIdentity) error {
	if client.UID != uint32(os.Geteuid()) {
		return fmt.Errorf(
			"local account administration requires the daemon's own user identity (peer UID %d, daemon UID %d)",
			client.UID,
			os.Geteuid(),
		)
	}
	return nil
}

// handleAccountCommand dispatches the local account administration commands.
//
// The second return value reports whether the request was an account command,
// so that the caller can fall through to the ordinary IPC command set.
func handleAccountCommand(
	ctx context.Context,
	request ipc.Request,
	client ClientIdentity,
	manager *accountManager,
) (ipc.Response, bool) {
	switch request.Command {
	case "account-bootstrap-admin",
		"account-activate",
		"account-list",
		"account-grant-project",
		"account-revoke-project",
		"account-disable",
		"account-revoke-sessions":
	default:
		return ipc.Response{}, false
	}

	if manager == nil {
		return ipc.Response{
			OK:      false,
			Message: "account administration is not available",
		}, true
	}

	if err := requireLocalAdministrator(client); err != nil {
		return ipc.Response{
			OK:      false,
			Message: err.Error(),
		}, true
	}

	service, err := manager.get(ctx)
	if err != nil {
		return ipc.Response{
			OK:      false,
			Message: err.Error(),
		}, true
	}

	switch request.Command {
	case "account-bootstrap-admin":
		return bootstrapAdministrator(ctx, request, service), true

	case "account-activate":
		return activateAccount(ctx, request, service), true

	case "account-list":
		return listAccounts(ctx, service), true

	case "account-grant-project":
		return grantProject(ctx, request, service), true

	case "account-revoke-project":
		return revokeProject(ctx, request, service), true

	case "account-disable":
		return disableAccount(ctx, request, service), true

	case "account-revoke-sessions":
		return revokeSessions(ctx, request, service), true
	}

	return ipc.Response{OK: false, Message: "unknown command"}, true
}

// bootstrapAdministrator creates the first administrator account. It is the
// only way to create the first identity, it requires the local
// administrative channel, and it never creates a hidden account: the
// enrollment material is returned to the local operator exactly once.
func bootstrapAdministrator(
	ctx context.Context,
	request ipc.Request,
	service *identity.Service,
) ipc.Response {
	result, err := service.BootstrapAdministrator(
		ctx,
		request.Username,
		request.DisplayName,
		request.Password,
	)
	if err != nil {
		return ipc.Response{OK: false, Message: err.Error()}
	}

	return ipc.Response{
		OK:      true,
		Message: "administrator account created; complete enrollment to activate it",
		Account: &ipc.Account{
			ID:           result.Account.ID,
			Username:     result.Account.Username,
			DisplayName:  result.Account.DisplayName,
			Role:         result.Account.Role,
			Status:       result.Account.Status,
			TOTPEnrolled: result.Account.TOTPEnrolled,
		},
		Enrollment: &ipc.Enrollment{
			Secret:          result.Secret,
			ProvisioningURI: result.ProvisioningURI,
			RecoveryCodes:   result.RecoveryCodes,
		},
	}
}

// activateAccount confirms one-time password enrollment and activates the
// account.
func activateAccount(
	ctx context.Context,
	request ipc.Request,
	service *identity.Service,
) ipc.Response {
	account, err := service.ConfirmEnrollment(ctx, request.Username, request.TOTPCode)
	if err != nil {
		return ipc.Response{OK: false, Message: err.Error()}
	}
	return ipc.Response{
		OK:      true,
		Message: "enrollment confirmed; the account is active",
		Account: &ipc.Account{
			ID:           account.ID,
			Username:     account.Username,
			DisplayName:  account.DisplayName,
			Role:         account.Role,
			Status:       account.Status,
			TOTPEnrolled: account.TOTPConfirmed,
		},
	}
}

// listAccounts returns every account without its secrets.
func listAccounts(ctx context.Context, service *identity.Service) ipc.Response {
	accounts, err := service.ListAccounts(ctx)
	if err != nil {
		return ipc.Response{OK: false, Message: err.Error()}
	}
	encoded := make([]ipc.Account, 0, len(accounts))
	for _, account := range accounts {
		encoded = append(encoded, ipc.Account{
			ID:           account.ID,
			Username:     account.Username,
			DisplayName:  account.DisplayName,
			Role:         account.Role,
			Status:       account.Status,
			TOTPEnrolled: account.TOTPEnrolled,
		})
	}
	return ipc.Response{OK: true, Accounts: encoded}
}

// grantProject authorizes an account to access a project.
func grantProject(
	ctx context.Context,
	request ipc.Request,
	service *identity.Service,
) ipc.Response {
	if request.ProjectID <= 0 {
		return ipc.Response{OK: false, Message: "a project id is required"}
	}
	username := request.Username
	if username == "" {
		return ipc.Response{OK: false, Message: "a username is required"}
	}
	account, err := service.LookupAccount(ctx, username)
	if err != nil {
		return ipc.Response{OK: false, Message: err.Error()}
	}
	if err := service.GrantProject(ctx, account.ID, request.ProjectID); err != nil {
		return ipc.Response{OK: false, Message: err.Error()}
	}
	return ipc.Response{
		OK:      true,
		Message: fmt.Sprintf("account %s may access project %d", account.Username, request.ProjectID),
	}
}

// revokeProject removes an account's authorization for a project.
func revokeProject(
	ctx context.Context,
	request ipc.Request,
	service *identity.Service,
) ipc.Response {
	if request.ProjectID <= 0 {
		return ipc.Response{OK: false, Message: "a project id is required"}
	}
	account, err := service.LookupAccount(ctx, request.Username)
	if err != nil {
		return ipc.Response{OK: false, Message: err.Error()}
	}
	if err := service.RevokeProject(ctx, account.ID, request.ProjectID); err != nil {
		return ipc.Response{OK: false, Message: err.Error()}
	}
	return ipc.Response{
		OK:      true,
		Message: fmt.Sprintf("account %s may no longer access project %d", account.Username, request.ProjectID),
	}
}

// disableAccount revokes an account and every session it owns.
func disableAccount(
	ctx context.Context,
	request ipc.Request,
	service *identity.Service,
) ipc.Response {
	account, err := service.LookupAccount(ctx, request.Username)
	if err != nil {
		return ipc.Response{OK: false, Message: err.Error()}
	}
	if err := service.DisableAccount(ctx, account.ID); err != nil {
		return ipc.Response{OK: false, Message: err.Error()}
	}
	return ipc.Response{
		OK:      true,
		Message: fmt.Sprintf("account %s is disabled", account.Username),
	}
}

// revokeSessions invalidates every session of an account.
func revokeSessions(
	ctx context.Context,
	request ipc.Request,
	service *identity.Service,
) ipc.Response {
	account, err := service.LookupAccount(ctx, request.Username)
	if err != nil {
		return ipc.Response{OK: false, Message: err.Error()}
	}
	revoked, err := service.RevokeAccountSessions(ctx, account.ID, "revoked by the local administrator")
	if err != nil {
		return ipc.Response{OK: false, Message: err.Error()}
	}
	return ipc.Response{
		OK:      true,
		Message: fmt.Sprintf("revoked %d session(s) for account %s", revoked, account.Username),
	}
}
