package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/version"
)

const socketName = "forgehand.sock"

func SocketPath() (string, error) {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return "", errors.New("XDG_RUNTIME_DIR is not set")
	}

	dir := filepath.Join(runtimeDir, "forgehand")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create runtime directory: %w", err)
	}

	return filepath.Join(dir, socketName), nil
}

func Run() error {
	socketPath, err := SocketPath()
	if err != nil {
		return err
	}

	// A Unix socket file can remain after an unclean shutdown.
	// Only remove it after confirming nothing is listening there.
	if conn, err := net.Dial("unix", socketPath); err == nil {
		conn.Close()
		return fmt.Errorf("Forgehand daemon is already running")
	}

	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", socketPath, err)
	}

	cleanup := func() {
		listener.Close()
		os.Remove(socketPath)
	}
	defer cleanup()

	if err := os.Chmod(socketPath, 0600); err != nil {
		return fmt.Errorf("secure socket: %w", err)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	go func() {
		<-signals
		listener.Close()
	}()

	fmt.Printf("Forgehand daemon running\n")
	fmt.Printf("Socket: %s\n", socketPath)
	fmt.Printf("PID: %d\n", os.Getpid())

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				fmt.Println("Forgehand daemon stopped")
				return nil
			}

			return fmt.Errorf("accept connection: %w", err)
		}

		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {
	defer conn.Close()

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)

	var request ipc.Request
	if err := decoder.Decode(&request); err != nil {
		if !errors.Is(err, io.EOF) {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: "invalid request",
			})
		}
		return
	}

	switch request.Command {
	case "status":
		_ = encoder.Encode(ipc.Response{
			OK:      true,
			Message: "running",
			Version: version.Version,
			PID:     os.Getpid(),
		})

	default:
		_ = encoder.Encode(ipc.Response{
			OK:      false,
			Message: "unknown command",
		})
	}
}
