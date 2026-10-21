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

const pauseStepEnvironment = "FORGEHAND_FAKE_WORKER_PAUSE_STEP=1"

func waitForDaemonSessionAndExecutionState(
	t *testing.T,
	socketPath string,
	sessionID int64,
	want string,
	timeout time.Duration,
) ipc.Response {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		response := sendDaemonRequest(t, socketPath, ipc.Request{
			Command:   "session-status",
			SessionID: sessionID,
		})
		if response.OK && response.Session != nil && response.Session.State == want && response.Execution != nil && response.Execution.Status == want {
			return response
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for session %d and execution state %s; last = %#v", sessionID, want, response)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func assertWaitingPersisted(
	t *testing.T,
	stateDir string,
	sessionID int64,
	interactionID int64,
) {
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
	if sessionState.State != state.SessionStateWaitingForUser {
		t.Fatalf("session state = %q, want %q", sessionState.State, state.SessionStateWaitingForUser)
	}
	if latest == nil || latest.Status != state.ExecutionStatusWaitingForUser {
		t.Fatalf("execution = %#v, want WAITING_FOR_USER", latest)
	}
	if latest.CurrentStep != 1 {
		t.Fatalf("current step = %d, want 1", latest.CurrentStep)
	}

	pending, err := db.GetPendingHumanInput(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetPendingHumanInput: %v", err)
	}
	if pending == nil || pending.ID != interactionID {
		t.Fatalf("pending interaction = %#v, want ID %d", pending, interactionID)
	}
}

// TestDaemonGracefulShutdownPreservesWaitingInteraction runs a paused worker to
// WAITING_FOR_USER, shuts the daemon down cleanly, and verifies the waiting
// state and pending question survive. A restarted daemon must still expose the
// question and resume the execution once it is answered.
func TestDaemonGracefulShutdownPreservesWaitingInteraction(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	socketPath := filepath.Join(shortSocketTestDir(t), "forgehand.sock")

	process := startDaemonProcess(t, stateDir, socketPath, "0600", pauseStepEnvironment)
	waitForDaemon(t, process, socketPath)

	start := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-start", Title: "graceful wait"})
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

	waiting := waitForDaemonSessionAndExecutionState(t, socketPath, sessionID, state.SessionStateWaitingForUser, 10*time.Second)
	if waiting.Execution == nil || waiting.Execution.Status != state.ExecutionStatusWaitingForUser {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("waiting execution = %#v", waiting.Execution)
	}
	if waiting.Interaction == nil {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatal("session-status reported no pending interaction")
	}
	interactionID := waiting.Interaction.ID

	stopDaemon(t, process, syscall.SIGTERM, true)

	assertWaitingPersisted(t, stateDir, sessionID, interactionID)

	restarted := startDaemonProcess(t, stateDir, socketPath, "0600", pauseStepEnvironment)
	waitForDaemon(t, restarted, socketPath)

	status := waitForDaemonSessionAndExecutionState(t, socketPath, sessionID, state.SessionStateWaitingForUser, 5*time.Second)
	if status.Interaction == nil || status.Interaction.ID != interactionID {
		stopDaemon(t, restarted, syscall.SIGTERM, true)
		t.Fatalf("restarted interaction = %#v, want ID %d", status.Interaction, interactionID)
	}

	answered := sendDaemonRequest(t, socketPath, ipc.Request{
		Command:       "interaction-answer",
		InteractionID: interactionID,
		Answer:        "continue",
	})
	if !answered.OK || !answered.AnswerApplied {
		stopDaemon(t, restarted, syscall.SIGTERM, true)
		t.Fatalf("interaction-answer response = %#v", answered)
	}

	resume := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-resume", SessionID: sessionID})
	if !resume.OK || resume.Execution == nil || resume.Execution.Status != state.ExecutionStatusQueued {
		stopDaemon(t, restarted, syscall.SIGTERM, true)
		t.Fatalf("session-resume response = %#v", resume)
	}
	if resume.Execution.CurrentStep != 1 {
		stopDaemon(t, restarted, syscall.SIGTERM, true)
		t.Fatalf("resumed current step = %d, want 1", resume.Execution.CurrentStep)
	}

	running := waitForDaemonSessionAndExecutionState(t, socketPath, sessionID, state.SessionStateRunning, 5*time.Second)
	if running.Execution == nil || running.Execution.Status != state.ExecutionStatusRunning {
		stopDaemon(t, restarted, syscall.SIGTERM, true)
		t.Fatalf("resumed execution = %#v", running.Execution)
	}

	stopDaemon(t, restarted, syscall.SIGTERM, true)
}

// TestDaemonSIGKILLPreservesWaitingInteraction verifies an unclean crash while
// waiting does not interrupt the waiting execution and the durable question is
// still available after restart.
func TestDaemonSIGKILLPreservesWaitingInteraction(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	socketPath := filepath.Join(shortSocketTestDir(t), "forgehand.sock")

	process := startDaemonProcess(t, stateDir, socketPath, "0600", pauseStepEnvironment)
	waitForDaemon(t, process, socketPath)

	start := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-start", Title: "crash wait"})
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

	waiting := waitForDaemonSessionAndExecutionState(t, socketPath, sessionID, state.SessionStateWaitingForUser, 10*time.Second)
	if waiting.Interaction == nil {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatal("session-status reported no pending interaction")
	}
	interactionID := waiting.Interaction.ID

	stopDaemon(t, process, syscall.SIGKILL, false)

	assertWaitingPersisted(t, stateDir, sessionID, interactionID)

	restarted := startDaemonProcess(t, stateDir, socketPath, "0600", pauseStepEnvironment)
	waitForDaemon(t, restarted, socketPath)

	status := waitForDaemonSessionAndExecutionState(t, socketPath, sessionID, state.SessionStateWaitingForUser, 5*time.Second)
	if status.Interaction == nil || status.Interaction.ID != interactionID {
		stopDaemon(t, restarted, syscall.SIGTERM, true)
		t.Fatalf("restarted interaction = %#v, want ID %d", status.Interaction, interactionID)
	}

	stopDaemon(t, restarted, syscall.SIGTERM, true)
}

// TestDaemonRestartDoesNotDuplicateWaitingQuestion restarts a waiting daemon
// several times and confirms the same single pending interaction is reported,
// with no duplicate question.
func TestDaemonRestartDoesNotDuplicateWaitingQuestion(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	socketPath := filepath.Join(shortSocketTestDir(t), "forgehand.sock")

	process := startDaemonProcess(t, stateDir, socketPath, "0600", pauseStepEnvironment)
	waitForDaemon(t, process, socketPath)

	start := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-start", Title: "no duplicate"})
	if !start.OK || start.Session == nil {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("session-start response = %#v", start)
	}
	sessionID := start.Session.ID

	if run := sendDaemonRequest(t, socketPath, ipc.Request{Command: "session-run", SessionID: sessionID}); !run.OK {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatalf("session-run response = %#v", run)
	}

	waiting := waitForDaemonSessionAndExecutionState(t, socketPath, sessionID, state.SessionStateWaitingForUser, 10*time.Second)
	if waiting.Interaction == nil {
		stopDaemon(t, process, syscall.SIGTERM, true)
		t.Fatal("session-status reported no pending interaction")
	}
	interactionID := waiting.Interaction.ID
	stopDaemon(t, process, syscall.SIGTERM, true)

	for attempt := 0; attempt < 2; attempt++ {
		restarted := startDaemonProcess(t, stateDir, socketPath, "0600", pauseStepEnvironment)
		waitForDaemon(t, restarted, socketPath)

		questions := sendDaemonRequest(t, socketPath, ipc.Request{
			Command:   "session-questions",
			SessionID: sessionID,
		})
		if !questions.OK || len(questions.Interactions) != 1 {
			stopDaemon(t, restarted, syscall.SIGTERM, true)
			t.Fatalf("restart %d questions = %#v", attempt, questions)
		}
		if questions.Interactions[0].ID != interactionID {
			stopDaemon(t, restarted, syscall.SIGTERM, true)
			t.Fatalf("restart %d interaction ID = %d, want %d", attempt, questions.Interactions[0].ID, interactionID)
		}

		stopDaemon(t, restarted, syscall.SIGTERM, true)
	}
}
