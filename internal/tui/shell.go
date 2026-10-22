package tui

import (
	"fmt"
	"strings"
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

// inputAreaLines renders the persistent interaction field.
func (m model) inputAreaLines(mainWidth int) []string {
	// Top separator.
	top := styleDim(strings.Repeat("─", mainWidth))
	// Input line.
	prompt := styleRunning("❯")
	value := m.input
	cursor := "█"
	if value == "" {
		placeholder := m.inputPlaceholder()
		value = styleDim(placeholder)
		cursor = ""
	}
	inputLine := prompt + " " + value + cursor
	inputLine = fitWidth(inputLine, mainWidth)
	// Hint line.
	hint := styleDim(m.inputHint())
	hint = fitWidth(hint, mainWidth)
	return []string{top, inputLine, hint}
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
		return "enter submit investigation · esc clear"
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
	if len(entry.Pending) > 0 {
		b.WriteString("\n" + styleHeading("PENDING DECISIONS") + "\n")
		for _, p := range entry.Pending {
			fmt.Fprintf(b, "- %s\n", singleLine(p.Interaction.Question))
		}
	}
	if len(entry.Active) > 0 {
		b.WriteString("\n" + styleHeading("WORKER ACTIVITY") + "\n")
		for _, s := range entry.Active {
			fmt.Fprintf(b, "- %s (%s)\n", singleLine(s.Title), s.State)
		}
	}
	if len(entry.Recent) > 0 {
		b.WriteString("\n" + styleHeading("RECENT") + "\n")
		for _, s := range entry.Recent {
			fmt.Fprintf(b, "- %s\n", singleLine(s.Title))
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
		return "n new  r refresh  b/esc back  ? help  q quit"
	case screenSession:
		return "↑↓ scroll  r refresh  b/esc back  q quit"
	case screenSessions:
		return "↑↓ select  enter open  b/esc back  q quit"
	default:
		return "? help  q quit"
	}
}
