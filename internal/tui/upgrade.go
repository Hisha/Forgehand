package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/service"
	"github.com/Hisha/Forgehand/internal/version"
)

type serviceController interface {
	Status() (service.State, error)
	Start() error
	Restart() error
	Update() error
}

type serviceFactory func() (serviceController, error)

func defaultServiceFactory() (serviceController, error) { return service.NewManager() }

type daemonUpgrade struct {
	daemon                 ipc.Response
	service                service.State
	serviceErr             error
	protocolCompatible     bool
	activeInspectionSafe   bool
	managedDaemonConfirmed bool
	actionErr              error
}

func upgradeRequired(response ipc.Response) bool {
	if response.ProtocolVersion == 0 {
		return false
	}
	return response.ProtocolVersion != ipc.ProtocolVersion ||
		response.BuildIdentity == "" ||
		response.BuildIdentity != version.BuildIdentity()
}

func compatibilityMismatch(response ipc.Response, err error) bool {
	if upgradeRequired(response) {
		return true
	}
	if err == nil {
		return false
	}
	diagnostic := strings.ToLower(err.Error())
	return strings.Contains(diagnostic, "incompatible") && strings.Contains(diagnostic, "protocol")
}

func inspectDaemonUpgrade(response ipc.Response, factory serviceFactory) *daemonUpgrade {
	upgrade := &daemonUpgrade{
		daemon:               response,
		protocolCompatible:   response.ProtocolVersion == ipc.ProtocolVersion,
		activeInspectionSafe: response.ProtocolVersion == ipc.ProtocolVersion && response.OK,
	}
	manager, err := factory()
	if err != nil {
		upgrade.serviceErr = err
		return upgrade
	}
	upgrade.service, upgrade.serviceErr = manager.Status()
	if upgrade.serviceErr == nil && upgrade.service.Installed && upgrade.service.ActiveState == "active" {
		upgrade.managedDaemonConfirmed = upgrade.service.MainPID != "" &&
			upgrade.service.MainPID != "0" &&
			strconv.Itoa(response.PID) == upgrade.service.MainPID
	}
	return upgrade
}

func (u *daemonUpgrade) activeWork() bool {
	return u != nil && u.activeInspectionSafe &&
		(u.daemon.ActiveWorkers > 0 || u.daemon.QueuedExecutions > 0)
}

func (u *daemonUpgrade) serviceNeedsUpdate() bool {
	return u != nil && (!u.service.ExecutableMatches || u.service.NeedsUpdate)
}

func (u *daemonUpgrade) restartAllowed(update bool) (bool, string) {
	if u == nil {
		return false, "Daemon upgrade details are unavailable."
	}
	if !u.protocolCompatible || !u.activeInspectionSafe {
		return false, "The IPC protocols differ, so Forgehand cannot safely inspect active work. Stop or upgrade the daemon manually after verifying its jobs."
	}
	if u.serviceErr != nil {
		return false, "Could not inspect the user service: " + u.serviceErr.Error()
	}
	if !u.service.Installed {
		return false, "The running daemon is not associated with an installed Forgehand user service. Stop that exact daemon before installing the user service."
	}
	if !u.service.Managed {
		return false, "The installed unit is not Forgehand-managed, so it will not be overwritten. Review the unit and its drop-ins manually."
	}
	if !u.managedDaemonConfirmed {
		return false, "The daemon PID does not match the active user service. Refusing to start a duplicate daemon."
	}
	if update {
		return true, ""
	}
	if u.service.NeedsUpdate {
		return false, "The service definition contains " + u.service.UpdateReason + ". Use U to repair the managed definition before restarting."
	}
	if !u.service.ExecutableMatches {
		return false, "The service points to a different executable. Use U to update the managed definition before restarting."
	}
	return true, ""
}

