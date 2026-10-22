package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Hisha/Forgehand/internal/ipc"
)

// conversationKind classifies a timeline entry by who produced it.
//
// The three kinds are deliberately distinct: the request the human made, the
// answers Forgehand produced, and the activity the recorded workers
// performed. A reader can always tell them apart.
type conversationKind int

const (
	// conversationUser is the original request the human made.
	conversationUser conversationKind = iota
	// conversationForgehand is a Forgehand response: a plan, a finding, or
	// the evidence a finding rests on.
	conversationForgehand
	// conversationActivity is a recorded worker or system event.
	conversationActivity
)

// conversationEntry is one message of the conversation timeline.
//
// Every field is filled from persisted state. The workspace never invents a
// turn, a finding, or worker progress that the daemon has not recorded.
type conversationEntry struct {
	Kind conversationKind
	// Author is the name shown beside the entry marker.
	Author string
	// Badge is a short qualifier such as "plan" or "findings".
	Badge string
	// Status is a state word such as "COMPLETED".
	Status string
	// Body is the entry's primary text.
	Body string
	// Details are secondary lines shown under the body.
	Details []string
}

// workspaceSession holds the persisted details of the session that the
// project conversation workspace is currently showing.
type workspaceSession struct {
	Session   *ipc.Session
	Execution *ipc.Execution
	Result    *ipc.ModelResult
	Evidence  *ipc.InvestigationEvidence
	WorkPlan  *ipc.WorkPlan
	Events    []ipc.SessionEvent
	// Loaded reports whether the persisted details were read at least once.
	Loaded bool
	// Loading reports that a refresh is in flight.
	Loading bool
	// Error is the last refresh failure, if any.
	Error string
}

// sessionID returns the identifier of the loaded session, or zero.
func (w workspaceSession) sessionID() int64 {
	if w.Session == nil {
		return 0
	}
	return w.Session.ID
}

// conversationEntries builds the timeline of the session shown in the project
// workspace.
//
// The entries are ordered the way the work actually happened: the request
// first, then what Forgehand produced in response, then the recorded
// activity of the workers that produced it. Optional pieces that the backend
// has not recorded are simply absent.
func (m model) conversationEntries() []conversationEntry {
	detail := m.workspace
	entries := make([]conversationEntry, 0, 5)

	if request := workspaceRequest(detail); request != "" {
		entries = append(entries, conversationEntry{
			Kind:   conversationUser,
			Author: "You",
			Body:   request,
		})
	}

	if entry, ok := m.workerEntry(); ok {
		entries = append(entries, entry)
	}
	if entry, ok := m.planEntry(); ok {
		entries = append(entries, entry)
	}
	if entry, ok := m.findingsEntry(); ok {
		entries = append(entries, entry)
	}
	if entry, ok := m.evidenceEntry(); ok {
		entries = append(entries, entry)
	}
	if entry, ok := m.activityEntry(); ok {
		entries = append(entries, entry)
	}

	if len(entries) == 0 {
		entries = append(entries, conversationEntry{
			Kind:   conversationForgehand,
			Author: "Forgehand",
			Body:   emptyConversationMessage(m.selectedProject),
		})
	}
	return entries
}

// workspaceRequest returns the original request of the loaded session.
//
// A work plan records the request the human made verbatim; other sessions
// keep it as their task. A session that never recorded a request has no user
// turn to show, and the timeline says so instead of inventing one.
func workspaceRequest(detail workspaceSession) string {
	if detail.WorkPlan != nil && strings.TrimSpace(detail.WorkPlan.OriginalRequest) != "" {
		return detail.WorkPlan.OriginalRequest
	}
	if detail.Session != nil && strings.TrimSpace(detail.Session.Task) != "" {
		return detail.Session.Task
	}
	return ""
}

// emptyConversationMessage explains an empty workspace in one sentence.
func emptyConversationMessage(project *ipc.Project) string {
	if project != nil {
		return fmt.Sprintf(
			"No conversation yet for %s. Type a request below to start one.",
			project.Name,
		)
	}
	return "No conversation yet. Type a request below to start one."
}

