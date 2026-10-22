package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/Hisha/Forgehand/internal/config"
	"github.com/Hisha/Forgehand/internal/ipc"
)

type transport interface {
	Request(ipc.Request) (ipc.Response, error)
}

type daemonTransport struct{}

func (daemonTransport) Request(request ipc.Request) (ipc.Response, error) {
	socketPath, err := config.SocketPath()
	if err != nil {
		return ipc.Response{}, err
	}
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return ipc.Response{}, fmt.Errorf("daemon unavailable at %s", socketPath)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return ipc.Response{}, err
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return ipc.Response{}, fmt.Errorf("send request: %w", err)
	}
	var response ipc.Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return ipc.Response{}, fmt.Errorf("read response: %w", err)
	}
	if response.ProtocolVersion != ipc.ProtocolVersion {
		return response, fmt.Errorf("daemon IPC protocol %d is incompatible with this frontend (requires %d)", response.ProtocolVersion, ipc.ProtocolVersion)
	}
	if !response.OK {
		return response, errors.New(response.Message)
	}
	return response, nil
}

type screen int

const (
	// screenDashboard is the project-selection dashboard. It is the screen
	// Forgehand opens to.
	screenDashboard screen = iota
	screenQuestion
	screenRegister
	screenSession
	screenRecovery
	screenIntake
	screenDiscardConfirm
	screenModels
	screenQualification
	screenModelInput
	screenUpgrade
	screenUpgradeConfirm
	// screenProject shows the detail of a single project.
	screenProject
	// screenSessions lists every durable session.
	screenSessions
	// screenHelp lists the keyboard actions of the current screen.
	screenHelp
)

type model struct {
	client                   transport
	screen                   screen
	projects                 []ipc.Project
	sessions                 []ipc.Session
	entries                  []projectEntry
	health                   []ipc.ProjectHealth
	pending                  map[int64][]ipc.Interaction
	daemonStatus             ipc.Response
	projectCursor            int
	sessionsCursor           int
	selectedProject          *ipc.Project
	selectedSession          *ipc.Session
	execution                *ipc.Execution
	result                   *ipc.ModelResult
	evidence                 *ipc.InvestigationEvidence
	workPlan                 *ipc.WorkPlan
	provider                 *ipc.ProviderConfiguration
	qualifications           []ipc.QualificationJob
	qualificationCursor      int
	selectedQualification    *ipc.QualificationJob
	qualificationModel       string
	qualificationRepetitions int
	intake                   *ipc.ProjectIntake
	input                    string
	busy                     bool
	status                   string
	recovery                 string
	width                    int
	height                   int
	scroll                   int
	ticks                    int
	polling                  bool
	helpReturn               screen
	serviceFactory           serviceFactory
	upgrade                  *daemonUpgrade
	upgradeAction            string
}

type loadedMsg struct {
	projects       []ipc.Project
	sessions       []ipc.Session
	pending        map[int64][]ipc.Interaction
	provider       *ipc.ProviderConfiguration
	qualifications []ipc.QualificationJob
	daemonStatus   ipc.Response
	err            error
	upgrade        *daemonUpgrade
}

// healthMsg carries the repository readiness of the registered projects. It is
// advisory: a failure never hides the dashboard.
type healthMsg struct {
	health []ipc.ProjectHealth
	err    error
}

// activityMsg carries refreshed session activity for the dashboard.
type activityMsg struct {
	sessions []ipc.Session
	pending  map[int64][]ipc.Interaction
	err      error
}

type registeredMsg struct {
	project *ipc.Project
	intake  *ipc.ProjectIntake
	err     error
}
type startedMsg struct {
	session   *ipc.Session
	execution *ipc.Execution
	workPlan  *ipc.WorkPlan
	err       error
}
type sessionMsg struct {
	session   *ipc.Session
	execution *ipc.Execution
	result    *ipc.ModelResult
	evidence  *ipc.InvestigationEvidence
	workPlan  *ipc.WorkPlan
	err       error
}
type recoveredMsg struct {
	err     error
	refresh bool
}
type pollMsg time.Time
type qualificationMsg struct {
	job      *ipc.QualificationJob
	jobs     []ipc.QualificationJob
	provider *ipc.ProviderConfiguration
	status   string
	err      error
}

func Run() error {
	_, err := tea.NewProgram(newModel(daemonTransport{})).Run()
	return err
}

func newModel(client transport) model {
	return model{client: client, serviceFactory: defaultServiceFactory, screen: screenDashboard, busy: true, width: 80, height: 24, qualificationRepetitions: 3, pending: map[int64][]ipc.Interaction{}}
}

func (m model) Init() tea.Cmd { return m.loadCmd() }

func (m model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		status, err := m.client.Request(ipc.Request{Command: "status"})
		if compatibilityMismatch(status, err) {
			return loadedMsg{upgrade: inspectDaemonUpgrade(status, m.serviceFactory)}
		}
		if err != nil {
			return loadedMsg{err: err}
		}
		projects, err := m.client.Request(ipc.Request{Command: "projects"})
		if err != nil {
			return loadedMsg{err: err}
		}
		sessions, err := m.client.Request(ipc.Request{Command: "sessions"})
		if err != nil {
			return loadedMsg{err: err}
		}
		visible := visibleSessions(sessions.Sessions)
		pending := m.pendingInteractions(visible)
		models, err := m.client.Request(ipc.Request{Command: "models"})
		if err != nil {
			return loadedMsg{projects: projects.Projects, sessions: visible, pending: pending, daemonStatus: status}
		}
		qualifications, err := m.client.Request(ipc.Request{Command: "qualification-list", Limit: 50})
		if err != nil {
			return loadedMsg{projects: projects.Projects, sessions: visible, pending: pending, daemonStatus: status, provider: models.ProviderConfiguration}
		}
		return loadedMsg{projects: projects.Projects, sessions: visible, pending: pending, daemonStatus: status, provider: models.ProviderConfiguration, qualifications: qualifications.Qualifications}
	}
}

