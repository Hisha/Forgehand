package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

const daemonHelperEnvironment = "FORGEHAND_TEST_DAEMON_HELPER"

func TestDaemonHelperProcess(t *testing.T) {
	if os.Getenv(daemonHelperEnvironment) != "1" {
		return
	}
	if err := Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func TestConfiguredDaemonStartupSocketAndCleanSIGTERM(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	socketPath := filepath.Join(shortSocketTestDir(t), "forgehand.sock")
	if err := os.Chmod(filepath.Dir(socketPath), 0o770); err != nil {
		t.Fatalf("prepare group-traversable runtime directory: %v", err)
	}
	process := startDaemonProcess(t, stateDir, socketPath, "0660")
	waitForDaemon(t, process, socketPath)

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat configured socket: %v", err)
	}
	if info.Mode().Perm() != 0o660 {
		t.Fatalf("socket mode = %#o, want 0660", info.Mode().Perm())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("socket stat lacks Linux ownership")
	}
	if stat.Uid != uint32(os.Getuid()) || stat.Gid != uint32(os.Getgid()) {
		t.Fatalf("socket owner = %d:%d, want %d:%d", stat.Uid, stat.Gid, os.Getuid(), os.Getgid())
	}

	stopDaemon(t, process, syscall.SIGTERM, true)
	db, err := state.OpenAt(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("open state after SIGTERM: %v", err)
	}
	defer db.Close()
	run, err := db.LastDaemonRun(context.Background())
	if err != nil {
		t.Fatalf("read daemon run: %v", err)
	}
	if run == nil || !run.ShutdownClean {
		t.Fatalf("SIGTERM run not recorded cleanly: %#v", run)
	}
}

func TestDuplicateDaemonRejectedAcrossDifferentSocketPaths(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	socketRoot := shortSocketTestDir(t)
	firstSocket := filepath.Join(socketRoot, "one", "forgehand.sock")
	secondSocket := filepath.Join(socketRoot, "two", "forgehand.sock")
	first := startDaemonProcess(t, stateDir, firstSocket, "0600")
	waitForDaemon(t, first, firstSocket)

	second := newDaemonCommand(stateDir, secondSocket, "0600")
	output, err := second.CombinedOutput()
	if err == nil {
		stopDaemon(t, first, syscall.SIGTERM, true)
		t.Fatal("second daemon unexpectedly started")
	}
	if !strings.Contains(string(output), "owns state directory") {
		stopDaemon(t, first, syscall.SIGTERM, true)
		t.Fatalf("duplicate daemon output = %q", output)
	}
	if _, err := os.Stat(filepath.Dir(secondSocket)); !os.IsNotExist(err) {
		stopDaemon(t, first, syscall.SIGTERM, true)
		t.Fatalf("rejected duplicate created second runtime directory: %v", err)
	}
	stopDaemon(t, first, syscall.SIGTERM, true)
}

func TestDuplicateDaemonRejectedOnSameSocketPath(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	socketPath := filepath.Join(shortSocketTestDir(t), "forgehand.sock")
	first := startDaemonProcess(t, stateDir, socketPath, "0600")
	waitForDaemon(t, first, socketPath)

	second := newDaemonCommand(stateDir, socketPath, "0600")
	output, err := second.CombinedOutput()
	if err == nil {
		stopDaemon(t, first, syscall.SIGTERM, true)
		t.Fatal("second daemon unexpectedly started")
	}
	if !strings.Contains(string(output), "owns state directory") {
		stopDaemon(t, first, syscall.SIGTERM, true)
		t.Fatalf("duplicate daemon output = %q", output)
	}
	stopDaemon(t, first, syscall.SIGTERM, true)
}

func TestDaemonRecoversOwnershipAfterCrash(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	socketPath := filepath.Join(shortSocketTestDir(t), "forgehand.sock")
	first := startDaemonProcess(t, stateDir, socketPath, "0600")
	waitForDaemon(t, first, socketPath)
	stopDaemon(t, first, syscall.SIGKILL, false)

	second := startDaemonProcess(t, stateDir, socketPath, "0600")
	waitForDaemon(t, second, socketPath)
	stopDaemon(t, second, syscall.SIGTERM, true)
}

func TestDaemonPreservesExistingSessionRecovery(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	db, err := state.OpenAt(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("open initial state: %v", err)
	}
	session, err := db.CreateSession(context.Background(), "Interrupted integration session")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := db.StartSessionExecution(context.Background(), session.ID, 5); err != nil {
		t.Fatalf("start session execution: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close initial state: %v", err)
	}

	socketPath := filepath.Join(shortSocketTestDir(t), "forgehand.sock")
	process := startDaemonProcess(t, stateDir, socketPath, "0600")
	waitForDaemon(t, process, socketPath)
	stopDaemon(t, process, syscall.SIGTERM, true)

	db, err = state.OpenAt(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("reopen state: %v", err)
	}
	defer db.Close()
	sessions, err := db.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].State != state.SessionStateInterrupted {
		t.Fatalf("recovered sessions = %#v", sessions)
	}
}

type daemonProcess struct {
	command *exec.Cmd
	output  *bytes.Buffer
}

func startDaemonProcess(t *testing.T, stateDir, socketPath, socketMode string) *daemonProcess {
	t.Helper()
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		t.Skip("non-root daemon integration test requires a non-root test account")
	}
	command := newDaemonCommand(stateDir, socketPath, socketMode)
	output := &bytes.Buffer{}
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		t.Fatalf("start daemon helper: %v", err)
	}
	return &daemonProcess{command: command, output: output}
}

func newDaemonCommand(stateDir, socketPath, socketMode string) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestDaemonHelperProcess$")
	environment := make([]string, 0, len(os.Environ())+4)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "FORGEHAND_") || strings.HasPrefix(value, "XDG_STATE_HOME=") || strings.HasPrefix(value, "XDG_RUNTIME_DIR=") {
			continue
		}
		environment = append(environment, value)
	}
	command.Env = append(environment,
		daemonHelperEnvironment+"=1",
		"FORGEHAND_STATE_DIR="+stateDir,
		"FORGEHAND_SOCKET_PATH="+socketPath,
		"FORGEHAND_SOCKET_MODE="+socketMode,
	)
	return command
}

func waitForDaemon(t *testing.T, process *daemonProcess, socketPath string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
		if err == nil {
			if err := json.NewEncoder(conn).Encode(ipc.Request{Command: "status"}); err == nil {
				var response ipc.Response
				if err := json.NewDecoder(conn).Decode(&response); err == nil && response.OK {
					conn.Close()
					return
				}
			}
			conn.Close()
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = process.command.Process.Kill()
	_ = process.command.Wait()
	t.Fatalf("daemon did not become ready: %s", process.output.String())
}

func stopDaemon(t *testing.T, process *daemonProcess, signal syscall.Signal, wantSuccess bool) {
	t.Helper()
	if err := process.command.Process.Signal(signal); err != nil {
		t.Fatalf("signal daemon: %v", err)
	}
	err := process.command.Wait()
	if wantSuccess && err != nil {
		t.Fatalf("daemon exit: %v\n%s", err, process.output.String())
	}
	if !wantSuccess && err == nil {
		t.Fatalf("crashed daemon exited successfully\n%s", process.output.String())
	}
}

func shortSocketTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".fh-sock-")
	if err != nil {
		t.Fatalf("create short socket directory: %v", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve short socket directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(abs); err != nil {
			t.Errorf("remove short socket directory: %v", err)
		}
	})
	return abs
}