// workerEntry describes the state of the session's execution.
func (m model) workerEntry() (conversationEntry, bool) {
	detail := m.workspace
	if detail.Session == nil {
		return conversationEntry{}, false
	}
	entry := conversationEntry{
		Kind:   conversationActivity,
		Author: "Worker",
		Status: detail.Session.State,
	}
	switch {
	case detail.Execution != nil:
		entry.Body = "Execution " + strings.ToLower(detail.Execution.Status)
		if detail.Execution.TotalSteps > 0 {
			entry.Body += fmt.Sprintf(" · %d of %d steps", detail.Execution.CurrentStep, detail.Execution.TotalSteps)
		}
		if detail.Execution.QueuePosition > 0 {
			entry.Body += fmt.Sprintf(" · queue position %d", detail.Execution.QueuePosition)
		}
		if detail.Execution.FailureReason != "" {
			if detail.Execution.FailureKind != "" {
				entry.Body += fmt.Sprintf(" · failure (%s)", detail.Execution.FailureKind)
			}
			entry.Body += ": " + detail.Execution.FailureReason
		}
	case detail.Session.State == "WAITING_FOR_USER":
		entry.Body = "Waiting for an answer before the work can continue."
	default:
		entry.Body = "No execution has been recorded for this session yet."
	}
	return entry, true
}

// planEntry describes the durable work plan of the session.
func (m model) planEntry() (conversationEntry, bool) {
	plan := m.workspace.WorkPlan
	if plan == nil {
		return conversationEntry{}, false
	}
	entry := conversationEntry{
		Kind:   conversationForgehand,
		Author: "Forgehand",
		Badge:  "plan",
		Status: plan.Status,
	}
	if plan.Objective != "" {
		entry.Body = plan.Objective
	}
	if plan.Clarification != "" {
		entry.Details = append(entry.Details, "Clarification: "+plan.Clarification)
	}
	for _, constraint := range plan.Constraints {
		entry.Details = append(entry.Details, "Constraint: "+constraint)
	}
	for _, question := range plan.UnresolvedQuestions {
		entry.Details = append(entry.Details, "Unresolved: "+question)
	}
	for _, task := range plan.Tasks {
		entry.Details = append(entry.Details, workPlanTaskLine(task))
	}
	entry.Details = append(entry.Details, fmt.Sprintf(
		"Read-only: %t · %d task(s)",
		plan.ReadOnly,
		len(plan.Tasks),
	))
	return entry, true
}

// workPlanTaskLine renders one work-plan task and its recorded outcome.
func workPlanTaskLine(task ipc.WorkPlanTask) string {
	marker := workTaskMarker(task.Status)
	line := fmt.Sprintf("%s %d. %s", marker, task.Position, singleLine(task.Objective))
	if task.Status != "" {
		line += " [" + task.Status + "]"
	}
	if task.Result != "" {
		line += "\n    result: " + singleLine(task.Result)
	}
	if task.BlockedReason != "" {
		line += "\n    blocked: " + singleLine(task.BlockedReason)
	}
	if task.FailureReason != "" {
		line += "\n    failed: " + singleLine(task.FailureReason)
	}
	return line
}

// workTaskMarker maps a work-plan task state onto a symbol.
func workTaskMarker(status string) string {
	switch status {
	case "COMPLETED":
		return "✓"
	case "RUNNING":
		return "→"
	case "BLOCKED", "FAILED":
		return "!"
	default:
		return "·"
	}
}

// findingsEntry presents the durable model response of the session.
func (m model) findingsEntry() (conversationEntry, bool) {
	result := m.workspace.Result
	if result == nil || strings.TrimSpace(result.Content) == "" {
		return conversationEntry{}, false
	}
	entry := conversationEntry{
		Kind:   conversationForgehand,
		Author: "Forgehand",
		Badge:  "findings",
		Body:   strings.TrimRight(result.Content, "\n"),
	}
	if result.Model != "" {
		entry.Details = append(entry.Details, fmt.Sprintf(
			"Model %s via %s",
			result.Model,
			result.Provider,
		))
	}
	return entry, true
}

