package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultBinDest       = "/usr/bin/forgehand"
	defaultUnitDest      = "/etc/systemd/system/forgehand@.service"
	defaultStateDir      = "/var/lib/forgehand"
	defaultRuntimeSocket = "/run/forgehand/forgehand.sock"
	defaultGroup         = "forgehand"
)

// ops holds the filesystem paths, external command runner, and timing policy
// used by installer operations. Tests construct an ops with temporary paths and
// a fake runner; production code uses newOps. No production behavior is
// controlled by environment variables: test isolation is achieved only by
// injecting dependencies (runner, lookPath, sleep) and filesystem paths.
type ops struct {
	runner commandRunner

	// lookPath reports whether an external command is available. It is injected
	// so tests can simulate missing systemd/getent/useradd commands.
	lookPath func(string) (string, error)

	binDest       string
	unitDest      string
	stateDir      string
	runtimeSocket string
	group         string
	templatePath  string
	tempDir       string

	readinessRetries int
	readinessDelay   time.Duration

	sleep func(time.Duration)
}

func newOps(runner commandRunner) *ops {
	return &ops{
		runner:           runner,
		lookPath:         execLookPath,
		binDest:          defaultBinDest,
		unitDest:         defaultUnitDest,
		stateDir:         defaultStateDir,
		runtimeSocket:    defaultRuntimeSocket,
		group:            defaultGroup,
		templatePath:     defaultTemplatePath(),
		tempDir:          os.TempDir(),
		readinessRetries: 30,
		readinessDelay:   2 * time.Second,
		sleep:            time.Sleep,
	}
}

// commandAvailable reports whether an external command exists on PATH.
func (o *ops) commandAvailable(name string) bool {
	if o.lookPath == nil {
		return false
	}
	_, err := o.lookPath(name)
	return err == nil
}

// exitCoder is implemented by errors that carry a process exit status
// (notably *exec.ExitError). It lets lookup helpers distinguish a definitive
// "not found" from a command failure without changing the runner interface.
type exitCoder interface {
	ExitCode() int
}

// commandExitCode extracts a process exit code from err when one is available.
func commandExitCode(err error) (int, bool) {
	var ec exitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode(), true
	}
	return 0, false
}

