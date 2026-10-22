package tui

import "github.com/charmbracelet/colorprofile"

// Restrained ANSI styling helpers. These use the terminal's standard color
// slots so they adapt to both dark and light themes, and they always reset
// their own attributes. Status is never communicated by color alone: every
// styled state also carries a textual label or symbol.

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
)

// terminalProfile reports the color capabilities of the terminal the
// interface is running in. The runtime announces its profile once at startup;
// the interface uses it to pick the richest accent the terminal can actually
// render and to drop color entirely on terminals that cannot show it.
var terminalProfile = colorprofile.ANSI256

// setTerminalProfile records the terminal's color capabilities.
func setTerminalProfile(profile colorprofile.Profile) {
	terminalProfile = profile
}

// colorEnabled reports whether the terminal can render colors at all. A
// terminal with no color support still receives the interface's structure,
// labels, and symbols.
func colorEnabled() bool { return terminalProfile > colorprofile.ASCII }

// colored wraps text in an SGR sequence when the terminal supports color.
func colored(sequence, text string) string {
	if !colorEnabled() {
		return text
	}
	return sequence + text + ansiReset
}

func styleBold(s string) string    { return ansiBold + s + ansiReset }
func styleDim(s string) string     { return ansiDim + s + ansiReset }
func styleHeading(s string) string { return ansiBold + s + ansiReset }
func styleSecondary(s string) string {
	return ansiDim + s + ansiReset
}
func styleSelected(s string) string {
	return "\x1b[7m" + s + ansiReset
}
func styleWarning(s string) string   { return colored("\x1b[33;1m", s) }
func styleSuccess(s string) string   { return colored("\x1b[32m", s) }
func styleCompleted(s string) string { return colored("\x1b[32m", s) }
func styleRunning(s string) string   { return colored("\x1b[36m", s) }
func styleActive(s string) string    { return colored("\x1b[36m", s) }
func styleDisabled(s string) string {
	return ansiDim + s + ansiReset
}

// Focused controls use a distinct but restrained highlight.
func styleFocused(s string) string { return colored("\x1b[1;36m", s) }

// Muted text for secondary details and placeholders.
func styleMuted(s string) string { return ansiDim + s + ansiReset }

// Status indicator helpers.
func styleStatusOK(s string) string    { return colored("\x1b[32m", s) }
func styleStatusWarn(s string) string  { return colored("\x1b[33m", s) }
func styleStatusError(s string) string { return colored("\x1b[31m", s) }
func styleStatusInfo(s string) string  { return colored("\x1b[36m", s) }

// styleAccent renders the interface accent: a restrained lavender used for
// the product name, the Forgehand voice, and the active composer. Terminals
// with fewer colors receive the closest color their palette can show.
func styleAccent(s string) string {
	switch {
	case !colorEnabled():
		return s
	case terminalProfile == colorprofile.ANSI:
		return colored("\x1b[1;35m", s)
	case terminalProfile == colorprofile.ANSI256:
		return colored("\x1b[38;5;141m", s)
	default:
		return colored("\x1b[38;2;175;140;255m", s)
	}
}

// styleAccentDim renders the accent without its full intensity, for chrome
// that should read as secondary.
func styleAccentDim(s string) string {
	switch {
	case !colorEnabled():
		return styleDim(s)
	case terminalProfile == colorprofile.ANSI:
		return colored("\x1b[2;35m", s)
	case terminalProfile == colorprofile.ANSI256:
		return colored("\x1b[38;5;140m", s)
	default:
		return colored("\x1b[38;2;150;118;225m", s)
	}
}
