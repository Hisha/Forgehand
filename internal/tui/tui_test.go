package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Hisha/Forgehand/internal/ipc"
)

type fakeTransport struct {
	requests []ipc.Request
	handler  func(ipc.Request) (ipc.Response, error)
}

func (f *fakeTransport) Request(request ipc.Request) (ipc.Response, error) {
	f.requests = append(f.requests, request)
	return f.handler(request)
}

func key(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Text: text})
}

func loadedModel(t *testing.T, client *fakeTransport) model {
	t.Helper()
	m := newModel(client)
	message := m.Init()().(loadedMsg)
	updated, _ := m.Update(message)
	return updated.(model)
}

func TestDashboardListsProjectsWithoutIDsAndWithoutSessionNoise(t *testing.T) {
	projectID := int64(42)
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status":
			return ipc.Response{OK: true}, nil
		case "projects":
			return ipc.Response{OK: true, Projects: []ipc.Project{{ID: 42, Name: "mod-native-hunts", RootPath: "/src/hunts"}}}, nil
		case "sessions":
			return ipc.Response{OK: true, Sessions: []ipc.Session{{ID: 91, Title: "Huntmaster eligibility", State: "COMPLETED", Kind: "investigation", ProjectID: &projectID}}}, nil
		}
		return ipc.Response{}, errors.New("unexpected request")
	}}
	m := loadedModel(t, client)
	view := m.View().Content
	if !strings.Contains(view, "mod-native-hunts") {
		t.Fatalf("dashboard missing project name:\n%s", view)
	}
	if strings.Contains(view, "Huntmaster eligibility") {
		t.Fatalf("dashboard let session history dominate the project list:\n%s", view)
	}
	if strings.Contains(view, "42") || strings.Contains(view, "91") {
		t.Fatalf("view exposed database IDs:\n%s", view)
	}
}

func TestSelectProjectAskQuestionAndStartWorkPlan(t *testing.T) {
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status":
			return ipc.Response{OK: true}, nil
		case "projects":
			return ipc.Response{OK: true, Projects: []ipc.Project{{ID: 42, Name: "mod-native-hunts"}}}, nil
		case "sessions":
			return ipc.Response{OK: true}, nil
		case "session-start":
			if request.ProjectID != 42 || request.Kind != "work-plan" || request.Task != "Which zones are eligible for Huntmaster?" {
				t.Fatalf("start request = %#v", request)
			}
			id := int64(42)
			return ipc.Response{OK: true, Session: &ipc.Session{ID: 7, Title: request.Task, Task: request.Task, State: "READY", Kind: "work-plan", ProjectID: &id}, WorkPlan: &ipc.WorkPlan{Objective: request.Task, ReadOnly: true, Status: "BLOCKED"}}, nil
		case "session-run":
			if request.SessionID != 7 {
				t.Fatalf("run request = %#v", request)
			}
			return ipc.Response{OK: true, Execution: &ipc.Execution{ID: 8, SessionID: 7, Status: "QUEUED"}}, nil
		}
		return ipc.Response{}, errors.New("unexpected request")
	}}
	m := loadedModel(t, client)
	updated, _ := m.Update(key(tea.KeyEnter, ""))
	m = updated.(model)
	if m.screen != screenProject {
		t.Fatalf("screen = %v", m.screen)
	}
	updated, _ = m.Update(key('n', "n"))
	m = updated.(model)
	if m.screen != screenQuestion {
		t.Fatalf("screen = %v", m.screen)
	}
	question := "Which zones are eligible for Huntmaster?"
	for _, r := range question {
		updated, _ = m.Update(key(r, string(r)))
		m = updated.(model)
	}
	updated, command := m.Update(key(tea.KeyEnter, ""))
	m = updated.(model)
	started := command().(startedMsg)
	updated, _ = m.Update(started)
	m = updated.(model)
	if m.screen != screenSession || m.selectedSession == nil || m.execution == nil {
		t.Fatalf("model did not open started session: %#v", m)
	}
}

func TestQuestionAcceptsBracketedPaste(t *testing.T) {
	m := model{
		screen:          screenQuestion,
		selectedProject: &ipc.Project{ID: 42, Name: "project"},
		input:           "Explain ",
	}

	if m.View().DisableBracketedPasteMode {
		t.Fatal("question view disabled terminal bracketed-paste mode")
	}
	updated, command := m.Update(tea.PasteMsg{Content: "which zones support Huntmaster — including Café?"})
	m = updated.(model)
	if command != nil {
		t.Fatal("pasting a question returned a command")
	}
	if m.input != "Explain which zones support Huntmaster — including Café?" {
		t.Fatalf("input = %q", m.input)
	}
}

