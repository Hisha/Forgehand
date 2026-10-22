package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/Hisha/Forgehand/internal/ipc"
)

func TestConversationEntriesPutTheRequestFirst(t *testing.T) {
	m := model{
		workspace: workspaceSession{
			Session:  &ipc.Session{ID: 4, Title: "Zones", State: "COMPLETED", Task: "which zones are eligible"},
			WorkPlan: &ipc.WorkPlan{Objective: "Map the zone rules", OriginalRequest: "which zones are eligible", Status: "BLOCKED", ReadOnly: true},
			Result:   &ipc.ModelResult{Content: "Eligible zones require an active hunt."},
		},
	}
	entries := m.conversationEntries()
	if len(entries) == 0 {
		t.Fatal("no conversation entries")
	}
	if entries[0].Kind != conversationUser {
		t.Fatalf("first entry = %#v", entries[0])
	}
	if entries[0].Body != "which zones are eligible" {
		t.Fatalf("request body = %q", entries[0].Body)
	}
	kinds := make([]conversationKind, 0, len(entries))
	for _, entry := range entries {
		kinds = append(kinds, entry.Kind)
	}
	if kinds[0] != conversationUser {
		t.Fatalf("kinds = %v", kinds)
	}
	seenForgehand := false
	for _, kind := range kinds[1:] {
		if kind == conversationForgehand {
			seenForgehand = true
		}
	}
	if !seenForgehand {
		t.Fatalf("no Forgehand response in %v", kinds)
	}
}

func TestConversationEntriesOmitWhatTheBackendDidNotRecord(t *testing.T) {
	m := model{
		workspace: workspaceSession{
			Session: &ipc.Session{ID: 4, Title: "Zones", State: "READY", Task: "which zones are eligible"},
		},
	}
	entries := m.conversationEntries()
	for _, entry := range entries {
		switch entry.Kind {
		case conversationForgehand:
			t.Fatalf("a Forgehand answer was invented: %#v", entry)
		}
	}
	// The worker entry reports that nothing has run yet.
	found := false
	for _, entry := range entries {
		if entry.Kind == conversationActivity && entry.Author == "Worker" {
			found = true
			if !strings.Contains(entry.Body, "No execution") {
				t.Fatalf("worker entry = %#v", entry)
			}
		}
	}
	if !found {
		t.Fatalf("no worker entry in %#v", entries)
	}
}

func TestConversationEntriesDescribeAWorkPlan(t *testing.T) {
	m := model{
		workspace: workspaceSession{
			Session: &ipc.Session{ID: 4, Title: "Plan", State: "RUNNING"},
			WorkPlan: &ipc.WorkPlan{
				Objective:           "Map the zone rules",
				OriginalRequest:     "map the zone rules",
				Status:              "BLOCKED",
				ReadOnly:            true,
				Clarification:       "Which zone set matters?",
				Constraints:         []string{"do not modify files"},
				UnresolvedQuestions: []string{"Which runtime owns the final value?"},
				Tasks: []ipc.WorkPlanTask{
					{Position: 1, Objective: "Gather evidence", Status: "COMPLETED", Result: "Found the loader"},
					{Position: 2, Objective: "Trace the rules", Status: "RUNNING"},
					{Position: 3, Objective: "Summarize", Status: "PENDING"},
					{Position: 4, Objective: "Blocked step", Status: "BLOCKED", BlockedReason: "prerequisite did not complete"},
				},
			},
		},
	}
	entries := m.conversationEntries()
	var plan *conversationEntry
	for index := range entries {
		if entries[index].Badge == "plan" {
			plan = &entries[index]
			break
		}
	}
	if plan == nil {
		t.Fatalf("no plan entry in %#v", entries)
	}
	if plan.Status != "BLOCKED" {
		t.Fatalf("plan status = %q", plan.Status)
	}
	joined := strings.Join(plan.Details, "\n")
	for _, want := range []string{
		"Clarification: Which zone set matters?",
		"Constraint: do not modify files",
		"Unresolved: Which runtime owns the final value?",
		"✓ 1. Gather evidence",
		"result: Found the loader",
		"→ 2. Trace the rules",
		"· 3. Summarize",
		"! 4. Blocked step",
		"blocked: prerequisite did not complete",
		"Read-only: true",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("plan details missing %q:\n%s", want, joined)
		}
	}
}

