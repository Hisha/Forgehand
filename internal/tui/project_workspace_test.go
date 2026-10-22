package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Hisha/Forgehand/internal/ipc"
)

// conversationFixture returns a project workspace that carries a realistic
// conversation: a completed investigation with a request, a model response,
// evidence, and recorded activity.
func conversationFixture(width, height int) model {
	projectID := int64(42)
	project := ipc.Project{ID: 42, Name: "mod-native-hunts", RootPath: "/src/hunts"}
	sessions := []ipc.Session{
		{ID: 3, Title: "Zone validation", State: "FAILED", Kind: "investigation", ProjectID: &projectID},
		{ID: 2, Title: "Huntmaster eligibility", State: "COMPLETED", Kind: "investigation", ProjectID: &projectID,
			Task: "How does mod-native-hunts determine eligible zones for Huntmaster assignments? Identify the implementation and the tests that cover it."},
		{ID: 1, Title: "Investigate eligibility", State: "RUNNING", Kind: "work-plan", ProjectID: &projectID,
			Task: "Which zones are eligible for Huntmaster?"},
	}
	m := model{
		screen:          screenProject,
		width:           width,
		height:          height,
		selectedProject: &project,
		sessions:        sessions,
		entries: []projectEntry{{
			Project: project,
			Group:   groupWorking,
			Active:  []ipc.Session{sessions[2]},
			Recent:  []ipc.Session{sessions[1], sessions[0]},
			Health: &ipc.ProjectHealth{
				ProjectID:       42,
				Status:          "current",
				Head:            "abcdef1234567890",
				InventoryCommit: "abcdef1234567890",
			},
		}},
		workspace: workspaceSession{
			Session: &sessions[1],
			Execution: &ipc.Execution{
				ID: 9, SessionID: 2, Status: "COMPLETED", CurrentStep: 1, TotalSteps: 1,
			},
			Result: &ipc.ModelResult{
				Provider: "openai-compatible",
				Model:    "worker",
				Content: "Eligible zones require an active hunt and a configured provider.\n" +
					"The zone is derived from the character's current specialization.",
			},
			Evidence: &ipc.InvestigationEvidence{
				Commit:      "abcdef1234567890",
				UsedChars:   1204,
				BudgetChars: 8000,
				Files: []ipc.InvestigationEvidenceFile{{
					Path: "huntmaster/zones.go",
					Locations: []ipc.InvestigationEvidenceSourceLocation{
						{Line: 27, Declaration: "func Eligible"},
					},
				}, {
					Path: "huntmaster/config.go",
					Locations: []ipc.InvestigationEvidenceSourceLocation{
						{Line: 11, Declaration: "func Load"},
					},
				}},
			},
			Events: []ipc.SessionEvent{
				{ID: 1, Type: "session.created", Timestamp: "2026-10-20T15:46:02.990773745Z"},
				{ID: 2, Type: "execution.queued", Timestamp: "2026-10-20T15:46:11.032221234Z"},
				{ID: 3, Type: "execution.dispatched", Timestamp: "2026-10-20T15:46:11.105549754Z"},
				{ID: 4, Type: "investigation.evidence.selected", Timestamp: "2026-10-20T15:46:11.706205070Z"},
				{ID: 5, Type: "model.response.completed", Timestamp: "2026-10-20T15:46:50.685767788Z"},
			},
			Loaded: true,
		},
	}
	m.projectSessionCursor = 2
	return m
}

// emptyConversationFixture returns a project workspace whose project has no
// sessions and no recorded results.
func emptyConversationFixture(width, height int) model {
	project := ipc.Project{ID: 7, Name: "fresh-repository", RootPath: "/src/fresh"}
	return model{
		screen:          screenProject,
		width:           width,
		height:          height,
		selectedProject: &project,
		entries:         []projectEntry{{Project: project, Group: groupIdle}},
	}
}