// visibleSessions drops the model-qualification sessions. They are not
// project work and never belong to a project on the dashboard.
func visibleSessions(sessions []ipc.Session) []ipc.Session {
	visible := make([]ipc.Session, 0, len(sessions))
	for _, session := range sessions {
		if session.Kind != "qualification" {
			visible = append(visible, session)
		}
	}
	return visible
}

// pendingInteractions loads the durable interactions that still wait for the
// user. Only sessions whose durable state says they are waiting are asked
// about, so the request count stays proportional to the number of waiting
// sessions.
//
// A failure to read one session's questions does not fail the load: the
// session state alone already proves that the project needs attention.
func (m model) pendingInteractions(sessions []ipc.Session) map[int64][]ipc.Interaction {
	pending := map[int64][]ipc.Interaction{}
	for _, session := range sessions {
		if session.State != "WAITING_FOR_USER" {
			continue
		}
		response, err := m.client.Request(ipc.Request{Command: "session-questions", SessionID: session.ID})
		if err != nil {
			continue
		}
		if len(response.Interactions) > 0 {
			pending[session.ID] = response.Interactions
		}
	}
	return pending
}

// healthCmd asks the daemon for the repository readiness of every registered
// project. It is a separate step so the dashboard appears without waiting for
// the repository checks.
func (m model) healthCmd() tea.Cmd {
	return func() tea.Msg {
		response, err := m.client.Request(ipc.Request{Command: "project-health"})
		if err != nil {
			return healthMsg{err: err}
		}
		return healthMsg{health: response.ProjectHealth}
	}
}