// evidenceEntry presents the immutable evidence selection of the session.
func (m model) evidenceEntry() (conversationEntry, bool) {
	evidence := m.workspace.Evidence
	if evidence == nil || len(evidence.Files) == 0 {
		return conversationEntry{}, false
	}
	entry := conversationEntry{
		Kind:   conversationForgehand,
		Author: "Forgehand",
		Badge:  "evidence",
	}
	summary := fmt.Sprintf(
		"%d file(s) · %d of %d characters",
		len(evidence.Files),
		evidence.UsedChars,
		evidence.BudgetChars,
	)
	if evidence.Commit != "" {
		summary += " · commit " + shortHash(evidence.Commit)
	}
	entry.Body = summary
	const maxEvidenceLines = 12
	shown := 0
	for _, file := range evidence.Files {
		if len(file.Locations) == 0 {
			if shown >= maxEvidenceLines {
				break
			}
			entry.Details = append(entry.Details, file.Path)
			shown++
			continue
		}
		for _, location := range file.Locations {
			if shown >= maxEvidenceLines {
				break
			}
			line := fmt.Sprintf("%s:%d", file.Path, location.Line)
			if location.Declaration != "" {
				line += "  " + singleLine(location.Declaration)
			}
			entry.Details = append(entry.Details, line)
			shown++
		}
	}
	if remaining := len(evidence.Files) - shown; remaining > 0 {
		entry.Details = append(entry.Details, fmt.Sprintf("… %d more location(s)", remaining))
	}
	return entry, true
}

// activityEntry presents the recorded session events as a chronological log.
func (m model) activityEntry() (conversationEntry, bool) {
	events := m.workspace.Events
	if len(events) == 0 {
		return conversationEntry{}, false
	}
	entry := conversationEntry{
		Kind:   conversationActivity,
		Author: "Activity",
	}
	const maxActivityLines = 10
	shown := events
	if len(shown) > maxActivityLines {
		shown = events[len(events)-maxActivityLines:]
	}
	for _, event := range shown {
		line := eventLabel(event)
		if stamp := eventTime(event); stamp != "" {
			line = stamp + "  " + line
		}
		entry.Details = append(entry.Details, line)
	}
	if hidden := len(events) - len(shown); hidden > 0 {
		entry.Details = append(entry.Details, fmt.Sprintf("… %d earlier event(s)", hidden))
	}
	return entry, true
}

// eventLabel describes a recorded session event in one phrase.
func eventLabel(event ipc.SessionEvent) string {
	switch event.Type {
	case "session.created":
		return "session created"
	case "execution.queued":
		return "queued for execution"
	case "execution.dispatched":
		return "dispatched to a worker"
	case "execution.started":
		return "execution started"
	case "execution.checkpoint":
		return "checkpoint reached"
	case "execution.completed":
		return "execution completed"
	case "execution.failed":
		return "execution failed"
	case "execution.interrupted":
		return "execution interrupted"
	case "execution.canceled":
		return "execution canceled"
	case "execution.resumed":
		return "execution resumed"
	case "execution.waiting_for_user":
		return "waiting for the user"
	case "execution.resumed_after_input":
		return "resumed after user input"
	case "interaction.requested":
		return "asked the user a question"
	case "interaction.answered":
		return "the user answered"
	case "model.response.completed":
		return "model response completed"
	case "investigation.evidence.selected":
		return "evidence selected"
	case "task.prepared":
		return "task workspace prepared"
	case "task.proposed":
		return "change proposal created"
	case "task.applied":
		return "approved changes applied"
	case "task.rejected":
		return "approved changes rejected"
	case "task.failed":
		return "task failed"
	case "task.completed":
		return "task completed"
	default:
		return event.Type
	}
}

// eventTime formats the recorded time of a session event.
func eventTime(event ipc.SessionEvent) string {
	parsed, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil {
		return ""
	}
	return parsed.Local().Format("15:04:05")
}

// renderConversation renders timeline entries as conversation messages.
//
// Each entry starts on its own line with a marker, the voice that produced
// it, and an optional badge or state. The body is indented under the voice so
// a reader can scan the timeline by voice alone.
func renderConversation(entries []conversationEntry, width int) string {
	width = renderWidth(width)
	var b strings.Builder
	for index, entry := range entries {
		if index > 0 {
			b.WriteString("\n")
		}
		b.WriteString(renderConversationEntry(entry, width))
	}
	return b.String()
}