func TestConversationWorkspaceLayoutAtRepresentativeSizes(t *testing.T) {
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 35}, {160, 45}} {
		m := conversationFixture(size.w, size.h)
		view := m.View().Content
		lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
		if len(lines) != size.h {
			t.Fatalf("%dx%d: rendered %d lines, want %d", size.w, size.h, len(lines), size.h)
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got > size.w {
				t.Fatalf("%dx%d: line %d width %d > %d: %q", size.w, size.h, i+1, got, size.w, ansi.Strip(line))
			}
		}

		// The header is the first row and names the product.
		if !strings.Contains(ansi.Strip(lines[0]), "Forgehand") {
			t.Fatalf("%dx%d: first row is not the application header: %q", size.w, size.h, ansi.Strip(lines[0]))
		}

		// The session selector is directly under the header.
		selectorIdx := -1
		for i, line := range lines {
			if strings.Contains(ansi.Strip(line), "SESSIONS") {
				selectorIdx = i
				break
			}
		}
		if selectorIdx != 1 {
			t.Fatalf("%dx%d: session selector at line %d, want 2", size.w, size.h, selectorIdx+1)
		}
		if !strings.Contains(ansi.Strip(lines[selectorIdx]), "Huntmast") {
			t.Fatalf("%dx%d: selector does not list the session: %q", size.w, size.h, ansi.Strip(lines[selectorIdx]))
		}

		// The conversation follows the selector and starts at the top of the
		// scrolling region.
		requestIdx := -1
		for i, line := range lines {
			if strings.Contains(ansi.Strip(line), "You") {
				requestIdx = i
				break
			}
		}
		if requestIdx < 0 {
			t.Fatalf("%dx%d: the user request is not rendered:\n%s", size.w, size.h, view)
		}
		if requestIdx <= selectorIdx {
			t.Fatalf("%dx%d: the request (%d) is not below the selector (%d)", size.w, size.h, requestIdx, selectorIdx)
		}

		// The composer stays at the bottom of the window.
		composerIdx := -1
		for i, line := range lines {
			if strings.Contains(ansi.Strip(line), "Composer") {
				composerIdx = i
				break
			}
		}
		if composerIdx < size.h-5 {
			t.Fatalf("%dx%d: composer at line %d, expected near the bottom", size.w, size.h, composerIdx+1)
		}
		if composerIdx <= requestIdx {
			t.Fatalf("%dx%d: composer (%d) is not below the conversation (%d)", size.w, size.h, composerIdx, requestIdx)
		}
	}
}

func TestConversationWorkspaceShowsRequestAndForgehandResponses(t *testing.T) {
	m := conversationFixture(120, 35)
	view := m.View().Content
	for _, want := range []string{
		"You",
		"How does mod-native-hunts determine eligible zones",
		"Forgehand",
		"Eligible zones require an active hunt",
		"huntmaster/zones.go",
		"func Eligible",
	} {
		if !strings.Contains(ansi.Strip(view), want) {
			t.Fatalf("conversation missing %q:\n%s", want, ansi.Strip(view))
		}
	}
	// The request is attributed to the human and the answer to Forgehand.
	plain := ansi.Strip(m.projectBody())
	userAt := strings.Index(plain, "You")
	forgehandAt := strings.Index(plain, "Forgehand")
	if userAt < 0 || forgehandAt < 0 || userAt > forgehandAt {
		t.Fatalf("the request must come before the answer:\n%s", plain)
	}
}

func TestConversationWorkspaceRendersWorkerStatusAndActivity(t *testing.T) {
	m := conversationFixture(120, 35)
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"Worker", "COMPLETED", "Activity", "model response completed", "evidence selected"} {
		if !strings.Contains(view, want) {
			t.Fatalf("conversation missing %q:\n%s", want, view)
		}
	}
}

func TestConversationWorkspaceDoesNotInventMissingResults(t *testing.T) {
	m := conversationFixture(120, 35)
	m.workspace.Result = nil
	m.workspace.Evidence = nil
	m.workspace.Events = nil
	view := ansi.Strip(m.projectBody())
	for _, unwanted := range []string{"findings", "evidence", "Activity", "model response completed"} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("conversation invented %q:\n%s", unwanted, view)
		}
	}
	// The request and the worker state are still shown.
	if !strings.Contains(view, "You") || !strings.Contains(view, "Worker") {
		t.Fatalf("conversation lost the recorded turns:\n%s", view)
	}
}

func TestEmptyProjectWorkspaceExplainsItself(t *testing.T) {
	m := emptyConversationFixture(120, 35)
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "No conversation yet") {
		t.Fatalf("empty workspace did not explain itself:\n%s", view)
	}
	if !strings.Contains(view, "no sessions yet") {
		t.Fatalf("empty workspace did not report the empty selector:\n%s", view)
	}
}