// activityCmd refreshes the session activity shown by the dashboard.
func (m model) activityCmd() tea.Cmd {
	return func() tea.Msg {
		sessions, err := m.client.Request(ipc.Request{Command: "sessions"})
		if err != nil {
			return activityMsg{err: err}
		}
		visible := visibleSessions(sessions.Sessions)
		return activityMsg{sessions: visible, pending: m.pendingInteractions(visible)}
	}
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.screen == screenSession && m.scroll > m.maxSessionScroll() {
			m.scroll = m.maxSessionScroll()
		}
	case loadedMsg:
		m.busy = false
		if msg.upgrade != nil {
			m.screen, m.upgrade, m.upgradeAction = screenUpgrade, msg.upgrade, ""
			return m, nil
		}
		if msg.err != nil {
			m.screen = screenRecovery
			m.recovery = msg.err.Error()
			return m, nil
		}
		m.screen = screenDashboard
		m.projects, m.sessions, m.provider, m.qualifications = msg.projects, msg.sessions, msg.provider, msg.qualifications
		m.pending, m.daemonStatus = msg.pending, msg.daemonStatus
		if m.qualificationModel == "" && m.provider != nil {
			m.qualificationModel = m.provider.Model
		}
		m.rebuildEntries()
		m.clampCursors()
		m.status = ""
		if m.polling {
			return m, m.healthCmd()
		}
		m.polling = true
		return m, tea.Batch(m.healthCmd(), pollCmd())
	case healthMsg:
		if msg.err != nil {
			// Readiness is advisory. The dashboard stays usable without it.
			m.status = "Project readiness unavailable: " + msg.err.Error()
			return m, nil
		}
		m.health = msg.health
		m.rebuildEntries()
		m.clampCursors()
		return m, nil
	case activityMsg:
		if msg.err != nil {
			return m, nil
		}
		m.sessions, m.pending = msg.sessions, msg.pending
		m.rebuildEntries()
		m.clampCursors()
		return m, nil
	case registeredMsg:
		m.busy = false
		if msg.err != nil {
			m.status = "Registration failed: " + msg.err.Error()
			return m, nil
		}
		if msg.intake != nil {
			m.intake = msg.intake
			m.screen = screenIntake
			m.status = "Choose how to establish the source-control baseline"
			return m, nil
		}
		m.status = "Registered " + msg.project.Name
		m.intake = nil
		m.screen = screenDashboard
		return m, m.loadCmd()
	case startedMsg:
		m.busy = false
		if msg.err != nil {
			m.status = "Could not start work plan: " + msg.err.Error()
			return m, nil
		}
		m.selectedSession, m.execution = msg.session, msg.execution
		m.result, m.evidence, m.workPlan = nil, nil, msg.workPlan
		m.scroll = 0
		m.screen = screenSession
		return m, nil
	case sessionMsg:
		m.busy = false
		if msg.err != nil {
			m.status = "Refresh failed: " + msg.err.Error()
			return m, nil
		}
		m.selectedSession, m.execution = msg.session, msg.execution
		m.result, m.evidence, m.workPlan = msg.result, msg.evidence, msg.workPlan
	case pollMsg:
		// The polling chain is armed once and kept alive for the lifetime of
		// the frontend, so exactly one chain exists no matter which screens
		// the user visits.
		m.ticks++
		switch m.screen {
		case screenSession:
			if m.selectedSession != nil {
				return m, tea.Batch(m.sessionCmd(m.selectedSession.ID), pollCmd())
			}
		case screenModels:
			// The model screen only needs to poll while an evaluation is in
			// flight; a finished list does not change on its own.
			if hasActiveQualification(m.qualifications) {
				return m, tea.Batch(m.qualificationsCmd(), pollCmd())
			}
		case screenQualification:
			if m.selectedQualification != nil {
				return m, tea.Batch(m.qualificationCmd(m.selectedQualification.ID), pollCmd())
			}
		case screenDashboard, screenProject:
			if m.ticks%2 == 0 {
				return m, tea.Batch(m.activityCmd(), pollCmd())
			}
		}
		return m, pollCmd()
	case qualificationMsg:
		m.busy = false
		if msg.err != nil {
			m.status = "Qualification: " + msg.err.Error()
			return m, nil
		}
		if msg.provider != nil {
			m.provider = msg.provider
		}
		if msg.jobs != nil {
			m.qualifications = msg.jobs
			m.clampCursors()
		}
		if msg.job != nil {
			m.selectedQualification = msg.job
			m.replaceQualification(*msg.job)
		}
		if msg.status != "" {
			m.status = msg.status
		}
		return m, nil
	case recoveredMsg:
		m.busy = false
		if msg.refresh {
			m.busy = true
			return m, m.loadCmd()
		}
		if msg.err != nil {
			if m.upgrade != nil && (m.screen == screenUpgrade || m.screen == screenUpgradeConfirm) {
				m.screen = screenUpgrade
				m.upgrade.actionErr = msg.err
				return m, nil
			}
			m.recovery = msg.err.Error()
			return m, nil
		}
		m.recovery = "Service started; reconnecting…"
		m.busy = true
		return m, m.loadCmd()
	case tea.PasteMsg:
		if !m.busy && m.screen == screenQuestion {
			m.input += normalizePastedText(msg.Content)
		}
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func normalizePastedText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.busy {
		return m, nil
	}
	switch m.screen {
	case screenUpgrade:
		switch key {
		case "q", "esc":
			return m, tea.Quit
		case "r":
			return m.beginUpgradeAction(false)
		case "u":
			return m.beginUpgradeAction(true)
		}
	case screenUpgradeConfirm:
		switch key {
		case "q":
			return m, tea.Quit
		case "b", "esc":
			m.screen = screenUpgrade
		case "y":
			update := m.upgradeAction == "update"
			m.busy = true
			return m, upgradeServiceCmd(m.client, m.serviceFactory, update, m.upgrade.daemon.PID, true)
		}
	case screenRecovery:
		switch key {
		case "q", "esc":
			return m, tea.Quit
		case "r":
			m.busy = true
			return m, m.loadCmd()
		case "s":
			m.busy = true
			return m, startServiceCmd(m.client, m.serviceFactory)
		}
	case screenDashboard:
		switch key {
		case "q":
			return m, tea.Quit
		case "up", "k":
			m.moveProject(-1)
		case "down", "j":
			m.moveProject(1)
		case "r":
			m.busy = true
			return m, m.loadCmd()
		case "a":
			m.screen, m.input, m.status = screenRegister, "", "Enter an absolute or relative Git repository path"
		case "s":
			m.screen = screenSessions
			m.sessionsCursor = 0
			return m, nil
		case "m":
			m.screen = screenModels
			return m, m.qualificationsCmd()
		case "?":
			return m.openHelp()
		case "enter":
			if len(m.entries) == 0 {
				return m, nil
			}
			entry := m.entries[m.projectCursor]
			project := entry.Project
			m.selectedProject, m.screen, m.input = &project, screenProject, ""
			m.scroll = 0
			return m, nil
		}
	case screenProject:
		switch key {
		case "q":
			return m, tea.Quit
		case "esc", "b":
			m.screen = screenDashboard
			return m, nil
		case "r":
			m.busy = true
			return m, tea.Batch(m.loadCmd(), m.healthCmd())
		case "n":
			if m.selectedProject == nil {
				return m, nil
			}
			m.screen, m.input = screenQuestion, ""
			return m, nil
		case "?":
			return m.openHelp()
		case "up", "k":
			m.scroll--
			if m.scroll < 0 {
				m.scroll = 0
			}
		case "down", "j":
			m.scroll++
			if limit := m.maxProjectScroll(); m.scroll > limit {
				m.scroll = limit
			}
		}
	case screenSessions:
		switch key {
		case "q":
			return m, tea.Quit
		case "esc", "b":
			m.screen = screenDashboard
			return m, nil
		case "r":
			m.busy = true
			return m, m.loadCmd()
		case "up", "k":
			m.moveSession(-1)
		case "down", "j":
			m.moveSession(1)
		case "?":
			return m.openHelp()
		case "enter":
			if len(m.sessions) == 0 {
				return m, nil
			}
			session := m.sessions[m.sessionsCursor]
			m.selectedSession, m.screen, m.busy = &session, screenSession, true
			m.scroll = 0
			return m, m.sessionCmd(session.ID)
		}
	case screenHelp:
		switch key {
		case "q":
			return m, tea.Quit
		case "esc", "b", "?":
			m.screen = m.helpReturn
			return m, nil
		}
	case screenModels:
		switch key {
		case "q":
			return m, tea.Quit
		case "esc", "b":
			m.screen = screenDashboard
			return m, nil
		case "up", "k":
			m.qualificationCursor--
			m.clampCursors()
		case "down", "j":
			m.qualificationCursor++
			m.clampCursors()
		case "left", "-":
			if m.qualificationRepetitions > 1 {
				m.qualificationRepetitions--
			}
		case "right", "+", "=":
			if m.qualificationRepetitions < 20 {
				m.qualificationRepetitions++
			}
		case "m":
			m.screen, m.input = screenModelInput, m.qualificationModel
		case "s":
			m.busy = true
			return m, m.startQualificationCmd()
		case "r":
			m.busy = true
			return m, m.qualificationsCmd()
		case "enter":
			if len(m.qualifications) > 0 {
				job := m.qualifications[m.qualificationCursor]
				m.selectedQualification = &job
				m.screen = screenQualification
				return m, m.qualificationCmd(job.ID)
			}
		case "c":
			if len(m.qualifications) > 0 {
				job := m.qualifications[m.qualificationCursor]
				if job.Status == "QUEUED" || job.Status == "RUNNING" {
					m.busy = true
					return m, m.qualificationActionCmd(job.ID, "qualification-cancel")
				}
			}
		case "t":
			if len(m.qualifications) > 0 {
				job := m.qualifications[m.qualificationCursor]
				if job.Status == "INTERRUPTED" {
					m.busy = true
					return m, m.qualificationActionCmd(job.ID, "qualification-retry")
				}
			}
		}
	case screenQualification:
		switch key {
		case "q":
			return m, tea.Quit
		case "esc", "b":
			m.screen = screenModels
			m.selectedQualification = nil
			return m, pollCmd()
		case "r":
			if m.selectedQualification != nil {
				m.busy = true
				return m, m.qualificationCmd(m.selectedQualification.ID)
			}
		case "c":
			if m.selectedQualification != nil && (m.selectedQualification.Status == "QUEUED" || m.selectedQualification.Status == "RUNNING") {
				m.busy = true
				return m, m.qualificationActionCmd(m.selectedQualification.ID, "qualification-cancel")
			}
		case "t":
			if m.selectedQualification != nil && m.selectedQualification.Status == "INTERRUPTED" {
				m.busy = true
				return m, m.qualificationActionCmd(m.selectedQualification.ID, "qualification-retry")
			}
		}
	case screenModelInput:
		if key == "esc" {
			m.screen = screenModels
			m.input = ""
			return m, nil
		}
		if key == "enter" {
			if strings.TrimSpace(m.input) == "" {
				m.status = "Model identifier is required"
				return m, nil
			}
			m.qualificationModel = strings.TrimSpace(m.input)
			m.input = ""
			m.screen = screenModels
			return m, nil
		}
		m.input = editInput(m.input, msg)
	case screenQuestion:
		if key == "esc" {
			m.screen = m.returnScreen()
			m.input = ""
			return m, nil
		}
		if key == "enter" {
			if strings.TrimSpace(m.input) == "" {
				m.status = "Enter a question first"
				return m, nil
			}
			m.busy = true
			return m, m.startCmd(*m.selectedProject, m.input)
		}
		m.input = editInput(m.input, msg)
	case screenRegister:
		if key == "esc" {
			m.screen = screenDashboard
			m.input = ""
			return m, nil
		}
		if key == "enter" {
			if strings.TrimSpace(m.input) == "" {
				m.status = "Enter a repository path first"
				return m, nil
			}
			m.busy = true
			return m, m.registerCmd(m.input)
		}
		m.input = editInput(m.input, msg)
	case screenSession:
		switch key {
		case "q":
			return m, tea.Quit
		case "esc", "b":
			m.screen = screenDashboard
			m.selectedSession = nil
			return m, nil
		case "r":
			m.busy = true
			return m, m.sessionCmd(m.selectedSession.ID)
		case "up", "k":
			if m.scroll > 0 {
				m.scroll--
			}
		case "down", "j":
			if m.scroll < m.maxSessionScroll() {
				m.scroll++
			}
		case "pgup":
			m.scroll -= max(1, m.height-5)
			if m.scroll < 0 {
				m.scroll = 0
			}
		case "pgdown":
			m.scroll += max(1, m.height-5)
			if limit := m.maxSessionScroll(); m.scroll > limit {
				m.scroll = limit
			}
		}
	case screenIntake:
		switch key {
		case "esc", "b":
			m.screen, m.intake = screenDashboard, nil
		case "c":
			m.busy = true
			return m, m.resolveIntakeCmd("commit")
		case "d":
			m.screen, m.input = screenDiscardConfirm, ""
		}
	case screenDiscardConfirm:
		if key == "esc" {
			m.screen, m.input = screenIntake, ""
			return m, nil
		}
		if key == "enter" {
			if m.input != "DISCARD" {
				m.status = "Confirmation must be exactly DISCARD"
				return m, nil
			}
			m.busy = true
			return m, m.resolveIntakeCmd("discard")
		}
		m.input = editInput(m.input, msg)
	}
	return m, nil
}

