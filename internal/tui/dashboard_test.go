package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/Hisha/Forgehand/internal/ipc"
)

// projectID returns a pointer to a project identifier.
func projectID(id int64) *int64 {
	return &id
}

// dashboardRowFor returns the rendered dashboard row of a project.
func dashboardRowFor(t *testing.T, view string, name string) string {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		// The application shell places the contextual sidebar beside the
		// main workspace, so strip the sidebar portion before inspecting a
		// dashboard row.
		main := line
		if idx := strings.Index(main, "│"); idx >= 0 {
			main = main[:idx]
		}
		main = ansi.Strip(main)
		if strings.Contains(main, name) {
			return strings.TrimRight(main, " ")
		}
	}
	t.Fatalf("dashboard has no row for %q:\n%s", name, view)
	return ""
}

// groupOf returns the group a project was placed in.
func groupOf(entries []projectEntry, name string) projectGroup {
	for _, entry := range entries {
		if entry.Project.Name == name {
			return entry.Group
		}
	}
	return groupIdle
}

func TestGroupProjectsSortsByActivity(t *testing.T) {
	projects := []ipc.Project{
		{ID: 1, Name: "idle-repo"},
		{ID: 2, Name: "working-repo"},
		{ID: 3, Name: "waiting-repo"},
	}
	sessions := []ipc.Session{
		{ID: 10, ProjectID: projectID(1), State: "COMPLETED", Kind: "investigation"},
		{ID: 11, ProjectID: projectID(2), State: "RUNNING", Kind: "work-plan"},
		{ID: 12, ProjectID: projectID(3), State: "WAITING_FOR_USER", Kind: "task"},
	}

	entries := groupProjects(projects, sessions, nil, nil)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[0].Project.Name != "waiting-repo" || entries[0].Group != groupNeedsAttention {
		t.Fatalf("first entry = %#v", entries[0])
	}
	if entries[1].Project.Name != "working-repo" || entries[1].Group != groupWorking {
		t.Fatalf("second entry = %#v", entries[1])
	}
	if entries[2].Project.Name != "idle-repo" || entries[2].Group != groupIdle {
		t.Fatalf("third entry = %#v", entries[2])
	}
}

func TestGroupProjectsRendersEveryGroupHeading(t *testing.T) {
	projects := []ipc.Project{
		{ID: 1, Name: "idle-repo"},
		{ID: 2, Name: "working-repo"},
		{ID: 3, Name: "waiting-repo"},
	}
	sessions := []ipc.Session{
		{ID: 11, ProjectID: projectID(2), State: "RUNNING", Kind: "work-plan"},
		{ID: 12, ProjectID: projectID(3), State: "WAITING_FOR_USER", Kind: "task"},
	}

	m := model{width: 80, entries: groupProjects(projects, sessions, nil, nil)}
	view := m.dashboardView()
	for _, want := range []string{"NEEDS ATTENTION", "WORKING", "IDLE"} {
		if !strings.Contains(view, want) {
			t.Fatalf("dashboard missing group heading %q:\n%s", want, view)
		}
	}
	if strings.Index(view, "NEEDS ATTENTION") > strings.Index(view, "WORKING") {
		t.Fatalf("group headings are out of order:\n%s", view)
	}
	if strings.Index(view, "WORKING") > strings.Index(view, "IDLE") {
		t.Fatalf("group headings are out of order:\n%s", view)
	}
}

func TestPendingInteractionTakesPrecedenceOverRunningWork(t *testing.T) {
	projects := []ipc.Project{{ID: 1, Name: "busy-repo"}}
	sessions := []ipc.Session{
		{ID: 11, ProjectID: projectID(1), State: "RUNNING", Kind: "work-plan"},
		{ID: 12, ProjectID: projectID(1), State: "WAITING_FOR_USER", Kind: "task"},
	}

	entries := groupProjects(projects, sessions, nil, nil)
	if entries[0].Group != groupNeedsAttention {
		t.Fatalf("group = %v, want needs attention", entries[0].Group)
	}
	if len(entries[0].Waiting) != 1 || len(entries[0].Active) != 1 {
		t.Fatalf("entry = %#v", entries[0])
	}
}

