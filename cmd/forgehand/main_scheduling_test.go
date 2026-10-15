package main

import (
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/ipc"
)

func TestStatusPrintsWorkerScheduling(t *testing.T) {
	received := make(chan ipc.Request, 1)
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		received <- request
		return ipc.Response{
			OK:               true,
			Message:          "running",
			Version:          "test",
			PID:              4242,
			WorkerCapacity:   2,
			ActiveWorkers:    1,
			QueuedExecutions: 3,
		}
	})

	output := captureStdout(t, func() {
		if err := status(); err != nil {
			t.Fatalf("status: %v", err)
		}
	})

	if request := <-received; request.Command != "status" {
		t.Fatalf("request = %#v", request)
	}
	for _, want := range []string{
		"Worker capacity: 2",
		"Active workers: 1",
		"Queued executions: 3",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
}

func TestSessionRunPrintsQueuePosition(t *testing.T) {
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		if request.Command != "session-run" || request.SessionID != 7 {
			t.Fatalf("request = %#v", request)
		}
		return ipc.Response{
			OK: true,
			Execution: &ipc.Execution{
				ID:            9,
				SessionID:     7,
				Status:        "QUEUED",
				CurrentStep:   0,
				TotalSteps:    5,
				QueuePosition: 2,
			},
		}
	})

	output := captureStdout(t, func() {
		if err := sessionCommand([]string{"run", "7"}); err != nil {
			t.Fatalf("sessionCommand run: %v", err)
		}
	})

	for _, want := range []string{
		"Queued session 7",
		"Status: QUEUED",
		"Queue position: 2",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("run output missing %q:\n%s", want, output)
		}
	}
}

func TestSessionResumePrintsQueuePosition(t *testing.T) {
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		if request.Command != "session-resume" || request.SessionID != 7 {
			t.Fatalf("request = %#v", request)
		}
		return ipc.Response{
			OK: true,
			Execution: &ipc.Execution{
				ID:            9,
				SessionID:     7,
				Status:        "QUEUED",
				CurrentStep:   1,
				TotalSteps:    5,
				QueuePosition: 1,
			},
		}
	})

	output := captureStdout(t, func() {
		if err := sessionCommand([]string{"resume", "7"}); err != nil {
			t.Fatalf("sessionCommand resume: %v", err)
		}
	})

	for _, want := range []string{
		"Queued resume for session 7",
		"Status: QUEUED",
		"Queue position: 1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("resume output missing %q:\n%s", want, output)
		}
	}
}

func TestSessionStatusPrintsQueuePosition(t *testing.T) {
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		if request.Command != "session-status" || request.SessionID != 7 {
			t.Fatalf("request = %#v", request)
		}
		return ipc.Response{
			OK:      true,
			Session: &ipc.Session{ID: 7, Title: "queued", State: "QUEUED"},
			Execution: &ipc.Execution{
				ID:            9,
				SessionID:     7,
				Status:        "QUEUED",
				CurrentStep:   0,
				TotalSteps:    5,
				QueuePosition: 3,
			},
		}
	})

	output := captureStdout(t, func() {
		if err := sessionStatus(7); err != nil {
			t.Fatalf("sessionStatus: %v", err)
		}
	})

	for _, want := range []string{
		"State: QUEUED",
		"Status: QUEUED",
		"Queue position: 3",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("session-status output missing %q:\n%s", want, output)
		}
	}
}