func defaultTemplatePath() string {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	candidates := []string{
		filepath.Join(cwd, "packaging", "systemd", "forgehand@.service"),
		filepath.Join(cwd, "..", "..", "packaging", "systemd", "forgehand@.service"),
		filepath.Join(cwd, "..", "..", "..", "packaging", "systemd", "forgehand@.service"),
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return candidates[0]
}

// conflict describes another active Forgehand instance.
type conflict struct {
	instance string
}

// detectConflictingInstances returns an error when an active forgehand service
// instance other than the selected account is running. Because the service
// template shares a single state directory and socket, two live instances would
// corrupt each other. Missing systemctl or a failed query is a hard error so an
// unverifiable state can never be treated as "no conflict".
func (o *ops) detectConflictingInstances(account string) error {
	if !o.commandAvailable("systemctl") {
		return errors.New("systemctl is required to detect conflicting instances")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, stderr, err := o.runner.Run(ctx, nil,
		"systemctl", "list-units", "--type=service", "--state=active",
		"--no-legend", "--no-pager", "forgehand@*.service")
	if err != nil {
		return fmt.Errorf("query active forgehand instances: %v: %s", err, strings.TrimSpace(stderr))
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		unit := fields[0]
		if !strings.HasPrefix(unit, "forgehand@") {
			continue
		}
		instance := strings.TrimSuffix(strings.TrimPrefix(unit, "forgehand@"), ".service")
		if instance != account {
			return fmt.Errorf("conflicting active instance forgehand@%s.service; refusing unsafe operation", instance)
		}
	}
	return nil
}

// lookupResult classifies an account/group database lookup so callers never
// confuse a confirmed absence with a lookup that failed or returned garbage.
type lookupResult int

const (
	lookupIndeterminate lookupResult = iota
	lookupFound
	lookupNotFound
)

// lookupEntity queries the NSS database via getent. getent exits with status 2
// when the key is definitively absent, so that specific exit status (with no
// data) is the only case reported as lookupNotFound. Any other failure,
// timeout, or unexpected data is reported as an error.
func (o *ops) lookupEntity(kind, name string) (lookupResult, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, stderr, err := o.runner.Run(ctx, nil, "getent", kind, name)
	trimmed := strings.TrimSpace(out)
	if err == nil {
		if trimmed == "" {
			return lookupIndeterminate, "", fmt.Errorf("getent %s %q returned no result", kind, name)
		}
		return lookupFound, trimmed, nil
	}
	code, ok := commandExitCode(err)
	if !ok {
		return lookupIndeterminate, "", fmt.Errorf("getent %s %q failed: %v: %s", kind, name, err, strings.TrimSpace(stderr))
	}
	switch code {
	case 2:
		if trimmed != "" {
			return lookupIndeterminate, "", fmt.Errorf("getent %s %q reported not-found but returned data", kind, name)
		}
		return lookupNotFound, "", nil
	default:
		return lookupIndeterminate, "", fmt.Errorf("getent %s %q failed (exit %d): %v: %s", kind, name, code, err, strings.TrimSpace(stderr))
	}
}

// groupEntryMatches reports whether a getent group line describes the expected
// group. Malformed lines are rejected rather than treated as a valid group.
func groupEntryMatches(name, line string) bool {
	fields := strings.Split(line, ":")
	return len(fields) >= 3 && fields[0] == name
}

// passwdUID returns the uid field of a getent passwd line after confirming the
// line is well-formed and describes the requested account.
func passwdUID(name, line string) (string, error) {
	fields := strings.Split(line, ":")
	if len(fields) < 3 || fields[0] != name {
		return "", fmt.Errorf("unexpected getent passwd result for %q", name)
	}
	return fields[2], nil
}

func (o *ops) ensureGroup() error {
	if !o.commandAvailable("getent") {
		return errors.New("getent is required to validate groups")
	}
	result, data, err := o.lookupEntity("group", o.group)
	if err != nil {
		return err
	}
	if result == lookupFound {
		if !groupEntryMatches(o.group, data) {
			return fmt.Errorf("unexpected getent group result for %q", o.group)
		}
		// Existing group: never modify its configuration.
		return nil
	}
	// Absence is confirmed; create the group.
	if !o.commandAvailable("groupadd") {
		return fmt.Errorf("group %q is missing and groupadd is not available", o.group)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, stderr, err := o.runner.Run(ctx, nil, "groupadd", "--system", o.group)
	if err != nil {
		return fmt.Errorf("create group %q: %v: %s", o.group, err, strings.TrimSpace(stderr))
	}
	return nil
}

func (o *ops) ensureUser(account string, create bool) error {
	if err := validateAccountName(account); err != nil {
		return err
	}
	if !o.commandAvailable("getent") {
		return errors.New("getent is required to validate accounts")
	}
	result, data, err := o.lookupEntity("passwd", account)
	if err != nil {
		return err
	}
	if result == lookupFound {
		uid, perr := passwdUID(account, data)
		if perr != nil {
			return perr
		}
		if uid == "0" {
			return errors.New("refusing to use the root account for the daemon")
		}
		// Existing account: never modify its configuration.
		return nil
	}
	// Absence is confirmed; only then may an account be created.
	if !create {
		return fmt.Errorf("account %q does not exist; pass --create-user to create a dedicated service account", account)
	}
	if !o.commandAvailable("useradd") {
		return fmt.Errorf("account %q is missing and useradd is not available", account)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, stderr, err := o.runner.Run(ctx, nil,
		"useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin",
		"--gid", o.group, account)
	if err != nil {
		_, stderr, err = o.runner.Run(ctx, nil,
			"useradd", "--system", "--no-create-home", "--shell", "/sbin/nologin",
			"--gid", o.group, account)
	}
	if err != nil {
		return fmt.Errorf("create account %q: %v: %s", account, err, strings.TrimSpace(stderr))
	}
	return nil
}

func (o *ops) stageBinary(source string) (string, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return "", fmt.Errorf("inspect source binary: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("source binary must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("source binary must be a regular file")
	}
	if info.Size() == 0 {
		return "", errors.New("source binary is empty")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("source binary is not executable")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", fmt.Errorf("read source binary: %w", err)
	}
	staged := filepath.Join(o.tempDir, fmt.Sprintf("forgehand-installer-%d", time.Now().UnixNano()))
	if err := os.WriteFile(staged, data, 0o755); err != nil {
		return "", fmt.Errorf("stage binary: %w", err)
	}
	return staged, nil
}

func (o *ops) binaryMatches(staged string) bool {
	current, err := os.ReadFile(o.binDest)
	if err != nil {
		return false
	}
	want, err := os.ReadFile(staged)
	if err != nil {
		return false
	}
	return bytes.Equal(current, want)
}

func (o *ops) unitMatches() bool {
	current, err := os.ReadFile(o.unitDest)
	if err != nil {
		return false
	}
	want, err := os.ReadFile(o.templatePath)
	if err != nil {
		return false
	}
	return bytes.Equal(current, want)
}

func (o *ops) backupCurrentBinary() (string, error) {
	if !fileExists(o.binDest) {
		return "", nil
	}
	if isSymlink(o.binDest) {
		return "", errors.New("installed binary is a symlink; refusing unsafe replacement")
	}
	data, err := os.ReadFile(o.binDest)
	if err != nil {
		return "", fmt.Errorf("read installed binary: %w", err)
	}
	backup := filepath.Join(o.tempDir, fmt.Sprintf("forgehand-backup-%d", time.Now().UnixNano()))
	if err := os.WriteFile(backup, data, 0o755); err != nil {
		return "", fmt.Errorf("backup installed binary: %w", err)
	}
	return backup, nil
}

func (o *ops) replaceBinary(staged string) error {
	if isSymlink(o.binDest) {
		return errors.New("refusing to replace a symlinked binary destination")
	}
	if err := os.MkdirAll(filepath.Dir(o.binDest), 0o755); err != nil {
		return fmt.Errorf("create binary directory: %w", err)
	}
	tmp := o.binDest + ".new"
	if err := copyFile(staged, tmp, 0o755); err != nil {
		return fmt.Errorf("write replacement binary: %w", err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return fmt.Errorf("set binary mode: %w", err)
	}
	if err := os.Rename(tmp, o.binDest); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	return nil
}

func (o *ops) restoreBinary(backup string) error {
	if backup == "" {
		return nil
	}
	if !fileExists(backup) {
		return fmt.Errorf("backup binary %s is missing", backup)
	}
	return copyFile(backup, o.binDest, 0o755)
}

func (o *ops) readUnitTemplate() ([]byte, error) {
	data, err := os.ReadFile(o.templatePath)
	if err != nil {
		return nil, fmt.Errorf("read service template: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("service template is empty")
	}
	return data, nil
}

func (o *ops) installUnit() error {
	if info, err := os.Lstat(o.unitDest); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("refusing to replace a symlinked unit path")
		}
	}
	data, err := o.readUnitTemplate()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.unitDest), 0o755); err != nil {
		return fmt.Errorf("create unit directory: %w", err)
	}
	if err := os.WriteFile(o.unitDest, data, 0o644); err != nil {
		return fmt.Errorf("install unit: %w", err)
	}
	return nil
}

// readExistingUnit returns the current unit file contents and whether it
// existed, so rollback can restore the exact previous state.
func (o *ops) readExistingUnit() ([]byte, bool, error) {
	data, err := os.ReadFile(o.unitDest)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read installed unit: %w", err)
	}
	return data, true, nil
}

func (o *ops) restoreUnit(previous []byte) error {
	if previous == nil {
		if err := os.Remove(o.unitDest); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(o.unitDest, previous, 0o644)
}

func (o *ops) unitMatchesTemplate() bool {
	data, err := os.ReadFile(o.unitDest)
	if err != nil {
		return false
	}
	tmpl, err := o.readUnitTemplate()
	if err != nil {
		return false
	}
	return bytes.Equal(data, tmpl)
}

func (o *ops) daemonReload() error {
	if !o.commandAvailable("systemctl") {
		return errors.New("systemctl is required to reload the service manager")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, stderr, err := o.runner.Run(ctx, nil, "systemctl", "daemon-reload")
	if err != nil {
		return fmt.Errorf("systemctl daemon-reload: %v: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

func (o *ops) instanceName(account string) string {
	return fmt.Sprintf("forgehand@%s.service", account)
}

func (o *ops) systemctl(action, account string, required bool) error {
	if !o.commandAvailable("systemctl") {
		return errors.New("systemctl is required for service operations")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, stderr, err := o.runner.Run(ctx, nil, "systemctl", action, o.instanceName(account))
	if err != nil && required {
		return fmt.Errorf("systemctl %s %s: %v: %s", action, o.instanceName(account), err, strings.TrimSpace(stderr))
	}
	return err
}

// serviceActiveState is a tri-state result for the running state of a unit.
type serviceActiveState int

const (
	serviceActiveIndeterminate serviceActiveState = iota
	serviceActiveRunning
	serviceActiveStopped
)

// serviceEnabledState is a tri-state result for the enablement state of a unit.
type serviceEnabledState int

const (
	serviceEnabledIndeterminate serviceEnabledState = iota
	serviceEnabledYes
	serviceEnabledNo
)

// systemctlState runs a read-only systemctl state query and returns the trimmed
// state token and the process exit code (0 on success). A missing command, an
// empty result, or a non-zero exit that does not carry a usable exit status is
// always an error: systemd prints a state token for is-active/is-enabled, so an
// empty result means the command failed, timed out, or produced malformed
// output.
func (o *ops) systemctlState(action, account string) (string, int, error) {
	if !o.commandAvailable("systemctl") {
		return "", 0, errors.New("systemctl is required to determine service state")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, stderr, runErr := o.runner.Run(ctx, nil, "systemctl", action, o.instanceName(account))
	token := strings.TrimSpace(out)
	if runErr != nil {
		code, ok := commandExitCode(runErr)
		if !ok {
			return "", 0, fmt.Errorf("systemctl %s %s failed: %v: %s", action, o.instanceName(account), runErr, strings.TrimSpace(stderr))
		}
		if token == "" {
			return "", code, fmt.Errorf("systemctl %s %s failed (exit %d): %v: %s", action, o.instanceName(account), code, runErr, strings.TrimSpace(stderr))
		}
		return token, code, nil
	}
	if token == "" {
		return "", 0, fmt.Errorf("systemctl %s %s returned no state", action, o.instanceName(account))
	}
	return token, 0, nil
}

// serviceActive reports the running state of the instance. Only recognized
// systemd states with their documented exit status are accepted; anything else
// is indeterminate and returned as an error so callers never treat an unknown
// state as "not running".
func (o *ops) serviceActive(account string) (serviceActiveState, error) {
	state, code, err := o.systemctlState("is-active", account)
	if err != nil {
		return serviceActiveIndeterminate, err
	}
	switch state {
	case "active":
		if code != 0 {
			return serviceActiveIndeterminate, fmt.Errorf("service %s reported %q with unexpected exit %d", o.instanceName(account), state, code)
		}
		return serviceActiveRunning, nil
	case "inactive", "failed":
		if code != 3 && code != 4 {
			return serviceActiveIndeterminate, fmt.Errorf("service %s reported %q with unexpected exit %d", o.instanceName(account), state, code)
		}
		return serviceActiveStopped, nil
	default:
		return serviceActiveIndeterminate, fmt.Errorf("service %s has indeterminate is-active state %q", o.instanceName(account), state)
	}
}

// serviceEnabled reports the enablement state of the instance. Only recognized
// systemd states with their documented exit status are accepted; anything else
// is indeterminate and returned as an error.
func (o *ops) serviceEnabled(account string) (serviceEnabledState, error) {
	state, code, err := o.systemctlState("is-enabled", account)
	if err != nil {
		return serviceEnabledIndeterminate, err
	}
	switch state {
	case "enabled", "enabled-runtime", "alias", "static", "indirect", "generated", "linked", "linked-runtime", "transient":
		if code != 0 {
			return serviceEnabledIndeterminate, fmt.Errorf("service %s reported %q with unexpected exit %d", o.instanceName(account), state, code)
		}
		return serviceEnabledYes, nil
	case "disabled", "masked", "masked-runtime", "not-found":
		if code != 1 && code != 4 {
			return serviceEnabledIndeterminate, fmt.Errorf("service %s reported %q with unexpected exit %d", o.instanceName(account), state, code)
		}
		return serviceEnabledNo, nil
	default:
		return serviceEnabledIndeterminate, fmt.Errorf("service %s has indeterminate is-enabled state %q", o.instanceName(account), state)
	}
}

func (o *ops) checkSocket() error {
	info, err := os.Lstat(o.runtimeSocket)
	if err != nil {
		return fmt.Errorf("socket %s: %w", o.runtimeSocket, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s is not a Unix socket", o.runtimeSocket)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 && perm != 0o660 {
		return fmt.Errorf("socket %s has unexpected permissions %04o", o.runtimeSocket, perm)
	}
	return nil
}

// checkReadiness verifies the daemon responds using the installed CLI and that
// the socket exists with appropriate permissions.
func (o *ops) checkReadiness() error {
	if err := o.checkSocket(); err != nil {
		return err
	}
	if !fileExists(o.binDest) {
		return fmt.Errorf("installed binary %s is missing", o.binDest)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	env := []string{"FORGEHAND_SOCKET_PATH=" + o.runtimeSocket}
	_, stderr, err := o.runner.Run(ctx, env, o.binDest, "status")
	if err != nil {
		return fmt.Errorf("daemon status check failed: %v: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

func (o *ops) verifyReadiness() error {
	var last error
	for i := 0; i < o.readinessRetries; i++ {
		if err := o.checkReadiness(); err == nil {
			return nil
		} else {
			last = err
		}
		if i < o.readinessRetries-1 {
			o.sleep(o.readinessDelay)
		}
	}
	if last == nil {
		last = errors.New("daemon did not become ready")
	}
	return fmt.Errorf("daemon readiness timeout: %w", last)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (o *ops) ensureStateDir() error {
	if err := os.MkdirAll(o.stateDir, 0o700); err != nil {
		return err
	}
	return nil
}

func execLookPath(name string) (string, error) {
	out, err := exec.Command("which", name).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func isSymlink(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSymlink != 0
}

func contextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func trimLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if ls := strings.TrimSpace(l); ls != "" {
			out = append(out, ls)
		}
	}
	return out
}

// serviceSnapshot captures the pre-mutation state of the previously installed
// service, binary, and unit so an install or upgrade can be rolled back. The
// application state directory (/var/lib/forgehand) is never modified by the
// installer, so application state survives rollback by construction.
type serviceSnapshot struct {
	binaryBackup string
	hadBinary    bool
	unit         []byte
	hadUnit      bool
	wasActive    serviceActiveState
	wasEnabled   serviceEnabledState

	// enabledByUs records whether this invocation enabled the unit, so rollback
	// only disables a unit that it actually enabled.
	enabledByUs bool
}

// captureSnapshot records the current service/binary/unit state. It fails
// before any mutation if the existing state cannot be reliably determined, so
// an operation never proceeds from an indeterminate starting state.
func (o *ops) captureSnapshot(account string) (serviceSnapshot, error) {
	active, err := o.serviceActive(account)
	if err != nil {
		return serviceSnapshot{}, err
	}
	enabled, err := o.serviceEnabled(account)
	if err != nil {
		return serviceSnapshot{}, err
	}
	snap := serviceSnapshot{
		wasActive:  active,
		wasEnabled: enabled,
	}
	if fileExists(o.binDest) {
		backup, err := o.backupCurrentBinary()
		if err != nil {
			return snap, err
		}
		snap.binaryBackup = backup
		snap.hadBinary = true
	}
	unit, had, err := o.readExistingUnit()
	if err != nil {
		return snap, err
	}
	snap.unit = unit
	snap.hadUnit = had
	return snap, nil
}

// rollback restores the pre-mutation service, binary, and unit. It aggregates
// every failure instead of returning early so that one bad step cannot mask
// another, and it never reports success when restoration failed. A service that
// was stopped or disabled before the operation is never silently started or
// enabled.
func (o *ops) rollback(account string, snap serviceSnapshot) error {
	var errs []error

	if err := o.systemctl("stop", account, false); err != nil {
		errs = append(errs, fmt.Errorf("stop service: %w", err))
	}
	if snap.enabledByUs && snap.wasEnabled == serviceEnabledNo {
		if err := o.systemctl("disable", account, false); err != nil {
			errs = append(errs, fmt.Errorf("disable service: %w", err))
		}
	}
	if snap.hadBinary {
		if err := o.restoreBinary(snap.binaryBackup); err != nil {
			errs = append(errs, fmt.Errorf("restore binary: %w", err))
		}
	} else if err := os.Remove(o.binDest); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("remove failed-install binary: %w", err))
	}
	if snap.hadUnit {
		if err := o.restoreUnit(snap.unit); err != nil {
			errs = append(errs, fmt.Errorf("restore unit: %w", err))
		}
	} else if err := os.Remove(o.unitDest); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("remove failed-install unit: %w", err))
	}
	if err := o.daemonReload(); err != nil {
		errs = append(errs, fmt.Errorf("reload service manager: %w", err))
	}
	if snap.wasActive == serviceActiveRunning {
		if err := o.systemctl("start", account, true); err != nil {
			errs = append(errs, fmt.Errorf("restart previous service: %w", err))
		}
	}
	return errors.Join(errs...)
}

// failWithRollback rolls back and wraps the original failure together with any
// rollback failure so neither is silently dropped.
func (o *ops) failWithRollback(op, account string, snap serviceSnapshot, cause error) error {
	if rerr := o.rollback(account, snap); rerr != nil {
		return fmt.Errorf("%s failed (%v) and rollback failed: %w", op, cause, rerr)
	}
	return fmt.Errorf("%s failed and rolled back: %w", op, cause)
}
