package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner records invocations and delegates to a configurable handler so
// tests can simulate systemctl/getent/useradd behavior and failures without
// touching the real system.
type fakeRunner struct {
	mu      sync.Mutex
	calls   []string
	handler func(name string, args ...string) (string, string, error)
}

func (f *fakeRunner) Run(_ context.Context, _ []string, name string, args ...string) (string, string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	f.mu.Unlock()
	if f.handler == nil {
		return "", "", nil
	}
	return f.handler(name, args...)
}

func (f *fakeRunner) called(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// fakeExitError models a command that exited non-zero, exposing an exit status
// the way *exec.ExitError does so lookup classification can be exercised.
type fakeExitError struct {
	code int
	msg  string
}

func (e fakeExitError) Error() string { return e.msg }
func (e fakeExitError) ExitCode() int { return e.code }

// baseHandler models a healthy system: no conflicting units, an existing
// service account and group, and a disabled inactive service.
func baseHandler(name string, args ...string) (string, string, error) {
	switch name {
	case "systemctl":
		if len(args) > 0 {
			switch args[0] {
			case "list-units":
				return "", "", nil
			case "is-active":
				// systemd exits 3 for an inactive unit while still printing
				// the state token.
				return "inactive\n", "", fakeExitError{code: 3, msg: "exit status 3"}
			case "is-enabled":
				// systemd exits 1 for a disabled unit.
				return "disabled\n", "", fakeExitError{code: 1, msg: "exit status 1"}
			}
		}
		return "", "", nil
	case "getent":
		if len(args) > 0 && args[0] == "group" {
			return "forgehand:x:999:\n", "", nil
		}
		if len(args) > 0 && args[0] == "passwd" {
			return "alice:x:1000:1000::/home/alice:/bin/bash\n", "", nil
		}
		return "", "", nil
	}
	return "", "", nil
}

type testEnv struct {
	t        *testing.T
	o        *ops
	runner   *fakeRunner
	dir      string
	listener net.Listener
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	runner := &fakeRunner{handler: baseHandler}
	o := &ops{
		runner:           runner,
		lookPath:         func(string) (string, error) { return "/usr/bin/tool", nil },
		binDest:          filepath.Join(dir, "usr", "bin", "forgehand"),
		unitDest:         filepath.Join(dir, "etc", "systemd", "system", "forgehand@.service"),
		stateDir:         filepath.Join(dir, "var", "lib", "forgehand"),
		runtimeSocket:    filepath.Join(dir, "run", "forgehand", "forgehand.sock"),
		group:            "forgehand",
		templatePath:     filepath.Join(dir, "forgehand@.service"),
		tempDir:          filepath.Join(dir, "tmp"),
		readinessRetries: 1,
		readinessDelay:   0,
		sleep:            func(time.Duration) {},
	}
	if err := os.MkdirAll(o.tempDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &testEnv{t: t, o: o, runner: runner, dir: dir}
}

func (e *testEnv) withSource(content string) string {
	e.t.Helper()
	src := filepath.Join(e.dir, "forgehand-build")
	e.writeExec(src, content)
	return src
}

func (e *testEnv) writeExec(path, content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) write(path, content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) read(path string) string {
	e.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func (e *testEnv) exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (e *testEnv) listenSocket() {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.o.runtimeSocket), 0o755); err != nil {
		e.t.Fatal(err)
	}
	ln, err := net.Listen("unix", e.o.runtimeSocket)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chmod(e.o.runtimeSocket, 0o600); err != nil {
		e.t.Fatal(err)
	}
	e.listener = ln
	e.t.Cleanup(func() { _ = ln.Close() })
}

// ---------------------------------------------------------------------------
// Environment bypasses must not exist.
// ---------------------------------------------------------------------------

func TestNoEnvironmentPathOverrides(t *testing.T) {
	t.Setenv("FORGEHAND_BIN_DEST", filepath.Join(t.TempDir(), "evil-bin"))
	t.Setenv("FORGEHAND_UNIT_DEST", filepath.Join(t.TempDir(), "evil-unit"))
	o := newOps(&fakeRunner{handler: baseHandler})
	if o.binDest != defaultBinDest {
		t.Fatalf("binDest = %q, want default %q", o.binDest, defaultBinDest)
	}
	if o.unitDest != defaultUnitDest {
		t.Fatalf("unitDest = %q, want default %q", o.unitDest, defaultUnitDest)
	}
}

func TestSkipEnvironmentVariablesDoNotBypassChecks(t *testing.T) {
	t.Setenv("FORGEHAND_SKIP_CONFLICT_CHECK", "1")
	t.Setenv("FORGEHAND_SKIP_USER", "1")
	t.Setenv("FORGEHAND_SKIP_SYSTEMCTL", "1")
	t.Setenv("FORGEHAND_SKIP_DAEMON_RELOAD", "1")

	env := newTestEnv(t)
	env.o.lookPath = func(name string) (string, error) {
		if name == "systemctl" {
			return "", errors.New("unavailable")
		}
		return "/usr/bin/" + name, nil
	}
	if err := env.o.detectConflictingInstances("alice"); err == nil {
		t.Fatal("conflict detection bypassed despite FORGEHAND_SKIP_CONFLICT_CHECK")
	}
	if err := env.o.daemonReload(); err == nil {
		t.Fatal("daemon-reload bypassed despite FORGEHAND_SKIP_DAEMON_RELOAD")
	}
	if err := env.o.systemctl("start", "alice", true); err == nil {
		t.Fatal("systemctl bypassed despite FORGEHAND_SKIP_SYSTEMCTL")
	}
}

// ---------------------------------------------------------------------------
// Missing systemd commands and failed queries are hard errors.
// ---------------------------------------------------------------------------

func TestMissingSystemctlIsHardError(t *testing.T) {
	env := newTestEnv(t)
	env.o.lookPath = func(name string) (string, error) {
		if name == "systemctl" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}
	if err := env.o.detectConflictingInstances("alice"); err == nil || !strings.Contains(err.Error(), "systemctl is required") {
		t.Fatalf("detectConflictingInstances error = %v, want systemctl required", err)
	}
	if err := env.o.daemonReload(); err == nil || !strings.Contains(err.Error(), "systemctl is required") {
		t.Fatalf("daemonReload error = %v, want systemctl required", err)
	}
	if err := env.o.systemctl("start", "alice", true); err == nil || !strings.Contains(err.Error(), "systemctl is required") {
		t.Fatalf("systemctl error = %v, want systemctl required", err)
	}
	shared, err := env.o.sharedComponentsInUse("alice")
	if err == nil || shared || !strings.Contains(err.Error(), "systemctl is required") {
		t.Fatalf("sharedComponentsInUse = (%v, %v), want systemctl required", shared, err)
	}
}

func TestFailedConflictQueryIsHardError(t *testing.T) {
	env := newTestEnv(t)
	src := env.withSource("NEWBIN")
	env.runner.handler = func(name string, args ...string) (string, string, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "list-units" {
			return "", "query failed", errors.New("exit status 1")
		}
		return baseHandler(name, args...)
	}
	err := doInstall(installConfig{User: "alice", Source: src}, env.o)
	if err == nil || !strings.Contains(err.Error(), "query active forgehand instances") {
		t.Fatalf("doInstall error = %v, want failed conflict query", err)
	}
	if env.exists(env.o.binDest) {
		t.Fatal("binary was installed despite failed conflict query")
	}
}

func TestConflictingInstanceRefusesOperation(t *testing.T) {
	env := newTestEnv(t)
	src := env.withSource("NEWBIN")
	env.runner.handler = func(name string, args ...string) (string, string, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "list-units" {
			return "forgehand@bob.service loaded active running\n", "", nil
		}
		return baseHandler(name, args...)
	}
	err := doInstall(installConfig{User: "alice", Source: src}, env.o)
	if err == nil || !strings.Contains(err.Error(), "conflicting active instance") {
		t.Fatalf("doInstall error = %v, want conflicting instance", err)
	}
}

// ---------------------------------------------------------------------------
// Failed account validation cannot silently succeed.
// ---------------------------------------------------------------------------

func TestEnsureUserValidationFailures(t *testing.T) {
	t.Run("missing getent", func(t *testing.T) {
		env := newTestEnv(t)
		env.o.lookPath = func(name string) (string, error) {
			if name == "getent" {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + name, nil
		}
		if err := env.o.ensureUser("alice", false); err == nil || !strings.Contains(err.Error(), "getent is required to validate accounts") {
			t.Fatalf("ensureUser error = %v, want getent required", err)
		}
	})

	t.Run("missing account without create", func(t *testing.T) {
		env := newTestEnv(t)
		env.runner.handler = func(name string, args ...string) (string, string, error) {
			if name == "getent" && len(args) > 0 && args[0] == "passwd" {
				return "", "", fakeExitError{code: 2, msg: "exit status 2"}
			}
			return baseHandler(name, args...)
		}
		if err := env.o.ensureUser("alice", false); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("ensureUser error = %v, want does not exist", err)
		}
	})

	t.Run("root account refused", func(t *testing.T) {
		env := newTestEnv(t)
		env.runner.handler = func(name string, args ...string) (string, string, error) {
			if name == "getent" && len(args) > 0 && args[0] == "passwd" {
				return "root:x:0:0:root:/root:/bin/bash\n", "", nil
			}
			return baseHandler(name, args...)
		}
		if err := env.o.ensureUser("root", false); err == nil || !strings.Contains(err.Error(), "root account") {
			t.Fatalf("ensureUser error = %v, want root refusal", err)
		}
	})
}

func TestInstallMissingGetentIsHardError(t *testing.T) {
	env := newTestEnv(t)
	src := env.withSource("NEWBIN")
	env.write(env.o.templatePath, "UNITCONTENT")
	env.o.lookPath = func(name string) (string, error) {
		if name == "getent" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}
	err := doInstall(installConfig{User: "alice", Source: src}, env.o)
	if err == nil || !strings.Contains(err.Error(), "getent is required") {
		t.Fatalf("doInstall error = %v, want getent required", err)
	}
	if env.exists(env.o.binDest) {
		t.Fatal("binary was installed despite failed account validation")
	}
}

// ---------------------------------------------------------------------------
// Successful install sanity check.
// ---------------------------------------------------------------------------

func TestSuccessfulInstall(t *testing.T) {
	env := newTestEnv(t)
	src := env.withSource("NEWBIN")
	env.write(env.o.templatePath, "UNITCONTENT")
	env.listenSocket()

	if err := doInstall(installConfig{User: "alice", Source: src}, env.o); err != nil {
		t.Fatalf("doInstall: %v", err)
	}
	if got := env.read(env.o.binDest); got != "NEWBIN" {
		t.Fatalf("installed binary = %q, want NEWBIN", got)
	}
	if got := env.read(env.o.unitDest); got != "UNITCONTENT" {
		t.Fatalf("installed unit = %q, want UNITCONTENT", got)
	}
	if !env.runner.called("systemctl enable forgehand@alice.service") {
		t.Fatal("expected service to be enabled")
	}
}

// ---------------------------------------------------------------------------
// Rollback preserves existing service, binary, unit, and reports failures.
// ---------------------------------------------------------------------------

func TestUpgradeRollbackPreservesExistingStateOnReplaceFailure(t *testing.T) {
	env := newTestEnv(t)
	src := env.withSource("NEWBIN")
	env.write(env.o.templatePath, "NEWUNIT")
	env.writeExec(env.o.binDest, "OLDBIN")
	env.write(env.o.unitDest, "OLDUNIT")
	// Force replaceBinary to fail by occupying its temporary path with a
	// directory, so the binary copy cannot be written.
	if err := os.MkdirAll(env.o.binDest+".new", 0o755); err != nil {
		t.Fatal(err)
	}
	env.runner.handler = func(name string, args ...string) (string, string, error) {
		if name == "systemctl" && len(args) > 0 {
			switch args[0] {
			case "is-active":
				return "active\n", "", nil
			case "is-enabled":
				return "enabled\n", "", nil
			}
		}
		return baseHandler(name, args...)
	}

	err := doUpgrade(installConfig{User: "alice", Source: src}, env.o)
	if err == nil || !strings.Contains(err.Error(), "upgrade failed and rolled back") {
		t.Fatalf("doUpgrade error = %v, want rolled back", err)
	}
	if got := env.read(env.o.binDest); got != "OLDBIN" {
		t.Fatalf("binary after rollback = %q, want OLDBIN", got)
	}
	if got := env.read(env.o.unitDest); got != "OLDUNIT" {
		t.Fatalf("unit after rollback = %q, want OLDUNIT (must not be deleted)", got)
	}
	if !env.runner.called("systemctl start forgehand@alice.service") {
		t.Fatal("rollback did not restart the previous service")
	}
}

func TestInstallRollbackOnReadinessFailureRestoresState(t *testing.T) {
	env := newTestEnv(t)
	src := env.withSource("NEWBIN")
	env.write(env.o.templatePath, "NEWUNIT")
	env.writeExec(env.o.binDest, "OLDBIN")
	env.write(env.o.unitDest, "OLDUNIT")
	env.listenSocket()
	env.runner.handler = func(name string, args ...string) (string, string, error) {
		if name == env.o.binDest {
			return "", "not ready", errors.New("exit status 1")
		}
		if name == "systemctl" && len(args) > 0 {
			switch args[0] {
			case "is-active":
				return "active\n", "", nil
			case "is-enabled":
				return "enabled\n", "", nil
			}
		}
		return baseHandler(name, args...)
	}

	err := doInstall(installConfig{User: "alice", Source: src}, env.o)
	if err == nil || !strings.Contains(err.Error(), "install failed and rolled back") {
		t.Fatalf("doInstall error = %v, want rolled back", err)
	}
	if got := env.read(env.o.binDest); got != "OLDBIN" {
		t.Fatalf("binary after rollback = %q, want OLDBIN", got)
	}
	if got := env.read(env.o.unitDest); got != "OLDUNIT" {
		t.Fatalf("unit after rollback = %q, want OLDUNIT", got)
	}
	if !env.runner.called("systemctl start forgehand@alice.service") {
		t.Fatal("rollback did not restart the previous service")
	}
}

func TestRollbackFailureIsReportedNotSwallowed(t *testing.T) {
	env := newTestEnv(t)
	src := env.withSource("NEWBIN")
	env.write(env.o.templatePath, "NEWUNIT")
	env.writeExec(env.o.binDest, "OLDBIN")
	env.write(env.o.unitDest, "OLDUNIT")
	env.listenSocket()
	env.runner.handler = func(name string, args ...string) (string, string, error) {
		if name == env.o.binDest {
			return "", "not ready", errors.New("exit status 1")
		}
		if name == "systemctl" && len(args) > 0 {
			switch args[0] {
			case "is-active":
				return "active\n", "", nil
			case "is-enabled":
				return "enabled\n", "", nil
			case "start":
				return "", "cannot start", errors.New("exit status 1")
			}
		}
		return baseHandler(name, args...)
	}

	err := doInstall(installConfig{User: "alice", Source: src}, env.o)
	if err == nil || !strings.Contains(err.Error(), "install failed") || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("doInstall error = %v, want combined install+rollback failure", err)
	}
	if !strings.Contains(err.Error(), "restart previous service") {
		t.Fatalf("doInstall error = %v, want rollback failure cause surfaced", err)
	}
}

