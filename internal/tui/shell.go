package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// composeShell combines the fixed application chrome, the main workspace, the
// optional sidebar, the persistent input area, and the status bar into one
// full-screen view sized to the terminal.
//
// The header and the session selector never move. Only the workspace between
// them and the composer scrolls, so the conversation has room to grow without
// pushing the composer or the sidebar around.
func (m model) composeShell(main, sidebar string) string {
	mainWidth := m.mainWidth()
	sideWidth := m.sidebarWidth()
	wsHeight := m.workspaceHeight()
	selector := m.selectorLines()
	bodyHeight := wsHeight + len(selector)

	mainLines := strings.Split(main, "\n")
	if len(mainLines) > wsHeight {
		mainLines = mainLines[:wsHeight]
	}
	for len(mainLines) < wsHeight {
		mainLines = append(mainLines, "")
	}
	for i := range mainLines {
		mainLines[i] = fitWidth(mainLines[i], mainWidth)
	}

	var sideLines []string
	if sideWidth > 0 {
		sideLines = strings.Split(sidebar, "\n")
		if len(sideLines) > bodyHeight {
			sideLines = sideLines[:bodyHeight]
		}
		for len(sideLines) < bodyHeight {
			sideLines = append(sideLines, "")
		}
		for i := range sideLines {
			sideLines[i] = fitWidth(sideLines[i], sideWidth)
		}
	}

	var b strings.Builder
	// The application header spans the whole window.
	for _, line := range m.headerLines() {
		b.WriteString(fitWidth(line, m.width) + "\n")
	}
	for i := 0; i < bodyHeight; i++ {
		line := ""
		if i < len(selector) {
			line = fitWidth(selector[i], mainWidth)
		} else {
			line = mainLines[i-len(selector)]
		}
		if sideWidth > 0 {
			line += styleDim("│") + sideLines[i]
		}
		b.WriteString(line + "\n")
	}

	for _, line := range m.inputAreaLines(mainWidth) {
		row := fitWidth(line, mainWidth)
		if sideWidth > 0 {
			row += styleDim("│") + strings.Repeat(" ", sideWidth)
		}
		row = fitWidth(row, m.width)
		b.WriteString(row + "\n")
	}

	status := fitWidth(m.statusBarLine(), m.width)
	b.WriteString(status + "\n")
	return b.String()
}

// headerLines renders the fixed top bar of the application shell.
func (m model) headerLines() []string {
	return []string{m.headerLine()}
}

// selectorLines renders the fixed region between the header and the
// scrolling workspace. The project workspace uses it for the session
// selector.
func (m model) selectorLines() []string {
	if m.screen != screenProject {
		return nil
	}
	lines := []string{m.sessionSelectorLine()}
	if renderHeight(m.height) >= 18 {
		lines = append(lines, styleDim(strings.Repeat("─", m.mainWidth())))
	}
	return lines
}

// headerLine renders the application header: the product name, the context
// that is open, and the current view.
func (m model) headerLine() string {
	width := renderWidth(m.width)
	brand := " " + styleAccent("Forgehand")
	right := styleMuted(m.viewName())
	available := width - ansi.StringWidth(brand) - ansi.StringWidth(right) - 8
	if available < 8 {
		available = 8
	}
	left := brand
	if crumb := m.headerCrumbs(available); crumb != "" {
		left += styleMuted("  ›  ") + crumb
	}
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(right) - 1
	if gap < 1 {
		return fitWidth(left, width)
	}
	return left + strings.Repeat(" ", gap) + right + " "
}

// headerCrumbs describes what the interface currently has open. The crumb is
// truncated so that the header always keeps the product name and the view.
func (m model) headerCrumbs(available int) string {
	switch m.screen {
	case screenProject:
		if m.selectedProject == nil {
			return ""
		}
		crumb := compactLine(singleLine(m.selectedProject.Name), available)
		if title := m.workspaceSessionTitle(); title != "" {
			room := available - ansi.StringWidth(crumb) - 6
			if room > 6 {
				crumb += styleMuted("  ›  ") + compactLine(title, room)
			}
		}
		return crumb
	case screenSession:
		if m.selectedSession != nil {
			return compactLine(singleLine(m.selectedSession.Title), available)
		}
	}
	return ""
}

