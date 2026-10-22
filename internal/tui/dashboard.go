package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/Hisha/Forgehand/internal/ipc"
)

// projectGroup is the dashboard's activity classification.
//
// Group membership is decided by activity alone. Repository health is a
// separate concern and never changes which group a project belongs to.
type projectGroup int

const (
	// groupNeedsAttention holds projects with work that is waiting for the
	// user: a question, an authorization, or an approval.
	groupNeedsAttention projectGroup = iota
	// groupWorking holds projects with active or queued work that can proceed
	// without the user.
	groupWorking
	// groupIdle holds projects without active work and without pending user
	// interaction.
	groupIdle
)

// projectGroups is the fixed dashboard order.
var projectGroups = []projectGroup{groupNeedsAttention, groupWorking, groupIdle}

// title returns the dashboard heading of a group.
func (g projectGroup) title() string {
	switch g {
	case groupNeedsAttention:
		return "NEEDS ATTENTION"
	case groupWorking:
		return "WORKING"
	default:
		return "IDLE"
	}
}

// pendingInteraction pairs a durable worker-to-human interaction with the
// session that raised it.
type pendingInteraction struct {
	Session     ipc.Session
	Interaction ipc.Interaction
}

// projectEntry is the dashboard's view of one project.
//
// Activity, health, and operation prerequisites are deliberately kept apart:
// Group, Waiting, Pending, and Active describe what is happening right now,
// Health describes repository maintenance only, and no field is derived from
// another.
type projectEntry struct {
	Project ipc.Project
	Group   projectGroup
	// Waiting holds the sessions whose durable state is WAITING_FOR_USER.
	Waiting []ipc.Session
	// Pending holds the durable interactions that still need an answer.
	Pending []pendingInteraction
	// Active holds the sessions that are running, queued, or ready to start.
	Active []ipc.Session
	// Recent holds the most recently finished sessions.
	Recent []ipc.Session
	// Health is nil until the daemon reports readiness for the project. An
	// unknown health state is never reported as a warning.
	Health *ipc.ProjectHealth
}

// classifySession maps a durable session state onto the dashboard's activity
// view.
//
// READY is treated as active work: the work has been requested and is waiting
// to start, so the project is not idle even though nothing runs yet. READY
// never waits for the user, so it is not "needs attention" either.
func classifySession(state string) sessionActivity {
	switch state {
	case "WAITING_FOR_USER":
		return activityWaiting
	case "RUNNING", "QUEUED", "READY":
		return activityActive
	default:
		return activityNone
	}
}

// sessionActivity is the activity class of a durable session.
type sessionActivity int

const (
	activityNone sessionActivity = iota
	activityWaiting
	activityActive
)

// groupProjects classifies every registered project by activity and returns
// the projects ordered by group. The order inside a group follows the order the
// daemon reports projects in, so the dashboard stays stable between refreshes.
//
// Precedence is fixed: a project with a pending user interaction belongs to
// NEEDS ATTENTION even when other sessions of the same project are still
// running, and a project with only finished work belongs to IDLE even when
// its stored repository observations are stale.
func groupProjects(
	projects []ipc.Project,
	sessions []ipc.Session,
	pending map[int64][]ipc.Interaction,
	health []ipc.ProjectHealth,
) []projectEntry {
	healthByProject := make(map[int64]*ipc.ProjectHealth, len(health))
	for index := range health {
		healthByProject[health[index].ProjectID] = &health[index]
	}

	sessionsByProject := make(map[int64][]ipc.Session)
	for _, session := range sessions {
		if session.ProjectID == nil {
			continue
		}
		sessionsByProject[*session.ProjectID] = append(
			sessionsByProject[*session.ProjectID],
			session,
		)
	}

	entries := make([]projectEntry, 0, len(projects))
	for _, project := range projects {
		entry := projectEntry{Project: project}
		if stored, ok := healthByProject[project.ID]; ok {
			entry.Health = stored
		}

		for _, session := range sessionsByProject[project.ID] {
			switch classifySession(session.State) {
			case activityWaiting:
				entry.Waiting = append(entry.Waiting, session)
				for _, interaction := range pending[session.ID] {
					entry.Pending = append(entry.Pending, pendingInteraction{
						Session:     session,
						Interaction: interaction,
					})
				}
			case activityActive:
				entry.Active = append(entry.Active, session)
			}
		}
		entry.Recent = recentSessions(sessionsByProject[project.ID])

		switch {
		case len(entry.Waiting) > 0:
			entry.Group = groupNeedsAttention
		case len(entry.Active) > 0:
			entry.Group = groupWorking
		default:
			entry.Group = groupIdle
		}

		entries = append(entries, entry)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Group < entries[j].Group
	})

	return entries
}

// recentSessions returns the most recently finished sessions of a project,
// newest first. Sessions that still wait for the user or that are still
// running are not history and are excluded.
func recentSessions(sessions []ipc.Session) []ipc.Session {
	finished := make([]ipc.Session, 0, len(sessions))
	for _, session := range sessions {
		switch classifySession(session.State) {
		case activityNone:
			finished = append(finished, session)
		}
	}
	sort.SliceStable(finished, func(i, j int) bool {
		return finished[i].ID > finished[j].ID
	})
	const maxRecentSessions = 5
	if len(finished) > maxRecentSessions {
		finished = finished[:maxRecentSessions]
	}
	return finished
}

// isApprovalInteraction reports whether a durable interaction asks for a
// change approval rather than for a free-form answer. The daemon records
// change approvals with the proposal hash in the interaction context.
func isApprovalInteraction(interaction ipc.Interaction) bool {
	_, ok := interaction.Context["proposal_hash"]
	return ok
}

