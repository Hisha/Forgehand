package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/ipc"
)

func startFakeDaemonSocket(
	t *testing.T,
	handle func(ipc.Request) ipc.Response,
) {
	t.Helper()

	dir, err := os.MkdirTemp(".", ".fh-cli-interaction-")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(abs) })

	socketPath := filepath.Join(abs, "forgehand.sock")
	t.Setenv("FORGEHAND_SOCKET_PATH", socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		var request ipc.Request
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			return
		}
		_ = json.NewEncoder(conn).Encode(handle(request))
	}()
}

func TestSessionQuestionsForwardsSessionAndPrintsQuestions(t *testing.T) {
	received := make(chan ipc.Request, 1)
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		received <- request
		return ipc.Response{
			OK: true,
			Interactions: []ipc.Interaction{
				{
					ID:          7,
					SessionID:   5,
					ExecutionID: 3,
					Question:    "Proceed with deployment?",
					Status:      "PENDING",
					CreatedAt:   "2026-10-06T00:00:00Z",
				},
			},
		}
	})

	output := captureStdout(t, func() {
		if err := sessionCommand([]string{"questions", "5"}); err != nil {
			t.Fatalf("sessionCommand questions: %v", err)
		}
	})

	request := <-received
	if request.Command != "session-questions" || request.SessionID != 5 {
		t.Fatalf("request = %#v", request)
	}
	for _, want := range []string{
		"Interaction 7",
		"Session: 5",
		"Execution: 3",
		"Status: PENDING",
		"Proceed with deployment?",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("questions output missing %q:\n%s", want, output)
		}
	}
}

func TestSessionQuestionsPrintsNoPendingMessage(t *testing.T) {
	startFakeDaemonSocket(t, func(ipc.Request) ipc.Response {
		return ipc.Response{OK: true}
	})

	output := captureStdout(t, func() {
		if err := sessionQuestions(9); err != nil {
			t.Fatalf("sessionQuestions: %v", err)
		}
	})
	if !strings.Contains(output, "No pending questions.") {
		t.Fatalf("questions output = %q", output)
	}
}

func TestAnswerInteractionForwardsAnswer(t *testing.T) {
	received := make(chan ipc.Request, 1)
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		received <- request
		answer := "yes, deploy to staging"
		return ipc.Response{
			OK:            true,
			AnswerApplied: true,
			Interaction: &ipc.Interaction{
				ID:     7,
				Status: "ANSWERED",
				Answer: &answer,
			},
		}
	})

	output := captureStdout(t, func() {
		if err := sessionCommand([]string{"answer", "7", "yes, deploy to staging"}); err != nil {
			t.Fatalf("sessionCommand answer: %v", err)
		}
	})

	request := <-received
	if request.Command != "interaction-answer" || request.InteractionID != 7 {
		t.Fatalf("request = %#v", request)
	}
	if request.Answer != "yes, deploy to staging" {
		t.Fatalf("answer not forwarded: %#v", request)
	}
	for _, want := range []string{"Answered interaction 7", "Status: ANSWERED"} {
		if !strings.Contains(output, want) {
			t.Fatalf("answer output missing %q:\n%s", want, output)
		}
	}
}

func TestAnswerInteractionReportsAlreadyAnswered(t *testing.T) {
	startFakeDaemonSocket(t, func(ipc.Request) ipc.Response {
		return ipc.Response{
			OK:            true,
			AnswerApplied: false,
			Interaction:   &ipc.Interaction{ID: 7, Status: "ANSWERED"},
		}
	})

	output := captureStdout(t, func() {
		if err := answerInteraction(7, "second answer"); err != nil {
			t.Fatalf("answerInteraction: %v", err)
		}
	})
	if !strings.Contains(output, "Interaction 7 was already answered") {
		t.Fatalf("answer output = %q", output)
	}
}

func TestSessionStatusPrintsAwaitingInput(t *testing.T) {
	startFakeDaemonSocket(t, func(request ipc.Request) ipc.Response {
		if request.Command != "session-status" || request.SessionID != 5 {
			t.Fatalf("request = %#v", request)
		}
		return ipc.Response{
			OK: true,
			Session: &ipc.Session{
				ID:    5,
				Title: "deploy",
				State: "WAITING_FOR_USER",
			},
			Execution: &ipc.Execution{
				ID:          3,
				SessionID:   5,
				Status:      "WAITING_FOR_USER",
				CurrentStep: 2,
				TotalSteps:  5,
			},
			Interaction: &ipc.Interaction{
				ID:       7,
				Question: "Proceed with deployment?",
				Status:   "PENDING",
			},
		}
	})

	output := captureStdout(t, func() {
		if err := sessionStatus(5); err != nil {
			t.Fatalf("sessionStatus: %v", err)
		}
	})

	for _, want := range []string{
		"Session 5",
		"State: WAITING_FOR_USER",
		"Status: WAITING_FOR_USER",
		"Progress: 2/5",
		"Awaiting input: interaction 7",
		"Proceed with deployment?",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
}

func TestSessionCommandInteractionArgumentValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "questions missing id", args: []string{"questions"}},
		{name: "questions invalid id", args: []string{"questions", "abc"}},
		{name: "questions zero id", args: []string{"questions", "0"}},
		{name: "answer missing id", args: []string{"answer"}},
		{name: "answer missing text", args: []string{"answer", "7"}},
		{name: "answer invalid id", args: []string{"answer", "abc", "text"}},
		{name: "answer zero id", args: []string{"answer", "0", "text"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := sessionCommand(testCase.args); err == nil {
				t.Fatalf("sessionCommand(%v) succeeded, want error", testCase.args)
			}
		})
	}
}