func TestConversationWorkspaceSessionSelectorSwitchesSessions(t *testing.T) {
	m := conversationFixture(120, 35)
	selector := ansi.Strip(m.sessionSelectorLine())
	for _, want := range []string{"Investig", "Zone validation", "Huntmast"} {
		if !strings.Contains(selector, want) {
			t.Fatalf("selector does not list every session:\n%s", selector)
		}
	}
	// The selected session is highlighted, the others are not.
	if !strings.Contains(m.sessionSelectorLine(), "\x1b[7m") {
		t.Fatalf("the selected session is not highlighted:\n%q", m.sessionSelectorLine())
	}
	// The selector never grows past the width of the workspace.
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 35}, {160, 45}} {
		m := conversationFixture(size.w, size.h)
		if got := ansi.StringWidth(m.sessionSelectorLine()); got > m.mainWidth() {
			t.Fatalf("%dx%d: selector width %d > %d", size.w, size.h, got, m.mainWidth())
		}
	}
}

func TestConversationWorkspaceLoadsTheSelectedSession(t *testing.T) {
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "session-status":
			return ipc.Response{
				OK:        true,
				Session:   &ipc.Session{ID: request.SessionID, Title: "Zone validation", State: "FAILED", Kind: "investigation", Task: "validate the zone rules"},
				Execution: &ipc.Execution{ID: 4, SessionID: request.SessionID, Status: "FAILED", FailureKind: "repository_context", FailureReason: "no readable committed source evidence"},
			}, nil
		case "session-events":
			return ipc.Response{OK: true, Events: []ipc.SessionEvent{{ID: 1, Type: "session.created", Timestamp: "2026-10-20T15:46:02Z"}}}, nil
		}
		return ipc.Response{}, nil
	}}
	m := conversationFixture(120, 35)
	m.client = client
	m.projectSessionCursor = 0

	command := m.loadProjectSession()
	if command == nil {
		t.Fatal("selecting a session did not load it")
	}
	message := command().(workspaceSessionMsg)
	if message.id != 1 {
		t.Fatalf("loaded session %d, want 1", message.id)
	}
	updated, _ := m.Update(message)
	m = updated.(model)
	view := collapseSpaces(ansi.Strip(m.projectBody()))
	if !strings.Contains(view, "validate the zone rules") {
		t.Fatalf("the switched conversation does not show the new request:\n%s", view)
	}
	if !strings.Contains(view, "no readable committed source evidence") {
		t.Fatalf("the switched conversation does not show the new state:\n%s", view)
	}
	if strings.Contains(view, "Eligible zones require an active hunt") {
		t.Fatalf("the switched conversation still shows the previous answer:\n%s", view)
	}
}

// collapseSpaces joins the wrapped lines of a rendered view so that a phrase
// that spans two lines can still be found.
func collapseSpaces(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func TestConversationWorkspaceSessionSwitchRequestsPersistedDetails(t *testing.T) {
	var requested []ipc.Request
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		requested = append(requested, request)
		return ipc.Response{OK: true}, nil
	}}
	m := conversationFixture(120, 35)
	m.client = client
	m.projectSessionCursor = 0
	command := m.moveProjectSession(1)
	if command == nil {
		t.Fatal("moving the session selection did not load a session")
	}
	_ = command()
	if len(requested) == 0 || requested[0].Command != "session-status" {
		t.Fatalf("requests = %#v", requested)
	}
	if requested[0].SessionID != 3 {
		t.Fatalf("loaded session %d, want 3", requested[0].SessionID)
	}
}

func TestConversationWorkspaceComposerKeepsFocusAndMultilineInput(t *testing.T) {
	m := conversationFixture(120, 35)
	updated, command := m.Update(key('h', "h"))
	m = updated.(model)
	if command != nil {
		t.Fatal("ordinary typing returned a command")
	}
	for _, r := range "ello" {
		updated, _ = m.Update(key(r, string(r)))
		m = updated.(model)
	}
	if m.input != "hello" {
		t.Fatalf("input = %q", m.input)
	}
	// A newline key inserts a line instead of submitting.
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}))
	m = updated.(model)
	if command != nil {
		t.Fatal("the composer submitted instead of inserting a newline")
	}
	if m.input != "hello\n" {
		t.Fatalf("input = %q", m.input)
	}
	view := m.View().Content
	if !strings.Contains(view, "hello") {
		t.Fatalf("composer lost its content:\n%s", view)
	}
	if !strings.Contains(ansi.Strip(view), "Composer") {
		t.Fatalf("composer is not labelled:\n%s", ansi.Strip(view))
	}
}