func editInput(value string, msg tea.KeyPressMsg) string {
	switch msg.String() {
	case "backspace":
		if value == "" {
			return value
		}
		_, size := utf8.DecodeLastRuneInString(value)
		return value[:len(value)-size]
	case "ctrl+w":
		trimmed := strings.TrimRight(value, " \t")
		if index := strings.LastIndexAny(trimmed, " \t"); index >= 0 {
			return strings.TrimRight(trimmed[:index], " \t")
		}
		return ""
	}
	if text := msg.Key().Text; text != "" {
		return value + text
	}
	return value
}

// openHelp opens the keyboard help and remembers the screen to return to.
func (m model) openHelp() (tea.Model, tea.Cmd) {
	m.helpReturn = m.screen
	m.screen = screenHelp
	return m, nil
}

// returnScreen reports the screen a sub-view returns to.
func (m model) returnScreen() screen {
	if m.selectedProject != nil {
		return screenProject
	}
	return screenDashboard
}

// rebuildEntries regroups the projects after a state change.
func (m *model) rebuildEntries() {
	m.entries = groupProjects(m.projects, m.sessions, m.pending, m.health)
}

// moveProject moves the dashboard selection.
func (m *model) moveProject(delta int) {
	m.projectCursor += delta
	m.clampCursors()
}

// moveSession moves the session-list selection.
func (m *model) moveSession(delta int) {
	m.sessionsCursor += delta
	m.clampCursors()
}

func (m *model) clampCursors() {
	if m.projectCursor < 0 {
		m.projectCursor = 0
	}
	if m.sessionsCursor < 0 {
		m.sessionsCursor = 0
	}
	if len(m.entries) > 0 && m.projectCursor >= len(m.entries) {
		m.projectCursor = len(m.entries) - 1
	}
	if len(m.sessions) > 0 && m.sessionsCursor >= len(m.sessions) {
		m.sessionsCursor = len(m.sessions) - 1
	}
	if m.qualificationCursor < 0 {
		m.qualificationCursor = 0
	}
	if len(m.qualifications) > 0 && m.qualificationCursor >= len(m.qualifications) {
		m.qualificationCursor = len(m.qualifications) - 1
	}
}

func (m model) registerCmd(path string) tea.Cmd {
	return func() tea.Msg {
		absolute, err := filepath.Abs(strings.TrimSpace(path))
		if err != nil {
			return registeredMsg{err: err}
		}
		response, err := m.client.Request(ipc.Request{Command: "project-add", Path: absolute})
		if err != nil {
			return registeredMsg{err: err}
		}
		if response.Intake != nil {
			return registeredMsg{intake: response.Intake}
		}
		if response.Project == nil {
			return registeredMsg{err: errors.New("daemon returned no project")}
		}
		return registeredMsg{project: response.Project}
	}
}