func TestMultilineQuestionPasteDoesNotSubmit(t *testing.T) {
	var started ipc.Request
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "session-start":
			started = request
			return ipc.Response{OK: true, Session: &ipc.Session{ID: 7}}, nil
		case "session-run":
			return ipc.Response{OK: true, Execution: &ipc.Execution{ID: 8, SessionID: 7}}, nil
		}
		return ipc.Response{}, errors.New("unexpected request")
	}}
	m := model{
		client:          client,
		screen:          screenQuestion,
		selectedProject: &ipc.Project{ID: 42, Name: "project"},
	}

	pasted := "Which zones are eligible?\r\nInclude:\r- native hunts\n- event hunts"
	updated, command := m.Update(tea.PasteMsg{Content: pasted})
	m = updated.(model)
	if command != nil {
		t.Fatal("multiline paste submitted the investigation")
	}
	want := "Which zones are eligible?\nInclude:\n- native hunts\n- event hunts"
	if m.input != want {
		t.Fatalf("input = %q, want %q", m.input, want)
	}
	if len(client.requests) != 0 {
		t.Fatalf("paste sent daemon requests: %#v", client.requests)
	}

	updated, command = m.Update(key(tea.KeyEnter, ""))
	m = updated.(model)
	if command == nil {
		t.Fatal("normal Enter did not submit pasted question")
	}
	_ = command().(startedMsg)
	if started.Task != want {
		t.Fatalf("submitted task = %q, want %q", started.Task, want)
	}
}

func TestEscapeCancelsPastedQuestion(t *testing.T) {
	m := model{screen: screenQuestion, input: "pasted question"}

	updated, command := m.Update(key(tea.KeyEscape, ""))
	m = updated.(model)
	if command != nil {
		t.Fatal("Escape returned a command")
	}
	if m.screen != screenDashboard || m.input != "" {
		t.Fatalf("Escape left model at screen %v with input %q", m.screen, m.input)
	}
}

func TestReopenCompletedInvestigationShowsFindingsAndEvidence(t *testing.T) {
	projectID := int64(42)
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status":
			return ipc.Response{OK: true}, nil
		case "projects":
			return ipc.Response{OK: true}, nil
		case "sessions":
			return ipc.Response{OK: true, Sessions: []ipc.Session{{ID: 9, Title: "Huntmaster", State: "COMPLETED", Kind: "investigation", ProjectID: &projectID, Task: "zone eligibility"}}}, nil
		case "session-status":
			return ipc.Response{OK: true, Session: &ipc.Session{ID: 9, Title: "Huntmaster", State: "COMPLETED", Kind: "investigation", Task: "zone eligibility"}, Execution: &ipc.Execution{Status: "COMPLETED", CurrentStep: 1, TotalSteps: 1}}, nil
		case "session-result":
			return ipc.Response{
				OK:     true,
				Result: &ipc.ModelResult{Content: "Eligible zones require an active hunt."},
				Evidence: &ipc.InvestigationEvidence{
					Commit: "abcdef1234567890",
					Files: []ipc.InvestigationEvidenceFile{{
						Path: "huntmaster/zones.go", BlobHash: "1234567890abcdef",
						Locations: []ipc.InvestigationEvidenceSourceLocation{{Line: 27, Declaration: "func Eligible"}},
					}},
				},
			}, nil
		}
		return ipc.Response{}, errors.New("unexpected request")
	}}
	m := loadedModel(t, client)
	updated, _ := m.Update(key('s', "s"))
	m = updated.(model)
	if m.screen != screenSessions {
		t.Fatalf("screen = %v", m.screen)
	}
	updated, command := m.Update(key(tea.KeyEnter, ""))
	m = updated.(model)
	message := command().(sessionMsg)
	updated, _ = m.Update(message)
	m = updated.(model)
	view := m.View().Content
	for _, want := range []string{"Eligible zones require an active hunt.", "huntmaster/zones.go", "line 27", "func Eligible"} {
		if !strings.Contains(view, want) {
			t.Fatalf("session view missing %q:\n%s", want, view)
		}
	}
}

