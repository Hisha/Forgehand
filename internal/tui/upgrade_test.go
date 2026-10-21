package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/service"
	"github.com/Hisha/Forgehand/internal/version"
)

type fakeServiceController struct {
	state      service.State
	statusErr  error
	startErr   error
	restartErr error
	updateErr  error
	calls      []string
}

func (f *fakeServiceController) Status() (service.State, error) {
	f.calls = append(f.calls, "status")
	return f.state, f.statusErr
}
func (f *fakeServiceController) Start() error {
	f.calls = append(f.calls, "start")
	return f.startErr
}
func (f *fakeServiceController) Restart() error {
	f.calls = append(f.calls, "restart")
	return f.restartErr
}
func (f *fakeServiceController) Update() error {
	f.calls = append(f.calls, "update")
	return f.updateErr
}

func upgradeServiceState(executable string) service.State {
	return service.State{
		Installed:           true,
		Managed:             true,
		ActiveState:         "active",
		SubState:            "running",
		MainPID:             "77",
		EffectiveExecutable: executable,
		CurrentExecutable:   "/current/forgehand",
		ExecutableMatches:   executable == "/current/forgehand",
	}
}

func TestBuildMismatchOpensUpgradeScreen(t *testing.T) {
	controller := &fakeServiceController{state: upgradeServiceState("/current/forgehand")}
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		if request.Command != "status" {
			return ipc.Response{}, errors.New("unexpected request")
		}
		return ipc.Response{OK: true, ProtocolVersion: ipc.ProtocolVersion, BuildIdentity: "older-build", PID: 77, Executable: "/current/forgehand"}, nil
	}}
	m := newModel(client)
	m.serviceFactory = func() (serviceController, error) { return controller, nil }
	message := m.Init()().(loadedMsg)
	updated, _ := m.Update(message)
	m = updated.(model)

	if m.screen != screenUpgrade {
		t.Fatalf("screen = %v", m.screen)
	}
	view := m.View().Content
	for _, want := range []string{"Daemon upgrade required", version.BuildIdentity(), "older-build", "Active workers: 0", "r restart"} {
		if !strings.Contains(view, want) {
			t.Fatalf("upgrade view missing %q:\n%s", want, view)
		}
	}
}

func TestActiveWorkRequiresExplicitUpgradeConfirmation(t *testing.T) {
	controller := &fakeServiceController{state: upgradeServiceState("/current/forgehand")}
	m := model{
		client:         &fakeTransport{handler: func(ipc.Request) (ipc.Response, error) { return ipc.Response{}, nil }},
		serviceFactory: func() (serviceController, error) { return controller, nil },
		screen:         screenUpgrade,
		upgrade: &daemonUpgrade{
			daemon:                 ipc.Response{OK: true, PID: 77, ActiveWorkers: 2, QueuedExecutions: 3},
			service:                controller.state,
			protocolCompatible:     true,
			activeInspectionSafe:   true,
			managedDaemonConfirmed: true,
		},
	}
	updated, command := m.Update(key('r', "r"))
	m = updated.(model)
	if command != nil || m.screen != screenUpgradeConfirm {
		t.Fatalf("restart did not stop for confirmation: screen=%v command=%v", m.screen, command)
	}
	if strings.Contains(strings.Join(controller.calls, ","), "restart") {
		t.Fatalf("service restarted before confirmation: %v", controller.calls)
	}
	view := m.View().Content
	for _, want := range []string{"2 active worker", "3 queued", "INTERRUPTED", "not automatically retry"} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirmation missing %q:\n%s", want, view)
		}
	}
}

func TestMismatchedServiceUpdateRestartsAndReconnects(t *testing.T) {
	controller := &fakeServiceController{state: upgradeServiceState("/old/forgehand")}
	controller.state.ExecutableMatches = false
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		if request.Command != "status" {
			return ipc.Response{}, errors.New("unexpected request")
		}
		return ipc.Response{OK: true, ProtocolVersion: ipc.ProtocolVersion, BuildIdentity: version.BuildIdentity(), PID: 77}, nil
	}}
	m := model{
		client:         client,
		serviceFactory: func() (serviceController, error) { return controller, nil },
		screen:         screenUpgrade,
		upgrade: &daemonUpgrade{
			daemon:                 ipc.Response{OK: true, PID: 77, BuildIdentity: "old"},
			service:                controller.state,
			protocolCompatible:     true,
			activeInspectionSafe:   true,
			managedDaemonConfirmed: true,
		},
	}
	updated, command := m.Update(key('u', "u"))
	m = updated.(model)
	if command == nil || !m.busy {
		t.Fatal("update did not begin")
	}
	message := command().(recoveredMsg)
	if message.err != nil {
		t.Fatalf("upgrade command failed: %v", message.err)
	}
	if got := strings.Join(controller.calls, ","); got != "update,restart,status" {
		t.Fatalf("service calls = %s", got)
	}
}

