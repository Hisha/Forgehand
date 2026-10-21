package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Hisha/Forgehand/internal/ipc"
)

func assertRenderedWidth(t *testing.T, rendered string, width int) {
	t.Helper()
	for number, line := range strings.Split(rendered, "\n") {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("line %d has visible width %d, want <= %d: %q", number+1, got, width, line)
		}
	}
}

func TestWrapContentPreservesAuthoredLinesAndIndentation(t *testing.T) {
	input := "Objective\n    preserve this deliberately indented explanation across several lines\n\n\ttab-indented detail\nNext"
	rendered := wrapContent(input, 20)

	assertRenderedWidth(t, rendered, 20)
	if !strings.Contains(rendered, "Objective\n    preserve") {
		t.Fatalf("initial indentation was not preserved:\n%s", rendered)
	}
	if !strings.Contains(rendered, "\n    deliberately") ||
		!strings.Contains(rendered, "\n\n    tab-indented") ||
		!strings.Contains(rendered, "\nNext") {
		t.Fatalf("continuation indentation or authored line structure was not preserved:\n%s", rendered)
	}
	for _, word := range strings.Fields(input) {
		if !strings.Contains(rendered, word) {
			t.Fatalf("wrapped output lost %q:\n%s", word, rendered)
		}
	}
}

func TestWrapContentMeasuresANSIAndUnicodeByTerminalCells(t *testing.T) {
	input := "\x1b[31mStyled Café 漢字 👩🏽‍💻 content remains visible\x1b[0m"
	rendered := wrapContent(input, 12)

	assertRenderedWidth(t, rendered, 12)
	if !strings.Contains(rendered, "\x1b[31m") || !strings.Contains(rendered, "\x1b[0m") {
		t.Fatalf("ANSI styling was lost: %q", rendered)
	}
	plain := strings.ReplaceAll(ansi.Strip(rendered), "\n", " ")
	for _, want := range []string{"Styled", "Café", "漢字", "👩🏽‍💻", "content", "visible"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("wrapped output lost %q: %q", want, plain)
		}
	}
}

func TestWrapContentHandlesNarrowWidthsWithoutLosingContent(t *testing.T) {
	for _, width := range []int{1, 2, 4} {
		input := "    abcdef"
		if width >= 2 {
			// A two-cell glyph cannot physically fit in a one-cell terminal.
			input += " 漢字"
		}
		rendered := wrapContent(input, width)
		assertRenderedWidth(t, rendered, width)
		plain := strings.Join(strings.Fields(ansi.Strip(rendered)), "")
		if !strings.Contains(plain, strings.ReplaceAll(strings.TrimSpace(input), " ", "")) {
			t.Fatalf("width %d lost content: %q", width, rendered)
		}
	}
}

func TestSessionWrappingTracksTerminalResizeAndScrolling(t *testing.T) {
	objective := "Locate the repository configuration declaration and explain how every consumer uses it without omitting evidence."
	m := model{
		screen: screenSession,
		width:  64,
		height: 12,
		selectedSession: &ipc.Session{
			Title: "A deliberately long repository request that must wrap cleanly",
			Task:  "Which component consumes the configuration declaration after startup?",
			State: "RUNNING",
			Kind:  "work-plan",
		},
		workPlan: &ipc.WorkPlan{
			Objective:           objective,
			Status:              "BLOCKED",
			Constraints:         []string{"do not modify generated files", "preserve Unicode names such as Café"},
			UnresolvedQuestions: []string{"Which runtime owns the final configuration value when multiple sources are present?"},
			Tasks:               []ipc.WorkPlanTask{{Position: 1, Objective: "Gather deterministic evidence", Status: "COMPLETED", ExecutionMethod: "repository.investigation.bootstrap", Result: "Deterministic discovery completed", Evidence: []ipc.WorkEvidence{{Kind: "candidate-file", Path: "internal/recovery/interaction_integration_test.go", BlobHash: "0123456789abcdef", Detail: "inferred relevance score 75 from deterministic path and content matches"}, {Kind: "investigation-topic", Detail: "TestDaemonGracefulShutdownPreservesWaitingInteraction"}}}},
		},
	}

	wideLines := len(strings.Split(m.renderedSessionBody(), "\n"))
	wideLimit := m.maxSessionScroll()
	assertRenderedWidth(t, m.View().Content, 64)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 24, Height: 12})
	m = updated.(model)
	narrowBody := m.renderedSessionBody()
	assertRenderedWidth(t, narrowBody, 24)
	assertRenderedWidth(t, m.View().Content, 24)
	if got := len(strings.Split(narrowBody, "\n")); got <= wideLines {
		t.Fatalf("resize did not reflow content: wide=%d narrow=%d", wideLines, got)
	}
	if m.maxSessionScroll() <= wideLimit {
		t.Fatalf("scroll range did not account for wrapped lines: wide=%d narrow=%d", wideLimit, m.maxSessionScroll())
	}
	for _, want := range []string{"configuration", "consumer", "generated", "Café", "runtime", "candidate-file", "01234567"} {
		if !strings.Contains(narrowBody, want) {
			t.Fatalf("narrow rendering lost %q:\n%s", want, narrowBody)
		}
	}
	collapsed := strings.NewReplacer("\n", "", " ", "").Replace(narrowBody)
	if !strings.Contains(collapsed, "TestDaemonGracefulShutdownPreservesWaitingInteraction") {
		t.Fatalf("narrow rendering lost investigation topic:\n%s", narrowBody)
	}
}

func TestAllTUIViewsRespectTerminalWidth(t *testing.T) {
	long := "A long piece of diagnostic or descriptive content with Unicode 漢字 that must remain within the terminal"
	views := []model{
		{screen: screenHome, width: 22, projects: []ipc.Project{{Name: long, RootPath: "/a/very/long/repository/path"}}, sessions: []ipc.Session{{Title: long, State: "RUNNING"}}},
		{screen: screenQuestion, width: 22, selectedProject: &ipc.Project{Name: long}, input: long + "\n    intentionally indented"},
		{screen: screenIntake, width: 22, intake: &ipc.ProjectIntake{RootPath: "/a/very/long/repository/path", Changes: []ipc.WorkingTreeChange{{Path: long}}}},
		{screen: screenRecovery, width: 22, recovery: long},
		{screen: screenModels, width: 22, provider: &ipc.ProviderConfiguration{Configured: true, Provider: "local", Endpoint: "http://example.invalid/a/very/long/endpoint", Model: long}, qualifications: []ipc.QualificationJob{{ID: 1, Model: long, Status: "COMPLETED"}}},
		{screen: screenQualification, width: 22, selectedQualification: &ipc.QualificationJob{ID: 1, Model: long, Capability: "planning", Status: "FAILED", FailureKind: "validation", FailureReason: long, Results: []ipc.QualificationResult{{Case: "long-case", Attempt: 1, ValidationErrors: []string{long}}}}},
	}

	for index, m := range views {
		assertRenderedWidth(t, m.View().Content, 22)
		if index > 0 && !strings.Contains(m.View().Content, "terminal") {
			// Compact list rows may intentionally truncate, but descriptive views
			// must preserve their important content.
			t.Fatalf("view %d lost descriptive content:\n%s", index, m.View().Content)
		}
	}
}