func TestReadySessionCountsAsActiveWork(t *testing.T) {
	projects := []ipc.Project{{ID: 1, Name: "queued-repo"}}
	sessions := []ipc.Session{
		{ID: 11, ProjectID: projectID(1), State: "READY", Kind: "work-plan"},
	}

	entries := groupProjects(projects, sessions, nil, nil)
	if entries[0].Group != groupWorking {
		t.Fatalf("group = %v, want working", entries[0].Group)
	}
	if len(entries[0].Active) != 1 {
		t.Fatalf("entry = %#v", entries[0])
	}
}

func TestHealthDoesNotChangeTheActivityGroup(t *testing.T) {
	projects := []ipc.Project{{ID: 1, Name: "stale-repo"}}
	health := []ipc.ProjectHealth{{
		ProjectID:       1,
		Status:          "stale",
		Head:            "1111111111111111111111111111111111111111",
		InventoryCommit: "2222222222222222222222222222222222222222",
	}}

	entries := groupProjects(projects, nil, nil, health)
	if entries[0].Group != groupIdle {
		t.Fatalf("group = %v, want idle", entries[0].Group)
	}
	if !entries[0].healthWarning() {
		t.Fatal("stale repository did not produce a warning")
	}
	if entries[0].Health == nil || entries[0].Health.Status != "stale" {
		t.Fatalf("health = %#v", entries[0].Health)
	}
}

func TestUnknownHealthIsNotReportedAsAWarning(t *testing.T) {
	projects := []ipc.Project{{ID: 1, Name: "unobserved-repo"}}
	health := []ipc.ProjectHealth{{ProjectID: 1, Status: "unobserved"}}

	entries := groupProjects(projects, nil, nil, health)
	if !entries[0].healthWarning() {
		t.Fatal("unobserved repository did not produce a warning")
	}
	if entries[0].Health != nil && entries[0].Health.Status == "current" {
		t.Fatalf("health = %#v", entries[0].Health)
	}

	// A project whose health the daemon never reported must not be shown as
	// unhealthy either.
	unreported := groupProjects(projects, nil, nil, nil)
	if unreported[0].healthWarning() {
		t.Fatal("a project without a health report was marked unhealthy")
	}
}

func TestHealthIndicatorIsRenderedSeparatelyFromActivity(t *testing.T) {
	projects := []ipc.Project{
		{ID: 1, Name: "stale-repo"},
		{ID: 2, Name: "healthy-repo"},
	}
	sessions := []ipc.Session{
		{ID: 21, ProjectID: projectID(2), State: "RUNNING", Kind: "work-plan"},
	}
	health := []ipc.ProjectHealth{
		{ProjectID: 1, Status: "stale", Head: "aaaaaaaaaaaa", InventoryCommit: "bbbbbbbbbbbb"},
		{ProjectID: 2, Status: "current", Head: "cccccccccccc"},
	}

	m := model{width: 80, entries: groupProjects(projects, sessions, nil, health)}
	view := m.dashboardView()

	staleRow := dashboardRowFor(t, view, "stale-repo")
	healthyRow := dashboardRowFor(t, view, "healthy-repo")
	if !strings.HasSuffix(staleRow, "!") {
		t.Fatalf("stale row has no maintenance marker: %q", staleRow)
	}
	if strings.HasSuffix(healthyRow, "!") {
		t.Fatalf("healthy row has a maintenance marker: %q", healthyRow)
	}
	if !strings.Contains(healthyRow, "Planning") {
		t.Fatalf("healthy row lost its activity label: %q", healthyRow)
	}
}

func TestHealthSummaryDescribesTheStoredObservation(t *testing.T) {
	entries := groupProjects(
		[]ipc.Project{{ID: 1, Name: "stale-repo"}},
		nil,
		nil,
		[]ipc.ProjectHealth{{
			ProjectID:       1,
			Status:          "stale",
			Head:            "1111111111111111",
			InventoryCommit: "2222222222222222",
		}},
	)
	summary := entries[0].healthSummary()
	for _, want := range []string{"stale", "222222222222", "111111111111"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q missing %q", summary, want)
		}
	}
}

