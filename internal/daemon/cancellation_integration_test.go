package daemon

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

func assertCanceledPersisted(t *testing.T, stateDir string, sessionID int64, wantReason string) {
	t.Helper()

	db, err := state.OpenAt(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer db.Close()

	sessionState, latest, err := db.SessionExecutionStatus(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if sessionState.State != state.SessionStateCanceled {
		t.Fatalf("session state = %q, want %q", sessionState.State, state.SessionStateCanceled)
	}
	if latest == nil || latest.Status != state.ExecutionStatusCanceled {
		t.Fatalf("execution = %#v, want CANCELED", latest)
	}
	if latest.CanceledAt == nil {
		t.Fatal("canceled_at not persisted")
	}
	if wantReason != "" {
		if latest.CancelReason == nil || *latest.CancelReason != wantReason {
			t.Fatalf("cancel reason = %v, want %q", latest.CancelReason, wantReason)
		}
	}

	events, _, err := db.ListSessionEvents(context.Background(), sessionID, 0, 100)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	canceledEvents := 0
	for _, event := range events {
		if event.Type == state.EventTypeExecutionCanceled {
			canceledEvents++
		}
	}
	if canceledEvents != 1 {
		t.Fatalf("execution.canceled event count = %d, want 1", canceledEvents)
	}
}

// TestDaemonCancelRunningSession verifies scenario A: a running fake worker is
// canceled through the daemon, the terminal CANCELED state is durable, and the
// session cannot be resumed or restarted afterwards.
func TestDaemonCancelRunningSession(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	socketPath := filepath.Join(shortSocketTestDir(t), "forgehand.sock")

	process := startDaemonProcess(t, stateDir, socketPath, "0600")
	waitForDaemon(t, process, socketPath)

	start := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-start", Title: "cancel running"})
	if !start.OK || start.Session == nil {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("session-start response = %#v", start)
	}
	sessionID := start.Session.ID

	run := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-run", SessionID: sessionID})
	if !run.OK || run.Execution == nil {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("session-run response = %#v", run)
	}

	waitForDaemonSessionState(t, socketPath, sessionID, state.SessionStateRunning, 5*time.Second)

	cancel := sendDaemonRequest(t, socketPath, ipc.Request{
		Command:   "session-cancel",
		SessionID: sessionID,
		Reason:    "user requested stop",
	})
	if !cancel.OK || cancel.CancelOutcome != string(state.CancellationApplied) {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("session-cancel response = %#v", cancel)
	}
	if cancel.Execution == nil || cancel.Execution.Status != state.ExecutionStatusCanceled {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("cancel execution = %#v", cancel.Execution)
	}

	// The worker must not overwrite CANCELED after it observes the stop.
	time.Sleep(200 * time.Millisecond)

	status := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-status", SessionID: sessionID})
	if !status.OK || status.Session == nil || status.Session.State != state.SessionStateCanceled {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("session-status response = %#v", status)
	}

	resume := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-resume", SessionID: sessionID})
	if resume.OK {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("canceled session unexpectedly resumed: %#v", resume)
	}

	rerun := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-run", SessionID: sessionID})
	if rerun.OK {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("canceled session unexpectedly rerun: %#v", rerun)
	}

	assertCanceledPersisted(t, stateDir, sessionID, "user requested stop")

	stopDaemon(t, process, syscall.SIGTERM, true)
}

// TestDaemonCancelWaitingSessionIsIdempotent verifies scenario B: a
// WAITING_FOR_USER execution can be canceled and repeated cancel requests are
// idempotent.
func TestDaemonCancelWaitingSessionIsIdempotent(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	socketPath := filepath.Join(shortSocketTestDir(t), "forgehand.sock")

	process := startDaemonProcess(t, stateDir, socketPath, "0600", pauseStepEnvironment)
	waitForDaemon(t, process, socketPath)

	start := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-start", Title: "cancel waiting"})
	if !start.OK || start.Session == nil {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("session-start response = %#v", start)
	}
	sessionID := start.Session.ID

	if run := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-run", SessionID: sessionID}); !run.OK {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("session-run response = %#v", run)
	}

	waitForDaemonSessionState(t, socketPath, sessionID, state.SessionStateWaitingForUser, 10*time.Second)

	first := sendDaemonRequest(t, socketPath, ipc.Request{
		Command:   "session-cancel",
		SessionID: sessionID,
	})
	if !first.OK || first.CancelOutcome != string(state.CancellationApplied) {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("first session-cancel = %#v", first)
	}

	second := sendDaemonRequest(t, socketPath, ipc.Request{
		Command:   "session-cancel",
		SessionID: sessionID,
	})
	if !second.OK || second.CancelOutcome != string(state.CancellationAlreadyCanceled) {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("second session-cancel = %#v", second)
	}

	assertCanceledPersisted(t, stateDir, sessionID, "")

	stopDaemon(t, process, syscall.SIGTERM, true)
}

// TestDaemonCancelCompletedSessionRejected verifies scenario C: a terminal
// execution cannot be canceled.
func TestDaemonCancelCompletedSessionRejected(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	socketPath := filepath.Join(shortSocketTestDir(t), "forgehand.sock")

	process := startDaemonProcess(t, stateDir, socketPath, "0600")
	waitForDaemon(t, process, socketPath)

	start := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-start", Title: "cancel completed"})
	if !start.OK || start.Session == nil {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("session-start response = %#v", start)
	}
	sessionID := start.Session.ID

	// Complete the execution deterministically through the shared state store
	// rather than waiting out the fake worker's stepped delay.
	db, err := state.OpenAt(context.Background(), stateDir)
	if err != nil {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("open state: %v", err)
	}
	execution, err := db.StartSessionExecution(context.Background(), sessionID, 2)
	if err != nil {
		db.Close()
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("StartSessionExecution: %v", err)
	}
	for step := 1; step <= 2; step++ {
		if err := db.AdvanceSessionExecution(context.Background(), execution.ID, step); err != nil {
			db.Close()
			stopDaemon(t, process, syscall.SIGTERM, true)
			t.Fatalf("advance: %v", err)
		}
	}
	if err := db.CompleteSessionExecution(context.Background(), execution.ID); err != nil {
		db.Close()
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("CompleteSessionExecution: %v", err)
	}
	if err := db.Close(); err != nil {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("close state: %v", err)
	}

	cancel := sendDaemonRequest(t, socketPath, ipc.Request{
		Command:   "session-cancel",
		SessionID: sessionID,
	})
	if cancel.OK {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("completed session unexpectedly canceled: %#v", cancel)
	}
	if cancel.CancelOutcome != string(state.CancellationRejectedTerminal) {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("outcome = %q, want %q", cancel.CancelOutcome, state.CancellationRejectedTerminal)
	}

	stopDaemon(t, process, syscall.SIGTERM, true)
}

// TestDaemonCancelSurvivesRestart verifies scenario D: CANCELED is durable and
// remains terminal across a daemon restart.
func TestDaemonCancelSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	socketPath := filepath.Join(shortSocketTestDir(t), "forgehand.sock")

	process := startDaemonProcess(t, stateDir, socketPath, "0600", pauseStepEnvironment)
	waitForDaemon(t, process, socketPath)

	start := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-start", Title: "cancel restart"})
	if !start.OK || start.Session == nil {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("session-start response = %#v", start)
	}
	sessionID := start.Session.ID

	if run := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-run", SessionID: sessionID}); !run.OK {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("session-run response = %#v", run)
	}
	waitForDaemonSessionState(t, socketPath, sessionID, state.SessionStateWaitingForUser, 10*time.Second)

	cancel := sendDaemonRequest(t, socketPath, ipc.Request{
		Command:   "session-cancel",
		SessionID: sessionID,
		Reason:    "before restart",
	})
	if !cancel.OK || cancel.CancelOutcome != string(state.CancellationApplied) {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("session-cancel response = %#v", cancel)
	}

	stopDaemon(t, process, syscall.SIGTERM, true)

	restarted := startDaemonProcess(t, stateDir, socketPath, "0600", pauseStepEnvironment)
	waitForDaemon(t, restarted, socketPath)

	status := waitForDaemonSessionState(t, socketPath, sessionID, state.SessionStateCanceled, 5*time.Second)
	if status.Execution == nil || status.Execution.Status != state.ExecutionStatusCanceled {
		stopDaemon(t, restarted, syscall.SIGTERM, true)
		t.Fatalf("restarted execution = %#v", status.Execution)
	}
	if status.Execution.CancelReason == nil || *status.Execution.CancelReason != "before restart" {
		stopDaemon(t, restarted, syscall.SIGTERM, true)
		t.Fatalf("restarted reason = %v, want %q", status.Execution.CancelReason, "before restart")
	}

	resume := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-resume", SessionID: sessionID})
	if resume.OK {
		stopDaemon(t, restarted, syscall.SIGTERM, true)
		t.Fatalf("canceled session resumed after restart: %#v", resume)
	}

	stopDaemon(t, restarted, syscall.SIGTERM, true)
}
