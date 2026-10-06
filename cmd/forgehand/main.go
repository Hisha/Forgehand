package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"

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
	socketPath, err := daemon.SocketPath()
	if err != nil {
		return err
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("daemon is not running")
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(ipc.Request{
		Command: "status",
	}); err != nil {
		return fmt.Errorf("send request: %w", err)
	}

	var response ipc.Response
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if !response.OK {
		return fmt.Errorf("daemon error: %s", response.Message)
	}

	fmt.Printf("Daemon: %s\n", response.Message)
	fmt.Printf("Version: %s\n", response.Version)
	fmt.Printf("PID: %d\n", response.PID)

	return nil
}

func usage() {
	fmt.Println("Forgehand")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  forgehand <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  version    Print Forgehand version")
	fmt.Println("  daemon     Run the Forgehand daemon")
	fmt.Println("  status     Query the Forgehand daemon")
}
