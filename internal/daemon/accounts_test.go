package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

// sendAccountRequest performs an account administration request over a
// simulated local connection with the given peer identity.
func sendAccountRequest(
	t *testing.T,
	ctx context.Context,
	db *state.Database,
	manager *accountManager,
	client ClientIdentity,
	request ipc.Request,
) ipc.Response {
	t.Helper()

	server, pipe := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleConnection(ctx, db, &workerGroup{}, nil, server, client, manager)
	}()

	if err := json.NewEncoder(pipe).Encode(request); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	var response ipc.Response
	if err := json.NewDecoder(pipe).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	pipe.Close()
	<-done

	return response
}

func newTestAccountManager(t *testing.T, db *state.Database) *accountManager {
	t.Helper()
	return newAccountManager(t.TempDir(), db, time.Hour)
}

func TestAccountAdministrationRequiresTheDaemonUserIdentity(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenAt(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()

	manager := newTestAccountManager(t, db)

	response := sendAccountRequest(t, ctx, db, manager, ClientIdentity{UID: uint32(os.Geteuid()) + 1}, ipc.Request{
		Command:  "account-bootstrap-admin",
		Username: "alice",
		Password: "correct horse battery staple",
	})

	if response.OK {
		t.Fatal("account administration was allowed for a different local user")
	}
	if !strings.Contains(response.Message, "own user identity") {
		t.Fatalf("message = %q, want an explanation of the identity requirement", response.Message)
	}

	count, err := db.CountAccounts(ctx)
	if err != nil {
		t.Fatalf("CountAccounts() returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("the refused request created %d account(s)", count)
	}
}

func TestAccountBootstrapCreatesAPendingAdministrator(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenAt(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()

	manager := newTestAccountManager(t, db)

	response := sendAccountRequest(t, ctx, db, manager, ClientIdentity{UID: uint32(os.Geteuid())}, ipc.Request{
		Command:  "account-bootstrap-admin",
		Username: "alice",
		Password: "correct horse battery staple",
	})

	if !response.OK {
		t.Fatalf("bootstrap failed: %s", response.Message)
	}
	if response.Account == nil {
		t.Fatal("the bootstrap response carries no account")
	}
	if response.Account.Status != state.AccountStatusPending {
		t.Fatalf("status = %q, want %q", response.Account.Status, state.AccountStatusPending)
	}
	if response.Account.Role != state.AccountRoleAdministrator {
		t.Fatalf("role = %q, want %q", response.Account.Role, state.AccountRoleAdministrator)
	}
	if response.Enrollment == nil {
		t.Fatal("the bootstrap response carries no enrollment material")
	}
	if response.Enrollment.Secret == "" {
		t.Fatal("the enrollment material carries no secret")
	}
	if len(response.Enrollment.RecoveryCodes) == 0 {
		t.Fatal("the enrollment material carries no recovery codes")
	}
}

func TestAccountBootstrapRefusesASecondAdministrator(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenAt(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()

	manager := newTestAccountManager(t, db)

	first := sendAccountRequest(t, ctx, db, manager, ClientIdentity{UID: uint32(os.Geteuid())}, ipc.Request{
		Command:  "account-bootstrap-admin",
		Username: "alice",
		Password: "correct horse battery staple",
	})
	if !first.OK {
		t.Fatalf("bootstrap failed: %s", first.Message)
	}

	second := sendAccountRequest(t, ctx, db, manager, ClientIdentity{UID: uint32(os.Geteuid())}, ipc.Request{
		Command:  "account-bootstrap-admin",
		Username: "mallory",
		Password: "correct horse battery staple",
	})
	if second.OK {
		t.Fatal("a second administrator was created")
	}

	count, err := db.CountAccounts(ctx)
	if err != nil {
		t.Fatalf("CountAccounts() returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("the store holds %d accounts, want 1", count)
	}
}

func TestAccountCommandsAreNotPartOfTheOrdinaryCommandSet(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenAt(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()

	manager := newTestAccountManager(t, db)

	// An unknown command must still be reported as unknown rather than
	// being swallowed by the account dispatcher.
	response := sendAccountRequest(t, ctx, db, manager, ClientIdentity{UID: uint32(os.Geteuid())}, ipc.Request{
		Command: "not-a-command",
	})
	if response.OK {
		t.Fatal("an unknown command was accepted")
	}
	if response.Message != "unknown command" {
		t.Fatalf("message = %q, want %q", response.Message, "unknown command")
	}
}

func TestAccountListingDoesNotExposeSecrets(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenAt(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()

	manager := newTestAccountManager(t, db)

	bootstrap := sendAccountRequest(t, ctx, db, manager, ClientIdentity{UID: uint32(os.Geteuid())}, ipc.Request{
		Command:  "account-bootstrap-admin",
		Username: "alice",
		Password: "correct horse battery staple",
	})
	if !bootstrap.OK {
		t.Fatalf("bootstrap failed: %s", bootstrap.Message)
	}

	listed := sendAccountRequest(t, ctx, db, manager, ClientIdentity{UID: uint32(os.Geteuid())}, ipc.Request{
		Command: "account-list",
	})
	if !listed.OK {
		t.Fatalf("account list failed: %s", listed.Message)
	}
	if len(listed.Accounts) != 1 {
		t.Fatalf("listed %d accounts, want 1", len(listed.Accounts))
	}

	encoded, err := json.Marshal(listed.Accounts)
	if err != nil {
		t.Fatalf("marshal accounts: %v", err)
	}
	if strings.Contains(string(encoded), "correct horse battery staple") {
		t.Fatal("the account listing contains the password")
	}
	if strings.Contains(string(encoded), bootstrap.Enrollment.Secret) {
		t.Fatal("the account listing contains the one-time password secret")
	}
}

func TestRemoteAPIIsDisabledByDefault(t *testing.T) {
	t.Setenv("FORGEHAND_API_ADDRESS", "")
	t.Setenv("FORGEHAND_API_SESSION_TTL", "")

	ctx := context.Background()
	db, err := state.OpenAt(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()

	manager := newTestAccountManager(t, db)

	server, err := startRemoteAPI(ctx, manager)
	if err != nil {
		t.Fatalf("startRemoteAPI() returned error: %v", err)
	}
	if server != nil {
		t.Fatal("the remote API listener was started by default")
	}
}

func TestRemoteAPIRefusesANonLoopbackAddress(t *testing.T) {
	t.Setenv("FORGEHAND_API_ADDRESS", "0.0.0.0:8787")
	t.Setenv("FORGEHAND_API_SESSION_TTL", "")

	ctx := context.Background()
	db, err := state.OpenAt(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()

	manager := newTestAccountManager(t, db)

	server, err := startRemoteAPI(ctx, manager)
	if err == nil {
		if server != nil {
			_ = server.Close()
		}
		t.Fatal("the remote API listener accepted a non-loopback address")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("error = %v, want an explanation of the loopback restriction", err)
	}
}
