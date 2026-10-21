package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Hisha/Forgehand/internal/daemon"
	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/service"
	forgetui "github.com/Hisha/Forgehand/internal/tui"
	"github.com/Hisha/Forgehand/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		if err := forgetui.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "forgehand: %v\n", err)
			os.Exit(1)
		}
		return
	}

	var err error

	switch os.Args[1] {
	case "version":
		fmt.Println(version.String())

	case "daemon":
		err = daemon.Run()

	case "project":
		err = projectCommand(os.Args[2:])

	case "projects":
		err = listProjects()

	case "inspect":
		err = inspectProject(os.Args[2:])

	case "query":
		err = queryCommand(os.Args[2:])

	case "investigate":
		err = investigateCommand(os.Args[2:])

	case "status":
		err = status()

	case "service":
		err = serviceCommand(os.Args[2:])

	case "tui":
		err = forgetui.Run()

	case "session":
		err = sessionCommand(os.Args[2:])

	case "task":
		err = taskCommand(os.Args[2:])

	case "model":
		err = modelCommand(os.Args[2:])

	case "sessions":
		err = listSessions()

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: %v\n", err)
		os.Exit(1)
	}
}

func status() error {
	response, socketPath, err := requestRaw(ipc.Request{
		Command: "status",
	})
	if err != nil {
		return fmt.Errorf("daemon stopped or unavailable at %s: %w; run `forgehand service start` if the user service is installed", socketPath, err)
	}

	compatible := response.ProtocolVersion == ipc.ProtocolVersion
	fmt.Printf("Daemon: %s\n", map[bool]string{true: "running", false: "running but incompatible"}[compatible])
	fmt.Printf("CLI: %s\n", version.BuildIdentity())
	fmt.Printf("Daemon version: %s\n", response.Version)
	fmt.Printf("Daemon build: %s\n", response.BuildIdentity)
	fmt.Printf("PID: %d\n", response.PID)
	fmt.Printf("Executable: %s\n", response.Executable)
	if response.SocketPath == "" {
		response.SocketPath = socketPath
	}
	fmt.Printf("Socket: %s\n", response.SocketPath)
	if response.StateDir != "" {
		fmt.Printf("State directory: %s\n", response.StateDir)
	}
	fmt.Printf("IPC protocol: daemon %d, CLI %d\n", response.ProtocolVersion, ipc.ProtocolVersion)
	if !compatible {
		return fmt.Errorf("incompatible daemon: use `forgehand service update` to run this executable")
	}
	if !response.OK {
		return fmt.Errorf("daemon error: %s", response.Message)
	}
	fmt.Printf("Worker capacity: %d\n", response.WorkerCapacity)
	fmt.Printf("Active workers: %d\n", response.ActiveWorkers)
	fmt.Printf("Queued executions: %d\n", response.QueuedExecutions)
	fmt.Printf("Resource-blocked executions: %d\n", response.ResourceBlockedExecutions)

	return nil
}

func serviceCommand(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: forgehand service <install|update|uninstall|start|stop|restart|status>")
	}
	manager, err := service.NewManager()
	if err != nil {
		return err
	}
	switch args[0] {
	case "install":
		if err := manager.Install(); err != nil {
			return err
		}
		fmt.Printf("Installed and enabled user service: %s\n", manager.UnitPath())
		fmt.Println("The daemon was not started; run `forgehand service start`.")
	case "update":
		if err := manager.Update(); err != nil {
			return err
		}
		fmt.Printf("Updated user service to %s\n", executablePath())
		fmt.Println("The daemon was not restarted; run `forgehand service restart` after checking active work, or use the TUI upgrade flow.")
	case "uninstall":
		if err := manager.Uninstall(); err != nil {
			return err
		}
		fmt.Println("Uninstalled user service; Forgehand state and registered projects were preserved.")
	case "start":
		if err := manager.Start(); err != nil {
			return err
		}
		return waitForManagedDaemon(manager)
	case "stop":
		if err := manager.Stop(); err != nil {
			return err
		}
		fmt.Println("Forgehand user service stopped.")
	case "restart":
		if err := manager.Restart(); err != nil {
			return err
		}
		return waitForManagedDaemon(manager)
	case "status":
		state, err := manager.Status()
		if err != nil {
			return err
		}
		fmt.Printf("User unit: %s\n", state.UnitPath)
		fmt.Printf("Installed: %t\n", state.Installed)
		if !state.Installed {
			fmt.Println("Recovery: run `forgehand service install`, then `forgehand service start`.")
			return nil
		}
		fmt.Printf("systemd: %s (%s), main PID %s\n", state.ActiveState, state.SubState, state.MainPID)
		fmt.Printf("Service executable: %s\n", state.EffectiveExecutable)
		fmt.Printf("Current executable: %s\n", state.CurrentExecutable)
		fmt.Printf("Executable matches: %t\n", state.ExecutableMatches)
		if state.NeedsUpdate {
			fmt.Printf("Service update required: %s\n", state.UpdateReason)
		}
		return status()
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
	return nil
}

