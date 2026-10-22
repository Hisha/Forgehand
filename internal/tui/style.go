package tui

// Restrained ANSI styling helpers. These use the terminal's standard color
// slots so they adapt to both dark and light themes, and they always reset
// their own attributes. Status is never communicated by color alone: every
// styled state also carries a textual label or symbol.

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
)

func styleBold(s string) string    { return ansiBold + s + ansiReset }
func styleDim(s string) string     { return ansiDim + s + ansiReset }
func styleHeading(s string) string { return ansiBold + s + ansiReset }
func styleSelected(s string) string {
	return "\x1b[7m" + s + ansiReset
}
func styleWarning(s string) string { return "\x1b[33;1m" + s + ansiReset }
func styleSuccess(s string) string { return "\x1b[32m" + s + ansiReset }
func styleRunning(s string) string { return "\x1b[36m" + s + ansiReset }
func styleDisabled(s string) string {
	return ansiDim + s + ansiReset
}
