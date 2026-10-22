package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// composeShell combines the main workspace, optional sidebar, persistent input
// area, and status bar into one full-screen view sized to the terminal.
func (m model) composeShell(main, sidebar string) string {
	mainWidth := m.mainWidth()
	sideWidth := m.sidebarWidth()
	wsHeight := m.workspaceHeight()

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
		if len(sideLines) > wsHeight {
			sideLines = sideLines[:wsHeight]
		}
		for len(sideLines) < wsHeight {
			sideLines = append(sideLines, "")
		}
		for i := range sideLines {
			sideLines[i] = fitWidth(sideLines[i], sideWidth)
		}
	}

	var b strings.Builder
	for i := 0; i < wsHeight; i++ {
		line := mainLines[i]
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

// inputAreaLines renders the persistent interaction field as a visually
// distinct, multiline-capable composer.
func (m model) inputAreaLines(mainWidth int) []string {
	height := m.inputAreaHeight()
	lines := make([]string, 0, height)

	focused := m.inputActive()
	title := "Composer"
	if !focused {
		title = "Input"
	}
	if m.screen == screenProject && m.projectNavigationFocused {
		title += " (navigation focus)"
	} else if focused {
		title += " (focused)"
	}
	// Top border with a section title.
	borderLabel := "─ " + title + " "
	remaining := mainWidth - ansi.StringWidth(borderLabel)
	if remaining < 0 {
		remaining = 0
	}
	top := styleDim(borderLabel + strings.Repeat("─", remaining))
	lines = append(lines, fitWidth(top, mainWidth))

	// Input lines.
	prompt := styleRunning("❯")
	if !focused {
		prompt = styleDim("❯")
	}
	inputLines := []string{""}
	if m.input != "" {
		inputLines = strings.Split(m.input, "\n")
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
		if i == len(inputLines)-1 && m.input != "" {
			rendered += "█"
		}
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
		return "Input unavailable on this screen"
	}
}

func (m model) inputActive() bool {
	switch m.screen {
	case screenDashboard, screenSessions, screenModels, screenQualification,
		screenSession, screenHelp, screenProject, screenUpgrade, screenUpgradeConfirm,
		screenRecovery, screenIntake:
		// screenProject allows input; keep active list explicit below.
	}
	switch m.screen {
	case screenQuestion, screenRegister, screenModelInput, screenDiscardConfirm, screenProject:
		return true
	}
	return false
}

func (m model) inputHint() string {
	if !m.inputActive() {
		return "Input disabled on this screen"
	}
	switch m.screen {
	case screenDashboard:
		return "Select a project to begin..."
	case screenProject:
		if m.projectNavigationFocused {
			return "tab composer · ↑↓ select session · enter open · r refresh · b/esc back"
		}
		return "enter submit/open · esc clear · tab navigate"
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
	b.WriteString("Health: " + entry.healthSummary() + "\n")

	if session := m.projectSelectedSession(); session != nil {
		b.WriteString("\n" + styleHeading("SELECTED SESSION") + "\n")
		fmt.Fprintf(b, "Title: %s\n", singleLine(session.Title))
		fmt.Fprintf(b, "State: %s\n", session.State)
		if session.Task != "" {
			fmt.Fprintf(b, "Task: %s\n", compactLine(singleLine(session.Task), m.sidebarWidth()))
		}
		if m.selectedSession != nil && m.selectedSession.ID == session.ID && m.execution != nil {
			fmt.Fprintf(b, "Execution: %s\n", m.execution.Status)
			if m.execution.TotalSteps > 0 {
				fmt.Fprintf(b, "Progress: %d/%d\n", m.execution.CurrentStep, m.execution.TotalSteps)
			}
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
			fmt.Fprintf(b, "- %s (%s)\n", singleLine(sess.Title), sess.State)
		}
	}
	if len(entry.Recent) > 0 {
		b.WriteString("\n" + styleHeading("RECENT") + "\n")
		for _, sess := range entry.Recent {
			fmt.Fprintf(b, "- %s\n", singleLine(sess.Title))
		}
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
func (m model) statusBarLine() string {
	viewName := m.viewName()
	context := ""
	if m.selectedProject != nil {
		context = m.selectedProject.Name
	} else if len(m.entries) > 0 {
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
	parts := []string{"Forgehand", viewName}
	if context != "" {
		parts = append(parts, context)
	}
	parts = append(parts, conn, workers)
	line := strings.Join(parts, "  ·  ")
	shortcuts := m.statusShortcuts()
	if shortcuts != "" {
		line += "  ·  " + shortcuts
	}
	return styleDim(line)
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
		return "tab focus  ↑↓ select  enter submit/open  esc clear  r refresh  b back  ? help  q quit"
	case screenSession:
		return "↑↓ scroll  r refresh  b/esc back  q quit"
	case screenSessions:
		return "↑↓ select  enter open  b/esc back  q quit"
	default:
		return "? help  q quit"
	}
}