func TestConversationWorkspaceComposerShowsFocusIndication(t *testing.T) {
	m := conversationFixture(120, 35)
	focused := m.inputAreaLines(m.mainWidth())
	m.projectNavigationFocused = true
	navigating := m.inputAreaLines(m.mainWidth())
	if strings.Join(focused, "\n") == strings.Join(navigating, "\n") {
		t.Fatal("the composer does not distinguish focused from navigated")
	}
	if !strings.Contains(ansi.Strip(strings.Join(navigating, "\n")), "session selector focused") {
		t.Fatalf("navigation focus is not explained:\n%s", ansi.Strip(strings.Join(navigating, "\n")))
	}
}

func TestConversationWorkspaceScrollsIndependentlyOfChrome(t *testing.T) {
	m := conversationFixture(120, 35)
	m.workspace.Result = &ipc.ModelResult{
		Content: strings.Repeat("Findings line about the zone rules. ", 40),
	}
	limit := m.maxProjectScroll()
	if limit <= 0 {
		t.Fatalf("a long conversation does not scroll: %d", limit)
	}
	m.scroll = limit
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "SESSIONS") {
		t.Fatalf("the session selector scrolled away:\n%s", view)
	}
	if !strings.Contains(view, "Composer") {
		t.Fatalf("the composer scrolled away:\n%s", view)
	}
}

func TestConversationWorkspaceSidebarStructure(t *testing.T) {
	m := conversationFixture(160, 45)
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"PROJECT", "Name: mod-native-hunts", "Activity:", "Health:", "SESSION", "State:", "Execution:"} {
		if !strings.Contains(view, want) {
			t.Fatalf("sidebar missing %q:\n%s", want, view)
		}
	}
}

func TestConversationWorkspaceSmallTerminalCollapsesSidebar(t *testing.T) {
	m := conversationFixture(80, 24)
	if m.sidebarVisible() {
		t.Fatal("sidebar should collapse at 80x24")
	}
	view := m.View().Content
	if strings.Contains(ansi.Strip(view), "Name: mod-native-hunts") {
		t.Fatalf("the sidebar is still rendered at 80x24:\n%s", ansi.Strip(view))
	}
}

func TestDumpConversationWorkspaceSnapshots(t *testing.T) {
	if os.Getenv("FORGEHAND_DUMP_SNAPSHOTS") != "1" {
		t.Skip("set FORGEHAND_DUMP_SNAPSHOTS=1 to write snapshots")
	}
	dir := "/tmp/opencode/conversation-workspace-snapshots"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 35}, {160, 45}} {
		m := conversationFixture(size.w, size.h)
		path := filepath.Join(dir, "conversation-"+strconv.Itoa(size.w)+"x"+strconv.Itoa(size.h)+".ansi")
		if err := os.WriteFile(path, []byte(m.View().Content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDumpDashboardSnapshots writes the startup screen for visual review.
func TestDumpDashboardSnapshots(t *testing.T) {
	if os.Getenv("FORGEHAND_DUMP_SNAPSHOTS") != "1" {
		t.Skip("set FORGEHAND_DUMP_SNAPSHOTS=1 to write snapshots")
	}
	dir := "/tmp/opencode/conversation-workspace-snapshots"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	projectID := int64(42)
	m := model{
		screen: screenDashboard,
		width:  120,
		height: 35,
		daemonStatus: ipc.Response{
			OK: true, PID: 1234, ActiveWorkers: 2, QueuedExecutions: 1,
		},
		entries: []projectEntry{
			{Project: ipc.Project{ID: 1, Name: "mod-native-hunts", RootPath: "/src/hunts"}, Group: groupNeedsAttention,
				Waiting: []ipc.Session{{ID: 3, Title: "Zone validation", State: "WAITING_FOR_USER"}},
				Pending: []pendingInteraction{{Session: ipc.Session{ID: 3}, Interaction: ipc.Interaction{ID: 1, Question: "Which zone should the sample run against?"}}}},
			{Project: ipc.Project{ID: 2, Name: "Forgehand", RootPath: "/src/forgehand"}, Group: groupWorking,
				Active: []ipc.Session{{ID: 1, Title: "Investigate daemon shutdown", State: "RUNNING"}}},
			{Project: ipc.Project{ID: 3, Name: "docs-site", RootPath: "/src/docs"}, Group: groupIdle},
		},
		sessions: []ipc.Session{
			{ID: 3, Title: "Zone validation", State: "WAITING_FOR_USER", ProjectID: &projectID},
		},
	}
	path := filepath.Join(dir, "dashboard-120x35.ansi")
	if err := os.WriteFile(path, []byte(m.View().Content), 0o644); err != nil {
		t.Fatal(err)
	}
}