func waitForManagedDaemon(manager *service.Manager) error {
	deadline := time.Now().Add(10 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		state, err := manager.Status()
		if err != nil {
			last = err.Error()
		} else if state.ActiveState == "active" && state.MainPID != "" && state.MainPID != "0" {
			response, _, requestErr := requestRaw(ipc.Request{Command: "status"})
			if requestErr == nil && response.OK && response.ProtocolVersion == ipc.ProtocolVersion && strconv.Itoa(response.PID) == state.MainPID {
				return status()
			}
			if requestErr != nil {
				last = requestErr.Error()
			} else {
				last = fmt.Sprintf("systemd PID %s, socket PID %d", state.MainPID, response.PID)
			}
		} else {
			last = fmt.Sprintf("systemd state %s/%s", state.ActiveState, state.SubState)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("user service did not become ready within 10s: %s; inspect `journalctl --user -u %s`", last, service.UnitName)
}

func executablePath() string {
	path, err := os.Executable()
	if err != nil {
		return "the current executable"
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return path
}

func sessionCommand(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: forgehand session start <title>")
	}

	switch args[0] {
	case "start":
		if len(args) < 2 {
			return fmt.Errorf("usage: forgehand session start <title> or forgehand session start --project <id> --task \"...\"")
		}
		// Model-backed sessions use --project/--task/--model. --investigate
		// selects the read-only evidence-backed investigation worker.
		if args[1][0] != '-' && len(args) == 2 {
			return startSession(args[1])
		}
		var title, task, model, component string
		var projectID int64
		var analysisID int64
		investigation := false
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--title":
				if i+1 >= len(args) {
					return fmt.Errorf("missing value for --title")
				}
				title = args[i+1]
				i++
			case "--project":
				if i+1 >= len(args) {
					return fmt.Errorf("missing value for --project")
				}
				id, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil || id <= 0 {
					return fmt.Errorf("invalid project ID: %s", args[i+1])
				}
				projectID = id
				i++
			case "--task":
				if i+1 >= len(args) {
					return fmt.Errorf("missing value for --task")
				}
				task = args[i+1]
				i++
			case "--model":
				if i+1 >= len(args) {
					return fmt.Errorf("missing value for --model")
				}
				model = args[i+1]
				i++
			case "--investigate":
				investigation = true
			case "--component":
				if i+1 >= len(args) {
					return fmt.Errorf("missing value for --component")
				}
				component = args[i+1]
				i++
			case "--analysis":
				if i+1 >= len(args) {
					return fmt.Errorf("missing value for --analysis")
				}
				id, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil || id <= 0 {
					return fmt.Errorf("invalid analysis ID: %s", args[i+1])
				}
				analysisID = id
				i++
			default:
				if i == 1 && args[i][0] != '-' {
					title = args[i]
					continue
				}
				return fmt.Errorf("unknown flag: %s", args[i])
			}
		}
		if investigation {
			return startInvestigationSession(title, task, model, component, projectID, analysisID)
		}
		return startCodingSession(title, task, model, projectID)

	case "run":
		var sessionID int64
		var resources []string
		for i := 1; i < len(args); i++ {
			switch {
			case args[i] == "--resource":
				if i+1 >= len(args) {
					return fmt.Errorf("missing value for --resource")
				}
				resources = append(resources, args[i+1])
				i++
			default:
				if sessionID == 0 && !strings.HasPrefix(args[i], "-") {
					id, err := strconv.ParseInt(args[i], 10, 64)
					if err != nil || id <= 0 {
						return fmt.Errorf("invalid session ID: %s", args[i])
					}
					sessionID = id
					continue
				}
				return fmt.Errorf("unknown flag: %s", args[i])
			}
		}
		if sessionID == 0 {
			return fmt.Errorf("usage: forgehand session run <id> [--resource <name>]...")
		}

		return runSession(sessionID, resources)

	case "resume":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand session resume <id>")
		}

		sessionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || sessionID <= 0 {
			return fmt.Errorf("invalid session ID: %s", args[1])
		}

		return resumeSession(sessionID)

	case "questions":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand session questions <id>")
		}

		sessionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || sessionID <= 0 {
			return fmt.Errorf("invalid session ID: %s", args[1])
		}

		return sessionQuestions(sessionID)

	case "answer":
		if len(args) != 3 {
			return fmt.Errorf("usage: forgehand session answer <interaction-id> \"answer text\"")
		}

		interactionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || interactionID <= 0 {
			return fmt.Errorf("invalid interaction ID: %s", args[1])
		}

		return answerInteraction(interactionID, args[2])

	case "status":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand session status <id>")
		}

		sessionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || sessionID <= 0 {
			return fmt.Errorf("invalid session ID: %s", args[1])
		}

		return sessionStatus(sessionID)

	case "result":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand session result <id>")
		}
		sessionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || sessionID <= 0 {
			return fmt.Errorf("invalid session ID: %s", args[1])
		}
		return sessionResult(sessionID)

	case "cancel":
		if len(args) < 2 {
			return fmt.Errorf("usage: forgehand session cancel <id> [--reason \"...\"]")
		}

		sessionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || sessionID <= 0 {
			return fmt.Errorf("invalid session ID: %s", args[1])
		}

		var reason string
		for i := 2; i < len(args); i++ {
			if args[i] != "--reason" {
				return fmt.Errorf("unknown flag: %s", args[i])
			}
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for --reason")
			}
			reason = args[i+1]
			i++
		}

		return cancelSession(sessionID, reason)

	case "events":
		if len(args) < 2 {
			return fmt.Errorf("usage: forgehand session events <id> [--after <event-id>] [--limit <number>]")
		}

		sessionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || sessionID <= 0 {
			return fmt.Errorf("invalid session ID: %s", args[1])
		}

		var afterEventID int64
		limit := 0
		for i := 2; i < len(args); i++ {
			if args[i] == "--after" {
				if i+1 >= len(args) {
					return fmt.Errorf("missing value for --after")
				}
				afterEventID, err = strconv.ParseInt(args[i+1], 10, 64)
				if err != nil || afterEventID < 0 {
					return fmt.Errorf("invalid event ID: %s", args[i+1])
				}
				i++
				continue
			}
			if args[i] == "--limit" {
				if i+1 >= len(args) {
					return fmt.Errorf("missing value for --limit")
				}
				limitVal, err := strconv.Atoi(args[i+1])
				if err != nil || limitVal <= 0 {
					return fmt.Errorf("invalid limit: %s", args[i+1])
				}
				limit = limitVal
				i++
				continue
			}
			return fmt.Errorf("unknown flag: %s", args[i])
		}

		return sessionEvents(sessionID, afterEventID, limit)

	default:
		return fmt.Errorf("unknown session command: %s", args[0])
	}
}

func taskCommand(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: forgehand task start --project <id> --task \"...\" [--title <title>] [--model <name>]")
	}

	switch args[0] {
	case "start":
		var title, task, model string
		var projectID int64
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--project":
				if i+1 >= len(args) {
					return fmt.Errorf("missing value for --project")
				}
				id, err := strconv.ParseInt(args[i+1], 10, 64)
				if err != nil || id <= 0 {
					return fmt.Errorf("invalid project ID: %s", args[i+1])
				}
				projectID = id
				i++
			case "--task":
				if i+1 >= len(args) {
					return fmt.Errorf("missing value for --task")
				}
				task = args[i+1]
				i++
			case "--title":
				if i+1 >= len(args) {
					return fmt.Errorf("missing value for --title")
				}
				title = args[i+1]
				i++
			case "--model":
				if i+1 >= len(args) {
					return fmt.Errorf("missing value for --model")
				}
				model = args[i+1]
				i++
			default:
				return fmt.Errorf("unknown flag: %s", args[i])
			}
		}
		if projectID <= 0 || strings.TrimSpace(task) == "" {
			return fmt.Errorf("project and task are required for a task session")
		}
		return startTaskSession(title, task, model, projectID)
	case "status":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand task status <session-id>")
		}
		sessionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || sessionID <= 0 {
			return fmt.Errorf("invalid session ID: %s", args[1])
		}
		return taskStatus(sessionID)
	case "cleanup":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand task cleanup <session-id>")
		}
		sessionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || sessionID <= 0 {
			return fmt.Errorf("invalid session ID: %s", args[1])
		}
		return taskCleanup(sessionID)
	case "diff":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand task diff <session-id>")
		}
		sessionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || sessionID <= 0 {
			return fmt.Errorf("invalid session ID: %s", args[1])
		}
		return taskDiff(sessionID)
	case "approve":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand task approve <session-id>")
		}
		sessionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || sessionID <= 0 {
			return fmt.Errorf("invalid session ID: %s", args[1])
		}
		return taskApprove(sessionID)
	case "reject":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand task reject <session-id>")
		}
		sessionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || sessionID <= 0 {
			return fmt.Errorf("invalid session ID: %s", args[1])
		}
		return taskReject(sessionID)
	default:
		return fmt.Errorf("unknown task command: %s", args[0])
	}
}

func startTaskSession(title, task, model string, projectID int64) error {
	response, err := request(ipc.Request{
		Command:   "task-start",
		Title:     title,
		Task:      task,
		Model:     model,
		ProjectID: projectID,
	})
	if err != nil {
		return err
	}
	if response.Session == nil {
		return fmt.Errorf("daemon returned no session")
	}
	if response.Execution == nil {
		return fmt.Errorf("daemon returned no execution")
	}

	fmt.Printf("Created task session %d\n", response.Session.ID)
	fmt.Printf("State: %s\n", response.Session.State)
	if response.Session.Title != "" {
		fmt.Printf("Title: %s\n", response.Session.Title)
	}
	fmt.Printf("Project: %d\n", response.Session.ProjectID)
	fmt.Printf("Task: %s\n", response.Session.Task)
	fmt.Printf("Execution: %d (%s)\n", response.Execution.ID, response.Execution.Status)
	return nil
}