// workspaceSessionTitle returns the title of the session the project
// workspace is showing.
func (m model) workspaceSessionTitle() string {
	if m.workspace.Session == nil {
		return ""
	}
	return singleLine(m.workspace.Session.Title)
}

// inputAreaLines renders the persistent interaction field as a visually
// distinct, multiline-capable composer.
func (m model) inputAreaLines(mainWidth int) []string {
	height := m.inputAreaHeight()
	lines := make([]string, 0, height)

	focused := m.inputActive()
	title := "Composer"
	if m.screen == screenProject && m.projectNavigationFocused {
		title += " · session selector focused"
	} else if focused {
		title += " · focused"
	}
	// Top border with a section title.
	borderLabel := "─ " + title + " "
	remaining := mainWidth - ansi.StringWidth(borderLabel)
	if remaining < 0 {
		remaining = 0
	}
	border := styleDim(borderLabel + strings.Repeat("─", remaining))
	if focused {
		border = styleAccentDim(borderLabel + strings.Repeat("─", remaining))
	}
	lines = append(lines, fitWidth(border, mainWidth))

	// Input lines.
	prompt := styleAccent("❯")
	if !focused {
		prompt = ""
	}
	inputLines := []string{""}
	if m.input != "" {
		inputLines = insertCursorBlock(m.input, m.inputCursor)
	} else {
		inputLines = []string{styleDim(m.inputPlaceholder())}
	}
	// Show the trailing portion when the input exceeds the available height.
	availableInputLines := height - 2
	if availableInputLines < 1 {
		availableInputLines = 1
	}
	if len(inputLines) > availableInputLines {
		inputLines = inputLines[len(inputLines)-availableInputLines:]
	}
	for i, line := range inputLines {
		prefix := "  "
		if i == 0 {
			prefix = prompt + " "
		}
		rendered := prefix + line
		lines = append(lines, fitWidth(rendered, mainWidth))
	}
	for len(lines) < height-1 {
		lines = append(lines, fitWidth("", mainWidth))
	}

	// Hint line.
	hint := styleDim(m.inputHint())
	if focused {
		hint = styleMuted(m.inputHint())
	}
	lines = append(lines, fitWidth(hint, mainWidth))

	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, fitWidth("", mainWidth))
	}
	return lines
}

func (m model) inputPlaceholder() string {
	switch m.screen {
	case screenDashboard:
		return "Select a project to begin..."
	case screenProject:
		return "Ask about this project..."
	case screenQuestion:
		return "Type your investigation question..."
	case screenRegister:
		return "Enter a repository path..."
	case screenModelInput:
		return "Enter a model identifier..."
	case screenDiscardConfirm:
		return "Type DISCARD to confirm..."
	case screenSessions:
		return "Select a session to inspect..."
	case screenModels:
		return "Model management input..."
	default:
		return "no text input on this screen"
	}
}

func (m model) inputActive() bool {
	switch m.screen {
	case screenQuestion, screenRegister, screenModelInput, screenDiscardConfirm, screenProject:
		return true
	}
	return false
}

func (m model) inputHint() string {
	if !m.inputActive() {
		return ""
	}
	switch m.screen {
	case screenDashboard:
		return "Select a project to begin..."
	case screenProject:
		if m.projectNavigationFocused {
			return "↑↓ switch session · enter open · n new request · tab composer · b back"
		}
		return "enter send · shift+enter newline · tab sessions · esc clear"
	case screenQuestion:
		return "enter submit · esc cancel"
	case screenRegister:
		return "enter register · esc cancel"
	case screenModelInput:
		return "enter confirm · esc cancel"
	case screenDiscardConfirm:
		return "enter confirm · esc cancel"
	default:
		return ""
	}
}

