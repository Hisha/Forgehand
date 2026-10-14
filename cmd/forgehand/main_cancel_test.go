package main

import (
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/ipc"
)

func TestSessionCancelForwardsReasonAndPrintsResult(t *testing.T) {
	received := make(chan ipc.Request, 1)
	reason := "user changed plans"
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		received <- request
		return ipc.Response{
			OK:            true,
			Canceled:      true,
			CancelOutcome: "canceled",
			Session: &ipc.Session{
				ID:    5,
				Title: "deploy",
				State: "CANCELED",
			},
			Execution: &ipc.Execution{
				ID:           3,
				SessionID:    5,
				Status:       "CANCELED",
				CurrentStep:  2,
				TotalSteps:   5,
				CancelReason: &reason,
			},
		}
	})

	output := captureStdout(t, func() {
		if err := sessionCommand([]string{"cancel", "5", "--reason", "user changed plans"}); err != nil {
			t.Fatalf("sessionCommand cancel: %v", err)
		}
	})

	request := <-received
	if request.Command != "session-cancel" || request.SessionID != 5 {
		t.Fatalf("request = %#v", request)
	}
	if request.Reason != "user changed plans" {
		t.Fatalf("reason not forwarded: %#v", request)
	}
	for _, want := range []string{
		"Canceled session 5",
		"State: CANCELED",
		"Execution: 3",
		"Status: CANCELED",
		"Progress: 2/5",
		"Reason: user changed plans",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("cancel output missing %q:\n%s", want, output)
		}
	}
}

func TestSessionCancelWithoutReasonOmitsReasonFlag(t *testing.T) {
	received := make(chan ipc.Request, 1)
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		received <- request
		return ipc.Response{
			OK:            true,
			Canceled:      true,
			CancelOutcome: "canceled",
			Session:       &ipc.Session{ID: 5, State: "CANCELED"},
			Execution:     &ipc.Execution{ID: 3, SessionID: 5, Status: "CANCELED"},
		}
	})

	_ = captureStdout(t, func() {
		if err := sessionCommand([]string{"cancel", "5"}); err != nil {
			t.Fatalf("sessionCommand cancel: %v", err)
		}
	})

	request := <-received
	if request.Reason != "" {
		t.Fatalf("unexpected reason: %#v", request)
	}
}

func TestSessionCancelReportsAlreadyCanceled(t *testing.T) {
	startFakeDaemonSocket(t, func(ipc.Request) ipc.Response {
		return ipc.Response{
			OK:            true,
			CancelOutcome: "already_canceled",
			Session:       &ipc.Session{ID: 5, State: "CANCELED"},
			Execution:     &ipc.Execution{ID: 3, SessionID: 5, Status: "CANCELED"},
		}
	})

	output := captureStdout(t, func() {
		if err := sessionCommand([]string{"cancel", "5"}); err != nil {
			t.Fatalf("sessionCommand cancel: %v", err)
		}
	})
	if !strings.Contains(output, "Session 5 was already canceled") {
		t.Fatalf("cancel output = %q", output)
	}
}

func TestSessionCancelSurfacesDaemonError(t *testing.T) {
	startFakeDaemonSocket(t, func(ipc.Request) ipc.Response {
		return ipc.Response{
			OK:            false,
			CancelOutcome: "rejected_terminal",
			Message:       "execution 3 is COMPLETED; cancellation is not permitted",
		}
	})

	err := sessionCommand([]string{"cancel", "5"})
	if err == nil {
		t.Fatal("sessionCommand cancel succeeded on terminal execution")
	}
	if !strings.Contains(err.Error(), "cancellation is not permitted") {
		t.Fatalf("error = %q", err)
	}
}

func TestSessionStatusPrintsCancellation(t *testing.T) {
	canceledAt := "2026-10-06T00:00:00Z"
	reason := "superseded"
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		if request.Command != "session-status" || request.SessionID != 5 {
			t.Fatalf("request = %#v", request)
		}
		return ipc.Response{
			OK:      true,
			Session: &ipc.Session{ID: 5, Title: "deploy", State: "CANCELED"},
			Execution: &ipc.Execution{
				ID:           3,
				SessionID:    5,
				Status:       "CANCELED",
				CurrentStep:  2,
				TotalSteps:   5,
				CanceledAt:   &canceledAt,
				CancelReason: &reason,
			},
		}
	})

	output := captureStdout(t, func() {
		if err := sessionStatus(5); err != nil {
			t.Fatalf("sessionStatus: %v", err)
		}
	})
	for _, want := range []string{
		"State: CANCELED",
		"Status: CANCELED",
		"Canceled: 2026-10-06T00:00:00Z",
		"Cancel reason: superseded",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
}

func TestSessionCommandCancelArgumentValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "missing id", args: []string{"cancel"}},
		{name: "invalid id", args: []string{"cancel", "abc"}},
		{name: "zero id", args: []string{"cancel", "0"}},
		{name: "reason missing value", args: []string{"cancel", "5", "--reason"}},
		{name: "unknown flag", args: []string{"cancel", "5", "--force"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := sessionCommand(testCase.args); err == nil {
				t.Fatalf("sessionCommand(%v) succeeded, want error", testCase.args)
			}
		})
	}
}