func TestConversationEntriesDescribeEvidence(t *testing.T) {
	m := model{
		workspace: workspaceSession{
			Session: &ipc.Session{ID: 4, Title: "Zones", State: "COMPLETED"},
			Evidence: &ipc.InvestigationEvidence{
				Commit:      "abcdef1234567890",
				UsedChars:   1204,
				BudgetChars: 8000,
				Files: []ipc.InvestigationEvidenceFile{{
					Path: "huntmaster/zones.go",
					Locations: []ipc.InvestigationEvidenceSourceLocation{
						{Line: 27, Declaration: "func Eligible"},
						{Line: 41, Declaration: "func Zones"},
					},
				}},
			},
		},
	}
	entries := m.conversationEntries()
	var evidence *conversationEntry
	for index := range entries {
		if entries[index].Badge == "evidence" {
			evidence = &entries[index]
			break
		}
	}
	if evidence == nil {
		t.Fatalf("no evidence entry in %#v", entries)
	}
	if !strings.Contains(evidence.Body, "1 file(s)") ||
		!strings.Contains(evidence.Body, "1204 of 8000") ||
		!strings.Contains(evidence.Body, "abcdef123456") {
		t.Fatalf("evidence summary = %q", evidence.Body)
	}
	joined := strings.Join(evidence.Details, "\n")
	for _, want := range []string{"huntmaster/zones.go:27  func Eligible", "huntmaster/zones.go:41  func Zones"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("evidence details missing %q:\n%s", want, joined)
		}
	}
}

func TestConversationEntriesDescribeRecordedActivity(t *testing.T) {
	m := model{
		workspace: workspaceSession{
			Session: &ipc.Session{ID: 4, Title: "Zones", State: "COMPLETED"},
			Events: []ipc.SessionEvent{
				{ID: 1, Type: "session.created", Timestamp: "2026-10-20T15:46:02.990773745Z"},
				{ID: 2, Type: "execution.dispatched", Timestamp: "2026-10-20T15:46:11.105549754Z"},
				{ID: 3, Type: "model.response.completed", Timestamp: "2026-10-20T15:46:50.685767788Z"},
			},
		},
	}
	entries := m.conversationEntries()
	var activity *conversationEntry
	for index := range entries {
		if entries[index].Author == "Activity" {
			activity = &entries[index]
			break
		}
	}
	if activity == nil {
		t.Fatalf("no activity entry in %#v", entries)
	}
	joined := strings.Join(activity.Details, "\n")
	for _, want := range []string{"session created", "dispatched to a worker", "model response completed"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("activity missing %q:\n%s", want, joined)
		}
	}
}

func TestRenderConversationDistinguishesTheVoices(t *testing.T) {
	entries := []conversationEntry{
		{Kind: conversationUser, Author: "You", Body: "which zones are eligible"},
		{Kind: conversationForgehand, Author: "Forgehand", Badge: "findings", Body: "Eligible zones require an active hunt."},
		{Kind: conversationActivity, Author: "Activity", Body: "session created"},
	}
	rendered := ansi.Strip(renderConversation(entries, 60))
	lines := strings.Split(rendered, "\n")
	if len(lines) < 7 {
		t.Fatalf("rendered %d lines:\n%s", len(lines), rendered)
	}
	if !strings.HasPrefix(lines[0], "● You") {
		t.Fatalf("user line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[3], "◆ Forgehand") {
		t.Fatalf("forgehand line = %q", lines[3])
	}
	if !strings.HasPrefix(lines[6], "• Activity") {
		t.Fatalf("activity line = %q", lines[6])
	}
	if !strings.Contains(rendered, "   which zones are eligible") {
		t.Fatalf("the request body is not indented:\n%s", rendered)
	}
}

func TestRenderConversationKeepsWrappedBodiesIndented(t *testing.T) {
	entries := []conversationEntry{
		{Kind: conversationUser, Author: "You", Body: strings.Repeat("long request ", 12)},
	}
	rendered := renderConversation(entries, 40)
	for _, line := range strings.Split(ansi.Strip(rendered), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "●") {
			continue
		}
		if !strings.HasPrefix(line, "   ") {
			t.Fatalf("a wrapped body line lost its indent: %q", line)
		}
	}
}