// activityLabel describes the project's current activity in a few words.
func (e projectEntry) activityLabel() string {
	switch e.Group {
	case groupNeedsAttention:
		label := "Waiting for input"
		if len(e.Pending) > 0 {
			label = "Waiting for answer"
			for _, pending := range e.Pending {
				if isApprovalInteraction(pending.Interaction) {
					label = "Waiting for approval"
					break
				}
			}
		}
		return withCount(label, len(e.Waiting))
	case groupWorking:
		label := "Working"
		if len(e.Active) > 0 {
			label = sessionKindLabel(e.Active[0].Kind)
		}
		return withCount(label, len(e.Active))
	default:
		return "Idle"
	}
}

// sessionKindLabel describes what a session of a given kind is doing.
func sessionKindLabel(kind string) string {
	switch kind {
	case "investigation":
		return "Investigating"
	case "work-plan":
		return "Planning"
	default:
		return "Working"
	}
}

// withCount appends the number of matching sessions when there is more than
// one.
func withCount(label string, count int) string {
	if count <= 1 {
		return label
	}
	return fmt.Sprintf("%s (%d)", label, count)
}

// healthWarning reports whether the project carries a maintenance warning.
//
// An unknown health state is not a warning: the dashboard never invents a
// health verdict from missing information.
func (e projectEntry) healthWarning() bool {
	return e.Health != nil && e.Health.Status != "current"
}

// healthSummary describes the project's maintenance state in one sentence.
func (e projectEntry) healthSummary() string {
	if e.Health == nil {
		return "Repository readiness has not been checked yet."
	}
	switch e.Health.Status {
	case "current":
		return "Repository observations are current."
	case "stale":
		return fmt.Sprintf(
			"Repository observations are stale: they describe %s, HEAD is %s.",
			shortHash(e.staleCommit()),
			shortHash(e.Health.Head),
		)
	case "unobserved":
		return "No repository inventory or analysis has been recorded yet."
	default:
		return "The repository revision could not be determined."
	}
}

// staleCommit returns the revision the stored observations describe.
func (e projectEntry) staleCommit() string {
	if e.Health == nil {
		return ""
	}
	if e.Health.AnalysisCommit != "" {
		return e.Health.AnalysisCommit
	}
	return e.Health.InventoryCommit
}

// dashboardView renders the project dashboard.
func (m model) dashboardView() string {
	var b strings.Builder

	b.WriteString("FORGEHAND\n")
	b.WriteString(m.connectionLine() + "\n\n")
	b.WriteString("PROJECTS\n\n")

	if len(m.entries) == 0 {
		b.WriteString("  No projects registered yet. Press a to add a repository.\n")
	}

	nameWidth := m.projectNameWidth()
	for _, group := range projectGroups {
		b.WriteString(styleHeading(group.title()) + "\n")
		for index := range m.entries {
			entry := m.entries[index]
			if entry.Group != group {
				continue
			}
			b.WriteString(dashboardRow(
				index == m.projectCursor,
				entry.Project.Name,
				entry.activityLabel(),
				entry.healthWarning(),
				nameWidth,
				m.mainWidth(),
			))
		}
	}

	b.WriteString("\n" + separatorLine(m.mainWidth()) + "\n")
	b.WriteString(dashboardHelpLine() + "\n")

	if m.busy {
		b.WriteString("\nWorking…\n")
	} else if m.status != "" {
		b.WriteString("\n" + m.status + "\n")
	}

	return b.String()
}

// connectionLine describes the daemon connection and its worker activity.
func (m model) connectionLine() string {
	workers := m.daemonStatus.ActiveWorkers
	line := fmt.Sprintf("Connected · %d %s", workers, pluralize(workers, "worker"))
	if m.daemonStatus.QueuedExecutions > 0 {
		line += fmt.Sprintf(" · %d queued", m.daemonStatus.QueuedExecutions)
	}
	return line
}

// pluralize returns the singular or plural form of a word for a count.
func pluralize(count int, word string) string {
	if count == 1 {
		return word
	}
	return word + "s"
}

// projectNameWidth returns the column width used for project names.
func (m model) projectNameWidth() int {
	width := 12
	for _, entry := range m.entries {
		if length := ansi.StringWidth(entry.Project.Name); length > width {
			width = length
		}
	}
	const maxNameWidth = 28
	if width > maxNameWidth {
		width = maxNameWidth
	}
	return width
}

// dashboardRow renders one project row. The maintenance warning is aligned to
// the right edge so that warnings read as a column instead of trailing noise.
func dashboardRow(
	selected bool,
	name string,
	label string,
	warning bool,
	nameWidth int,
	width int,
) string {
	prefix := "    "
	if selected {
		prefix = "  > "
		prefix = styleSelected(prefix)
	}
	content := prefix + padRight(name, nameWidth) + "  " + label

	limit := width
	if warning {
		limit = width - 1
	}
	content = compactLine(content, limit)
	if !warning {
		return content + "\n"
	}

	padding := width - 1 - ansi.StringWidth(content)
	if padding < 0 {
		padding = 0
	}
	return content + strings.Repeat(" ", padding) + "!\n"
}

// padRight pads a string with spaces up to a visible width.
func padRight(text string, width int) string {
	visible := ansi.StringWidth(text)
	if visible >= width {
		return text
	}
	return text + strings.Repeat(" ", width-visible)
}

// separatorLine draws a horizontal rule.
func separatorLine(width int) string {
	width = renderWidth(width)
	if width > 120 {
		width = 120
	}
	return strings.Repeat("─", width)
}

// dashboardHelpLine lists the dashboard's keyboard actions.
func dashboardHelpLine() string {
	return "↑↓ select   enter open   a add   s sessions   m models   r refresh   ? help   q quit"
}
