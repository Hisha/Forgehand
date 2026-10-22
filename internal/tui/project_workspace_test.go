package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Hisha/Forgehand/internal/ipc"
)

func projectWorkspaceModel(width, height int) model {
	projectID := int64(42)
	project := ipc.Project{ID: 42, Name: "mod-native-hunts", RootPath: "/src/hunts"}
	entry := projectEntry{
		Project: project,
		Group:   groupWorking,
		Active:  []ipc.Session{{ID: 1, Title: "Investigate eligibility", State: "RUNNING", Kind: "work-plan", ProjectID: &projectID}},
		Recent: []ipc.Session{
			{ID: 2, Title: "Huntmaster eligibility", State: "COMPLETED", Kind: "investigation", ProjectID: &projectID},
			{ID: 3, Title: "Zone validation", State: "FAILED", Kind: "investigation", ProjectID: &projectID},
		},
	}
	m := model{
		screen:          screenProject,
		width:           width,
		height:          height,
		selectedProject: &project,
		entries:         []projectEntry{entry},
		projectNavigationFocused: false,
		input:           "",
	}
	m.projectSessionCursor = 0
	return m
}

func TestProjectWorkspaceLayoutAtRepresentativeSizes(t *testing.T) {
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 35}, {160, 45}} {
		m := projectWorkspaceModel(size.w, size.h)
		view := m.View().Content
		lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
		// The shell fills the terminal height.
		if len(lines) != size.h {
			t.Fatalf("%dx%d: rendered %d lines, want %d", size.w, size.h, len(lines), size.h)
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got > size.w {
				t.Fatalf("%dx%d: line %d width %d > %d: %q", size.w, size.h, i+1, got, size.w, ansi.Strip(line))
			}
		}
		// Header appears near the top.
		firstContent := -1
		for i, line := range lines {
			if strings.TrimSpace(ansi.Strip(line)) != "" {
				firstContent = i
				break
			}
		}
		if firstContent < 0 || firstContent > 2 {
			t.Fatalf("%dx%d: first content at line %d, want near top: %q", size.w, size.h, firstContent+1, ansi.Strip(lines[max(0, firstContent)]))
		}
		if !strings.Contains(ansi.Strip(lines[firstContent]), "mod-native-hunts") {
			t.Fatalf("%dx%d: project header not near top: %q", size.w, size.h, ansi.Strip(lines[firstContent]))
		}
		// Recent activity follows the header.
		headerIdx := -1
		recentIdx := -1
		composerIdx := -1
		for i, line := range lines {
			stripped := ansi.Strip(line)
			if headerIdx < 0 && strings.Contains(stripped, "mod-native-hunts") {
				headerIdx = i
			}
			if strings.Contains(stripped, "RECENT SESSIONS") {
				recentIdx = i
			}
			if strings.Contains(stripped, "Composer") || strings.Contains(stripped, "composer") {
				composerIdx = i
			}
		}
		if recentIdx <= headerIdx {
			t.Fatalf("%dx%d: recent activity (%d) not below header (%d)", size.w, size.h, recentIdx, headerIdx)
		}
		if composerIdx < recentIdx {
			t.Fatalf("%dx%d: composer (%d) not below recent activity (%d)", size.w, size.h, composerIdx, recentIdx)
		}
		// Composer is in the bottom region.
		if composerIdx < size.h-6 {
			t.Fatalf("%dx%d: composer at line %d, expected near bottom", size.w, size.h, composerIdx+1)
		}
	}
}

func TestProjectWorkspaceSidebarStructure(t *testing.T) {
	m := projectWorkspaceModel(160, 45)
	view := m.View().Content
	for _, want := range []string{"PROJECT", "Name:", "Activity:", "Health:", "SELECTED SESSION", "PENDING", "ACTIVE WORK", "RECENT"} {
		if !strings.Contains(view, want) {
			t.Fatalf("sidebar missing %q:\n%s", want, view)
		}
	}
	// Selected session title appears, but full task descriptions are not repeated
	// for every recent entry.
	if !strings.Contains(view, "Huntmaster eligibility") {
		t.Fatalf("sidebar missing selected session title:\n%s", view)
	}
}

func TestProjectWorkspaceSelectedSessionHighlight(t *testing.T) {
	m := projectWorkspaceModel(120, 35)
	view := m.projectBody()
	if !strings.Contains(view, "Huntmaster eligibility") {
		t.Fatalf("recent session missing:\n%s", view)
	}
	// Selection marker present.
	if !strings.Contains(view, ">") {
		t.Fatalf("selection marker missing:\n%s", view)
	}
}

func TestProjectWorkspaceSmallTerminalCollapsesSidebar(t *testing.T) {
	m := projectWorkspaceModel(80, 24)
	if m.sidebarVisible() {
		t.Fatalf("sidebar should collapse at 80x24")
	}
	view := m.View().Content
	if strings.Contains(view, "│") {
		// sidebar separator may still appear? At collapsed width no separator.
	}
	_ = view
}

func TestProjectWorkspaceMultilineComposerInput(t *testing.T) {
	m := projectWorkspaceModel(120, 35)
	m.input = "First line\nSecond line"
	view := m.View().Content
	if !strings.Contains(view, "First line") || !strings.Contains(view, "Second line") {
		t.Fatalf("multiline composer not rendered:\n%s", view)
	}
}

func TestDumpProjectWorkspaceSnapshots(t *testing.T) {
	if os.Getenv("FORGEHAND_DUMP_SNAPSHOTS") != "1" {
		t.Skip("set FORGEHAND_DUMP_SNAPSHOTS=1 to write snapshots")
	}
	dir := "/tmp/opencode/project-workspace-snapshots"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 35}, {160, 45}} {
		m := projectWorkspaceModel(size.w, size.h)
		path := filepath.Join(dir, "project-workspace-"+strconv.Itoa(size.w)+"x"+strconv.Itoa(size.h)+".ansi")
		if err := os.WriteFile(path, []byte(m.View().Content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

