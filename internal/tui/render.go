package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const defaultTerminalWidth = 80
const defaultTerminalHeight = 24

// renderWidth returns a usable width before Bubble Tea has delivered its first
// WindowSizeMsg. A width of one is still valid and is handled by the wrappers.
func renderWidth(width int) int {
	if width <= 0 {
		return defaultTerminalWidth
	}
	return max(1, width)
}

func renderHeight(height int) int {
	if height <= 0 {
		return defaultTerminalHeight
	}
	return max(1, height)
}

// wrapContent wraps every physical line independently so authored line breaks
// and indentation remain meaningful. ansi.Wrap measures terminal cells rather
// than bytes or runes, so styled text, combining characters, and wide Unicode
// glyphs all obey the same width limit.
func wrapContent(text string, width int) string {
	width = renderWidth(width)
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = wrapPhysicalLine(line, width)
	}
	return strings.Join(lines, "\n")
}

func wrapPhysicalLine(line string, width int) string {
	// Tabs have terminal-dependent stops and are not assigned a printable width
	// by ANSI parsers. Expanding them makes indentation stable and measurable.
	line = strings.ReplaceAll(line, "\t", "    ")
	if line == "" || ansi.StringWidth(line) <= width {
		return line
	}

	indent, content := splitIndent(line)
	// On very narrow terminals, retain as much indentation as possible while
	// always reserving a cell for content. This is the only graceful rendering
	// possible without either overflowing or discarding the content itself.
	if ansi.StringWidth(indent) >= width {
		indent = ansi.Truncate(indent, max(0, width-1), "")
	}
	contentWidth := max(1, width-ansi.StringWidth(indent))
	// Wrap favors word boundaries; Hardwrap is a final guard for unusually
	// narrow widths and long tokens. Both preserve ANSI control sequences.
	wrapped := ansi.Hardwrap(ansi.Wrap(content, contentWidth, "/.,_:"), contentWidth, false)
	parts := strings.Split(wrapped, "\n")
	for index := range parts {
		parts[index] = indent + parts[index]
	}
	return ansi.Hardwrap(strings.Join(parts, "\n"), width, false)
}

func splitIndent(line string) (string, string) {
	index := 0
	for index < len(line) {
		switch line[index] {
		case ' ':
			index++
		default:
			return line[:index], line[index:]
		}
	}
	return line, ""
}

// compactLine is for navigational rows and status bars whose one-line shape
// conveys structure. Descriptive content uses wrapContent instead.
func compactLine(text string, width int) string {
	width = renderWidth(width)
	if ansi.StringWidth(text) <= width {
		return text
	}
	tail := "…"
	if width < ansi.StringWidth(tail) {
		tail = ""
	}
	return ansi.Truncate(text, width, tail)
}

func compactRow(selected bool, text string, width int) string {
	prefix := "    "
	if selected {
		prefix = "  > "
	}
	return compactLine(prefix+text, width) + "\n"
}

// Application shell layout helpers.

func (m model) sidebarVisible() bool {
	switch m.screen {
	case screenDashboard, screenProject, screenSession, screenSessions,
		screenModels, screenQualification:
	default:
		return false
	}
	return renderWidth(m.width) >= 90 && renderHeight(m.height) >= 14
}

func (m model) sidebarWidth() int {
	if !m.sidebarVisible() {
		return 0
	}
	width := renderWidth(m.width) * 28 / 100
	if width < 22 {
		width = 22
	}
	if width > 40 {
		width = 40
	}
	if renderWidth(m.width)-width-1 < 40 {
		width = m.width - 41
	}
	if width < 0 {
		width = 0
	}
	return width
}

func (m model) mainWidth() int {
	width := renderWidth(m.width) - m.sidebarWidth()
	if m.sidebarWidth() > 0 {
		width-- // separator column
	}
	if width < 1 {
		width = 1
	}
	return width
}

func (m model) inputLineCount() int {
	if strings.TrimSpace(m.input) == "" {
		return 1
	}
	count := strings.Count(m.input, "\n") + 1
	if count < 1 {
		count = 1
	}
	if count > 6 {
		count = 6
	}
	return count
}

func (m model) inputAreaHeight() int {
	height := m.inputLineCount() + 2
	if height < 3 {
		height = 3
	}
	if height > 8 {
		height = 8
	}
	if renderHeight(m.height) < height+3 {
		height = renderHeight(m.height) - 3
		if height < 1 {
			height = 1
		}
	}
	return height
}

func (m model) statusBarHeight() int { return 1 }

func (m model) workspaceHeight() int {
	height := renderHeight(m.height) - m.inputAreaHeight() - m.statusBarHeight()
	if height < 1 {
		height = 1
	}
	return height
}

func fitWidth(text string, width int) string {
	width = renderWidth(width)
	if ansi.StringWidth(text) > width {
		return compactLine(text, width)
	}
	return text + strings.Repeat(" ", width-ansi.StringWidth(text))
}