func TestDashboardWithNoProjectsExplainsHowToAddOne(t *testing.T) {
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status", "projects", "sessions":
			return ipc.Response{OK: true}, nil
		}
		return ipc.Response{}, errors.New("unexpected request")
	}}

	m := loadedModel(t, client)
	view := m.View().Content
	if !strings.Contains(view, "No projects registered yet") {
		t.Fatalf("dashboard did not explain the empty state:\n%s", view)
	}
	if len(m.entries) != 0 {
		t.Fatalf("entries = %#v", m.entries)
	}
}

func TestDashboardCountsEverySessionOfAProject(t *testing.T) {
	projects := []ipc.Project{{ID: 1, Name: "busy-repo"}}
	sessions := []ipc.Session{
		{ID: 11, ProjectID: projectID(1), State: "RUNNING", Kind: "work-plan"},
		{ID: 12, ProjectID: projectID(1), State: "QUEUED", Kind: "work-plan"},
		{ID: 13, ProjectID: projectID(1), State: "WAITING_FOR_USER", Kind: "task"},
		{ID: 14, ProjectID: projectID(1), State: "WAITING_FOR_USER", Kind: "task"},
	}

	entries := groupProjects(projects, sessions, nil, nil)
	if entries[0].Group != groupNeedsAttention {
		t.Fatalf("group = %v, want needs attention", entries[0].Group)
	}
	if len(entries[0].Waiting) != 2 || len(entries[0].Active) != 2 {
		t.Fatalf("entry = %#v", entries[0])
	}
	if label := entries[0].activityLabel(); !strings.Contains(label, "(2)") {
		t.Fatalf("label = %q, want the session count", label)
	}

	m := model{width: 80, entries: entries}
	view := m.dashboardView()
	row := dashboardRowFor(t, view, "busy-repo")
	if !strings.Contains(row, "Waiting for input (2)") {
		t.Fatalf("waiting sessions were not counted: %q", row)
	}
	if !strings.Contains(view, "WORKING") {
		t.Fatalf("dashboard lost the working group:\n%s", view)
	}
}

func TestDashboardShowsPendingApprovalDistinctlyFromAnAnswer(t *testing.T) {
	projects := []ipc.Project{{ID: 1, Name: "approval-repo"}}
	sessions := []ipc.Session{{ID: 11, ProjectID: projectID(1), State: "WAITING_FOR_USER", Kind: "task"}}
	pending := map[int64][]ipc.Interaction{
		11: {{ID: 1, SessionID: 11, Question: "Apply the patch?", Context: map[string]any{"proposal_hash": "abc"}}},
	}

	entries := groupProjects(projects, sessions, pending, nil)
	if entries[0].Group != groupNeedsAttention {
		t.Fatalf("group = %v, want needs attention", entries[0].Group)
	}
	if !isApprovalInteraction(entries[0].Pending[0].Interaction) {
		t.Fatal("proposal interaction was not recognised as an approval")
	}
	if label := entries[0].activityLabel(); !strings.Contains(label, "approval") {
		t.Fatalf("label = %q", label)
	}

	answer := groupProjects(projects, sessions, map[int64][]ipc.Interaction{
		11: {{ID: 2, SessionID: 11, Question: "Which zone?", Context: map[string]any{"step": "1", "total_steps": "2", "source": "worker"}}},
	}, nil)
	if label := answer[0].activityLabel(); !strings.Contains(label, "answer") {
		t.Fatalf("label = %q", label)
	}
}