func taskStatus(sessionID int64) error {
	response, err := request(ipc.Request{
		Command:   "task-status",
		SessionID: sessionID,
	})
	if err != nil {
		return err
	}
	if response.TaskRun == nil {
		return fmt.Errorf("daemon returned no task run")
	}

	run := response.TaskRun
	fmt.Printf("Session %d task run %d (exec %d)\n", run.SessionID, run.ID, run.ExecutionID)
	fmt.Printf("Phase: %s\n", run.Phase)
	switch run.Phase {
	case "PROPOSED":
		fmt.Println("Status: awaiting approval — run `task diff` to review, then `task approve` or `task reject`")
	case "APPLIED":
		fmt.Println("Status: approved changes applied")
	case "REJECTED":
		fmt.Println("Status: proposal rejected; no changes were applied")
	case "FAILED":
		fmt.Println("Status: task failed")
	}
	if run.BaseCommit != "" {
		fmt.Printf("Base commit: %s\n", run.BaseCommit)
	}
	if run.WorkspacePath != "" {
		fmt.Printf("Workspace: %s\n", run.WorkspacePath)
	}
	if run.ProposalHash != "" {
		fmt.Printf("Proposal hash: %s\n", run.ProposalHash)
	}
	if run.Summary != "" {
		fmt.Printf("Summary: %s\n", run.Summary)
	}
	if run.Error != "" {
		fmt.Printf("Error: %s\n", run.Error)
	}
	printValidationStatus(run)
	if run.CleanedAt != nil {
		fmt.Printf("Cleaned at: %s\n", *run.CleanedAt)
	}
	fmt.Printf("Created: %s\n", run.CreatedAt)
	fmt.Printf("Updated: %s\n", run.UpdatedAt)
	if len(run.Changes) > 0 {
		fmt.Println("Proposed changes:")
		for _, ch := range run.Changes {
			fmt.Printf("  %s %s\n", ch.Op, ch.Path)
		}
	}
	if len(response.TaskOperations) > 0 {
		fmt.Printf("Operations (%d):\n", len(response.TaskOperations))
		for _, op := range response.TaskOperations {
			fmt.Printf("  %d. %s %s %s (%s)\n", op.Sequence, op.Kind, op.Path, op.Detail, op.Outcome)
			if op.Error != "" {
				fmt.Printf("     err: %s\n", op.Error)
			}
		}
	}
	return nil
}