func (m model) upgradeView() string {
	u := m.upgrade
	if u == nil {
		return "Forgehand · Daemon upgrade required\n\nUpgrade details are unavailable.\n\nq quit\n"
	}
	daemonBuild := u.daemon.BuildIdentity
	if daemonBuild == "" {
		daemonBuild = "unknown"
	}
	var b strings.Builder
	b.WriteString("Forgehand · Daemon upgrade required\n\n")
	fmt.Fprintf(&b, "Client build: %s\nDaemon build: %s\n", version.BuildIdentity(), daemonBuild)
	if u.daemon.Executable != "" {
		fmt.Fprintf(&b, "Running executable: %s\n", u.daemon.Executable)
	}
	if u.serviceErr != nil {
		fmt.Fprintf(&b, "Service inspection: %s\n", u.serviceErr)
	} else if !u.service.Installed {
		b.WriteString("User service: not installed\n")
	} else {
		fmt.Fprintf(&b, "User service: %s (%s), PID %s\n", u.service.ActiveState, u.service.SubState, u.service.MainPID)
		fmt.Fprintf(&b, "Service executable: %s\n", u.service.EffectiveExecutable)
		fmt.Fprintf(&b, "Current executable: %s\n", u.service.CurrentExecutable)
		if u.service.NeedsUpdate {
			fmt.Fprintf(&b, "Service repair required: %s\n", u.service.UpdateReason)
		}
	}
	b.WriteString("\nThe running daemon uses a different Forgehand build.\n")
	if !u.activeInspectionSafe {
		b.WriteString("Active work cannot be inspected safely across this IPC protocol mismatch. Automatic restart is disabled.\n")
	} else {
		fmt.Fprintf(&b, "Active workers: %d\nQueued executions: %d\n", u.daemon.ActiveWorkers, u.daemon.QueuedExecutions)
		if u.activeWork() {
			b.WriteString("Restarting will interrupt running work. Durable state is preserved; interrupted work is not retried automatically.\n")
		} else {
			b.WriteString("No running or queued work was reported. Waiting interactions and SQLite state are preserved.\n")
		}
	}
	if u.actionErr != nil {
		fmt.Fprintf(&b, "\nUpgrade failed: %s\n", u.actionErr)
	}
	if allowed, _ := u.restartAllowed(false); allowed {
		b.WriteString("\nr restart with the current executable")
	}
	if allowed, _ := u.restartAllowed(true); allowed && u.serviceNeedsUpdate() {
		b.WriteString("\nu update the managed service definition, then restart")
	}
	if _, reason := u.restartAllowed(u.serviceNeedsUpdate()); reason != "" {
		b.WriteString("\n\n" + reason)
	}
	b.WriteString("\n\nq quit\n")
	if m.busy {
		b.WriteString("\nWorking…\n")
	}
	return b.String()
}

func (m model) upgradeConfirmView() string {
	action := "restart the daemon"
	if m.upgradeAction == "update" {
		action = "update the managed service definition and restart the daemon"
	}
	return fmt.Sprintf("Forgehand · Confirm daemon upgrade\n\n%d active worker(s) and %d queued execution(s) were reported.\n\nThis will %s. Running executions will become INTERRUPTED during clean shutdown; queued work, waiting interactions, and SQLite state remain durable. Forgehand will not automatically retry interrupted work.\n\ny confirm  b/esc back  q quit\n", m.upgrade.daemon.ActiveWorkers, m.upgrade.daemon.QueuedExecutions, action)
}

func (m model) beginUpgradeAction(update bool) (tea.Model, tea.Cmd) {
	allowed, reason := m.upgrade.restartAllowed(update)
	if !allowed {
		m.upgrade.actionErr = errors.New(reason)
		return m, nil
	}
	m.upgrade.actionErr = nil
	m.upgradeAction = "restart"
	if update {
		m.upgradeAction = "update"
	}
	if m.upgrade.activeWork() {
		m.screen = screenUpgradeConfirm
		return m, nil
	}
	m.busy = true
	return m, upgradeServiceCmd(m.client, m.serviceFactory, update, m.upgrade.daemon.PID, false)
}

func upgradeServiceCmd(client transport, factory serviceFactory, update bool, expectedPID int, activeAuthorized bool) tea.Cmd {
	return func() tea.Msg {
		before, err := client.Request(ipc.Request{Command: "status"})
		if err != nil {
			return recoveredMsg{err: fmt.Errorf("recheck daemon before restart: %w", err)}
		}
		if before.ProtocolVersion != ipc.ProtocolVersion || !before.OK {
			return recoveredMsg{err: errors.New("daemon status became incompatible before restart; inspect active work again")}
		}
		if before.PID != expectedPID {
			return recoveredMsg{err: fmt.Errorf("daemon PID changed from %d to %d; inspect the service again before restarting", expectedPID, before.PID)}
		}
		if !activeAuthorized && (before.ActiveWorkers > 0 || before.QueuedExecutions > 0) {
			return recoveredMsg{err: fmt.Errorf("active work appeared after the upgrade screen was opened"), refresh: true}
		}

		manager, err := factory()
		if err != nil {
			return recoveredMsg{err: err}
		}
		if update {
			if err := manager.Update(); err != nil {
				return recoveredMsg{err: fmt.Errorf("update user service: %w", err)}
			}
		}
		if err := manager.Restart(); err != nil {
			return recoveredMsg{err: fmt.Errorf("restart user service: %w", err)}
		}

		deadline := time.Now().Add(10 * time.Second)
		var last error
		for time.Now().Before(deadline) {
			state, stateErr := manager.Status()
			response, requestErr := client.Request(ipc.Request{Command: "status"})
			if stateErr == nil && requestErr == nil && response.OK &&
				response.ProtocolVersion == ipc.ProtocolVersion &&
				response.BuildIdentity == version.BuildIdentity() &&
				state.ActiveState == "active" && state.MainPID == strconv.Itoa(response.PID) {
				return recoveredMsg{}
			}
			last = errors.Join(stateErr, requestErr)
			if last == nil {
				last = fmt.Errorf("service PID %s has not reconnected on the expected build", state.MainPID)
			}
			time.Sleep(100 * time.Millisecond)
		}
		return recoveredMsg{err: fmt.Errorf("daemon did not reconnect within 10 seconds: %w", last)}
	}
}