func TestDashboardNavigationMovesTheSelectionAndOpensAProject(t *testing.T) {
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status":
			return ipc.Response{OK: true}, nil
		case "projects":
			return ipc.Response{OK: true, Projects: []ipc.Project{
				{ID: 1, Name: "idle-repo"},
				{ID: 2, Name: "working-repo"},
			}}, nil
		case "sessions":
			return ipc.Response{OK: true, Sessions: []ipc.Session{
				{ID: 11, ProjectID: projectID(2), State: "RUNNING", Kind: "work-plan"},
			}}, nil
		}
		return ipc.Response{}, errors.New("unexpected request")
	}}

	m := loadedModel(t, client)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want the dashboard", m.screen)
	}
	if m.projectCursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.projectCursor)
	}
	view := m.View().Content
	if !strings.Contains(dashboardRowFor(t, view, "working-repo"), ">") {
		t.Fatalf("the selected row is not marked:\n%s", view)
	}

	updated, _ := m.Update(key('j', "j"))
	m = updated.(model)
	if m.projectCursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.projectCursor)
	}
	updated, _ = m.Update(key('k', "k"))
	m = updated.(model)
	if m.projectCursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.projectCursor)
	}

	// The selection must not run past the end of the list.
	updated, _ = m.Update(key('j', "j"))
	m = updated.(model)
	updated, _ = m.Update(key('j', "j"))
	m = updated.(model)
	if m.projectCursor != 1 {
		t.Fatalf("cursor = %d, want the last entry", m.projectCursor)
	}

	updated, _ = m.Update(key(tea.KeyEnter, ""))
	m = updated.(model)
	if m.screen != screenProject {
		t.Fatalf("screen = %v, want the project screen", m.screen)
	}
	if m.selectedProject == nil || m.selectedProject.Name != "idle-repo" {
		t.Fatalf("selected project = %#v", m.selectedProject)
	}

	// Opening the first entry selects the project that needs the user.
	updated, _ = m.Update(key(tea.KeyEscape, ""))
	m = updated.(model)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want the dashboard", m.screen)
	}
	updated, _ = m.Update(key('k', "k"))
	m = updated.(model)
	updated, _ = m.Update(key(tea.KeyEnter, ""))
	m = updated.(model)
	if m.selectedProject == nil || m.selectedProject.Name != "working-repo" {
		t.Fatalf("selected project = %#v", m.selectedProject)
	}
}

func TestDashboardSelectionSurvivesAProjectListShrink(t *testing.T) {
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status":
			return ipc.Response{OK: true}, nil
		case "projects":
			return ipc.Response{OK: true, Projects: []ipc.Project{
				{ID: 1, Name: "one"},
				{ID: 2, Name: "two"},
			}}, nil
		case "sessions":
			return ipc.Response{OK: true}, nil
		}
		return ipc.Response{}, errors.New("unexpected request")
	}}

	m := loadedModel(t, client)
	updated, _ := m.Update(key('j', "j"))
	m = updated.(model)
	if m.projectCursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.projectCursor)
	}

	m.projects = m.projects[:1]
	m.rebuildEntries()
	m.clampCursors()
	if m.projectCursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.projectCursor)
	}
}

func TestDashboardFollowsTerminalResizing(t *testing.T) {
	projects := []ipc.Project{
		{ID: 1, Name: "a-repository-with-a-long-name"},
		{ID: 2, Name: "another-repository-with-a-long-name"},
	}
	sessions := []ipc.Session{
		{ID: 21, ProjectID: projectID(2), State: "RUNNING", Kind: "work-plan"},
	}
	health := []ipc.ProjectHealth{{ProjectID: 1, Status: "stale", Head: "aaaaaaaaaaaa"}}

	m := model{width: 80, entries: groupProjects(projects, sessions, nil, health)}
	for _, width := range []int{120, 80, 40, 24, 20} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = updated.(model)
		if m.width != width {
			t.Fatalf("width = %d, want %d", m.width, width)
		}
		content := m.View().Content
		assertRenderedWidth(t, content, width)
	}
}

func TestDashboardResizeKeepsTheMaintenanceMarkerInsideTheTerminal(t *testing.T) {
	projects := []ipc.Project{{ID: 1, Name: "stale-repo"}}
	health := []ipc.ProjectHealth{{ProjectID: 1, Status: "stale", Head: "aaaaaaaaaaaa"}}

	m := model{width: 80, entries: groupProjects(projects, nil, nil, health)}
	for _, width := range []int{80, 40, 24} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = updated.(model)
		row := dashboardRowFor(t, m.View().Content, "stale-repo")
		if !strings.HasSuffix(strings.TrimRight(row, " "), "!") {
			t.Fatalf("width %d lost the maintenance marker: %q", width, row)
		}
	}
}