func TestFreshInstallRollbackRemovesNewArtifacts(t *testing.T) {
	env := newTestEnv(t)
	src := env.withSource("NEWBIN")
	env.write(env.o.templatePath, "NEWUNIT")
	env.runner.handler = func(name string, args ...string) (string, string, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "restart" {
			return "", "cannot start", errors.New("exit status 1")
		}
		return baseHandler(name, args...)
	}

	err := doInstall(installConfig{User: "alice", Source: src}, env.o)
	if err == nil || !strings.Contains(err.Error(), "install failed and rolled back") {
		t.Fatalf("doInstall error = %v, want rolled back", err)
	}
	if env.exists(env.o.binDest) {
		t.Fatal("failed fresh install left a new binary behind")
	}
	if env.exists(env.o.unitDest) {
		t.Fatal("failed fresh install left a new unit behind")
	}
	if !env.runner.called("systemctl disable forgehand@alice.service") {
		t.Fatal("rollback did not disable the unit it enabled")
	}
}

// ---------------------------------------------------------------------------
// Uninstall failures and shared-component preservation.
// ---------------------------------------------------------------------------

func TestUninstallDisableFailureReported(t *testing.T) {
	env := newTestEnv(t)
	env.writeExec(env.o.binDest, "BIN")
	env.write(env.o.unitDest, "UNIT")
	env.runner.handler = func(name string, args ...string) (string, string, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "disable" {
			return "", "cannot disable", errors.New("exit status 1")
		}
		return baseHandler(name, args...)
	}
	err := doUninstall(installConfig{User: "alice"}, env.o)
	if err == nil || !strings.Contains(err.Error(), "disable") {
		t.Fatalf("doUninstall error = %v, want disable failure", err)
	}
}