func TestConversationUsesTheAccentOnlyWhenTheTerminalHasColor(t *testing.T) {
	previous := terminalProfile
	defer func() { terminalProfile = previous }()

	setTerminalProfile(colorprofile.TrueColor)
	accent := styleAccent("Forgehand")
	if !strings.Contains(accent, "\x1b[38;2;") {
		t.Fatalf("truecolor accent = %q", accent)
	}

	setTerminalProfile(colorprofile.ANSI256)
	accent = styleAccent("Forgehand")
	if !strings.Contains(accent, "\x1b[38;5;") {
		t.Fatalf("ansi256 accent = %q", accent)
	}

	setTerminalProfile(colorprofile.ANSI)
	accent = styleAccent("Forgehand")
	if strings.Contains(accent, "38;5;") || strings.Contains(accent, "38;2;") {
		t.Fatalf("16-color accent = %q", accent)
	}
	if !strings.Contains(accent, "Forgehand") {
		t.Fatalf("16-color accent lost its text: %q", accent)
	}

	setTerminalProfile(colorprofile.ASCII)
	if styleAccent("Forgehand") != "Forgehand" {
		t.Fatalf("ascii accent = %q", styleAccent("Forgehand"))
	}
	if styleCompleted("COMPLETED") != "COMPLETED" {
		t.Fatalf("ascii status = %q", styleCompleted("COMPLETED"))
	}
	// Structure survives without color.
	m := conversationFixture(80, 24)
	rendered := ansi.Strip(m.View().Content)
	for _, want := range []string{"SESSIONS", "You", "Forgehand", "Composer"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the interface lost %q without color:\n%s", want, rendered)
		}
	}
}

func TestProjectSessionListOrdersByAttention(t *testing.T) {
	projectID := int64(42)
	other := int64(7)
	sessions := []ipc.Session{
		{ID: 1, State: "COMPLETED", ProjectID: &projectID},
		{ID: 2, State: "RUNNING", ProjectID: &projectID},
		{ID: 3, State: "WAITING_FOR_USER", ProjectID: &projectID},
		{ID: 4, State: "COMPLETED", ProjectID: &other},
		{ID: 5, State: "COMPLETED", ProjectID: &projectID},
	}
	list := projectSessionList(sessions, projectID)
	if len(list) != 4 {
		t.Fatalf("list = %#v", list)
	}
	want := []int64{3, 2, 5, 1}
	for index, id := range want {
		if list[index].ID != id {
			t.Fatalf("list[%d] = %d, want %d (%#v)", index, list[index].ID, id, list)
		}
	}
}

func TestSessionSelectorShowsTheStateOfEverySession(t *testing.T) {
	m := conversationFixture(120, 35)
	selector := m.sessionSelectorLine()
	for _, want := range []string{"✓", "!", "●"} {
		if !strings.Contains(selector, want) {
			t.Fatalf("selector missing the state marker %q:\n%q", want, selector)
		}
	}
	if !strings.Contains(selector, "\x1b[7m") {
		t.Fatalf("the selected session is not highlighted:\n%q", selector)
	}
}

func TestComposerCursorEditsTheText(t *testing.T) {
	text, cursor := insertText("hello", 5, " world")
	if text != "hello world" || cursor != 11 {
		t.Fatalf("text = %q cursor = %d", text, cursor)
	}
	text, cursor = deleteBackward(text, cursor)
	if text != "hello worl" || cursor != 10 {
		t.Fatalf("text = %q cursor = %d", text, cursor)
	}
	text, cursor = insertText(text, cursor, "\nsecond")
	if text != "hello worl\nsecond" {
		t.Fatalf("text = %q", text)
	}
	if moved, ok := moveCursorLine(text, cursor, -1); !ok || moved != 6 {
		t.Fatalf("moved = %d ok = %t", moved, ok)
	}
	if moved, ok := moveCursorLine(text, 0, -1); ok {
		t.Fatalf("the cursor moved above the first line: %d", moved)
	}
	if edge := moveCursorLineEdge(text, cursor, false); edge != 11 {
		t.Fatalf("line start = %d", edge)
	}
}

func TestComposerKeepsTypingOutOfTheScrollKeys(t *testing.T) {
	m := conversationFixture(120, 35)
	for _, r := range "jkq?b" {
		updated, command := m.Update(key(r, string(r)))
		m = updated.(model)
		if command != nil {
			t.Fatalf("typing %q returned a command", r)
		}
	}
	if m.input != "jkq?b" {
		t.Fatalf("input = %q", m.input)
	}
}
