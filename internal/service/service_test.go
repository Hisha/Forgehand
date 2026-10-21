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
	for _, forbidden := range []string{"CapabilityBoundingSet=", "AmbientCapabilities="} {
		if strings.Contains(unit, forbidden) {
			t.Fatalf("user unit contains unsupported capability directive %q:\n%s", forbidden, unit)
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

func TestInstallIsIdempotentAndPreservesExistingEnvironment(t *testing.T) {
	manager, runner := testManager(t)
	if err := manager.Install(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manager.unitPath)
	if err != nil {
		t.Fatal(err)
	}
	custom := "Environment=\"FORGEHAND_PROVIDER_MODEL=operator-model\"\n"
	data = []byte(strings.Replace(string(data), "\n[Install]", "\n"+custom+"[Install]", 1))
	if err := os.WriteFile(manager.unitPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	runner.calls = nil
	if err := manager.Install(); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(manager.unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(updated), custom) != 1 {
		t.Fatalf("idempotent install did not preserve environment once:\n%s", updated)
	}
	if strings.Contains(strings.Join(runner.calls, "\n"), "restart") {
		t.Fatalf("idempotent install restarted service: %v", runner.calls)
	}
}

func TestUnitGenerationRequiresAbsoluteExecutable(t *testing.T) {
	manager, _ := testManager(t)
	manager.executable = "relative/forgehand"
	if _, err := manager.unitContents(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("unitContents error = %v", err)
	}
}

func TestUnitExecutableParsesQuotedAbsolutePath(t *testing.T) {
	path := "/home/user/Forgehand Builds/forgehand"
	unit := managedMarker + "\n[Service]\nExecStart=" + strconv.Quote(path) + " daemon\n"
	got, ok := unitExecutable(unit)
	if !ok || got != path {
		t.Fatalf("unitExecutable = %q, %t", got, ok)
	}
}

func TestUpdateReloadsDefinitionWithoutRestarting(t *testing.T) {
	manager, runner := testManager(t)
	if err := os.MkdirAll(filepath.Dir(manager.unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	oldExecutable := filepath.Join(t.TempDir(), "old", "forgehand")
	unit := managedMarker + "\n[Service]\nExecStart=" + strconv.Quote(oldExecutable) + " daemon\n"
	if err := os.WriteFile(manager.unitPath, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(runner.calls, "\n")
	if !strings.Contains(calls, "systemctl --user daemon-reload") {
		t.Fatalf("update calls:\n%s", calls)
	}
	if strings.Contains(calls, "restart") || strings.Contains(calls, "is-active") {
		t.Fatalf("update unexpectedly restarted or inspected active state:\n%s", calls)
	}
	data, err := os.ReadFile(manager.unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ExecStart="+strconv.Quote(manager.executable)+" daemon") {
		t.Fatalf("updated unit did not select current executable:\n%s", data)
	}
}

func TestUpdatePreservesInstalledStateAndSocketPaths(t *testing.T) {
	manager, _ := testManager(t)
	oldState := filepath.Join(t.TempDir(), "old-state")
	oldSocket := filepath.Join(t.TempDir(), "old.sock")
	if err := os.MkdirAll(filepath.Dir(manager.unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	provider := "FORGEHAND_PROVIDER_API_KEY=keep-secret-reference"
	unit := managedMarker + "\n[Service]\nExecStart=" + strconv.Quote("/old/forgehand") + " daemon\nEnvironment=" + strconv.Quote(config.StateDirEnvironment+"="+oldState) + "\nEnvironment=" + strconv.Quote(config.SocketPathEnvironment+"="+oldSocket) + "\nEnvironment=" + strconv.Quote(provider) + "\nReadWritePaths=/operator/custom/path\n"
	if err := os.WriteFile(manager.unitPath, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manager.unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), oldState) || !strings.Contains(string(data), oldSocket) || !strings.Contains(string(data), provider) || !strings.Contains(string(data), "ReadWritePaths=/operator/custom/path") {
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
	unit := managedMarker + "\n[Service]\nExecStart=" + strconv.Quote(manager.executable) + " daemon\n"
	if err := os.WriteFile(manager.unitPath, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	call := "systemctl --user show " + UnitName + " --property=ActiveState,SubState,MainPID,ExecStart"
	runner.output[call] = "MainPID=123\nExecStart={ path=" + manager.executable + " ; argv[]=" + manager.executable + " daemon ; }\nSubState=running\nActiveState=active"
	state, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed || !state.Managed || state.ActiveState != "active" || state.SubState != "running" || state.MainPID != "123" || !state.ExecutableMatches || state.EffectiveExecutable != manager.executable {
		t.Fatalf("state = %#v", state)
	}
}

func TestUpdateRemovesLegacyEmptyCapabilitiesButPreservesNonemptyCustomization(t *testing.T) {
	manager, _ := testManager(t)
	if err := os.MkdirAll(filepath.Dir(manager.unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	unit := managedMarker + "\n[Service]\nExecStart=\"/old/forgehand\" daemon\nCapabilityBoundingSet=\nAmbientCapabilities=\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE\nEnvironment=\"FORGEHAND_PROVIDER_MODEL=custom\"\n"
	if err := os.WriteFile(manager.unitPath, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manager.unitPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(data)
	if strings.Contains(updated, "CapabilityBoundingSet=\n") || strings.Contains(updated, "AmbientCapabilities=\n") {
		t.Fatalf("legacy empty capability directives remain:\n%s", updated)
	}
	for _, want := range []string{"CapabilityBoundingSet=CAP_NET_BIND_SERVICE", "FORGEHAND_PROVIDER_MODEL=custom"} {
		if !strings.Contains(updated, want) {
			t.Fatalf("update discarded customization %q:\n%s", want, updated)
		}
	}
}

func TestStatusFlagsLegacyCapabilityDirectivesForRepair(t *testing.T) {
	manager, runner := testManager(t)
	if err := os.MkdirAll(filepath.Dir(manager.unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	unit := managedMarker + "\n[Service]\nExecStart=" + strconv.Quote(manager.executable) + " daemon\nCapabilityBoundingSet=\nAmbientCapabilities=\n"
	if err := os.WriteFile(manager.unitPath, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	show := "systemctl --user show " + UnitName + " --property=ActiveState,SubState,MainPID,ExecStart"
	runner.output[show] = "ActiveState=active\nMainPID=41\nExecStart={ path=" + manager.executable + " ; argv[]=" + manager.executable + " daemon ; }"
	state, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !state.ExecutableMatches || !state.NeedsUpdate || !strings.Contains(state.UpdateReason, "capability") {
		t.Fatalf("state = %#v", state)
	}
	if err := manager.Restart(); err == nil || !strings.Contains(err.Error(), "service update") {
		t.Fatalf("Restart error = %v", err)
	}
}

func TestRestartRequiresEffectiveServiceExecutableToMatch(t *testing.T) {
	manager, runner := testManager(t)
	if err := os.MkdirAll(filepath.Dir(manager.unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	unit := managedMarker + "\n[Service]\nExecStart=\"/old/forgehand\" daemon\n"
	if err := os.WriteFile(manager.unitPath, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	show := "systemctl --user show " + UnitName + " --property=ActiveState,SubState,MainPID,ExecStart"
	runner.output[show] = "ActiveState=active\nMainPID=41\nExecStart={ path=/drop-in/forgehand ; argv[]=/drop-in/forgehand daemon ; }"
	err := manager.Restart()
	if err == nil || !strings.Contains(err.Error(), "service update") {
		t.Fatalf("Restart error = %v", err)
	}
	if strings.Contains(strings.Join(runner.calls, "\n"), "systemctl --user restart") {
		t.Fatalf("restart launched mismatched executable: %v", runner.calls)
	}
}

func TestRestartUsesVerifiedCurrentExecutableWithoutRewritingUnit(t *testing.T) {
	manager, runner := testManager(t)
	if err := os.MkdirAll(filepath.Dir(manager.unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	custom := "Environment=\"FORGEHAND_PROVIDER_MODEL=operator-model\""
	unit := managedMarker + "\n[Service]\nExecStart=" + strconv.Quote(manager.executable) + " daemon\n" + custom + "\n"
	if err := os.WriteFile(manager.unitPath, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	show := "systemctl --user show " + UnitName + " --property=ActiveState,SubState,MainPID,ExecStart"
	runner.output[show] = "ActiveState=active\nMainPID=41\nExecStart={ path=" + manager.executable + " ; argv[]=" + manager.executable + " daemon ; }"
	if err := manager.Restart(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(runner.calls, "\n"), "systemctl --user restart "+UnitName) {
		t.Fatalf("restart calls = %v", runner.calls)
	}
	data, err := os.ReadFile(manager.unitPath)
	if err != nil || string(data) != unit {
		t.Fatalf("restart rewrote unit: %q, %v", data, err)
	}
}

func TestUpdateRefusesAmbiguousManagedUnitWithoutOverwriting(t *testing.T) {
	manager, _ := testManager(t)
	if err := os.MkdirAll(filepath.Dir(manager.unitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	original := managedMarker + "\n[Service]\nEnvironment=\"CUSTOM=value\"\n"
	if err := os.WriteFile(manager.unitPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(); err == nil || !strings.Contains(err.Error(), "no service ExecStart") {
		t.Fatalf("Update error = %v", err)
	}
	data, err := os.ReadFile(manager.unitPath)
	if err != nil || string(data) != original {
		t.Fatalf("ambiguous unit was overwritten: %q, %v", data, err)
	}
}