func TestProjectHealthFailureDoesNotHideTheDashboard(t *testing.T) {
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status":
			return ipc.Response{OK: true}, nil
		case "projects":
			return ipc.Response{OK: true, Projects: []ipc.Project{{ID: 1, Name: "repo"}}}, nil
		case "sessions":
			return ipc.Response{OK: true}, nil
		}
		return ipc.Response{}, errors.New("unexpected request")
	}}

	m := loadedModel(t, client)
	updated, _ := m.Update(healthMsg{err: errors.New("repository unreadable")})
	m = updated.(model)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want the dashboard", m.screen)
	}
	view := m.View().Content
	if !strings.Contains(view, "repo") {
		t.Fatalf("dashboard lost its projects:\n%s", view)
	}
	if !strings.Contains(view, "readiness unavailable") {
		t.Fatalf("dashboard did not report the readiness failure:\n%s", view)
	}
}

func TestPendingInteractionsAreOnlyRequestedForWaitingSessions(t *testing.T) {
	var asked []int64
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status":
			return ipc.Response{OK: true}, nil
		case "projects":
			return ipc.Response{OK: true}, nil
		case "sessions":
			return ipc.Response{OK: true, Sessions: []ipc.Session{
				{ID: 11, State: "WAITING_FOR_USER", Kind: "task"},
				{ID: 12, State: "RUNNING", Kind: "work-plan"},
				{ID: 13, State: "COMPLETED", Kind: "investigation"},
			}}, nil
		case "session-questions":
			asked = append(asked, request.SessionID)
			return ipc.Response{OK: true, Interactions: []ipc.Interaction{
				{ID: 1, SessionID: request.SessionID, Question: "Proceed?"},
			}}, nil
		}
		return ipc.Response{}, errors.New("unexpected request")
	}}

	m := loadedModel(t, client)
	if len(asked) != 1 || asked[0] != 11 {
		t.Fatalf("asked = %#v, want only the waiting session", asked)
	}
	if len(m.pending) != 1 {
		t.Fatalf("pending = %#v", m.pending)
	}
}

func TestSessionsScreenRemainsReachableFromTheDashboard(t *testing.T) {
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status":
			return ipc.Response{OK: true}, nil
		case "projects":
			return ipc.Response{OK: true, Projects: []ipc.Project{{ID: 1, Name: "repo"}}}, nil
		case "sessions":
			return ipc.Response{OK: true, Sessions: []ipc.Session{
				{ID: 11, ProjectID: projectID(1), Title: "Huntmaster", State: "COMPLETED", Kind: "investigation"},
			}}, nil
		}
		return ipc.Response{}, errors.New("unexpected request")
	}}

	m := loadedModel(t, client)
	updated, _ := m.Update(key('s', "s"))
	m = updated.(model)
	if m.screen != screenSessions {
		t.Fatalf("screen = %v, want the sessions screen", m.screen)
	}
	view := m.View().Content
	if !strings.Contains(view, "Huntmaster") {
		t.Fatalf("sessions screen lost the session:\n%s", view)
	}

	updated, _ = m.Update(key(tea.KeyEscape, ""))
	m = updated.(model)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want the dashboard", m.screen)
	}
}

func TestProjectViewShowsTheSessionsOfTheSelectedProject(t *testing.T) {
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status":
			return ipc.Response{OK: true}, nil
		case "projects":
			return ipc.Response{OK: true, Projects: []ipc.Project{{ID: 1, Name: "repo", RootPath: "/src/repo"}}}, nil
		case "sessions":
			return ipc.Response{OK: true, Sessions: []ipc.Session{
				{ID: 11, ProjectID: projectID(1), Title: "Running work", State: "RUNNING", Kind: "work-plan"},
				{ID: 12, ProjectID: projectID(1), Title: "Finished work", State: "COMPLETED", Kind: "investigation"},
			}}, nil
		case "session-status":
			return ipc.Response{OK: true, Session: &ipc.Session{ID: request.SessionID, Title: "Finished work", State: "COMPLETED", Kind: "investigation", Task: "which zones are eligible"}}, nil
		case "session-events":
			return ipc.Response{OK: true}, nil
		}
		return ipc.Response{}, errors.New("unexpected request")
	}}

	m := loadedModel(t, client)
	updated, command := m.Update(key(tea.KeyEnter, ""))
	m = updated.(model)
	if command != nil {
		updated, _ = m.Update(command().(workspaceSessionMsg))
		m = updated.(model)
	}
	view := m.View().Content
	// The project workspace is a conversation: the sessions of the project are
	// selectable and the selected session's persisted request is on screen.
	for _, want := range []string{"SESSIONS", "Running work", "Finished work", "which zones are eligible"} {
		if !strings.Contains(view, want) {
			t.Fatalf("project view missing %q:\n%s", want, view)
		}
	}
}