// sidebarContent returns the contextual sidebar text.
func (m model) sidebarContent() string {
	if !m.sidebarVisible() {
		return ""
	}
	var b strings.Builder
	switch m.screen {
	case screenDashboard:
		m.writeDashboardSidebar(&b)
	case screenProject:
		m.writeProjectSidebar(&b)
	case screenSession:
		m.writeSessionSidebar(&b)
	case screenModels, screenModelInput, screenQualification:
		m.writeModelsSidebar(&b)
	case screenSessions:
		m.writeSessionsSidebar(&b)
	default:
		b.WriteString(styleHeading("CONTEXT") + "\n")
		b.WriteString(styleDim("No contextual information.") + "\n")
	}
	return wrapContent(b.String(), m.sidebarWidth())
}

func (m model) selectedDashboardEntry() *projectEntry {
	if len(m.entries) == 0 {
		return nil
	}
	index := m.projectCursor
	if index < 0 {
		index = 0
	}
	if index >= len(m.entries) {
		index = len(m.entries) - 1
	}
	entry := m.entries[index]
	return &entry
}

func (m model) writeDashboardSidebar(b *strings.Builder) {
	b.WriteString(styleHeading("CONTEXT") + "\n")
	entry := m.selectedDashboardEntry()
	if entry == nil {
		b.WriteString(styleDim("No projects registered.") + "\n")
		return
	}
	fmt.Fprintf(b, "Project: %s\n", entry.Project.Name)
	if entry.Project.RootPath != "" {
		fmt.Fprintf(b, "Path: %s\n", entry.Project.RootPath)
	}
	fmt.Fprintf(b, "Activity: %s\n", entry.activityLabel())
	b.WriteString("Health: " + entry.healthSummary() + "\n")
	if len(entry.Pending) > 0 {
		b.WriteString("\n" + styleHeading("PENDING") + "\n")
		for _, p := range entry.Pending {
			fmt.Fprintf(b, "- %s\n", singleLine(p.Interaction.Question))
		}
	}
	if len(entry.Active) > 0 {
		b.WriteString("\n" + styleHeading("ACTIVE WORK") + "\n")
		for _, s := range entry.Active {
			fmt.Fprintf(b, "- %s\n", singleLine(s.Title))
		}
	}
}

func (m model) writeProjectSidebar(b *strings.Builder) {
	b.WriteString(styleHeading("PROJECT") + "\n")
	if m.selectedProject == nil {
		b.WriteString(styleDim("No project selected.") + "\n")
		return
	}
	fmt.Fprintf(b, "Name: %s\n", m.selectedProject.Name)
	if m.selectedProject.RootPath != "" {
		fmt.Fprintf(b, "Path: %s\n", m.selectedProject.RootPath)
	}
	entry := m.projectEntry(*m.selectedProject)
	fmt.Fprintf(b, "Activity: %s\n", entry.activityLabel())
	if entry.healthWarning() {
		fmt.Fprintf(b, "%s %s\n", styleWarning("!"), styleWarning("Health: "+entry.healthLabel()))
	} else {
		fmt.Fprintf(b, "Health: %s\n", entry.healthLabel())
	}

	if session := m.workspace.Session; session != nil {
		b.WriteString("\n" + styleHeading("SESSION") + "\n")
		fmt.Fprintf(b, "Title: %s\n", compactLine(singleLine(session.Title), m.sidebarWidth()))
		fmt.Fprintf(b, "State: %s\n", styleState(session.State, session.State))
		if session.Kind != "" {
			fmt.Fprintf(b, "Kind: %s\n", session.Kind)
		}
		if session.Task != "" {
			fmt.Fprintf(b, "Request: %s\n", compactLine(singleLine(session.Task), m.sidebarWidth()))
		}
		if m.workspace.Execution != nil {
			fmt.Fprintf(b, "Execution: %s\n", styleState(m.workspace.Execution.Status, m.workspace.Execution.Status))
			if m.workspace.Execution.TotalSteps > 0 {
				fmt.Fprintf(b, "Progress: %d/%d\n", m.workspace.Execution.CurrentStep, m.workspace.Execution.TotalSteps)
			}
			if m.workspace.Execution.QueuePosition > 0 {
				fmt.Fprintf(b, "Queue: %d\n", m.workspace.Execution.QueuePosition)
			}
		}
		if m.workspace.WorkPlan != nil {
			fmt.Fprintf(b, "Plan: %s\n", styleState(m.workspace.WorkPlan.Status, m.workspace.WorkPlan.Status))
			fmt.Fprintf(b, "Read-only: %t\n", m.workspace.WorkPlan.ReadOnly)
		}
		if m.workspace.Result != nil && m.workspace.Result.Model != "" {
			fmt.Fprintf(b, "Model: %s\n", m.workspace.Result.Model)
		}
	}

	if len(entry.Pending) > 0 {
		b.WriteString("\n" + styleHeading("PENDING") + "\n")
		for _, p := range entry.Pending {
			fmt.Fprintf(b, "- %s\n", compactLine(singleLine(p.Interaction.Question), m.sidebarWidth()))
		}
	}
	if len(entry.Active) > 0 {
		b.WriteString("\n" + styleHeading("ACTIVE WORK") + "\n")
		for _, sess := range entry.Active {
			fmt.Fprintf(b, "- %s (%s)\n", compactLine(singleLine(sess.Title), m.sidebarWidth()), sess.State)
		}
	}
	if m.provider != nil && m.provider.Configured {
		b.WriteString("\n" + styleHeading("MODEL / PROVIDER") + "\n")
		fmt.Fprintf(b, "Provider: %s\n", m.provider.Provider)
		fmt.Fprintf(b, "Model: %s\n", m.provider.Model)
	}
}