func TestWorkPlanViewShowsInvestigationTopicsPerEvidence(t *testing.T) {
	m := model{width: 120}
	m.selectedSession = &ipc.Session{ID: 3, Title: "Shutdown investigation", State: "COMPLETED", Kind: "work-plan", Task: "Investigate daemon shutdown"}
	m.workPlan = &ipc.WorkPlan{
		Objective: "Investigate daemon shutdown",
		Status:    "BLOCKED",
		ReadOnly:  true,
		Tasks: []ipc.WorkPlanTask{{
			Key:             "deterministic-discovery",
			Position:        1,
			Objective:       "Gather bounded committed-repository evidence",
			ExecutionMethod: "repository.investigation.bootstrap",
			Status:          "COMPLETED",
			Result:          "Deterministic discovery completed.",
			Evidence: []ipc.WorkEvidence{
				{Kind: "investigation-topic", Detail: "daemon shutdown — 7 matched line(s) across 6 scanned file(s)", Topics: []string{"daemon shutdown"}},
				{Kind: "candidate-file", Path: "internal/daemon/daemon.go", BlobHash: "abcdef1234567890", Detail: "inferred relevance score 337", Topics: []string{"daemon shutdown", "recovery"}},
			},
		}},
	}
	view := m.sessionBody()
	for _, want := range []string{"deterministic-discovery", "daemon shutdown", "topics: daemon shutdown, recovery", "internal/daemon/daemon.go", "Deterministic discovery completed."} {
		if !strings.Contains(view, want) {
			t.Fatalf("work plan view missing %q:\n%s", want, view)
		}
	}
}

func TestFailedSessionViewShowsFailureDiagnostic(t *testing.T) {
	m := model{
		screen:          screenSession,
		selectedSession: &ipc.Session{ID: 3, Title: "Huntmaster", State: "FAILED", Kind: "investigation"},
		execution:       &ipc.Execution{ID: 3, Status: "FAILED", TotalSteps: 5, FailureKind: "repository_context", FailureReason: "no readable committed source evidence"},
	}
	view := m.sessionBody()
	if !strings.Contains(view, "Failure (repository_context): no readable committed source evidence") {
		t.Fatalf("failed session view omitted diagnostic:\n%s", view)
	}
}

func TestModelsNavigationStartsQualificationAndDisplaysProgress(t *testing.T) {
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "qualification-create":
			if request.Model != "worker" || request.Capability != "planning" || request.Repetitions != 3 {
				t.Fatalf("request=%#v", request)
			}
			return ipc.Response{OK: true, Qualification: &ipc.QualificationJob{ID: 12, Model: "worker", Capability: "planning", Repetitions: 3, TotalEvaluations: 21, Status: "QUEUED"}}, nil
		}
		return ipc.Response{}, errors.New("unexpected")
	}}
	m := model{client: client, screen: screenDashboard, provider: &ipc.ProviderConfiguration{Configured: true, Provider: "openai-compatible", Endpoint: "http://127.0.0.1:8000/v1", Model: "worker"}, qualificationModel: "worker", qualificationRepetitions: 3, width: 80, height: 24}
	updated, _ := m.Update(key('m', "m"))
	m = updated.(model)
	if m.screen != screenModels {
		t.Fatalf("screen=%v", m.screen)
	}
	updated, command := m.Update(key('s', "s"))
	m = updated.(model)
	message := command().(qualificationMsg)
	updated, _ = m.Update(message)
	m = updated.(model)
	view := m.View().Content
	for _, want := range []string{"Endpoint: http://127.0.0.1:8000/v1", "worker", "Repetitions: 3", "#12", "0/21", "QUEUED"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestQualificationResultViewDistinguishesModelAndInfrastructureFailures(t *testing.T) {
	m := model{screen: screenQualification, selectedQualification: &ipc.QualificationJob{ID: 9, Model: "worker", Capability: "planning", Status: "FAILED", CompletedEvaluations: 1, TotalEvaluations: 21, Recommendation: "INCONCLUSIVE", FailureKind: "provider", FailureReason: "request timeout", Results: []ipc.QualificationResult{{Case: "huntmaster-geography", Attempt: 1, InfrastructureFailure: true, ValidationErrors: []string{"deadline exceeded"}}}}}
	view := m.View().Content
	for _, want := range []string{"INCONCLUSIVE", "Infrastructure failure (provider): request timeout", "huntmaster-geography", "[infrastructure]", "deadline exceeded"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestTUIReconnectsToPersistedQualification(t *testing.T) {
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status", "projects", "sessions":
			return ipc.Response{OK: true}, nil
		case "models":
			return ipc.Response{OK: true, ProviderConfiguration: &ipc.ProviderConfiguration{Configured: true, Model: "worker"}}, nil
		case "qualification-list":
			return ipc.Response{OK: true, Qualifications: []ipc.QualificationJob{{ID: 13, Model: "worker", Capability: "planning", CompletedEvaluations: 8, TotalEvaluations: 21, Status: "RUNNING"}}}, nil
		}
		return ipc.Response{}, errors.New("unexpected")
	}}
	m := loadedModel(t, client)
	if len(m.qualifications) != 1 || m.qualifications[0].CompletedEvaluations != 8 {
		t.Fatalf("qualifications=%#v", m.qualifications)
	}
	m.screen = screenModels
	view := m.View().Content
	if !strings.Contains(view, "8/21") || !strings.Contains(view, "RUNNING") {
		t.Fatalf("view=%s", view)
	}
}

