package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const defaultTerminalWidth = 80

// renderWidth returns a usable width before Bubble Tea has delivered its first
// WindowSizeMsg. A width of one is still valid and is handled by the wrappers.
func renderWidth(width int) int {
	if width <= 0 {
		return defaultTerminalWidth
	}
	return max(1, width)
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