func (m model) resolveIntakeCmd(action string) tea.Cmd {
	return func() tea.Msg {
		if m.intake == nil {
			return registeredMsg{err: errors.New("project intake is unavailable")}
		}
		response, err := m.client.Request(ipc.Request{Command: "project-resolve-intake", Path: m.intake.RootPath, Action: action, ExpectedState: m.intake.ExpectedState})
		if err != nil {
			return registeredMsg{err: err}
		}
		if response.Project == nil {
			return registeredMsg{err: errors.New("daemon returned no project")}
		}
		return registeredMsg{project: response.Project}
	}
}

func (m model) startCmd(project ipc.Project, question string) tea.Cmd {
	return func() tea.Msg {
		created, err := m.client.Request(ipc.Request{Command: "session-start", Kind: "work-plan", ProjectID: project.ID, Task: strings.TrimSpace(question)})
		if err != nil {
			return startedMsg{err: err}
		}
		if created.Session == nil {
			return startedMsg{err: errors.New("daemon returned no session")}
		}
		run, err := m.client.Request(ipc.Request{Command: "session-run", SessionID: created.Session.ID})
		if err != nil {
			return startedMsg{err: err}
		}
		return startedMsg{session: created.Session, execution: run.Execution, workPlan: created.WorkPlan}
	}
}

func (m model) sessionCmd(id int64) tea.Cmd {
	return func() tea.Msg {
		response, err := m.client.Request(ipc.Request{Command: "session-status", SessionID: id})
		if err != nil {
			return sessionMsg{err: err}
		}
		message := sessionMsg{session: response.Session, execution: response.Execution, evidence: response.Evidence, workPlan: response.WorkPlan}
		if response.Session != nil && response.Session.State == "COMPLETED" && response.Session.Kind != "work-plan" {
			result, resultErr := m.client.Request(ipc.Request{Command: "session-result", SessionID: id})
			if resultErr == nil {
				message.result, message.evidence = result.Result, result.Evidence
			}
		}
		return message
	}
}

func (m model) qualificationsCmd() tea.Cmd {
	return func() tea.Msg {
		models, err := m.client.Request(ipc.Request{Command: "models"})
		if err != nil {
			return qualificationMsg{err: err}
		}
		jobs, err := m.client.Request(ipc.Request{Command: "qualification-list", Limit: 50})
		if err != nil {
			return qualificationMsg{err: err}
		}
		return qualificationMsg{provider: models.ProviderConfiguration, jobs: jobs.Qualifications}
	}
}

func (m model) startQualificationCmd() tea.Cmd {
	return func() tea.Msg {
		response, err := m.client.Request(ipc.Request{Command: "qualification-create", Model: strings.TrimSpace(m.qualificationModel), Capability: "planning", Repetitions: m.qualificationRepetitions})
		if err != nil {
			return qualificationMsg{err: err}
		}
		if response.Qualification == nil {
			return qualificationMsg{err: errors.New("daemon returned no qualification job")}
		}
		return qualificationMsg{job: response.Qualification, status: "Qualification queued"}
	}
}

func (m model) qualificationCmd(id int64) tea.Cmd {
	return func() tea.Msg {
		response, err := m.client.Request(ipc.Request{Command: "qualification-get", QualificationID: id})
		if err != nil {
			return qualificationMsg{err: err}
		}
		return qualificationMsg{job: response.Qualification}
	}
}

func (m model) qualificationActionCmd(id int64, command string) tea.Cmd {
	return func() tea.Msg {
		response, err := m.client.Request(ipc.Request{Command: command, QualificationID: id, Reason: "cancelled from TUI"})
		if err != nil {
			return qualificationMsg{err: err}
		}
		return qualificationMsg{job: response.Qualification}
	}
}

func (m *model) replaceQualification(job ipc.QualificationJob) {
	for index := range m.qualifications {
		if m.qualifications[index].ID == job.ID {
			m.qualifications[index] = job
			return
		}
	}
	m.qualifications = append([]ipc.QualificationJob{job}, m.qualifications...)
}

func terminalQualification(status string) bool {
	return status == "COMPLETED" || status == "FAILED" || status == "CANCELLED" || status == "INTERRUPTED"
}

// hasActiveQualification reports whether any evaluation is still in flight.
func hasActiveQualification(jobs []ipc.QualificationJob) bool {
	for _, job := range jobs {
		if !terminalQualification(job.Status) {
			return true
		}
	}
	return false
}

func startServiceCmd(client transport, factory serviceFactory) tea.Cmd {
	return func() tea.Msg {
		manager, err := factory()
		if err != nil {
			return recoveredMsg{err: err}
		}
		if err = manager.Start(); err != nil {
			return recoveredMsg{err: err}
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			state, stateErr := manager.Status()
			response, requestErr := client.Request(ipc.Request{Command: "status"})
			if stateErr == nil && requestErr == nil && state.ActiveState == "active" && state.MainPID == fmt.Sprint(response.PID) {
				return recoveredMsg{}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return recoveredMsg{err: errors.New("user service did not become ready within 10 seconds; inspect `journalctl --user -u forgehand.service`")}
	}
}

func pollCmd() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return pollMsg(t) }) }

func (m model) View() tea.View {
	var body string
	switch m.screen {
	case screenRecovery:
		body = m.recoveryView()
	case screenQuestion:
		body = m.inputView("New work plan", "Repository request")
	case screenRegister:
		body = m.inputView("Register Git repository", "Path")
	case screenSession:
		body = m.sessionView()
	case screenIntake:
		body = m.intakeView()
	case screenDiscardConfirm:
		body = m.inputView("Discard repository changes", "Type DISCARD to permanently delete all uncommitted and untracked changes")
	case screenModels:
		body = m.modelsView()
	case screenQualification:
		body = m.qualificationView()
	case screenModelInput:
		body = m.inputView("Select model", "Model identifier")
	case screenUpgrade:
		body = m.upgradeView()
	case screenUpgradeConfirm:
		body = m.upgradeConfirmView()
	case screenProject:
		body = m.projectView()
	case screenSessions:
		body = m.sessionsView()
	case screenHelp:
		body = m.helpView()
	default:
		body = m.dashboardView()
	}
	// This final pass is a safety boundary for all present and future views.
	// Session content is already wrapped before pagination, so this is a no-op
	// there except for guarding newly added footer text.
	body = wrapContent(body, m.width)
	view := tea.NewView(body)
	view.AltScreen = true
	return view
}

