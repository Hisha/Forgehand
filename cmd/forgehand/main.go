package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Hisha/Forgehand/internal/daemon"
	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	var err error

	switch os.Args[1] {
	case "version":
		fmt.Printf("%s %s\n", version.Name, version.Version)

	case "daemon":
		err = daemon.Run()

	case "project":
		err = projectCommand(os.Args[2:])

	case "projects":
		err = listProjects()

	case "inspect":
		err = inspectProject(os.Args[2:])

	case "status":
		err = status()

	case "session":
		err = sessionCommand(os.Args[2:])

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
	response, err := request(ipc.Request{
		Command: "status",
	})
	if err != nil {
		return err
	}

	fmt.Printf("Daemon: %s\n", response.Message)
	fmt.Printf("Version: %s\n", response.Version)
	fmt.Printf("PID: %d\n", response.PID)

	return nil
}

func sessionCommand(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: forgehand session start <title>")
	}

	switch args[0] {
	case "start":
		if len(args) < 2 {
			return fmt.Errorf("usage: forgehand session start <title>")
		}

		if len(args) > 2 {
			return fmt.Errorf("session title must be quoted when it contains spaces")
		}

		return startSession(args[1])

	case "run":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand session run <id>")
		}

		sessionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || sessionID <= 0 {
			return fmt.Errorf("invalid session ID: %s", args[1])
		}

		return runSession(sessionID)

	case "resume":
		if len(args) != 2 {
			return fmt.Errorf("usage: forgehand session resume <id>")
		}

		sessionID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || sessionID <= 0 {
			return fmt.Errorf("invalid session ID: %s", args[1])
		}

		return resumeSession(sessionID)

	default:
		return fmt.Errorf("unknown session command: %s", args[0])
	}
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

func runSession(sessionID int64) error {
	response, err := request(ipc.Request{
		Command:   "session-run",
		SessionID: sessionID,
	})
	if err != nil {
		return err
	}

	if response.Execution == nil {
		return fmt.Errorf("daemon returned no execution")
	}

	fmt.Printf("Started session %d\n", response.Execution.SessionID)
	fmt.Printf("Execution: %d\n", response.Execution.ID)
	fmt.Printf(
		"Progress: %d/%d\n",
		response.Execution.CurrentStep,
		response.Execution.TotalSteps,
	)

	return nil
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

	fmt.Printf("Resumed session %d\n", response.Execution.SessionID)
	fmt.Printf("Execution: %d\n", response.Execution.ID)
	fmt.Printf(
		"Progress: %d/%d\n",
		response.Execution.CurrentStep,
		response.Execution.TotalSteps,
	)

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
	socketPath, err := daemon.SocketPath()
	if err != nil {
		return ipc.Response{}, err
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return ipc.Response{}, fmt.Errorf("daemon is not running")
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return ipc.Response{}, fmt.Errorf("send request: %w", err)
	}

	var response ipc.Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return ipc.Response{}, fmt.Errorf("read response: %w", err)
	}

	if !response.OK {
		return ipc.Response{}, fmt.Errorf("daemon error: %s", response.Message)
	}

	return response, nil
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

func inspectProject(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: forgehand inspect <id> [--evidence]")
	}

	includeFiles := false
	if len(args) == 2 {
		if args[1] != "--evidence" {
			return fmt.Errorf("unknown inspect option: %s", args[1])
		}
		includeFiles = true
	}

	projectID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || projectID <= 0 {
		return fmt.Errorf("invalid project ID: %s", args[0])
	}

	response, err := request(ipc.Request{
		Command:      "project-inspect",
		ProjectID:    projectID,
		IncludeFiles: includeFiles,
	})
	if err != nil {
		return err
	}
	if response.Inspection == nil || response.Inspection.Inventory == nil {
		return fmt.Errorf("daemon returned no project inspection")
	}

	return formatProjectInspection(os.Stdout, *response.Inspection, includeFiles)
}

func formatProjectInspection(
	writer io.Writer,
	inspection ipc.Inspection,
	includeFiles bool,
) error {
	inv := inspection.Inventory

	var output strings.Builder
	fmt.Fprintf(&output, "Inspected project %d\n", inv.ProjectID)
	fmt.Fprintf(&output, "Status: %s\n", inspection.Status)
	fmt.Fprintf(&output, "Inventory: %d\n", inv.ID)
	fmt.Fprintf(&output, "Commit analyzed: %s\n", inv.CommitHash)
	fmt.Fprintf(&output, "HEAD now: %s\n", inspection.Head)
	if inspection.Dirty {
		fmt.Fprintln(&output, "Working tree dirty (now): yes")
	} else {
		fmt.Fprintln(&output, "Working tree dirty (now): no")
	}
	if inv.IsDirty {
		fmt.Fprintln(&output, "Dirty at inventory: yes")
	} else {
		fmt.Fprintln(&output, "Dirty at inventory: no")
	}
	fmt.Fprintf(&output, "Observed at: %s\n", inv.ObservedAt)
	fmt.Fprintf(&output, "Engine version: %d\n", inv.EngineVersion)
	fmt.Fprintf(&output, "Tracked files: %d\n", inv.TrackedFiles)
	if inv.Truncated {
		fmt.Fprintln(&output, "Truncated: yes (file budget reached)")
	} else {
		fmt.Fprintln(&output, "Truncated: no")
	}

	switch inspection.Status {
	case "stale":
		fmt.Fprintf(
			&output,
			"This inventory describes commit %s; HEAD has advanced to %s.\n",
			inv.CommitHash,
			inspection.Head,
		)
		fmt.Fprintf(
			&output,
			"Run `forgehand project inventory %d` to refresh.\n",
			inv.ProjectID,
		)
	case "unavailable":
		fmt.Fprintln(
			&output,
			"Unable to verify the current repository HEAD (unborn HEAD or missing repository).",
		)
	}

	if err := appendInventorySummary(&output, inv.Summary); err != nil {
		return err
	}

	if includeFiles {
		fmt.Fprintln(&output)
		fmt.Fprintln(&output, "Evidence files:")
		fmt.Fprintln(&output, "PATH\tCATEGORY\tLANGUAGE\tSIZE\tBLOB")
		for _, file := range inv.Files {
			fmt.Fprintf(
				&output,
				"%s\t%s\t%s\t%d\t%s\n",
				file.Path,
				file.Category,
				file.Language,
				file.Size,
				file.BlobHash,
			)
		}
	}

	fmt.Fprintln(&output, "Inventory reports committed files only; no builds or tests were run.")
	_, err := io.WriteString(writer, output.String())
	return err
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
	fmt.Println("  inspect <id> [--evidence]       Show the latest project inventory")
	fmt.Println("  status                          Query the Forgehand daemon")
	fmt.Println("  session start <title>           Create a session")
	fmt.Println("  session run <id>                Run a session")
	fmt.Println("  session resume <id>             Resume an interrupted session")
	fmt.Println("  sessions                        List sessions")
}