func TestDashboardKeepsItsActivityGroupings(t *testing.T) {
	projects := []ipc.Project{
		{ID: 1, Name: "needs-a-question", RootPath: "/src/one"},
		{ID: 2, Name: "is-working", RootPath: "/src/two"},
		{ID: 3, Name: "is-idle", RootPath: "/src/three"},
	}
	sessions := []ipc.Session{
		{ID: 11, ProjectID: projectID(1), Title: "Waiting", State: "WAITING_FOR_USER", Kind: "investigation"},
		{ID: 12, ProjectID: projectID(2), Title: "Running", State: "RUNNING", Kind: "work-plan"},
	}
	pending := map[int64][]ipc.Interaction{
		11: {{ID: 1, SessionID: 11, Question: "Which zone?"}},
	}

	m := model{
		screen:  screenDashboard,
		width:   120,
		height:  35,
		entries: groupProjects(projects, sessions, pending, nil),
	}
	view := m.View().Content
	for _, want := range []string{"NEEDS ATTENTION", "WORKING", "IDLE", "needs-a-question", "is-working", "is-idle"} {
		if !strings.Contains(view, want) {
			t.Fatalf("dashboard missing %q:\n%s", want, view)
		}
	}
	// The groups keep their order and their membership.
	needsAt := strings.Index(view, "NEEDS ATTENTION")
	workingAt := strings.Index(view, "WORKING")
	idleAt := strings.Index(view, "IDLE")
	if !(needsAt < workingAt && workingAt < idleAt) {
		t.Fatalf("the dashboard groups are out of order: %d %d %d", needsAt, workingAt, idleAt)
	}
	if groupOf(m.entries, "needs-a-question") != groupNeedsAttention {
		t.Fatalf("the waiting project moved out of NEEDS ATTENTION")
	}
	if groupOf(m.entries, "is-working") != groupWorking {
		t.Fatalf("the running project moved out of WORKING")
	}
	if groupOf(m.entries, "is-idle") != groupIdle {
		t.Fatalf("the idle project moved out of IDLE")
	}
	// The dashboard still opens the project workspace on enter.
	updated, _ := m.Update(key(tea.KeyEnter, ""))
	m = updated.(model)
	if m.screen != screenProject {
		t.Fatalf("screen = %v, want the project workspace", m.screen)
	}
}

func TestHelpScreenIsReachableAndReturnsToTheDashboard(t *testing.T) {
	client := &fakeTransport{handler: func(request ipc.Request) (ipc.Response, error) {
		switch request.Command {
		case "status", "projects", "sessions":
			return ipc.Response{OK: true}, nil
		}
		return ipc.Response{}, errors.New("unexpected request")
	}}

	m := loadedModel(t, client)
	updated, _ := m.Update(key('?', "?"))
	m = updated.(model)
	if m.screen != screenHelp {
		t.Fatalf("screen = %v, want the help screen", m.screen)
	}
	view := m.View().Content
	for _, want := range []string{"Dashboard", "enter", "open the selected project", "list all sessions"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help view missing %q:\n%s", want, view)
		}
	}

	updated, _ = m.Update(key(tea.KeyEscape, ""))
	m = updated.(model)
	if m.screen != screenDashboard {
		t.Fatalf("screen = %v, want the dashboard", m.screen)
	}
}

func TestSessionsOfOtherProjectsAreNotListedOnTheDashboard(t *testing.T) {
	projects := []ipc.Project{{ID: 1, Name: "repo"}}
	sessions := []ipc.Session{
		{ID: 11, State: "RUNNING", Kind: "work-plan"},
		{ID: 12, ProjectID: projectID(1), State: "RUNNING", Kind: "work-plan"},
	}

	entries := groupProjects(projects, sessions, nil, nil)
	if len(entries[0].Active) != 1 {
		t.Fatalf("a session without a project was attributed to a project: %#v", entries[0])
	}
}