func (m model) writeSessionSidebar(b *strings.Builder) {
	b.WriteString(styleHeading("SESSION") + "\n")
	if m.selectedSession == nil {
		b.WriteString(styleDim("No session selected.") + "\n")
		return
	}
	s := m.selectedSession
	fmt.Fprintf(b, "Title: %s\n", s.Title)
	fmt.Fprintf(b, "State: %s\n", s.State)
	if s.Task != "" {
		fmt.Fprintf(b, "Question: %s\n", singleLine(s.Task))
	}
	if m.execution != nil {
		fmt.Fprintf(b, "Execution: %s\n", m.execution.Status)
		if m.execution.TotalSteps > 0 {
			fmt.Fprintf(b, "Progress: %d/%d\n", m.execution.CurrentStep, m.execution.TotalSteps)
		}
		if m.execution.QueuePosition > 0 {
			fmt.Fprintf(b, "Queue: %d\n", m.execution.QueuePosition)
		}
	}
	if m.provider != nil && m.provider.Configured {
		b.WriteString("\n" + styleHeading("MODEL / PROVIDER") + "\n")
		fmt.Fprintf(b, "Provider: %s\n", m.provider.Provider)
		fmt.Fprintf(b, "Model: %s\n", m.provider.Model)
	}
	if m.workPlan != nil {
		b.WriteString("\n" + styleHeading("PLAN") + "\n")
		fmt.Fprintf(b, "Status: %s\n", m.workPlan.Status)
		fmt.Fprintf(b, "Read-only: %t\n", m.workPlan.ReadOnly)
	}
}

func (m model) writeModelsSidebar(b *strings.Builder) {
	b.WriteString(styleHeading("MODELS") + "\n")
	if m.provider == nil || !m.provider.Configured {
		b.WriteString(styleDim("Provider not configured.") + "\n")
	} else {
		fmt.Fprintf(b, "Provider: %s\n", m.provider.Provider)
		fmt.Fprintf(b, "Endpoint: %s\n", m.provider.Endpoint)
		fmt.Fprintf(b, "Model: %s\n", m.provider.Model)
	}
	fmt.Fprintf(b, "Qualification model: %s\n", m.qualificationModel)
	fmt.Fprintf(b, "Repetitions: %d\n", m.qualificationRepetitions)
	if m.selectedQualification != nil {
		b.WriteString("\n" + styleHeading("SELECTED") + "\n")
		fmt.Fprintf(b, "#%d %s\n", m.selectedQualification.ID, m.selectedQualification.Status)
	}
}