func (m model) intakeView() string {
	var b strings.Builder
	b.WriteString("Forgehand · repository has uncommitted changes\n\n")
	if m.intake != nil {
		fmt.Fprintf(&b, "%s\n\n", m.intake.RootPath)
		for _, change := range m.intake.Changes {
			status := change.IndexStatus + change.WorkStatus
			if change.Untracked {
				status = "??"
			}
			fmt.Fprintf(&b, "  %-2s  %s\n", status, change.Path)
		}
	}
	b.WriteString("\nc commit current state  d discard all changes  esc cancel\n")
	b.WriteString("Discard requires typing DISCARD on a separate confirmation screen.\n")
	if m.busy {
		b.WriteString("\nWorking…\n")
	}
	return b.String()
}

// projectView renders the detail of the selected project: the user
// interactions that wait for an answer, the maintenance warnings, the work
// that is running, and the recent session history.
func (m model) projectBody() string {
	if m.selectedProject == nil {
		return "Forgehand · No project selected\n"
	}

	entry := m.projectEntry(*m.selectedProject)
	var b strings.Builder

	fmt.Fprintf(&b, "Forgehand · %s\n\n%s\n\n", m.selectedProject.Name, m.selectedProject.RootPath)

	b.WriteString("PENDING INPUT\n")
	if len(entry.Pending) == 0 {
		if len(entry.Waiting) > 0 {
			b.WriteString("  A session is waiting for input. Open it to read the question.\n")
		} else {
			b.WriteString("  Nothing is waiting for you.\n")
		}
	}
	for _, pending := range entry.Pending {
		kind := "answer"
		if isApprovalInteraction(pending.Interaction) {
			kind = "approval"
		}
		fmt.Fprintf(&b, "  %s · %s\n", kind, singleLine(pending.Interaction.Question))
	}

	b.WriteString("\nACTIVE WORK\n")
	if len(entry.Active) == 0 {
		b.WriteString("  No running or queued work.\n")
	}
	for _, session := range entry.Active {
		fmt.Fprintf(&b, "  %-14s %s\n", session.State, singleLine(session.Title))
	}

	b.WriteString("\nRECENT SESSIONS\n")
	if len(entry.Recent) == 0 {
		b.WriteString("  No finished sessions yet.\n")
	}
	for _, session := range entry.Recent {
		fmt.Fprintf(&b, "  %-14s %s\n", session.State, singleLine(session.Title))
	}

	b.WriteString("\nMAINTENANCE\n")
	if entry.Health == nil {
		b.WriteString("  Repository readiness has not been checked yet.\n")
	} else if !entry.healthWarning() {
		b.WriteString("  No maintenance warnings.\n")
	} else {
		fmt.Fprintf(&b, "  ! %s\n", entry.healthSummary())
	}

	return b.String()
}

// projectView renders the detail of the selected project with pagination.
func (m model) projectView() string {
	lines := strings.Split(m.projectBody(), "\n")
	visible := max(6, m.height-3)
	offset := min(m.scroll, max(0, len(lines)-visible))
	end := min(len(lines), offset+visible)
	footer := compactLine("n new request   r refresh   b/esc back   q quit", m.width)

	out := strings.Join(lines[offset:end], "\n") + "\n" + footer + "\n"
	if m.busy {
		out += "\nWorking…\n"
	} else if m.status != "" {
		out += "\n" + m.status + "\n"
	}
	return out
}

// maxProjectScroll returns the largest scroll offset of the project view.
func (m model) maxProjectScroll() int {
	lines := strings.Split(m.projectBody(), "\n")
	return max(0, len(lines)-max(6, m.height-3))
}

// projectEntry returns the dashboard entry of a project.
func (m model) projectEntry(project ipc.Project) projectEntry {
	for _, entry := range m.entries {
		if entry.Project.ID == project.ID {
			return entry
		}
	}
	return projectEntry{Project: project, Group: groupIdle}
}

// sessionsView renders the durable session history.
func (m model) sessionsView() string {
	var b strings.Builder
	b.WriteString("Forgehand · Sessions\n\n")
	if len(m.sessions) == 0 {
		b.WriteString("  No sessions yet.\n")
	}
	for index, session := range m.sessions {
		label := fmt.Sprintf("%-14s %s", session.State, singleLine(session.Title))
		if name := m.projectName(session.ProjectID); name != "" {
			label = fmt.Sprintf("%-14s %-24s %s", session.State, name, singleLine(session.Title))
		}
		b.WriteString(compactRow(index == m.sessionsCursor, label, m.width))
	}
	b.WriteString("\n↑/↓ select   enter open session   r refresh   b/esc back   q quit\n")
	if m.busy {
		b.WriteString("\nWorking…\n")
	} else if m.status != "" {
		b.WriteString("\n" + m.status + "\n")
	}
	return b.String()
}

// projectName returns the name of a project by identifier.
func (m model) projectName(projectID *int64) string {
	if projectID == nil {
		return ""
	}
	for _, entry := range m.entries {
		if entry.Project.ID == *projectID {
			return entry.Project.Name
		}
	}
	return ""
}

// helpView renders the keyboard help.
func (m model) helpView() string {
	return "Forgehand · Help\n\n" +
		"Dashboard\n" +
		"  ↑/↓ or j/k   select a project\n" +
		"  enter        open the selected project\n" +
		"  a            add a Git repository\n" +
		"  s            list all sessions\n" +
		"  m            model management\n" +
		"  r            refresh\n" +
		"  ?            this help\n" +
		"  q            quit\n\n" +
		"Project\n" +
		"  n            new work plan request\n" +
		"  r            refresh\n" +
		"  b/esc        back to the dashboard\n\n" +
		"b/esc returns to the previous screen. Quitting closes the frontend only;\n" +
		"the daemon and its work keep running.\n"
}