// printValidationStatus renders the post-apply validation outcome, including
// each recorded check, so a failure is visible without digging through logs.
func printValidationStatus(run *ipc.TaskRun) {
	switch run.ValidationStatus {
	case "":
		return
	case "skipped":
		fmt.Println("Validation: skipped (not enabled or nothing to validate)")
		return
	case "passed":
		fmt.Println("Validation: passed")
	case "failed":
		fmt.Println("Validation: FAILED — the changes were applied but did not pass gofmt/go test")
	case "error":
		fmt.Println("Validation: ERROR — the validation stage could not run")
	}
	if len(run.Validation) == 0 {
		return
	}
	var result struct {
		Checks []struct {
			Name      string   `json:"name"`
			Command   string   `json:"command"`
			ExitCode  int      `json:"exit_code"`
			Duration  string   `json:"duration"`
			Stdout    string   `json:"stdout"`
			Stderr    string   `json:"stderr"`
			Error     string   `json:"error"`
			Formatted []string `json:"formatted"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(run.Validation, &result); err != nil {
		return
	}
	for _, check := range result.Checks {
		outcome := "ok"
		if check.ExitCode != 0 {
			outcome = fmt.Sprintf("exit %d", check.ExitCode)
		}
		fmt.Printf("  %s (%s) %s in %s\n", check.Name, check.Command, outcome, check.Duration)
		for _, path := range check.Formatted {
			fmt.Printf("    formatted: %s\n", path)
		}
		if check.Error != "" {
			fmt.Printf("    err: %s\n", check.Error)
		}
		if check.Stderr != "" {
			for _, line := range strings.SplitN(strings.TrimSpace(check.Stderr), "\n", 15) {
				fmt.Printf("    | %s\n", line)
			}
		}
	}
}

func taskCleanup(sessionID int64) error {
	response, err := request(ipc.Request{
		Command:   "task-cleanup",
		SessionID: sessionID,
	})
	if err != nil {
		return err
	}
	if response.OK {
		if response.Message != "" {
			fmt.Println(response.Message)
			return nil
		}
		fmt.Println("Task workspace cleaned")
		return nil
	}
	if response.Message != "" {
		return fmt.Errorf("%s", response.Message)
	}
	return fmt.Errorf("task cleanup failed")
}

func startSession(title string) error {
	response, err := request(ipc.Request{
		Command: "session-start",
		Title:   title,
	})
	if err != nil {
		return err
	}

	if response.Session == nil {
		return fmt.Errorf("daemon returned no session")
	}

	fmt.Printf("Created session %d\n", response.Session.ID)
	fmt.Printf("State: %s\n", response.Session.State)
	fmt.Printf("Title: %s\n", response.Session.Title)

	return nil
}

func runSession(sessionID int64, resources []string) error {
	response, err := request(ipc.Request{
		Command:   "session-run",
		SessionID: sessionID,
		Resources: resources,
	})
	if err != nil {
		return err
	}

	if response.Execution == nil {
		return fmt.Errorf("daemon returned no execution")
	}

	fmt.Printf("Queued session %d\n", response.Execution.SessionID)
	fmt.Printf("Execution: %d\n", response.Execution.ID)
	fmt.Printf("Status: %s\n", response.Execution.Status)
	fmt.Printf(
		"Progress: %d/%d\n",
		response.Execution.CurrentStep,
		response.Execution.TotalSteps,
	)
	if response.Execution.QueuePosition > 0 {
		fmt.Printf("Queue position: %d\n", response.Execution.QueuePosition)
	}
	printExecutionResources(response.Execution)

	return nil
}

// printExecutionResources reports an execution's exclusive resource
// requirements and, for a blocked queued execution, which running execution
// holds each resource.
func printExecutionResources(execution *ipc.Execution) {
	if len(execution.Resources) > 0 {
		fmt.Printf("Resources: %s\n", strings.Join(execution.Resources, ", "))
	}
	for _, block := range execution.BlockedBy {
		fmt.Printf(
			"Blocked by resource %s (execution %d)\n",
			block.Resource,
			block.HeldByExecutionID,
		)
	}
}

func resumeSession(sessionID int64) error {
	response, err := request(ipc.Request{
		Command:   "session-resume",
		SessionID: sessionID,
	})
	if err != nil {
		return err
	}

	if response.Execution == nil {
		return fmt.Errorf("daemon returned no execution")
	}

	fmt.Printf("Queued resume for session %d\n", response.Execution.SessionID)
	fmt.Printf("Execution: %d\n", response.Execution.ID)
	fmt.Printf("Status: %s\n", response.Execution.Status)
	fmt.Printf(
		"Progress: %d/%d\n",
		response.Execution.CurrentStep,
		response.Execution.TotalSteps,
	)
	if response.Execution.QueuePosition > 0 {
		fmt.Printf("Queue position: %d\n", response.Execution.QueuePosition)
	}
	printExecutionResources(response.Execution)

	return nil
}

func sessionQuestions(sessionID int64) error {
	response, err := request(ipc.Request{
		Command:   "session-questions",
		SessionID: sessionID,
	})
	if err != nil {
		return err
	}

	if len(response.Interactions) == 0 {
		fmt.Println("No pending questions.")
		return nil
	}

	for _, interaction := range response.Interactions {
		fmt.Printf("Interaction %d\n", interaction.ID)
		fmt.Printf("Session: %d\n", interaction.SessionID)
		fmt.Printf("Execution: %d\n", interaction.ExecutionID)
		fmt.Printf("Status: %s\n", interaction.Status)
		fmt.Printf("Created: %s\n", interaction.CreatedAt)
		fmt.Printf("Question: %s\n", interaction.Question)
	}

	return nil
}

func answerInteraction(interactionID int64, answer string) error {
	response, err := request(ipc.Request{
		Command:       "interaction-answer",
		InteractionID: interactionID,
		Answer:        answer,
	})
	if err != nil {
		return err
	}

	if response.Interaction == nil {
		return fmt.Errorf("daemon returned no interaction")
	}

	if !response.AnswerApplied {
		fmt.Printf(
			"Interaction %d was already answered\n",
			response.Interaction.ID,
		)
		return nil
	}

	fmt.Printf("Answered interaction %d\n", response.Interaction.ID)
	fmt.Printf("Status: %s\n", response.Interaction.Status)

	return nil
}

func sessionStatus(sessionID int64) error {
	response, err := request(ipc.Request{
		Command:   "session-status",
		SessionID: sessionID,
	})
	if err != nil {
		return err
	}

	if response.Session == nil {
		return fmt.Errorf("daemon returned no session")
	}

	fmt.Printf("Session %d\n", response.Session.ID)
	fmt.Printf("Title: %s\n", response.Session.Title)
	fmt.Printf("State: %s\n", response.Session.State)

	if response.Session.ProjectID != nil {
		fmt.Printf("Project: %d\n", *response.Session.ProjectID)
	}
	if response.Session.Kind != "" {
		fmt.Printf("Kind: %s\n", response.Session.Kind)
	}
	if response.Session.Task != "" {
		fmt.Printf("Task: %s\n", response.Session.Task)
	}
	if response.Session.Provider != "" {
		fmt.Printf("Provider: %s\n", response.Session.Provider)
	}
	if response.Session.Model != "" {
		fmt.Printf("Model: %s\n", response.Session.Model)
	}

	if response.Execution == nil {
		fmt.Println("Execution: none")
		return nil
	}

	fmt.Printf("Execution: %d\n", response.Execution.ID)
	fmt.Printf("Status: %s\n", response.Execution.Status)
	fmt.Printf(
		"Progress: %d/%d\n",
		response.Execution.CurrentStep,
		response.Execution.TotalSteps,
	)
	if response.Execution.QueuePosition > 0 {
		fmt.Printf("Queue position: %d\n", response.Execution.QueuePosition)
	}
	printExecutionResources(response.Execution)

	if response.Execution.CanceledAt != nil {
		fmt.Printf("Canceled: %s\n", *response.Execution.CanceledAt)
	}
	if response.Execution.CancelReason != nil {
		fmt.Printf("Cancel reason: %s\n", *response.Execution.CancelReason)
	}
	if response.Execution.FailureReason != "" {
		if response.Execution.FailureKind != "" {
			fmt.Printf("Failure kind: %s\n", response.Execution.FailureKind)
		}
		fmt.Printf("Failure reason: %s\n", response.Execution.FailureReason)
	}

	if response.Interaction != nil {
		fmt.Printf("Awaiting input: interaction %d\n", response.Interaction.ID)
		fmt.Printf("Question: %s\n", response.Interaction.Question)
	}
	printInvestigationEvidence(response.Evidence)

	return nil
}

func cancelSession(sessionID int64, reason string) error {
	response, err := request(ipc.Request{
		Command:   "session-cancel",
		SessionID: sessionID,
		Reason:    reason,
	})
	if err != nil {
		return err
	}

	if response.Session == nil {
		return fmt.Errorf("daemon returned no session")
	}

	if response.CancelOutcome == "already_canceled" {
		fmt.Printf("Session %d was already canceled\n", response.Session.ID)
		return nil
	}

	fmt.Printf("Canceled session %d\n", response.Session.ID)
	fmt.Printf("State: %s\n", response.Session.State)

	if response.Execution != nil {
		fmt.Printf("Execution: %d\n", response.Execution.ID)
		fmt.Printf("Status: %s\n", response.Execution.Status)
		fmt.Printf(
			"Progress: %d/%d\n",
			response.Execution.CurrentStep,
			response.Execution.TotalSteps,
		)
		if response.Execution.CancelReason != nil {
			fmt.Printf("Reason: %s\n", *response.Execution.CancelReason)
		}
	}

	return nil
}

func listSessions() error {
	response, err := request(ipc.Request{
		Command: "sessions",
	})
	if err != nil {
		return err
	}

	if len(response.Sessions) == 0 {
		fmt.Println("No sessions.")
		return nil
	}

	writer := tabwriter.NewWriter(
		os.Stdout,
		0,
		4,
		2,
		' ',
		0,
	)

	fmt.Fprintln(writer, "ID\tSTATE\tTITLE")

	for _, session := range response.Sessions {
		fmt.Fprintf(
			writer,
			"%d\t%s\t%s\n",
			session.ID,
			session.State,
			session.Title,
		)
	}

	return writer.Flush()
}

func request(req ipc.Request) (ipc.Response, error) {
	response, _, err := requestRaw(req)
	if err != nil {
		return ipc.Response{}, err
	}
	if response.ProtocolVersion != ipc.ProtocolVersion {
		return ipc.Response{}, fmt.Errorf("incompatible daemon IPC protocol %d (CLI requires %d); run `forgehand service update` or stop the manually managed daemon", response.ProtocolVersion, ipc.ProtocolVersion)
	}
	if !response.OK {
		return ipc.Response{}, fmt.Errorf("daemon error: %s", response.Message)
	}
	return response, nil
}

func requestRaw(req ipc.Request) (ipc.Response, string, error) {
	socketPath, err := daemon.SocketPath()
	if err != nil {
		return ipc.Response{}, "(unresolved)", err
	}

	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return ipc.Response{}, socketPath, err
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return ipc.Response{}, socketPath, fmt.Errorf("send request: %w", err)
	}

	var response ipc.Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return ipc.Response{}, socketPath, fmt.Errorf("read response: %w", err)
	}
	return response, socketPath, nil
}

func projectCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: forgehand project add <path>")
	}

	switch args[0] {
	case "add":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand project add <path>")
		}

		return addProject(args[1])

	case "observe":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand project observe <id>")
		}

		projectID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || projectID <= 0 {
			return fmt.Errorf("invalid project ID: %s", args[1])
		}

		return observeProject(projectID)

	case "discover":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand project discover <id>")
		}

		projectID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || projectID <= 0 {
			return fmt.Errorf("invalid project ID: %s", args[1])
		}

		return discoverProject(projectID)

	case "inventory":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand project inventory <id>")
		}

		projectID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || projectID <= 0 {
			return fmt.Errorf("invalid project ID: %s", args[1])
		}

		return inventoryProject(projectID)

	case "analyze":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand project analyze <id>")
		}

		projectID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || projectID <= 0 {
			return fmt.Errorf("invalid project ID: %s", args[1])
		}

		return analyzeProject(projectID)

	default:
		return fmt.Errorf("unknown project command: %s", args[0])
	}
}

func addProject(path string) error {
	response, err := request(ipc.Request{
		Command: "project-add",
		Path:    path,
	})
	if err != nil {
		return err
	}

	if response.Intake != nil {
		fmt.Println("Forgehand found uncommitted changes:")
		fmt.Println()

		for _, change := range response.Intake.Changes {
			status, path := formatWorkingTreeChange(change)
			fmt.Printf("%s  %s\n", status, path)
		}

		fmt.Println()
		fmt.Println("Choose how to establish the Forgehand source-control baseline:")
		fmt.Println("  1. Commit the current state")
		fmt.Println("  2. Discard ALL uncommitted changes and untracked files")
		fmt.Println("  3. Cancel without changing the repository")
		fmt.Print("Choice [1/2/3]: ")

		reader := bufio.NewReader(os.Stdin)
		choice, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read project intake choice: %w", err)
		}
		choice = strings.TrimSpace(choice)

		action := ""
		switch choice {
		case "1":
			action = "commit"
		case "2":
			fmt.Println("WARNING: This permanently deletes all uncommitted changes and untracked files.")
			fmt.Print("Type DISCARD to confirm: ")
			confirmation, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("read discard confirmation: %w", err)
			}
			if strings.TrimSpace(confirmation) != "DISCARD" {
				fmt.Println("Cancelled; the repository was not changed.")
				return nil
			}
			action = "discard"
		case "3":
			fmt.Println("Cancelled; the repository was not changed.")
			return nil
		default:
			return fmt.Errorf("invalid choice; project was not changed")
		}

		response, err = request(ipc.Request{
			Command:       "project-resolve-intake",
			Path:          response.Intake.RootPath,
			Action:        action,
			ExpectedState: response.Intake.ExpectedState,
		})
		if err != nil {
			return err
		}
	}

	if response.Project == nil {
		return fmt.Errorf("daemon returned neither project nor intake result")
	}

	fmt.Printf("Added project %d\n", response.Project.ID)
	fmt.Printf("Name: %s\n", response.Project.Name)
	fmt.Printf("Root: %s\n", response.Project.RootPath)

	return nil
}

func formatWorkingTreeChange(change ipc.WorkingTreeChange) (string, string) {
	status := strings.ReplaceAll(change.IndexStatus+change.WorkStatus, ".", " ")
	if change.Untracked {
		status = "??"
	}

	path := change.Path
	if change.OriginalPath != "" {
		path = change.OriginalPath + " -> " + change.Path
	}

	return status, path
}

func observeProject(projectID int64) error {
	response, err := request(ipc.Request{
		Command:   "project-observe",
		ProjectID: projectID,
	})
	if err != nil {
		return err
	}

	if response.Snapshot == nil {
		return fmt.Errorf("daemon returned no repository snapshot")
	}

	snapshot := response.Snapshot

	fmt.Printf("Observed project %d\n", snapshot.ProjectID)
	fmt.Printf("Snapshot: %d\n", snapshot.ID)

	if snapshot.HeadCommit == "" {
		fmt.Println("HEAD: none")
	} else {
		fmt.Printf("HEAD: %s\n", snapshot.HeadCommit)
	}

	switch {
	case snapshot.Detached:
		fmt.Println("Branch: detached HEAD")
	case snapshot.Branch == "":
		fmt.Println("Branch: unborn")
	default:
		fmt.Printf("Branch: %s\n", snapshot.Branch)
	}

	if snapshot.Dirty {
		fmt.Println("Dirty: yes")
	} else {
		fmt.Println("Dirty: no")
	}

	fmt.Printf("Tracked files: %d\n", snapshot.TrackedFiles)
	fmt.Printf("Untracked files: %d\n", snapshot.UntrackedFiles)

	return nil
}

func discoverProject(projectID int64) error {
	response, err := request(ipc.Request{
		Command:   "project-discover",
		ProjectID: projectID,
	})
	if err != nil {
		return err
	}
	if response.Discovery == nil {
		return fmt.Errorf("daemon returned no project discovery")
	}
	return formatProjectDiscovery(os.Stdout, *response.Discovery)
}

func formatProjectDiscovery(writer io.Writer, result ipc.ProjectDiscovery) error {
	var output strings.Builder
	fmt.Fprintf(&output, "Discovered project %d\n", result.Project.ID)
	fmt.Fprintf(&output, "Observation: %d\n", result.ObservationID)
	fmt.Fprintf(&output, "Name: %s\n", result.Project.Name)
	fmt.Fprintf(&output, "Root: %s\n", result.Project.RootPath)
	fmt.Fprintf(&output, "Commit: %s\n", result.CommitHash)
	fmt.Fprintf(&output, "Tracked files: %d\n", result.TrackedFiles)
	fmt.Fprintf(&output, "Unclassified files: %d\n", result.UnclassifiedFiles)

	fmt.Fprintln(&output, "Languages:")
	if len(result.Languages) == 0 {
		fmt.Fprintln(&output, "  none")
	} else {
		for _, language := range result.Languages {
			fmt.Fprintf(&output, "  %s: %d\n", language.Language, language.Count)
		}
	}

	fmt.Fprintln(&output, "Build-system indicators:")
	if len(result.BuildSystems) == 0 {
		fmt.Fprintln(&output, "  none")
	} else {
		for _, indicator := range result.BuildSystems {
			fmt.Fprintf(&output, "  %s: %s\n", indicator.Name, indicator.Evidence.Path)
		}
	}
	fmt.Fprintln(&output, "Build indicators report committed files only; no builds or tests were run.")
	_, err := io.WriteString(writer, output.String())
	return err
}

func inventoryProject(projectID int64) error {
	response, err := request(ipc.Request{
		Command:   "project-inventory",
		ProjectID: projectID,
	})
	if err != nil {
		return err
	}
	if response.Inventory == nil {
		return fmt.Errorf("daemon returned no project inventory")
	}

	fmt.Printf("Inventoried project %d\n", response.Inventory.ProjectID)
	fmt.Printf("Inventory: %d\n", response.Inventory.ID)
	fmt.Printf("Commit: %s\n", response.Inventory.CommitHash)
	fmt.Printf("Head at inventory: %s\n", response.Inventory.HeadCommit)
	if response.Inventory.IsDirty {
		fmt.Println("Dirty at inventory: yes")
	} else {
		fmt.Println("Dirty at inventory: no")
	}
	fmt.Printf("Engine version: %d\n", response.Inventory.EngineVersion)
	fmt.Printf("Tracked files: %d\n", response.Inventory.TrackedFiles)
	if response.Inventory.Truncated {
		fmt.Println("Truncated: yes (file budget reached)")
	} else {
		fmt.Println("Truncated: no")
	}

	if err := formatInventorySummary(os.Stdout, response.Inventory.Summary); err != nil {
		return err
	}

	fmt.Println("Inventory reports committed files only; no builds or tests were run.")
	return nil
}

func analyzeProject(projectID int64) error {
	response, err := request(ipc.Request{
		Command:   "project-analyze",
		ProjectID: projectID,
	})
	if err != nil {
		return err
	}
	if response.Analysis == nil {
		return fmt.Errorf("daemon returned no component analysis")
	}
	fmt.Printf("Analyzed project %d\n", response.Analysis.ProjectID)
	fmt.Printf("Analysis: %d\n", response.Analysis.ID)
	fmt.Printf("Commit: %s\n", response.Analysis.CommitHash)
	fmt.Printf("Head at analysis: %s\n", response.Analysis.HeadCommit)
	if response.Analysis.IsDirty {
		fmt.Println("Dirty at analysis: yes")
	} else {
		fmt.Println("Dirty at analysis: no")
	}
	fmt.Printf("Engine version: %d\n", response.Analysis.EngineVersion)
	fmt.Printf("Status: %s\n", response.Analysis.Status)
	if response.Analysis.Status == "current" {
		fmt.Println("HEAD now: " + response.Analysis.Head)
		if response.Analysis.Dirty {
			fmt.Println("Working tree dirty (now): yes")
		} else {
			fmt.Println("Working tree dirty (now): no")
		}
	}
	fmt.Printf("Complete: %t, Truncated: %t\n", response.Analysis.Summary.Complete, response.Analysis.Summary.Truncated)
	fmt.Printf("Sources: %d, Analyzed: %d, Skipped: %d\n", response.Analysis.Summary.Sources, response.Analysis.Summary.Analyzed, response.Analysis.Summary.Skipped)
	fmt.Printf("Components: modules=%d packages=%d commands=%d build=%d config=%d\n",
		response.Analysis.Summary.Modules,
		response.Analysis.Summary.Packages,
		response.Analysis.Summary.Commands,
		response.Analysis.Summary.BuildComponents,
		response.Analysis.Summary.ConfigurationComponents,
	)
	return nil
}

func inspectProject(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: forgehand inspect <id> [--components] [--dependencies] [--evidence]")
	}

	includeFiles := false
	includeComponents := false
	includeDependencies := false
	for _, a := range args[1:] {
		switch a {
		case "--evidence":
			includeFiles = true
		case "--components":
			includeComponents = true
		case "--dependencies":
			includeDependencies = true
		default:
			return fmt.Errorf("unknown inspect option: %s", a)
		}
	}

	projectID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || projectID <= 0 {
		return fmt.Errorf("invalid project ID: %s", args[0])
	}

	response, err := request(ipc.Request{
		Command:             "project-inspect",
		ProjectID:           projectID,
		IncludeFiles:        includeFiles,
		IncludeComponents:   includeComponents,
		IncludeDependencies: includeDependencies,
	})
	if err != nil {
		return err
	}
	if response.Inspection == nil {
		return fmt.Errorf("daemon returned no project inspection")
	}

	return formatProjectInspection(os.Stdout, *response.Inspection, inspectionFormatOptions{
		includeInventoryFiles: includeFiles,
		includeComponents:     includeComponents,
		includeDependencies:   includeDependencies,
		includeEvidence:       includeFiles,
	})
}

type inspectionFormatOptions struct {
	includeInventoryFiles bool
	includeComponents     bool
	includeDependencies   bool
	includeEvidence       bool
}

func formatProjectInspection(
	writer io.Writer,
	inspection ipc.Inspection,
	options inspectionFormatOptions,
) error {
	var output strings.Builder

	projectID := int64(0)
	if inspection.Inventory != nil {
		projectID = inspection.Inventory.ProjectID
	} else if inspection.Analysis != nil {
		projectID = inspection.Analysis.ProjectID
	}

	fmt.Fprintf(&output, "Inspected project %d\n", projectID)
	fmt.Fprintf(&output, "HEAD now: %s\n", inspection.Head)
	if inspection.Dirty {
		fmt.Fprintln(&output, "Working tree dirty (now): yes")
	} else {
		fmt.Fprintln(&output, "Working tree dirty (now): no")
	}

	if inventory := inspection.Inventory; inventory != nil {
		appendInventoryInspection(&output, inspection, *inventory, options.includeInventoryFiles)
	} else {
		fmt.Fprintln(&output, "No inventory recorded; run `forgehand project inventory "+strconv.FormatInt(projectID, 10)+"`.")
	}

	if options.includeComponents || options.includeDependencies {
		if inspection.Analysis == nil {
			fmt.Fprintln(&output)
			fmt.Fprintln(&output, "No component analysis recorded; run `forgehand project analyze "+strconv.FormatInt(projectID, 10)+"`.")
		} else {
			appendAnalysisInspection(&output, *inspection.Analysis, options)
		}
	}

	return writeTabularOutput(writer, output.String())
}

func appendInventoryInspection(
	output *strings.Builder,
	inspection ipc.Inspection,
	inv ipc.Inventory,
	includeFiles bool,
) {
	fmt.Fprintf(output, "Status: %s\n", inspection.Status)
	fmt.Fprintf(output, "Inventory: %d\n", inv.ID)
	fmt.Fprintf(output, "Commit analyzed: %s\n", inv.CommitHash)
	if inv.IsDirty {
		fmt.Fprintln(output, "Dirty at inventory: yes")
	} else {
		fmt.Fprintln(output, "Dirty at inventory: no")
	}
	fmt.Fprintf(output, "Observed at: %s\n", inv.ObservedAt)
	fmt.Fprintf(output, "Engine version: %d\n", inv.EngineVersion)
	fmt.Fprintf(output, "Tracked files: %d\n", inv.TrackedFiles)
	if inv.Truncated {
		fmt.Fprintln(output, "Truncated: yes (file budget reached)")
	} else {
		fmt.Fprintln(output, "Truncated: no")
	}

	switch inspection.Status {
	case "stale":
		fmt.Fprintf(
			output,
			"This inventory describes commit %s; HEAD has advanced to %s.\n",
			inv.CommitHash,
			inspection.Head,
		)
		fmt.Fprintf(
			output,
			"Run `forgehand project inventory %d` to refresh.\n",
			inv.ProjectID,
		)
	case "unavailable":
		fmt.Fprintln(
			output,
			"Unable to verify the current repository HEAD (unborn HEAD or missing repository).",
		)
	}

	_ = appendInventorySummary(output, inv.Summary)

	if includeFiles {
		fmt.Fprintln(output)
		fmt.Fprintln(output, "Evidence files:")
		fmt.Fprintln(output, "PATH\tCATEGORY\tLANGUAGE\tSIZE\tBLOB")
		for _, file := range inv.Files {
			fmt.Fprintf(
				output,
				"%s\t%s\t%s\t%d\t%s\n",
				file.Path,
				file.Category,
				file.Language,
				file.Size,
				file.BlobHash,
			)
		}
	}

	fmt.Fprintln(output, "Inventory reports committed files only; no builds or tests were run.")
}

func appendAnalysisInspection(
	output *strings.Builder,
	analysis ipc.ComponentAnalysis,
	options inspectionFormatOptions,
) {
	fmt.Fprintln(output)
	fmt.Fprintf(output, "Analysis: %d\n", analysis.ID)
	fmt.Fprintf(output, "Analysis status: %s\n", analysis.Status)
	fmt.Fprintf(output, "Analysis commit: %s\n", analysis.CommitHash)
	fmt.Fprintf(output, "HEAD at analysis: %s\n", analysis.HeadCommit)
	if analysis.IsDirty {
		fmt.Fprintln(output, "Dirty at analysis: yes")
	} else {
		fmt.Fprintln(output, "Dirty at analysis: no")
	}
	fmt.Fprintf(output, "Observed at: %s\n", analysis.ObservedAt)
	fmt.Fprintf(output, "Analysis engine version: %d\n", analysis.EngineVersion)
	if analysis.Summary.Complete {
		fmt.Fprintln(output, "Analysis complete: yes")
	} else {
		fmt.Fprintln(output, "Analysis complete: no (partial)")
	}
	if analysis.Summary.Truncated {
		fmt.Fprintln(output, "Analysis truncated: yes (source budget reached)")
	} else {
		fmt.Fprintln(output, "Analysis truncated: no")
	}
	fmt.Fprintf(
		output,
		"Sources: %d, analyzed: %d, skipped: %d\n",
		analysis.Summary.Sources,
		analysis.Summary.Analyzed,
		analysis.Summary.Skipped,
	)
	fmt.Fprintf(
		output,
		"Components: modules=%d packages=%d commands=%d build=%d config=%d\n",
		analysis.Summary.Modules,
		analysis.Summary.Packages,
		analysis.Summary.Commands,
		analysis.Summary.BuildComponents,
		analysis.Summary.ConfigurationComponents,
	)
	if len(analysis.Summary.Dependencies) > 0 {
		fmt.Fprint(output, "Dependencies:")
		for _, count := range analysis.Summary.Dependencies {
			fmt.Fprintf(output, " %s=%d", count.Kind, count.Count)
		}
		fmt.Fprintln(output)
	}

	if analysis.Status != "current" {
		fmt.Fprintf(
			output,
			"This analysis describes commit %s; HEAD is %s.\n",
			analysis.CommitHash,
			analysis.Head,
		)
		fmt.Fprintf(
			output,
			"Run `forgehand project analyze %d` to refresh.\n",
			analysis.ProjectID,
		)
	}

	if len(analysis.SkippedFiles) > 0 {
		fmt.Fprintln(output, "Skipped sources:")
		fmt.Fprintln(output, "PATH\tREASON\tBLOB")
		for _, skipped := range analysis.SkippedFiles {
			fmt.Fprintf(output, "%s\t%s\t%s\n", skipped.Path, skipped.Reason, skipped.BlobHash)
		}
	}

	if len(analysis.ModuleRequirements) > 0 {
		fmt.Fprintln(output, "Declared module requirements:")
		fmt.Fprintln(output, "PATH\tVERSION\tINDIRECT")
		for _, requirement := range analysis.ModuleRequirements {
			fmt.Fprintf(
				output,
				"%s\t%s\t%t\n",
				requirement.Path,
				requirement.Version,
				requirement.Indirect,
			)
		}
	}

	if options.includeComponents {
		fmt.Fprintln(output)
		fmt.Fprintln(output, "Components:")
		fmt.Fprintln(output, "KIND\tIDENTITY\tPATH\tINTERNAL")
		for _, component := range analysis.Components {
			fmt.Fprintf(
				output,
				"%s\t%s\t%s\t%t\n",
				component.Kind,
				component.Identity,
				component.Path,
				component.Internal,
			)
		}
		if options.includeEvidence {
			fmt.Fprintln(output, "Component evidence:")
			fmt.Fprintln(output, "IDENTITY\tPATH\tLINE\tDECLARATION\tBLOB")
			for _, component := range analysis.Components {
				for _, evidence := range component.Evidence {
					fmt.Fprintf(
						output,
						"%s\t%s\t%d\t%s\t%s\n",
						component.Identity,
						evidence.Path,
						evidence.Line,
						sanitizeTableCell(evidence.Declaration),
						evidence.BlobHash,
					)
				}
			}
		}
	}

	if options.includeDependencies {
		fmt.Fprintln(output)
		fmt.Fprintln(output, "Dependencies:")
		fmt.Fprintln(output, "KIND\tSOURCE\tTARGET\tMODULE")
		for _, dependency := range analysis.Dependencies {
			fmt.Fprintf(
				output,
				"%s\t%s\t%s\t%s\n",
				dependency.Kind,
				dependency.Source,
				dependency.Target,
				dependency.TargetModule,
			)
		}
		if options.includeEvidence {
			fmt.Fprintln(output, "Dependency evidence:")
			fmt.Fprintln(output, "SOURCE\tTARGET\tPATH\tLINE\tDECLARATION\tBLOB")
			for _, dependency := range analysis.Dependencies {
				for _, evidence := range dependency.Evidence {
					fmt.Fprintf(
						output,
						"%s\t%s\t%s\t%d\t%s\t%s\n",
						dependency.Source,
						dependency.Target,
						evidence.Path,
						evidence.Line,
						sanitizeTableCell(evidence.Declaration),
						evidence.BlobHash,
					)
				}
			}
		}
	}

	fmt.Fprintln(output, "Analysis reports committed declarations only; no builds or tests were run.")
}

func formatInventorySummary(writer io.Writer, summary ipc.InventorySummary) error {
	var output strings.Builder
	if err := appendInventorySummary(&output, summary); err != nil {
		return err
	}
	_, err := io.WriteString(writer, output.String())
	return err
}

func appendInventorySummary(
	output *strings.Builder,
	summary ipc.InventorySummary,
) error {
	fmt.Fprintln(output, "Unclassified files:", summary.UnclassifiedFiles)

	fmt.Fprintln(output, "Languages:")
	if len(summary.Languages) == 0 {
		fmt.Fprintln(output, "  none")
	} else {
		for _, language := range summary.Languages {
			fmt.Fprintf(output, "  %s: %d\n", language.Language, language.Count)
		}
	}

	fmt.Fprintln(output, "Build-system indicators:")
	if len(summary.BuildSystems) == 0 {
		fmt.Fprintln(output, "  none")
	} else {
		for _, indicator := range summary.BuildSystems {
			fmt.Fprintf(
				output,
				"  %s: %s\n",
				indicator.Name,
				indicator.Evidence.Path,
			)
		}
	}

	fmt.Fprintln(output, "File classifications:")
	if len(summary.Categories) == 0 {
		fmt.Fprintln(output, "  none")
	} else {
		for _, category := range summary.Categories {
			fmt.Fprintf(
				output,
				"  %s: %d\n",
				category.Category,
				category.Count,
			)
		}
	}

	fmt.Fprintln(output, "Directories:")
	if len(summary.Directories) == 0 {
		fmt.Fprintln(output, "  none")
	} else {
		for _, directory := range summary.Directories {
			fmt.Fprintf(
				output,
				"  %s: %d\n",
				directory.Path,
				directory.Files,
			)
		}
	}

	return nil
}

func listProjects() error {
	response, err := request(ipc.Request{
		Command: "projects",
	})
	if err != nil {
		return err
	}

	if len(response.Projects) == 0 {
		fmt.Println("No projects")
		return nil
	}

	fmt.Println("ID  NAME  ROOT")

	for _, project := range response.Projects {
		fmt.Printf(
			"%d   %s  %s\n",
			project.ID,
			project.Name,
			project.RootPath,
		)
	}

	return nil
}

func sessionEvents(sessionID int64, afterEventID int64, limit int) error {
	response, err := request(ipc.Request{
		Command:      "session-events",
		SessionID:    sessionID,
		AfterEventID: afterEventID,
		Limit:        limit,
	})
	if err != nil {
		return err
	}

	if len(response.Events) == 0 {
		if afterEventID == 0 {
			fmt.Println("No events")
		} else {
			fmt.Println("No events after cursor")
		}
		return nil
	}

	writer := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	defer writer.Flush()

	fmt.Fprintln(writer, "ID	SESSION	EXECUTION	TYPE	TIMESTAMP	DETAILS")

	for _, event := range response.Events {
		exec := "-"
		if event.ExecutionID != nil {
			exec = fmt.Sprintf("%d", *event.ExecutionID)
		}
		details := ""
		if len(event.Details) > 0 {
			b, _ := json.Marshal(event.Details)
			details = string(b)
			if len(details) > 80 {
				details = details[:77] + "..."
			}
		}
		fmt.Fprintf(writer, "%d\t%d\t%s\t%s\t%s\t%s\n",
			event.ID,
			event.SessionID,
			exec,
			event.Type,
			event.Timestamp,
			details,
		)
	}

	if response.NextEventID > 0 && response.NextEventID >= response.Events[len(response.Events)-1].ID {
		// next cursor is last id
	}
	return nil
}

func usage() {
	fmt.Println("Forgehand")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  forgehand <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  version                         Print Forgehand version")
	fmt.Println("  daemon                          Run the Forgehand daemon")
	fmt.Println("  project add <path>              Add a Git project")
	fmt.Println("  projects                        List projects")
	fmt.Println("  project observe <id>            Observe a project's Git state")
	fmt.Println("  project discover <id>           Discover a committed project tree")
	fmt.Println("  project inventory <id>          Build a committed-tree inventory")
	fmt.Println("  project analyze <id>             Analyze components and dependencies")
	fmt.Println("  inspect <id> [--components] [--dependencies] [--evidence]  Show project state")
	fmt.Println("  query components <id> [--kind K] [--identity I] [--path P] [--contains S] [--analysis N] [--limit N] [--evidence]  List components")
	fmt.Println("  query dependencies <id> <identity> [--analysis N] [--evidence]  List outgoing dependencies")
	fmt.Println("  query dependents <id> <identity> [--analysis N] [--evidence]  List direct dependents")
	fmt.Println("  query evidence <id> <identity> [--analysis N]  Show component evidence")
	fmt.Println("  query changes <id> --from N --to N  Compare two analyses")
	fmt.Println("  investigate <id> --component <identity> [--analysis <id>] [--format text|json] [--limit <n>]  Investigate component")
	fmt.Println("  status                          Query the Forgehand daemon")
	fmt.Println("  service <action>                Install, update, uninstall, start, stop, restart, or inspect the user service")
	fmt.Println("  tui                             Open the terminal interface (also the default with no command)")
	fmt.Println("  session start <title>           Create a session")
	fmt.Println("  session start --investigate --project <id> --task \"question\" [--component <identity>] [--analysis <id>] [--title <t>] [--model <m>]  Create a read-only model investigation")
	fmt.Println("  session run <id> [--resource <name>]...  Queue a session for a worker, optionally holding exclusive resources")
	fmt.Println("  session resume <id>             Queue an interrupted or answered waiting session")
	fmt.Println("  session status <id>             Show a session's execution progress")
	fmt.Println("  session cancel <id> [--reason \"...\"]  Permanently cancel a session")
	fmt.Println("  session questions <id>          List pending questions for a session")
	fmt.Println("  session answer <interaction-id> \"answer text\"  Answer a pending question")
	fmt.Println("  session events <id> [--after N] [--limit N]  List session events")
	fmt.Println("  sessions                        List sessions")
	fmt.Println("  task start --project <id> --task \"...\" [--title <t>] [--model <m>]  Start a controlled coding task")
	fmt.Println("  task status <session-id>        Show task run state, proposal, and validation")
	fmt.Println("  task diff <session-id>          Show the proposed or applied changes as a unified diff")
	fmt.Println("  task approve <session-id>       Approve the pending proposal and resume the task")
	fmt.Println("  task reject <session-id>        Reject the pending proposal and resume the task")
	fmt.Println("  task cleanup <session-id>       Remove the isolated task workspace")
	fmt.Println("  model qualify --model <id> --capability planning --runs <n> --output <path> [--timeout <duration>]  Evaluate a model without changing authorization")
}

func startCodingSession(title, task, model string, projectID int64) error {
	if projectID <= 0 || strings.TrimSpace(task) == "" {
		return fmt.Errorf("project and task are required for a coding session")
	}
	response, err := request(ipc.Request{
		Command:   "session-start",
		ProjectID: projectID,
		Task:      task,
		Title:     title,
		Model:     model,
	})
	if err != nil {
		return err
	}
	if response.Session == nil {
		return fmt.Errorf("daemon returned no session")
	}
	fmt.Printf("Created coding session %d\n", response.Session.ID)
	fmt.Printf("State: %s\n", response.Session.State)
	fmt.Printf("Title: %s\n", response.Session.Title)
	if response.Session.ProjectID != nil {
		fmt.Printf("Project: %d\n", *response.Session.ProjectID)
	}
	fmt.Printf("Task: %s\n", response.Session.Task)
	fmt.Printf("Kind: %s\n", response.Session.Kind)
	if response.Session.Provider != "" {
		fmt.Printf("Provider: %s\n", response.Session.Provider)
	}
	if response.Session.Model != "" {
		fmt.Printf("Model: %s\n", response.Session.Model)
	}
	return nil
}

func startInvestigationSession(title, task, model, component string, projectID, analysisID int64) error {
	if projectID <= 0 || strings.TrimSpace(task) == "" {
		return fmt.Errorf("project and task are required for an investigation session")
	}
	if analysisID > 0 && strings.TrimSpace(component) == "" {
		return fmt.Errorf("--analysis requires --component")
	}
	response, err := request(ipc.Request{Command: "session-start", Kind: "investigation", ProjectID: projectID, Task: task, Title: title, Model: model, Identity: component, AnalysisID: analysisID})
	if err != nil {
		return err
	}
	if response.Session == nil {
		return fmt.Errorf("daemon returned no session")
	}
	fmt.Printf("Created investigation session %d\n", response.Session.ID)
	fmt.Printf("State: %s\n", response.Session.State)
	fmt.Printf("Title: %s\n", response.Session.Title)
	fmt.Printf("Project: %d\n", *response.Session.ProjectID)
	fmt.Printf("Question: %s\n", response.Session.Task)
	if component != "" {
		fmt.Printf("Component: %s\n", component)
	}
	fmt.Printf("Kind: %s\n", response.Session.Kind)
	return nil
}

func sessionResult(sessionID int64) error {
	response, err := request(ipc.Request{
		Command:   "session-result",
		SessionID: sessionID,
	})
	if err != nil {
		return err
	}
	if response.Result == nil {
		return fmt.Errorf("daemon returned no result")
	}
	fmt.Printf("Session: %d\n", response.Result.SessionID)
	fmt.Printf("Execution: %d\n", response.Result.ExecutionID)
	if response.Result.Provider != "" {
		fmt.Printf("Provider: %s\n", response.Result.Provider)
	}
	if response.Result.Model != "" {
		fmt.Printf("Model: %s\n", response.Result.Model)
	}
	fmt.Printf("Created: %s\n", response.Result.CreatedAt)
	if response.Result.TotalTokens > 0 {
		fmt.Printf("Tokens: %d prompt + %d completion = %d total\n", response.Result.PromptTokens, response.Result.CompletionTokens, response.Result.TotalTokens)
	}
	if response.Result.LatencyMS > 0 {
		fmt.Printf("Model latency: %d ms\n", response.Result.LatencyMS)
	}
	fmt.Printf("Content:\n%s\n", response.Result.Content)
	printInvestigationEvidence(response.Evidence)
	return nil
}

func printInvestigationEvidence(evidence *ipc.InvestigationEvidence) {
	if evidence == nil {
		return
	}
	fmt.Printf("Evidence commit: %s\n", evidence.Commit)
	fmt.Printf("Evidence budget: %d/%d chars\n", evidence.UsedChars, evidence.BudgetChars)
	for _, file := range evidence.Files {
		fmt.Printf("Evidence file: %s (blob=%s, %d bytes)\n", file.Path, file.BlobHash, file.Size)
		for _, location := range file.Locations {
			line := fmt.Sprintf("%d", location.Line)
			if location.EndLine > location.Line {
				line = fmt.Sprintf("%d-%d", location.Line, location.EndLine)
			}
			fmt.Printf("  source: %s:%s %s\n", file.Path, line, location.Declaration)
		}
	}
	if evidence.Truncated {
		fmt.Println("Evidence selection: truncated by context limits")
	}
}

func taskDiff(sessionID int64) error {
	response, err := request(ipc.Request{
		Command:   "task-diff",
		SessionID: sessionID,
	})
	if err != nil {
		return err
	}
	if !response.OK {
		if response.Message != "" {
			return fmt.Errorf("%s", response.Message)
		}
		return fmt.Errorf("task diff failed")
	}
	switch response.DiffSource {
	case "proposed":
		fmt.Println("# Proposed changes (awaiting approval) — this is exactly what task approve would apply")
	case "applied":
		fmt.Println("# Applied workspace changes")
	}
	if response.Diff == "" {
		fmt.Println("(no changes)")
		return nil
	}
	fmt.Print(response.Diff)
	if !strings.HasSuffix(response.Diff, "\n") {
		fmt.Println()
	}
	return nil
}

func taskApprove(sessionID int64) error {
	response, err := request(ipc.Request{
		Command:   "task-approve",
		SessionID: sessionID,
	})
	if err != nil {
		return err
	}
	if !response.OK {
		if response.Message != "" {
			return fmt.Errorf("%s", response.Message)
		}
		return fmt.Errorf("task approve failed")
	}
	if !response.AnswerApplied && response.Message != "" {
		fmt.Println(response.Message)
		return nil
	}
	if response.TaskRun != nil {
		fmt.Printf("Approved task run %d; resuming\n", response.TaskRun.ID)
		return nil
	}
	fmt.Println("Approved; resuming task")
	return nil
}

func taskReject(sessionID int64) error {
	response, err := request(ipc.Request{
		Command:   "task-reject",
		SessionID: sessionID,
	})
	if err != nil {
		return err
	}
	if !response.OK {
		if response.Message != "" {
			return fmt.Errorf("%s", response.Message)
		}
		return fmt.Errorf("task reject failed")
	}
	if !response.AnswerApplied && response.Message != "" {
		fmt.Println(response.Message)
		return nil
	}
	if response.TaskRun != nil {
		fmt.Printf("Rejected task run %d; resuming\n", response.TaskRun.ID)
		return nil
	}
	fmt.Println("Rejected; resuming task")
	return nil
}
