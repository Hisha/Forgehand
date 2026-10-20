package main

import (
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/ipc"
)

func TestSessionStartCodingPrintsProjectIDValue(t *testing.T) {
	projectID := int64(1)
	received := make(chan ipc.Request, 1)
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		received <- request
		return ipc.Response{
			OK: true,
			Session: &ipc.Session{
				ID:        4,
				Title:     "summarize worker",
				State:     "READY",
				ProjectID: &projectID,
				Task:      "summarize worker",
				Kind:      "coding",
				Provider:  "openai-compatible",
				Model:     "mock-model",
			},
		}
	})

	output := captureStdout(t, func() {
		if err := startCodingSession("", "summarize worker", "", 1); err != nil {
			t.Fatalf("startCodingSession: %v", err)
		}
	})

	request := <-received
	if request.Command != "session-start" || request.ProjectID != 1 || request.Task != "summarize worker" {
		t.Fatalf("request = %#v", request)
	}
	for _, want := range []string{
		"Created coding session 4",
		"State: READY",
		"Project: 1",
		"Kind: coding",
		"Provider: openai-compatible",
		"Model: mock-model",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("coding start output missing %q:\n%s", want, output)
		}
	}
}

func TestSessionStartInvestigationSendsExplicitKindAndSelection(t *testing.T) {
	projectID := int64(7)
	received := make(chan ipc.Request, 1)
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		received <- request
		return ipc.Response{OK: true, Session: &ipc.Session{ID: 12, Title: "why", State: "READY", ProjectID: &projectID, Task: "why does greet fail?", Kind: "investigation"}}
	})
	output := captureStdout(t, func() {
		err := sessionCommand([]string{"start", "--investigate", "--project", "7", "--task", "why does greet fail?", "--component", "example/greet", "--analysis", "9", "--title", "why"})
		if err != nil {
			t.Fatalf("sessionCommand: %v", err)
		}
	})
	request := <-received
	if request.Kind != "investigation" || request.ProjectID != 7 || request.Identity != "example/greet" || request.AnalysisID != 9 {
		t.Fatalf("request = %#v", request)
	}
	if !strings.Contains(output, "Created investigation session 12") || !strings.Contains(output, "Kind: investigation") {
		t.Fatalf("output = %s", output)
	}
}

func TestSessionStatusPrintsCodingFields(t *testing.T) {
	projectID := int64(1)
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		if request.Command != "session-status" {
			t.Fatalf("request = %#v", request)
		}
		return ipc.Response{
			OK: true,
			Session: &ipc.Session{
				ID:        4,
				Title:     "summarize worker",
				State:     "COMPLETED",
				ProjectID: &projectID,
				Task:      "summarize worker",
				Kind:      "coding",
				Provider:  "openai-compatible",
				Model:     "mock-model",
			},
			Execution: &ipc.Execution{ID: 9, Status: "COMPLETED", CurrentStep: 5, TotalSteps: 5},
		}
	})

	output := captureStdout(t, func() {
		if err := sessionStatus(4); err != nil {
			t.Fatalf("sessionStatus: %v", err)
		}
	})

	for _, want := range []string{
		"Project: 1",
		"Kind: coding",
		"Task: summarize worker",
		"Provider: openai-compatible",
		"Model: mock-model",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
}

func TestSessionStatusPrintsFailureDiagnostic(t *testing.T) {
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		return ipc.Response{
			OK:        true,
			Session:   &ipc.Session{ID: 3, Title: "failed investigation", State: "FAILED", Kind: "investigation"},
			Execution: &ipc.Execution{ID: 3, Status: "FAILED", TotalSteps: 5, FailureKind: "repository_context", FailureReason: "no readable committed source evidence"},
		}
	})
	output := captureStdout(t, func() {
		if err := sessionStatus(3); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"Failure kind: repository_context", "Failure reason: no readable committed source evidence"} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
}

func TestSessionResultPrintsContent(t *testing.T) {
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		if request.Command != "session-result" || request.SessionID != 4 {
			t.Fatalf("request = %#v", request)
		}
		return ipc.Response{
			OK: true,
			Result: &ipc.ModelResult{
				SessionID:   4,
				ExecutionID: 9,
				Provider:    "openai-compatible",
				Model:       "mock-model",
				Content:     "model says hello",
				CreatedAt:   "2026-10-16T00:00:00Z",
			},
		}
	})

	output := captureStdout(t, func() {
		if err := sessionResult(4); err != nil {
			t.Fatalf("sessionResult: %v", err)
		}
	})

	for _, want := range []string{
		"Session: 4",
		"Execution: 9",
		"Provider: openai-compatible",
		"Model: mock-model",
		"model says hello",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("result output missing %q:\n%s", want, output)
		}
	}
}

func TestStartCodingSessionValidatesInputs(t *testing.T) {
	cases := []struct {
		name      string
		task      string
		projectID int64
	}{
		{name: "missing project", task: "do it", projectID: 0},
		{name: "missing task", task: "   ", projectID: 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := startCodingSession("", testCase.task, "", testCase.projectID); err == nil {
				t.Fatal("startCodingSession succeeded, want error")
			}
		})
	}
}