// renderConversationEntry renders one conversation entry.
func renderConversationEntry(entry conversationEntry, width int) string {
	width = renderWidth(width)
	const indent = "   "
	contentWidth := max(8, width-len(indent))

	var b strings.Builder
	header := conversationMarker(entry.Kind) + " " + conversationVoice(entry.Kind, entry.Author)
	if entry.Badge != "" {
		header += styleMuted("  ·  " + entry.Badge)
	}
	if entry.Status != "" {
		header += "  " + styleState(entry.Status, entry.Status)
	}
	b.WriteString(compactLine(header, width) + "\n")

	for _, line := range append(strings.Split(entry.Body, "\n"), entry.Details...) {
		if entry.Body == "" && line == "" {
			continue
		}
		for _, part := range strings.Split(wrapPhysicalLine(line, contentWidth), "\n") {
			b.WriteString(indent + part + "\n")
		}
	}
	return b.String()
}

// conversationMarker returns the symbol that identifies a conversation voice.
func conversationMarker(kind conversationKind) string {
	switch kind {
	case conversationUser:
		return "●"
	case conversationForgehand:
		return "◆"
	default:
		return "•"
	}
}

// conversationVoice styles the name of the voice that produced an entry.
func conversationVoice(kind conversationKind, author string) string {
	switch kind {
	case conversationUser:
		return styleBold(author)
	case conversationForgehand:
		return styleAccent(author)
	default:
		return styleMuted(author)
	}
}

// styleState colours a state word. Success is green, a warning is amber, and
// work in progress is the active color. The word itself always carries the
// state, so color is never the only signal.
func styleState(state, text string) string {
	switch state {
	case "COMPLETED":
		return styleCompleted(text)
	case "RUNNING", "QUEUED", "READY":
		return styleActive(text)
	case "FAILED", "BLOCKED", "INTERRUPTED", "WAITING_FOR_USER", "CANCELED":
		return styleWarning(text)
	default:
		return styleMuted(text)
	}
}

// sessionStateMarker maps a durable session state onto a symbol.
func sessionStateMarker(state string) string {
	switch state {
	case "COMPLETED":
		return styleCompleted("✓")
	case "RUNNING", "QUEUED", "READY":
		return styleActive("●")
	case "FAILED", "BLOCKED", "INTERRUPTED":
		return styleWarning("!")
	case "WAITING_FOR_USER":
		return styleWarning("?")
	default:
		return styleMuted("·")
	}
}

// sessionSelectorLine renders the visible session selector of the project
// workspace.
//
// The selector is a row of tabs. The selected tab is highlighted, every tab
// carries the state of its session, and the window slides so the selected
// session is always visible. Titles are shortened so that the visible tabs
// always fit on one line.
func (m model) sessionSelectorLine() string {
	width := m.mainWidth()
	prefix := styleHeading(" SESSIONS ")
	sessions := m.projectSessions()
	if len(sessions) == 0 {
		return compactLine(prefix+styleMuted(" no sessions yet"), width)
	}

	selected := m.projectSessionCursor
	if selected < 0 || selected >= len(sessions) {
		selected = 0
	}

	// Reserve room for the label, the separators, and the window markers.
	const minTabWidth = 16
	available := width - ansi.StringWidth(prefix) - 4
	capacity := max(1, available/(minTabWidth+2))
	start := 0
	if selected >= capacity {
		start = selected - capacity + 1
	}
	if start+capacity > len(sessions) {
		start = max(0, len(sessions)-capacity)
	}
	end := min(len(sessions), start+capacity)
	visible := sessions[start:end]

	// Share the remaining width between the visible tabs.
	perTab := max(minTabWidth, (available-2*(len(visible)-1))/max(1, len(visible)))

	var b strings.Builder
	b.WriteString(prefix)
	if start > 0 {
		b.WriteString(styleMuted("‹ "))
	}
	for index, session := range visible {
		if index > 0 {
			b.WriteString("  ")
		}
		tab := sessionTabLabel(session, perTab)
		if start+index == selected {
			b.WriteString(styleSelected(tab))
		} else {
			b.WriteString(styleMuted(tab))
		}
	}
	if end < len(sessions) {
		b.WriteString(styleMuted(" ›"))
	}
	return compactLine(b.String(), width)
}

// sessionTabLabel renders one session tab: the state of the session and its
// title, shortened to the width the tab may use.
func sessionTabLabel(session ipc.Session, width int) string {
	title := singleLine(session.Title)
	if title == "" {
		title = singleLine(session.Task)
	}
	if title == "" {
		title = "session"
	}
	room := width - 2
	if room < 4 {
		room = 4
	}
	return sessionStateMarker(session.State) + " " + compactMiddle(title, room)
}
