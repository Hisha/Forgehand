package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
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

func usage() {
	fmt.Println("Forgehand")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  forgehand <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  version                         Print Forgehand version")
	fmt.Println("  daemon                          Run the Forgehand daemon")
	fmt.Println("  status                          Query the Forgehand daemon")
	fmt.Println("  session start <title>           Create a session")
	fmt.Println("  session run <id>                Run a session")
	fmt.Println("  session resume <id>             Resume an interrupted session")
	fmt.Println("  sessions                        List sessions")
}
