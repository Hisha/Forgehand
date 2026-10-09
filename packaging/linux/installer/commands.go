package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// installConfig holds the parsed command-line configuration for a command.
type installConfig struct {
	User       string
	CreateUser bool
	Source     string
	AssumeYes  bool
}

func (cfg installConfig) validateUser() error {
	return validateAccountName(cfg.User)
}

// resolveSource validates the source binary and returns its path.
func (o *ops) resolveSource(cfg installConfig) (string, error) {
	source := cfg.Source
	if source == "" {
		source = "forgehand"
	}
	if _, err := os.Lstat(source); err != nil {
		return "", fmt.Errorf("source binary %q not found; pass --source", source)
	}
	if err := o.stageBinaryCheck(source); err != nil {
		return "", err
	}
	return source, nil
}

// stageBinaryCheck validates a source binary without creating a staged copy.
func (o *ops) stageBinaryCheck(source string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect source binary: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("source binary must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return errors.New("source binary must be a regular file")
	}
	if info.Size() == 0 {
		return errors.New("source binary is empty")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return errors.New("source binary is not executable")
	}
	return nil
}

func doInstall(cfg installConfig, o *ops) error {
	if err := cfg.validateUser(); err != nil {
		return err
	}
	source, err := o.resolveSource(cfg)
	if err != nil {
		return err
	}
	if err := o.detectConflictingInstances(cfg.User); err != nil {
		return err
	}
	if err := o.ensureGroup(); err != nil {
		return err
	}
	if err := o.ensureUser(cfg.User, cfg.CreateUser); err != nil {
		return err
	}

	staged, err := o.stageBinary(source)
	if err != nil {
		return err
	}
	defer os.Remove(staged)

	active, err := o.serviceActive(cfg.User)
	if err != nil {
		return err
	}
	if o.binaryMatches(staged) && o.unitMatchesTemplate() && active == serviceActiveRunning {
		fmt.Println("installation is already current")
		return nil
	}

	snap, err := o.captureSnapshot(cfg.User)
	if err != nil {
		return err
	}

	if err := o.replaceBinary(staged); err != nil {
		return err
	}
	if err := o.installUnit(); err != nil {
		return o.failWithRollback("install", cfg.User, snap, err)
	}
	if err := o.daemonReload(); err != nil {
		return o.failWithRollback("install", cfg.User, snap, err)
	}
	if err := o.systemctl("enable", cfg.User, true); err != nil {
		return o.failWithRollback("install", cfg.User, snap, err)
	}
	snap.enabledByUs = true
	if err := o.systemctl("restart", cfg.User, true); err != nil {
		return o.failWithRollback("install", cfg.User, snap, err)
	}
	if err := o.verifyReadiness(); err != nil {
		return o.failWithRollback("install", cfg.User, snap, err)
	}
	return nil
}

func doUpgrade(cfg installConfig, o *ops) error {
	if err := cfg.validateUser(); err != nil {
		return err
	}
	if !fileExists(o.binDest) {
		return fmt.Errorf("no existing installation found at %s; use install", o.binDest)
	}
	source, err := o.resolveSource(cfg)
	if err != nil {
		return err
	}
	if err := o.detectConflictingInstances(cfg.User); err != nil {
		return err
	}
	if err := o.ensureGroup(); err != nil {
		return err
	}
	if err := o.ensureUser(cfg.User, cfg.CreateUser); err != nil {
		return err
	}

	staged, err := o.stageBinary(source)
	if err != nil {
		return err
	}
	defer os.Remove(staged)

	snap, err := o.captureSnapshot(cfg.User)
	if err != nil {
		return err
	}

	if err := o.systemctl("stop", cfg.User, true); err != nil {
		return err
	}
	if err := o.replaceBinary(staged); err != nil {
		return o.failWithRollback("upgrade", cfg.User, snap, err)
	}
	if err := o.installUnit(); err != nil {
		return o.failWithRollback("upgrade", cfg.User, snap, err)
	}
	if err := o.daemonReload(); err != nil {
		return o.failWithRollback("upgrade", cfg.User, snap, err)
	}
	if err := o.systemctl("start", cfg.User, true); err != nil {
		return o.failWithRollback("upgrade", cfg.User, snap, err)
	}
	if err := o.verifyReadiness(); err != nil {
		return o.failWithRollback("upgrade", cfg.User, snap, err)
	}
	return nil
}

func doStatus(cfg installConfig, o *ops) error {
	if err := cfg.validateUser(); err != nil {
		return err
	}
	if !fileExists(o.binDest) {
		return fmt.Errorf("binary not installed: %s", o.binDest)
	}
	active, err := o.serviceActive(cfg.User)
	if err != nil {
		return err
	}
	running := active == serviceActiveRunning
	fmt.Printf("service %s active: %v\n", o.instanceName(cfg.User), running)
	if err := o.checkSocket(); err != nil {
		return err
	}
	fmt.Printf("socket %s present with valid permissions\n", o.runtimeSocket)
	if !running {
		return fmt.Errorf("service %s is not active", o.instanceName(cfg.User))
	}
	if err := o.checkReadiness(); err != nil {
		return err
	}
	fmt.Println("daemon responded to status")
	return nil
}

func doUninstall(cfg installConfig, o *ops) error {
	if err := cfg.validateUser(); err != nil {
		return err
	}
	if err := o.detectConflictingInstances(cfg.User); err != nil {
		return err
	}

	var errs []error
	if err := o.systemctl("stop", cfg.User, false); err != nil {
		errs = append(errs, fmt.Errorf("stop %s: %w", o.instanceName(cfg.User), err))
	}
	if err := o.systemctl("disable", cfg.User, false); err != nil {
		errs = append(errs, fmt.Errorf("disable %s: %w", o.instanceName(cfg.User), err))
	}

	// Remove application components only when they are not shared with other
	// installed instances. State, accounts, groups, and repositories are always
	// preserved by default.
	shared, err := o.sharedComponentsInUse(cfg.User)
	if err != nil {
		errs = append(errs, err)
	} else if shared {
		fmt.Println("other forgehand service instances exist; preserving shared unit file and binary")
	} else {
		if err := os.Remove(o.unitDest); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove unit %s: %w", o.unitDest, err))
		}
		if err := os.Remove(o.binDest); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove binary %s: %w", o.binDest, err))
		}
	}
	if err := o.daemonReload(); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	fmt.Printf("preserved state directory %s\n", o.stateDir)
	fmt.Println("preserved service accounts, groups, and registered repositories")
	return nil
}

// sharedComponentsInUse reports whether any forgehand service instance other
// than the selected account still exists. A missing systemctl or failed query
// is an error so uninstall cannot remove shared components on unverifiable
// state.
func (o *ops) sharedComponentsInUse(account string) (bool, error) {
	if !o.commandAvailable("systemctl") {
		return false, errors.New("systemctl is required to detect shared forgehand instances")
	}
	self := o.instanceName(account)
	ctx, cancel := contextWithTimeout()
	defer cancel()
	out, stderr, err := o.runner.Run(ctx, nil,
		"systemctl", "list-units", "--type=service", "--all",
		"--no-legend", "--no-pager", "forgehand@*.service")
	if err != nil {
		return false, fmt.Errorf("query forgehand instances: %v: %s", err, strings.TrimSpace(stderr))
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		unit := fields[0]
		if unit == self || unit == "forgehand@.service" {
			continue
		}
		if strings.HasPrefix(unit, "forgehand@") && strings.HasSuffix(unit, ".service") {
			return true, nil
		}
	}
	return false, nil
}