// singleLine collapses a text into one line so a row keeps its shape.
func singleLine(text string) string {
	replaced := strings.ReplaceAll(text, "\r\n", " ")
	replaced = strings.ReplaceAll(replaced, "\n", " ")
	replaced = strings.ReplaceAll(replaced, "\r", " ")
	return strings.TrimSpace(replaced)
}

func (m model) inputView(title, label string) string {
	project := ""
	if m.selectedProject != nil {
		project = "\nProject: " + m.selectedProject.Name
	}
	busy := ""
	if m.busy {
		busy = "\n\nWorking…"
	}
	displayedInput := strings.ReplaceAll(m.input, "\n", "\n  ")
	return fmt.Sprintf("Forgehand · %s%s\n\n%s:\n> %s█\n\nenter submit  esc cancel%s\n\n%s\n", title, project, label, displayedInput, busy, m.status)
}

func (m model) recoveryView() string {
	working := ""
	if m.busy {
		working = "\n\nWorking…"
	}
	return "Forgehand · daemon unavailable\n\n" + m.recovery + "\n\nPress s to start the installed user service.\nPress r to retry, or q to quit.\nIf it is not installed: forgehand service install" + working + "\n"
}

func (m model) modelsView() string {
	var b strings.Builder
	b.WriteString("Forgehand · Models\n\n")
	if m.provider == nil || !m.provider.Configured {
		b.WriteString("Provider: not configured\n")
	} else {
		fmt.Fprintf(&b, "Provider: %s\nEndpoint: %s\nConfigured model: %s\n", m.provider.Provider, m.provider.Endpoint, m.provider.Model)
	}
	fmt.Fprintf(&b, "\nQualification\nCapability: planning\nModel: %s\nRepetitions: %d\n", m.qualificationModel, m.qualificationRepetitions)
	b.WriteString("\nRecent evaluations\n")
	if len(m.qualifications) == 0 {
		b.WriteString("  No qualification jobs yet.\n")
	}
	for index, job := range m.qualifications {
		label := fmt.Sprintf("#%d  %-16s planning  %d/%d  %s", job.ID, job.Model, job.CompletedEvaluations, job.TotalEvaluations, job.Status)
		if job.Recommendation != "" {
			label += "  " + job.Recommendation
		}
		b.WriteString(compactRow(index == m.qualificationCursor, label, m.width))
	}
	b.WriteString("\ns start  m model  ←/→ repetitions  enter results  c cancel  t retry interrupted  r refresh  b/esc back  q quit\n")
	if m.busy {
		b.WriteString("\nWorking…\n")
	} else if m.status != "" {
		b.WriteString("\n" + m.status + "\n")
	}
	return b.String()
}

func (m model) qualificationView() string {
	if m.selectedQualification == nil {
		return "Forgehand · Qualification\n\nNo qualification selected.\n"
	}
	job := m.selectedQualification
	var b strings.Builder
	fmt.Fprintf(&b, "Forgehand · Qualification #%d\n\nModel: %s\nCapability: %s\nStatus: %s\nProgress: %d/%d\n", job.ID, job.Model, job.Capability, job.Status, job.CompletedEvaluations, job.TotalEvaluations)
	if job.Recommendation != "" {
		fmt.Fprintf(&b, "Recommendation: %s\n", job.Recommendation)
	}
	if job.FailureReason != "" {
		fmt.Fprintf(&b, "Infrastructure failure (%s): %s\n", job.FailureKind, job.FailureReason)
	}
	if job.Status == "COMPLETED" {
		a := job.Aggregate
		fmt.Fprintf(&b, "\nResults\nPassing evaluations: %d/%d (%.0f%%)\nModel failures: %d\nInfrastructure failures: %d\n", a.PassingRuns, a.ExpectedRuns, a.PassRate*100, a.ModelFailures, a.InfrastructureFailures)
		fmt.Fprintf(&b, "Requirement coverage: %d/%d\nConstraint preservation: %d/%d\nDependency correctness: %d/%d\nSupported operations: %d/%d\nEvidence specificity: %d/%d\nUnsupported-work recognition: %d/%d\n", a.RequirementCoverageRuns, a.CompletedRuns, a.ConstraintsPreservedRuns, a.CompletedRuns, a.DependenciesCorrectRuns, a.CompletedRuns, a.OperationsSupportedRuns, a.CompletedRuns, a.EvidenceRequirementsRuns, a.CompletedRuns, a.UnsupportedRecognizedRuns, a.CompletedRuns)
	}
	if len(job.Results) > 0 {
		b.WriteString("\nEvaluation details\n")
		for _, result := range job.Results {
			if result.Passed {
				continue
			}
			kind := "model"
			if result.InfrastructureFailure {
				kind = "infrastructure"
			}
			fmt.Fprintf(&b, "  %s attempt %d [%s]\n", result.Case, result.Attempt, kind)
			c := result.Criteria
			fmt.Fprintf(&b, "    schema:%t requirements:%t constraints:%t dependencies:%t operations:%t evidence:%t unsupported:%t\n", c.SchemaValid, c.RequirementCoverage, c.ConstraintsPreserved, c.DependenciesCorrect, c.OperationsSupported, c.EvidenceRequirements, c.UnsupportedRecognized)
			for _, diagnostic := range result.ValidationErrors {
				fmt.Fprintf(&b, "    %s\n", diagnostic)
			}
			for _, requirement := range result.RequirementResults {
				if !requirement.Covered {
					fmt.Fprintf(&b, "    missing requirement: %s\n", requirement.Requirement)
				}
			}
		}
	}
	if len(job.Consistency) > 0 {
		b.WriteString("\nConsistency\n")
		for _, item := range job.Consistency {
			fmt.Fprintf(&b, "  %-28s %d/%d\n", item.Case, item.PassingAttempts, item.Attempts)
		}
	}
	b.WriteString("\nr refresh  c cancel  t retry interrupted  b/esc back  q quit\n")
	return b.String()
}

