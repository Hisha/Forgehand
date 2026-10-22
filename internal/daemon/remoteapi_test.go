package daemon

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

// TestUnixSocketCommandSetIsUnchangedByTheAccountCommands proves that the
// ordinary local socket behaviour survives the introduction of the account
// administration commands.
func TestUnixSocketCommandSetIsUnchangedByTheAccountCommands(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenAt(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()

	project, err := db.CreateProject(ctx, "fixture", t.TempDir())
	if err != nil {
		t.Fatalf("CreateProject() returned error: %v", err)
	}

	manager := newAccountManager(t.TempDir(), db, time.Hour)

	response := sendAccountRequest(t, ctx, db, manager, ClientIdentity{UID: uint32(os.Geteuid())}, ipc.Request{
		Command: "projects",
	})
	if !response.OK {
		t.Fatalf("projects command failed: %s", response.Message)
	}
	if len(response.Projects) != 1 || response.Projects[0].ID != project.ID {
		t.Fatalf("projects = %v, want only %d", response.Projects, project.ID)
	}

	status := sendAccountRequest(t, ctx, db, manager, ClientIdentity{UID: uint32(os.Geteuid())}, ipc.Request{
		Command: "status",
	})
	if !status.OK {
		t.Fatalf("status command failed: %s", status.Message)
	}
	if status.Version == "" {
		t.Fatal("the status command reports no version")
	}
}

// TestRemoteAPIIsReachableOnLoopbackOnly starts the network listener and
// checks that it answers on the loopback interface.
func TestRemoteAPIIsReachableOnLoopbackOnly(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a loopback port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}

	t.Setenv("FORGEHAND_API_ADDRESS", address)
	t.Setenv("FORGEHAND_API_SESSION_TTL", "")

	ctx := context.Background()
	db, err := state.OpenAt(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()

	manager := newAccountManager(t.TempDir(), db, time.Hour)

	server, err := startRemoteAPI(ctx, manager)
	if err != nil {
		t.Fatalf("startRemoteAPI() returned error: %v", err)
	}
	if server == nil {
		t.Fatal("the remote API listener was not started")
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	})

	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(fmt.Sprintf("http://%s/api/v1/health", address))
	if err != nil {
		t.Fatalf("query the remote API: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the remote API response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", response.StatusCode, body)
	}
	if !strings.Contains(string(body), "\"api_version\":\"v1\"") {
		t.Fatalf("the health response does not report the API version: %s", body)
	}
}

// TestRemoteAPIDoesNotModifyTheStateDirectoryWithoutUse proves that the
// daemon does not write authentication material into the state directory
// until the remote API or the account commands are used.
func TestRemoteAPIDoesNotModifyTheStateDirectoryWithoutUse(t *testing.T) {
	stateDir := t.TempDir()

	t.Setenv("FORGEHAND_API_ADDRESS", "")
	t.Setenv("FORGEHAND_API_SESSION_TTL", "")

	ctx := context.Background()
	db, err := state.OpenAt(ctx, stateDir)
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer db.Close()

	manager := newAccountManager(stateDir, db, time.Hour)

	server, err := startRemoteAPI(ctx, manager)
	if err != nil {
		t.Fatalf("startRemoteAPI() returned error: %v", err)
	}
	if server != nil {
		t.Fatal("the remote API listener was started by default")
	}

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("read the state directory: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "auth") {
			t.Fatalf("the disabled remote API created %q in the state directory", entry.Name())
		}
	}
}