func TestUnavailableDaemonOffersRecoveryActions(t *testing.T) {
	client := &fakeTransport{handler: func(ipc.Request) (ipc.Response, error) {
		return ipc.Response{}, errors.New("daemon IPC protocol 0 is incompatible")
	}}
	m := newModel(client)
	m.serviceFactory = func() (serviceController, error) {
		return nil, errors.New("systemd inspection unavailable in test")
	}
	message := m.Init()().(loadedMsg)
	updated, _ := m.Update(message)
	m = updated.(model)
	if m.screen != screenUpgrade {
		t.Fatalf("screen = %v", m.screen)
	}
	view := m.View().Content
	for _, want := range []string{"upgrade required", "cannot be inspected safely", "Automatic restart is disabled"} {
		if !strings.Contains(view, want) {
			t.Fatalf("recovery view missing %q:\n%s", want, view)
		}
	}
}

func TestRegistrationIsExplicitAction(t *testing.T) {
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status", "sessions":
			return ipc.Response{OK: true}, nil
		case "projects":
			return ipc.Response{OK: true}, nil
		case "project-add":
			return ipc.Response{OK: true, Project: &ipc.Project{ID: 1, Name: "repo", RootPath: request.Path}}, nil
		}
		return ipc.Response{}, errors.New("unexpected")
	}}
	m := loadedModel(t, client)
	updated, _ := m.Update(key('a', "a"))
	m = updated.(model)
	if m.screen != screenRegister {
		t.Fatalf("screen = %v", m.screen)
	}
	m.input = "."
	updated, command := m.Update(key(tea.KeyEnter, ""))
	m = updated.(model)
	message := command().(registeredMsg)
	if message.err != nil || message.project == nil {
		t.Fatalf("registration = %#v", message)
	}
}

func TestDirtyRepositoryRequiresExplicitIntakeChoice(t *testing.T) {
	var resolved ipc.Request
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status", "sessions":
			return ipc.Response{OK: true}, nil
		case "projects":
			return ipc.Response{OK: true}, nil
		case "project-add":
			return ipc.Response{OK: true, Intake: &ipc.ProjectIntake{RootPath: request.Path, ExpectedState: "fingerprint", Changes: []ipc.WorkingTreeChange{{Path: "changed.go", WorkStatus: "M"}}}}, nil
		case "project-resolve-intake":
			resolved = request
			return ipc.Response{OK: true, Project: &ipc.Project{Name: "repo", RootPath: request.Path}}, nil
		}
		return ipc.Response{}, errors.New("unexpected")
	}}
	m := loadedModel(t, client)
	m.screen, m.input = screenRegister, "."
	updated, command := m.Update(key(tea.KeyEnter, ""))
	m = updated.(model)
	updated, _ = m.Update(command().(registeredMsg))
	m = updated.(model)
	if m.screen != screenIntake || !strings.Contains(m.View().Content, "changed.go") {
		t.Fatalf("intake not shown:\n%s", m.View().Content)
	}
	updated, command = m.Update(key('c', "c"))
	m = updated.(model)
	_ = command().(registeredMsg)
	if resolved.Action != "commit" || resolved.ExpectedState != "fingerprint" {
		t.Fatalf("resolve request = %#v", resolved)
	}
}

func TestQuitFrontendDoesNotSendDaemonStop(t *testing.T) {
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status", "projects", "sessions":
			return ipc.Response{OK: true}, nil
		}
		return ipc.Response{}, errors.New("unexpected")
	}}
	m := loadedModel(t, client)
	_, command := m.Update(key('q', "q"))
	if command == nil {
		t.Fatal("quit did not return a command")
	}
	_ = command()
	for _, request := range client.requests {
		if request.Command == "stop" || request.Command == "shutdown" {
			t.Fatalf("frontend quit sent %#v", request)
		}
	}
}
