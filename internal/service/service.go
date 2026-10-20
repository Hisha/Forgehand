package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Hisha/Forgehand/internal/config"
)

const (
	UnitName      = "forgehand.service"
	managedMarker = "# Managed by Forgehand user-service lifecycle."
)

type runner interface {
	Run(context.Context, string, ...string) (string, error)
}

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return strings.TrimSpace(output.String()), err
}

type Manager struct {
	unitPath   string
	executable string
	stateDir   string
	socketPath string
	runner     runner
}

type State struct {
	Installed   bool
	ActiveState string
	SubState    string
	MainPID     string
	UnitPath    string
}

func NewManager() (*Manager, error) {
	if os.Geteuid() == 0 {
		return nil, errors.New("Forgehand user-service operations refuse root; run as the target user")
	}
	configHome, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("determine user configuration directory: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("determine Forgehand executable: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
		executable = resolved
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve Forgehand executable: %w", err)
	}
	stateDir, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	socketPath, err := config.SocketPath()
	if err != nil {
		return nil, err
	}
	return &Manager{
		unitPath:   filepath.Join(configHome, "systemd", "user", UnitName),
		executable: executable,
		stateDir:   stateDir,
		socketPath: socketPath,
		runner:     commandRunner{},
	}, nil
}

func (m *Manager) UnitPath() string { return m.unitPath }

func (m *Manager) Install() error {
	if data, err := os.ReadFile(m.unitPath); err == nil {
		if !bytes.Contains(data, []byte(managedMarker)) {
			return fmt.Errorf("existing unit %s is not managed by Forgehand; refusing to overwrite it", m.unitPath)
		}
		m.preserveConfiguredPaths(string(data))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect user service: %w", err)
	}
	if _, err := m.systemctl("show-environment"); err != nil {
		return fmt.Errorf("systemd user manager is unavailable: %w", err)
	}
	if err := m.writeUnit(); err != nil {
		return err
	}
	if _, err := m.systemctl("daemon-reload"); err != nil {
		return err
	}
	if _, err := m.systemctl("enable", UnitName); err != nil {
		return err
	}
	return nil
}

func (m *Manager) Update() error {
	if err := m.requireManagedUnit(); err != nil {
		return err
	}
	data, err := os.ReadFile(m.unitPath)
	if err != nil {
		return fmt.Errorf("read user service: %w", err)
	}
	m.preserveConfiguredPaths(string(data))
	active, err := m.isActive()
	if err != nil {
		return err
	}
	if err := m.writeUnit(); err != nil {
		return err
	}
	if _, err := m.systemctl("daemon-reload"); err != nil {
		return err
	}
	if active {
		_, err = m.systemctl("restart", UnitName)
	}
	return err
}

// preserveConfiguredPaths keeps update/install idempotent even when the shell's
// current XDG or FORGEHAND variables differ from the environment originally
// recorded in the unit. An explicit uninstall/install is required to retarget
// a service to another state directory.
func (m *Manager) preserveConfiguredPaths(unit string) {
	for _, line := range strings.Split(unit, "\n") {
		encoded, ok := strings.CutPrefix(strings.TrimSpace(line), "Environment=")
		if !ok {
			continue
		}
		value, err := strconv.Unquote(encoded)
		if err != nil {
			continue
		}
		if state, ok := strings.CutPrefix(value, config.StateDirEnvironment+"="); ok && state != "" {
			m.stateDir = state
		}
		if socket, ok := strings.CutPrefix(value, config.SocketPathEnvironment+"="); ok && socket != "" {
			m.socketPath = socket
		}
	}
}

func (m *Manager) Start() error   { _, err := m.systemctl("start", UnitName); return err }
func (m *Manager) Stop() error    { _, err := m.systemctl("stop", UnitName); return err }
func (m *Manager) Restart() error { _, err := m.systemctl("restart", UnitName); return err }

func (m *Manager) Uninstall() error {
	if err := m.requireManagedUnit(); err != nil {
		return err
	}
	var errs []error
	if _, err := m.systemctl("disable", "--now", UnitName); err != nil {
		errs = append(errs, err)
	}
	if err := os.Remove(m.unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("remove user service: %w", err))
	}
	if _, err := m.systemctl("daemon-reload"); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (m *Manager) Status() (State, error) {
	state := State{UnitPath: m.unitPath}
	if _, err := os.Stat(m.unitPath); errors.Is(err, os.ErrNotExist) {
		return state, nil
	} else if err != nil {
		return state, fmt.Errorf("inspect user service: %w", err)
	}
	state.Installed = true
	output, err := m.systemctl("show", UnitName, "--property=ActiveState,SubState,MainPID")
	if err != nil {
		return state, err
	}
	for _, line := range strings.Split(output, "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch name {
		case "ActiveState":
			state.ActiveState = value
		case "SubState":
			state.SubState = value
		case "MainPID":
			state.MainPID = value
		}
	}
	return state, nil
}

func (m *Manager) isActive() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := m.runner.Run(ctx, "systemctl", "--user", "is-active", UnitName)
	if err == nil {
		return strings.TrimSpace(output) == "active", nil
	}
	if strings.TrimSpace(output) == "inactive" || strings.TrimSpace(output) == "failed" || strings.TrimSpace(output) == "unknown" {
		return false, nil
	}
	return false, fmt.Errorf("systemctl --user is-active %s: %v: %s", UnitName, err, output)
}

func (m *Manager) requireManagedUnit() error {
	data, err := os.ReadFile(m.unitPath)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Forgehand user service is not installed; run `forgehand service install`")
	}
	if err != nil {
		return fmt.Errorf("read user service: %w", err)
	}
	if !bytes.Contains(data, []byte(managedMarker)) {
		return fmt.Errorf("unit %s is not managed by Forgehand; refusing to modify it", m.unitPath)
	}
	return nil
}

func (m *Manager) writeUnit() error {
	data, err := m.unitContents()
	if err != nil {
		return err
	}
	dir := filepath.Dir(m.unitPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create user systemd directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".forgehand.service-*")
	if err != nil {
		return fmt.Errorf("stage user service: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure staged user service: %w", err)
	}
	if _, err := temporary.WriteString(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write staged user service: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync staged user service: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close staged user service: %w", err)
	}
	if err := os.Rename(temporaryPath, m.unitPath); err != nil {
		return fmt.Errorf("install user service: %w", err)
	}
	return nil
}

func (m *Manager) unitContents() (string, error) {
	for name, value := range map[string]string{"executable": m.executable, "state directory": m.stateDir, "socket path": m.socketPath} {
		if value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return "", fmt.Errorf("invalid %s for systemd unit", name)
		}
	}
	quote := func(value string) string { return strconv.Quote(value) }
	return managedMarker + `
[Unit]
Description=Forgehand persistent engineering daemon
Documentation=https://github.com/Hisha/Forgehand

[Service]
Type=simple
ExecStart=` + quote(m.executable) + ` daemon
Environment=` + quote(config.StateDirEnvironment+"="+m.stateDir) + `
Environment=` + quote(config.SocketPathEnvironment+"="+m.socketPath) + `
Environment=` + quote(config.SocketModeEnvironment+"=0600") + `
Restart=on-failure
RestartSec=2s
KillSignal=SIGTERM
TimeoutStopSec=90s
UMask=0077
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
ProtectSystem=full
ProtectHome=no
PrivateTmp=no

[Install]
WantedBy=default.target
`, nil
}

func (m *Manager) systemctl(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := m.runner.Run(ctx, "systemctl", append([]string{"--user"}, args...)...)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("systemd user services are unavailable: systemctl was not found")
		}
		return "", fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return output, nil
}