func TestUninstallStopFailureReported(t *testing.T) {
	env := newTestEnv(t)
	env.writeExec(env.o.binDest, "BIN")
	env.write(env.o.unitDest, "UNIT")
	env.runner.handler = func(name string, args ...string) (string, string, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "stop" {
			return "", "cannot stop", errors.New("exit status 1")
		}
		return baseHandler(name, args...)
	}
	err := doUninstall(installConfig{User: "alice"}, env.o)
	if err == nil || !strings.Contains(err.Error(), "stop") {
		t.Fatalf("doUninstall error = %v, want stop failure", err)
	}
}

func TestUninstallRemoveFailureReported(t *testing.T) {
	env := newTestEnv(t)
	env.writeExec(env.o.binDest, "BIN")
	// Make the unit path a non-empty directory so removal fails.
	if err := os.MkdirAll(env.o.unitDest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.o.unitDest, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := doUninstall(installConfig{User: "alice"}, env.o)
	if err == nil || !strings.Contains(err.Error(), "remove unit") {
		t.Fatalf("doUninstall error = %v, want remove unit failure", err)
	}
}

func TestUninstallPreservesSharedComponents(t *testing.T) {
	env := newTestEnv(t)
	env.writeExec(env.o.binDest, "BIN")
	env.write(env.o.unitDest, "UNIT")
	env.runner.handler = func(name string, args ...string) (string, string, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "list-units" {
			for _, a := range args {
				if a == "--all" {
					return "forgehand@bob.service loaded active running\n", "", nil
				}
			}
			return "", "", nil
		}
		return baseHandler(name, args...)
	}
	if err := doUninstall(installConfig{User: "alice"}, env.o); err != nil {
		t.Fatalf("doUninstall: %v", err)
	}
	if !env.exists(env.o.binDest) || !env.exists(env.o.unitDest) {
		t.Fatal("shared binary/unit were removed while another instance exists")
	}
}

func TestUninstallFailedSharedQueryIsHardError(t *testing.T) {
	env := newTestEnv(t)
	env.writeExec(env.o.binDest, "BIN")
	env.write(env.o.unitDest, "UNIT")
	env.runner.handler = func(name string, args ...string) (string, string, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "list-units" {
			for _, a := range args {
				if a == "--all" {
					return "", "query failed", errors.New("exit status 1")
				}
			}
			return "", "", nil
		}
		return baseHandler(name, args...)
	}
	err := doUninstall(installConfig{User: "alice"}, env.o)
	if err == nil || !strings.Contains(err.Error(), "query forgehand instances") {
		t.Fatalf("doUninstall error = %v, want failed shared query", err)
	}
	if !env.exists(env.o.binDest) || !env.exists(env.o.unitDest) {
		t.Fatal("uninstall removed components despite unverifiable shared state")
	}
}

func TestUninstallMissingSystemctlIsHardError(t *testing.T) {
	env := newTestEnv(t)
	env.writeExec(env.o.binDest, "BIN")
	env.write(env.o.unitDest, "UNIT")
	env.o.lookPath = func(name string) (string, error) {
		if name == "systemctl" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}
	err := doUninstall(installConfig{User: "alice"}, env.o)
	if err == nil || !strings.Contains(err.Error(), "systemctl is required") {
		t.Fatalf("doUninstall error = %v, want systemctl required", err)
	}
	if !env.exists(env.o.binDest) || !env.exists(env.o.unitDest) {
		t.Fatal("uninstall removed components without a working systemctl")
	}
}

// ---------------------------------------------------------------------------
// Account/group lookup classification: found / confirmed-absent /
// indeterminate. Only a confirmed absence may trigger creation.
// ---------------------------------------------------------------------------

func TestEnsureGroupLookupOutcomes(t *testing.T) {
	t.Run("found does not create or modify", func(t *testing.T) {
		env := newTestEnv(t)
		if err := env.o.ensureGroup(); err != nil {
			t.Fatalf("ensureGroup: %v", err)
		}
		if env.runner.called("groupadd") {
			t.Fatal("existing group was modified")
		}
	})

	t.Run("confirmed absent creates group", func(t *testing.T) {
		env := newTestEnv(t)
		env.runner.handler = func(name string, args ...string) (string, string, error) {
			if name == "getent" && len(args) > 0 && args[0] == "group" {
				return "", "", fakeExitError{code: 2, msg: "exit status 2"}
			}
			return baseHandler(name, args...)
		}
		if err := env.o.ensureGroup(); err != nil {
			t.Fatalf("ensureGroup: %v", err)
		}
		if !env.runner.called("groupadd --system forgehand") {
			t.Fatal("confirmed-absent group was not created")
		}
	})

	t.Run("indeterminate failure does not create", func(t *testing.T) {
		env := newTestEnv(t)
		env.runner.handler = func(name string, args ...string) (string, string, error) {
			if name == "getent" && len(args) > 0 && args[0] == "group" {
				return "", "database unavailable", fakeExitError{code: 1, msg: "exit status 1"}
			}
			return baseHandler(name, args...)
		}
		if err := env.o.ensureGroup(); err == nil {
			t.Fatal("expected error for indeterminate group lookup")
		}
		if env.runner.called("groupadd") {
			t.Fatal("group was created from an indeterminate lookup")
		}
	})

	t.Run("error without exit code does not create", func(t *testing.T) {
		env := newTestEnv(t)
		env.runner.handler = func(name string, args ...string) (string, string, error) {
			if name == "getent" && len(args) > 0 && args[0] == "group" {
				return "", "", errors.New("exec: pipe closed")
			}
			return baseHandler(name, args...)
		}
		if err := env.o.ensureGroup(); err == nil {
			t.Fatal("expected error for group lookup without exit code")
		}
		if env.runner.called("groupadd") {
			t.Fatal("group was created from a lookup failure")
		}
	})

	t.Run("not-found exit with data does not create", func(t *testing.T) {
		env := newTestEnv(t)
		env.runner.handler = func(name string, args ...string) (string, string, error) {
			if name == "getent" && len(args) > 0 && args[0] == "group" {
				return "forgehand:x:999:\n", "", fakeExitError{code: 2, msg: "exit status 2"}
			}
			return baseHandler(name, args...)
		}
		if err := env.o.ensureGroup(); err == nil {
			t.Fatal("expected error for contradictory group lookup")
		}
		if env.runner.called("groupadd") {
			t.Fatal("group was created from contradictory lookup data")
		}
	})

	t.Run("malformed result does not create", func(t *testing.T) {
		env := newTestEnv(t)
		env.runner.handler = func(name string, args ...string) (string, string, error) {
			if name == "getent" && len(args) > 0 && args[0] == "group" {
				return "othergroup:x:1:\n", "", nil
			}
			return baseHandler(name, args...)
		}
		if err := env.o.ensureGroup(); err == nil {
			t.Fatal("expected error for malformed group lookup")
		}
		if env.runner.called("groupadd") {
			t.Fatal("group was created from a malformed lookup")
		}
	})
}

func TestEnsureUserLookupOutcomes(t *testing.T) {
	absent := func(t *testing.T) *testEnv {
		t.Helper()
		env := newTestEnv(t)
		env.runner.handler = func(name string, args ...string) (string, string, error) {
			if name == "getent" && len(args) > 0 && args[0] == "passwd" {
				return "", "", fakeExitError{code: 2, msg: "exit status 2"}
			}
			return baseHandler(name, args...)
		}
		return env
	}

	t.Run("found does not create or modify", func(t *testing.T) {
		env := newTestEnv(t)
		if err := env.o.ensureUser("alice", false); err != nil {
			t.Fatalf("ensureUser: %v", err)
		}
		if env.runner.called("useradd") {
			t.Fatal("existing account was modified")
		}
	})

	t.Run("confirmed absent without create errors", func(t *testing.T) {
		env := absent(t)
		if err := env.o.ensureUser("alice", false); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("ensureUser = %v, want does not exist", err)
		}
		if env.runner.called("useradd") {
			t.Fatal("account created without --create-user")
		}
	})

	t.Run("confirmed absent with create adds account", func(t *testing.T) {
		env := absent(t)
		if err := env.o.ensureUser("alice", true); err != nil {
			t.Fatalf("ensureUser: %v", err)
		}
		if !env.runner.called("useradd --system") {
			t.Fatal("confirmed-absent account was not created")
		}
	})

	t.Run("indeterminate failure does not create", func(t *testing.T) {
		env := newTestEnv(t)
		env.runner.handler = func(name string, args ...string) (string, string, error) {
			if name == "getent" && len(args) > 0 && args[0] == "passwd" {
				return "", "database unavailable", fakeExitError{code: 1, msg: "exit status 1"}
			}
			return baseHandler(name, args...)
		}
		if err := env.o.ensureUser("alice", true); err == nil {
			t.Fatal("expected error for indeterminate account lookup")
		}
		if env.runner.called("useradd") {
			t.Fatal("account was created from an indeterminate lookup")
		}
	})

	t.Run("empty success does not create", func(t *testing.T) {
		env := newTestEnv(t)
		env.runner.handler = func(name string, args ...string) (string, string, error) {
			if name == "getent" && len(args) > 0 && args[0] == "passwd" {
				return "", "", nil
			}
			return baseHandler(name, args...)
		}
		if err := env.o.ensureUser("alice", true); err == nil {
			t.Fatal("expected error for empty account lookup")
		}
		if env.runner.called("useradd") {
			t.Fatal("account was created from an empty lookup")
		}
	})

	t.Run("not-found with data does not create", func(t *testing.T) {
		env := newTestEnv(t)
		env.runner.handler = func(name string, args ...string) (string, string, error) {
			if name == "getent" && len(args) > 0 && args[0] == "passwd" {
				return "alice:x:1000:1000::/home/alice:/bin/bash\n", "", fakeExitError{code: 2, msg: "exit status 2"}
			}
			return baseHandler(name, args...)
		}
		if err := env.o.ensureUser("alice", true); err == nil {
			t.Fatal("expected error for contradictory account lookup")
		}
		if env.runner.called("useradd") {
			t.Fatal("account was created from contradictory lookup data")
		}
	})

	t.Run("malformed passwd does not create", func(t *testing.T) {
		env := newTestEnv(t)
		env.runner.handler = func(name string, args ...string) (string, string, error) {
			if name == "getent" && len(args) > 0 && args[0] == "passwd" {
				return "bob:x:1000:1000::/home/bob:/bin/bash\n", "", nil
			}
			return baseHandler(name, args...)
		}
		if err := env.o.ensureUser("alice", true); err == nil {
			t.Fatal("expected error for malformed account lookup")
		}
		if env.runner.called("useradd") {
			t.Fatal("account was created from a malformed lookup")
		}
	})
}