func (m model) writeSessionsSidebar(b *strings.Builder) {
	b.WriteString(styleHeading("SESSIONS") + "\n")
	if len(m.sessions) == 0 {
		b.WriteString(styleDim("No sessions.") + "\n")
		return
	}
	idx := m.sessionsCursor
	if idx < 0 {
		idx = 0
	}
	if idx >= len(m.sessions) {
		idx = len(m.sessions) - 1
	}
	s := m.sessions[idx]
	fmt.Fprintf(b, "Title: %s\n", s.Title)
	fmt.Fprintf(b, "State: %s\n", s.State)
	if name := m.projectName(s.ProjectID); name != "" {
		fmt.Fprintf(b, "Project: %s\n", name)
	}
}

// statusBarLine renders the persistent bottom status/navigation bar.
//
// The bar never truncates: the keyboard shortcuts are dropped first when the
// terminal is too narrow to hold them.
func (m model) statusBarLine() string {
	width := renderWidth(m.width)
	context := ""
	switch {
	case m.screen == screenProject && m.selectedProject != nil:
		context = m.selectedProject.Name
		if title := m.workspaceSessionTitle(); title != "" {
			context += "  ·  " + compactLine(title, 40)
		}
	case m.selectedProject != nil:
		context = m.selectedProject.Name
	case len(m.entries) > 0:
		entry := m.selectedDashboardEntry()
		if entry != nil {
			context = entry.Project.Name
		}
	}
	conn := "disconnected"
	if m.daemonStatus.OK || m.daemonStatus.PID > 0 {
		conn = "connected"
	}
	if m.busy {
		conn += " · working"
	}
	workers := fmt.Sprintf("%d %s", m.daemonStatus.ActiveWorkers, pluralize(m.daemonStatus.ActiveWorkers, "worker"))
	if m.daemonStatus.QueuedExecutions > 0 {
		workers += fmt.Sprintf(" · %d queued", m.daemonStatus.QueuedExecutions)
	}
	parts := []string{}
	if context != "" {
		parts = append(parts, context)
	}
	if status := m.statusText(); status != "" {
		parts = append(parts, status)
	}
	parts = append(parts, conn, workers)
	line := strings.Join(parts, "  ·  ")
	if ansi.StringWidth(line) >= width {
		return styleDim(compactLine(line, width))
	}
	shortcuts := m.statusShortcuts()
	if shortcuts == "" {
		return styleDim(fitWidth(line, width))
	}
	if ansi.StringWidth(line)+3+ansi.StringWidth(shortcuts) > width {
		return styleDim(fitWidth(line, width))
	}
	return styleDim(fitWidth(line+"  ·  "+shortcuts, width))
}

func (m model) viewName() string {
	switch m.screen {
	case screenDashboard:
		return "Dashboard"
	case screenProject:
		return "Project"
	case screenSessions:
		return "Sessions"
	case screenSession:
		return "Session"
	case screenModels:
		return "Models"
	case screenQualification:
		return "Qualification"
	case screenHelp:
		return "Help"
	case screenQuestion:
		return "New request"
	case screenRegister:
		return "Register"
	case screenIntake:
		return "Intake"
	case screenRecovery:
		return "Recovery"
	case screenUpgrade, screenUpgradeConfirm:
		return "Upgrade"
	default:
		return "Forgehand"
	}
}

func (m model) statusShortcuts() string {
	switch m.screen {
	case screenDashboard:
		return "↑↓ select  enter open  a add  s sessions  m models  ? help  q quit"
	case screenProject:
		if m.projectNavigationFocused {
			return "↑↓ session  enter open  n new  ? help  b back  q quit"
		}
		return "enter send  shift+enter newline  tab sessions  esc clear"
	case screenSession:
		return "↑↓ scroll  r refresh  b/esc back  q quit"
	case screenSessions:
		return "↑↓ select  enter open  b/esc back  q quit"
	default:
		return "? help  q quit"
	}
}