func (m model) sessionView() string {
	body := m.renderedSessionBody()
	lines := strings.Split(body, "\n")
	visible := max(6, m.height-3)
	offset := m.scroll
	limit := max(0, len(lines)-visible)
	if offset > limit {
		offset = limit
	}
	end := min(len(lines), offset+visible)
	footer := compactLine("↑/↓ scroll  r refresh  b/esc back  q quit (daemon keeps working)", m.width)
	return strings.Join(lines[offset:end], "\n") + "\n" + footer + "\n"
}

func (m model) maxSessionScroll() int {
	return max(0, len(strings.Split(m.renderedSessionBody(), "\n"))-max(6, m.height-3))
}

func (m model) renderedSessionBody() string {
	return wrapContent(m.sessionBody(), m.width)
}

func (m model) sessionBody() string {
	if m.selectedSession == nil {
		return "Forgehand\n\nNo session selected.\n"
	}
	s := m.selectedSession
	var b strings.Builder
	title := "Investigation"
	if s.Kind == "work-plan" || m.workPlan != nil {
		title = "Work plan"
	}
	fmt.Fprintf(&b, "Forgehand · %s\n\n%s\nState: %s\n", title, s.Title, s.State)
	if s.Task != "" {
		fmt.Fprintf(&b, "Question: %s\n", s.Task)
	}
	if m.execution != nil {
		fmt.Fprintf(&b, "Execution: %s", m.execution.Status)
		if m.execution.TotalSteps > 0 {
			fmt.Fprintf(&b, "  progress %d/%d", m.execution.CurrentStep, m.execution.TotalSteps)
		}
		if m.execution.QueuePosition > 0 {
			fmt.Fprintf(&b, "  queue %d", m.execution.QueuePosition)
		}
		b.WriteString("\n")
		if m.execution.Status == "FAILED" && m.execution.FailureReason != "" {
			if m.execution.FailureKind != "" {
				fmt.Fprintf(&b, "Failure (%s): %s\n", m.execution.FailureKind, m.execution.FailureReason)
			} else {
				fmt.Fprintf(&b, "Failure: %s\n", m.execution.FailureReason)
			}
		}
	}
	if m.result != nil {
		b.WriteString("\nFindings\n────────\n" + m.result.Content + "\n")
	}
	if m.evidence != nil {
		fmt.Fprintf(&b, "\nEvidence · commit %s · %d/%d chars\n", m.evidence.Commit, m.evidence.UsedChars, m.evidence.BudgetChars)
		for _, file := range m.evidence.Files {
			fmt.Fprintf(&b, "  %s  %s\n", file.Path, shortHash(file.BlobHash))
			for _, location := range file.Locations {
				fmt.Fprintf(&b, "    line %d: %s\n", location.Line, location.Declaration)
			}
		}
	}
	if m.workPlan != nil {
		fmt.Fprintf(&b, "\nObjective\n─────────\n%s\nPlan: %s · read-only: %t\n", m.workPlan.Objective, m.workPlan.Status, m.workPlan.ReadOnly)
		if m.workPlan.Clarification != "" {
			fmt.Fprintf(&b, "Clarification: %s\n", m.workPlan.Clarification)
		}
		if len(m.workPlan.Constraints) > 0 {
			fmt.Fprintf(&b, "Constraints: %s\n", strings.Join(m.workPlan.Constraints, "; "))
		}
		for _, question := range m.workPlan.UnresolvedQuestions {
			fmt.Fprintf(&b, "Unresolved: %s\n", question)
		}
		for _, task := range m.workPlan.Tasks {
			marker := "[ ]"
			if task.Status == "RUNNING" {
				marker = "[>]"
			} else if task.Status == "COMPLETED" {
				marker = "[✓]"
			} else if task.Status == "BLOCKED" || task.Status == "FAILED" {
				marker = "[!]"
			}
			fmt.Fprintf(&b, "\n%s %d. %s [%s]\n", marker, task.Position, task.Objective, task.Status)
			if task.Key != "" {
				fmt.Fprintf(&b, "    key: %s\n", task.Key)
			}
			fmt.Fprintf(&b, "    method: %s · model: %t\n", task.ExecutionMethod, task.RequiresModel)
			if len(task.Dependencies) > 0 {
				fmt.Fprintf(&b, "    depends on: %s\n", strings.Join(task.Dependencies, ", "))
			}
			if task.Result != "" {
				fmt.Fprintf(&b, "    result: %s\n", task.Result)
			}
			if task.BlockedReason != "" {
				fmt.Fprintf(&b, "    blocked: %s\n", task.BlockedReason)
			}
			if task.FailureReason != "" {
				fmt.Fprintf(&b, "    failed: %s\n", task.FailureReason)
			}
			const maxDisplayedTaskEvidence = 50
			for index, ev := range task.Evidence {
				if index >= maxDisplayedTaskEvidence {
					fmt.Fprintf(&b, "      … %d more evidence item(s)\n", len(task.Evidence)-index)
					break
				}
				if ev.Path != "" {
					if ev.Line > 0 {
						fmt.Fprintf(&b, "      %s: %s:%d", ev.Kind, ev.Path, ev.Line)
						if ev.EndLine > ev.Line {
							fmt.Fprintf(&b, "-%d", ev.EndLine)
						}
						b.WriteString("\n")
					} else {
						fmt.Fprintf(&b, "      %s: %s\n", ev.Kind, ev.Path)
					}
					if ev.BlobHash != "" {
						fmt.Fprintf(&b, "        blob: %s\n", shortHash(ev.BlobHash))
					}
					if len(ev.Topics) > 0 {
						fmt.Fprintf(&b, "        topics: %s\n", strings.Join(ev.Topics, ", "))
					}
					if ev.Detail != "" {
						fmt.Fprintf(&b, "        %s\n", ev.Detail)
					}
					if ev.Excerpt != "" {
						fmt.Fprintf(&b, "        %s\n", strings.ReplaceAll(ev.Excerpt, "\n", "\n        "))
					}
				} else if ev.Detail != "" {
					fmt.Fprintf(&b, "      %s: %s\n", ev.Kind, ev.Detail)
				}
			}
		}
	}
	if m.busy {
		b.WriteString("\nRefreshing…\n")
	}
	return b.String()
}

func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