// ---------------------------------------------------------------------------
// systemd service-state detection: active/inactive/enabled/disabled and
// indeterminate outcomes.
// ---------------------------------------------------------------------------

func TestServiceActiveStateDetection(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		runErr  error
		want    serviceActiveState
		wantErr bool
	}{
		{"active", "active\n", nil, serviceActiveRunning, false},
		{"inactive", "inactive\n", fakeExitError{code: 3, msg: "x"}, serviceActiveStopped, false},
		{"failed", "failed\n", fakeExitError{code: 3, msg: "x"}, serviceActiveStopped, false},
		{"unknown-unit inactive", "inactive\n", fakeExitError{code: 4, msg: "x"}, serviceActiveStopped, false},
		{"transient activating", "activating\n", nil, serviceActiveIndeterminate, true},
		{"active with bad exit", "active\n", fakeExitError{code: 1, msg: "x"}, serviceActiveIndeterminate, true},
		{"inactive with bad exit", "inactive\n", fakeExitError{code: 0, msg: "x"}, serviceActiveIndeterminate, true},
		{"empty success", "", nil, serviceActiveIndeterminate, true},
		{"empty failure", "", errors.New("boom"), serviceActiveIndeterminate, true},
		{"timeout", "", context.DeadlineExceeded, serviceActiveIndeterminate, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			out, runErr := tc.out, tc.runErr
			env.runner.handler = func(name string, args ...string) (string, string, error) {
				if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
					return out, "", runErr
				}
				return baseHandler(name, args...)
			}
			got, err := env.o.serviceActive("alice")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("serviceActive = (%v, nil), want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("serviceActive: %v", err)
			}
			if got != tc.want {
				t.Fatalf("serviceActive = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServiceEnabledStateDetection(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		runErr  error
		want    serviceEnabledState
		wantErr bool
	}{
		{"enabled", "enabled\n", nil, serviceEnabledYes, false},
		{"enabled-runtime", "enabled-runtime\n", nil, serviceEnabledYes, false},
		{"static", "static\n", nil, serviceEnabledYes, false},
		{"disabled", "disabled\n", fakeExitError{code: 1, msg: "x"}, serviceEnabledNo, false},
		{"masked", "masked\n", fakeExitError{code: 1, msg: "x"}, serviceEnabledNo, false},
		{"not-found", "not-found\n", fakeExitError{code: 4, msg: "x"}, serviceEnabledNo, false},
		{"unexpected token", "garbage\n", nil, serviceEnabledIndeterminate, true},
		{"enabled with bad exit", "enabled\n", fakeExitError{code: 1, msg: "x"}, serviceEnabledIndeterminate, true},
		{"disabled with bad exit", "disabled\n", fakeExitError{code: 0, msg: "x"}, serviceEnabledIndeterminate, true},
		{"empty success", "", nil, serviceEnabledIndeterminate, true},
		{"empty failure", "", errors.New("boom"), serviceEnabledIndeterminate, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			out, runErr := tc.out, tc.runErr
			env.runner.handler = func(name string, args ...string) (string, string, error) {
				if name == "systemctl" && len(args) > 0 && args[0] == "is-enabled" {
					return out, "", runErr
				}
				return baseHandler(name, args...)
			}
			got, err := env.o.serviceEnabled("alice")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("serviceEnabled = (%v, nil), want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("serviceEnabled: %v", err)
			}
			if got != tc.want {
				t.Fatalf("serviceEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServiceStateMissingSystemctlIsError(t *testing.T) {
	env := newTestEnv(t)
	env.o.lookPath = func(name string) (string, error) {
		if name == "systemctl" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}
	if _, err := env.o.serviceActive("alice"); err == nil {
		t.Fatal("serviceActive succeeded without systemctl")
	}
	if _, err := env.o.serviceEnabled("alice"); err == nil {
		t.Fatal("serviceEnabled succeeded without systemctl")
	}
}

// ---------------------------------------------------------------------------
// Indeterminate state must abort before any mutation, and rollback must not
// start a service that was previously stopped.
// ---------------------------------------------------------------------------

func TestInstallAbortsWhenServiceStateIndeterminate(t *testing.T) {
	env := newTestEnv(t)
	src := env.withSource("NEWBIN")
	env.write(env.o.templatePath, "NEWUNIT")
	env.writeExec(env.o.binDest, "OLDBIN")
	env.write(env.o.unitDest, "OLDUNIT")
	env.runner.handler = func(name string, args ...string) (string, string, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
			return "", "failed to connect to bus", errors.New("connection failed")
		}
		return baseHandler(name, args...)
	}
	err := doInstall(installConfig{User: "alice", Source: src}, env.o)
	if err == nil {
		t.Fatal("doInstall succeeded despite indeterminate service state")
	}
	if got := env.read(env.o.binDest); got != "OLDBIN" {
		t.Fatalf("binary changed despite indeterminate state: %q", got)
	}
	if got := env.read(env.o.unitDest); got != "OLDUNIT" {
		t.Fatalf("unit changed despite indeterminate state: %q", got)
	}
}

func TestUpgradeAbortsWhenEnablementIndeterminate(t *testing.T) {
	env := newTestEnv(t)
	src := env.withSource("NEWBIN")
	env.write(env.o.templatePath, "NEWUNIT")
	env.writeExec(env.o.binDest, "OLDBIN")
	env.write(env.o.unitDest, "OLDUNIT")
	env.runner.handler = func(name string, args ...string) (string, string, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "is-enabled" {
			return "garbage\n", "", nil
		}
		return baseHandler(name, args...)
	}
	err := doUpgrade(installConfig{User: "alice", Source: src}, env.o)
	if err == nil {
		t.Fatal("doUpgrade succeeded despite indeterminate enablement state")
	}
	if got := env.read(env.o.binDest); got != "OLDBIN" {
		t.Fatalf("binary changed despite indeterminate state: %q", got)
	}
	if env.runner.called("systemctl stop forgehand@alice.service") {
		t.Fatal("service was stopped despite indeterminate state")
	}
}

func TestRollbackDoesNotStartPreviouslyStoppedService(t *testing.T) {
	env := newTestEnv(t)
	src := env.withSource("NEWBIN")
	env.write(env.o.templatePath, "NEWUNIT")
	env.writeExec(env.o.binDest, "OLDBIN")
	env.write(env.o.unitDest, "OLDUNIT")
	env.listenSocket()
	env.runner.handler = func(name string, args ...string) (string, string, error) {
		if name == env.o.binDest {
			return "", "not ready", errors.New("exit status 1")
		}
		return baseHandler(name, args...)
	}

	err := doInstall(installConfig{User: "alice", Source: src}, env.o)
	if err == nil || !strings.Contains(err.Error(), "install failed and rolled back") {
		t.Fatalf("doInstall error = %v, want rolled back", err)
	}
	if env.runner.called("systemctl start forgehand@alice.service") {
		t.Fatal("rollback started a service that was previously stopped")
	}
	if !env.runner.called("systemctl disable forgehand@alice.service") {
		t.Fatal("rollback did not disable the unit it enabled")
	}
	if got := env.read(env.o.binDest); got != "OLDBIN" {
		t.Fatalf("binary after rollback = %q, want OLDBIN", got)
	}
	if got := env.read(env.o.unitDest); got != "OLDUNIT" {
		t.Fatalf("unit after rollback = %q, want OLDUNIT", got)
	}
}
