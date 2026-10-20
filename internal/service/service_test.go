package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/config"
)

type fakeRunner struct {
	calls  []string
	output map[string]string
	errors map[string]error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, call)
	return f.output[call], f.errors[call]
}

func testManager(t *testing.T) (*Manager, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	runner := &fakeRunner{output: map[string]string{}, errors: map[string]error{}}
	return &Manager{
		unitPath:   filepath.Join(dir, "config", UnitName),
		executable: filepath.Join(dir, "bin", "forgehand"),
		stateDir:   filepath.Join(dir, "state"),
		socketPath: filepath.Join(dir, "run", "forgehand.sock"),
		runner:     runner,
	}, runner
}

func TestInstallWritesUserUnitAndNeverStartsIt(t *testing.T) {
	manager, runner := testManager(t)
	if err := manager.Install(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manager.unitPath)
	if err != nil {
		t.Fatal(err)
	}
	unit := string(data)
	for _, want := range []string{managedMarker, manager.executable, manager.stateDir, manager.socketPath, "NoNewPrivileges=yes", "WantedBy=default.target"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
	calls := strings.Join(runner.calls, "\n")
	if !strings.Contains(calls, "systemctl --user daemon-reload") || !strings.Contains(calls, "systemctl --user enable "+UnitName) {
		t.Fatalf("install calls:\n%s", calls)
	}
	if strings.Contains(calls, " start ") || strings.Contains(calls, " --now ") {
		t.Fatalf("install unexpectedly started service:\n%s", calls)
	}
}

func TestInstallRefusesUnmanagedUnit(t *testing.T) {
	manager, runner := testManager(t)
	if err := os.MkdirAll(filepath.Dir(manager.unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.unitPath, []byte("[Service]\nExecStart=/something/else\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := manager.Install()
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("Install error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("systemctl called: %v", runner.calls)
	}
}

func TestUpdateRestartsOnlyWhenAlreadyActive(t *testing.T) {
	manager, runner := testManager(t)
	if err := os.MkdirAll(filepath.Dir(manager.unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.unitPath, []byte(managedMarker+"\nold"), 0o600); err != nil {
		t.Fatal(err)
	}
	activeCall := "systemctl --user is-active " + UnitName
	runner.output[activeCall] = "active"
	if err := manager.Update(); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(runner.calls, "\n")
	if !strings.Contains(calls, "systemctl --user restart "+UnitName) {
		t.Fatalf("update calls:\n%s", calls)
	}
}

func TestUpdatePreservesInstalledStateAndSocketPaths(t *testing.T) {
	manager, runner := testManager(t)
	oldState := filepath.Join(t.TempDir(), "old-state")
	oldSocket := filepath.Join(t.TempDir(), "old.sock")
	if err := os.MkdirAll(filepath.Dir(manager.unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	unit := managedMarker + "\nEnvironment=" + strconv.Quote(config.StateDirEnvironment+"="+oldState) + "\nEnvironment=" + strconv.Quote(config.SocketPathEnvironment+"="+oldSocket) + "\n"
	if err := os.WriteFile(manager.unitPath, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	runner.output["systemctl --user is-active "+UnitName] = "inactive"
	runner.errors["systemctl --user is-active "+UnitName] = fakeExitError{}
	if err := manager.Update(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manager.unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), oldState) || !strings.Contains(string(data), oldSocket) {
		t.Fatalf("updated unit retargeted state:\n%s", data)
	}
}

type fakeExitError struct{}

func (fakeExitError) Error() string { return "exit status 3" }

func TestUninstallPreservesState(t *testing.T) {
	manager, _ := testManager(t)
	if err := os.MkdirAll(filepath.Dir(manager.unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.unitPath, []byte(managedMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(manager.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(manager.stateDir, "forgehand.db")
	if err := os.WriteFile(stateFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manager.unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit still present: %v", err)
	}
	if data, err := os.ReadFile(stateFile); err != nil || string(data) != "keep" {
		t.Fatalf("state not preserved: %q %v", data, err)
	}
}

func TestStatusParsesNamedProperties(t *testing.T) {
	manager, runner := testManager(t)
	if err := os.MkdirAll(filepath.Dir(manager.unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.unitPath, []byte(managedMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	call := "systemctl --user show " + UnitName + " --property=ActiveState,SubState,MainPID"
	runner.output[call] = "MainPID=123\nSubState=running\nActiveState=active"
	state, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed || state.ActiveState != "active" || state.SubState != "running" || state.MainPID != "123" {
		t.Fatalf("state = %#v", state)
	}
}