func TestUpgradeRestartFailureRemainsActionable(t *testing.T) {
	controller := &fakeServiceController{state: upgradeServiceState("/current/forgehand"), restartErr: errors.New("systemd refused restart")}
	m := model{
		client: &fakeTransport{handler: func(ipc.Request) (ipc.Response, error) {
			return ipc.Response{OK: true, ProtocolVersion: ipc.ProtocolVersion, BuildIdentity: "old", PID: 77}, nil
		}},
		serviceFactory: func() (serviceController, error) { return controller, nil },
		screen:         screenUpgrade,
		upgrade: &daemonUpgrade{
			daemon:                 ipc.Response{OK: true, PID: 77},
			service:                controller.state,
			protocolCompatible:     true,
			activeInspectionSafe:   true,
			managedDaemonConfirmed: true,
		},
	}
	updated, command := m.Update(key('r', "r"))
	m = updated.(model)
	message := command().(recoveredMsg)
	updated, _ = m.Update(message)
	m = updated.(model)
	if m.screen != screenUpgrade || m.busy || m.upgrade.actionErr == nil {
		t.Fatalf("failed restart state = %#v", m)
	}
	if !strings.Contains(m.View().Content, "systemd refused restart") {
		t.Fatalf("failure is not visible:\n%s", m.View().Content)
	}
}

func TestProtocolMismatchDisablesAutomaticRestart(t *testing.T) {
	m := model{screen: screenUpgrade, upgrade: &daemonUpgrade{daemon: ipc.Response{ProtocolVersion: ipc.ProtocolVersion + 1}}}
	updated, command := m.Update(key('r', "r"))
	m = updated.(model)
	if command != nil || m.busy {
		t.Fatal("protocol mismatch started an unsafe restart")
	}
	if m.upgrade.actionErr == nil || !strings.Contains(m.upgrade.actionErr.Error(), "cannot safely inspect") {
		t.Fatalf("action error = %v", m.upgrade.actionErr)
	}
}

func TestLegacyCapabilityUnitOffersExplicitRepair(t *testing.T) {
	state := upgradeServiceState("/current/forgehand")
	state.NeedsUpdate = true
	state.UpdateReason = "legacy empty capability directives"
	m := model{screen: screenUpgrade, upgrade: &daemonUpgrade{
		daemon:                 ipc.Response{OK: true, PID: 77},
		service:                state,
		protocolCompatible:     true,
		activeInspectionSafe:   true,
		managedDaemonConfirmed: true,
	}}
	view := m.View().Content
	for _, want := range []string{"Service repair required", "legacy empty capability directives", "u update"} {
		if !strings.Contains(view, want) {
			t.Fatalf("repair view missing %q:\n%s", want, view)
		}
	}
	updated, command := m.Update(key('r', "r"))
	m = updated.(model)
	if command != nil || m.upgrade.actionErr == nil {
		t.Fatal("plain restart bypassed required service repair")
	}
}

func TestUpgradeConfirmationCanBeCancelledWithoutServiceAction(t *testing.T) {
	controller := &fakeServiceController{}
	m := model{screen: screenUpgradeConfirm, upgradeAction: "restart", upgrade: &daemonUpgrade{daemon: ipc.Response{ActiveWorkers: 1}}, serviceFactory: func() (serviceController, error) { return controller, nil }}
	updated, command := m.Update(key(tea.KeyEscape, ""))
	m = updated.(model)
	if command != nil || m.screen != screenUpgrade || len(controller.calls) != 0 {
		t.Fatalf("cancel state screen=%v command=%v calls=%v", m.screen, command, controller.calls)
	}
}

func TestConfirmedActiveUpgradeRestartsAndReconnects(t *testing.T) {
	controller := &fakeServiceController{state: upgradeServiceState("/current/forgehand")}
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		return ipc.Response{OK: true, ProtocolVersion: ipc.ProtocolVersion, BuildIdentity: version.BuildIdentity(), PID: 77}, nil
	}}
	m := model{
		client:         client,
		serviceFactory: func() (serviceController, error) { return controller, nil },
		screen:         screenUpgradeConfirm,
		upgradeAction:  "restart",
		upgrade: &daemonUpgrade{
			daemon:                 ipc.Response{OK: true, PID: 77, ActiveWorkers: 1},
			service:                controller.state,
			protocolCompatible:     true,
			activeInspectionSafe:   true,
			managedDaemonConfirmed: true,
		},
	}
	updated, command := m.Update(key('y', "y"))
	m = updated.(model)
	if command == nil || !m.busy {
		t.Fatal("explicit confirmation did not begin restart")
	}
	message := command().(recoveredMsg)
	if message.err != nil {
		t.Fatalf("confirmed restart failed: %v", message.err)
	}
	if got := strings.Join(controller.calls, ","); got != "restart,status" {
		t.Fatalf("service calls = %s", got)
	}
}

func TestUpgradeRechecksForNewActiveWorkBeforeRestart(t *testing.T) {
	controller := &fakeServiceController{state: upgradeServiceState("/current/forgehand")}
	client := &fakeTransport{handler: func(ipc.Request) (ipc.Response, error) {
		return ipc.Response{OK: true, ProtocolVersion: ipc.ProtocolVersion, BuildIdentity: "old", PID: 77, ActiveWorkers: 1}, nil
	}}
	m := model{
		client:         client,
		serviceFactory: func() (serviceController, error) { return controller, nil },
		screen:         screenUpgrade,
		upgrade: &daemonUpgrade{
			daemon:                 ipc.Response{OK: true, PID: 77},
			service:                controller.state,
			protocolCompatible:     true,
			activeInspectionSafe:   true,
			managedDaemonConfirmed: true,
		},
	}
	updated, command := m.Update(key('r', "r"))
	m = updated.(model)
	message := command().(recoveredMsg)
	if message.err == nil || !message.refresh || !strings.Contains(message.err.Error(), "active work appeared") {
		t.Fatalf("preflight error = %v", message.err)
	}
	if len(controller.calls) != 0 {
		t.Fatalf("service changed despite new work: %v", controller.calls)
	}
}
