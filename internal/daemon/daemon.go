package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
	"github.com/Hisha/Forgehand/internal/version"
)

const (
	socketName      = "forgehand.sock"
	fakeWorkerSteps = 5
	fakeWorkerDelay = 3 * time.Second
)

func SocketPath() (string, error) {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return "", errors.New("XDG_RUNTIME_DIR is not set")
	}

	return filepath.Join(runtimeDir, "forgehand", socketName), nil
}

func ensureRuntimeDir() error {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return errors.New("XDG_RUNTIME_DIR is not set")
	}

	dir := filepath.Join(runtimeDir, "forgehand")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create runtime directory: %w", err)
	}

	return nil
}

func Run() error {
	if err := ensureRuntimeDir(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stateDB, err := state.Open(ctx)
	if err != nil {
		return fmt.Errorf("open persistent state: %w", err)
	}
	defer stateDB.Close()

	socketPath, err := SocketPath()
	if err != nil {
		return err
	}

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

	previousRun, err := stateDB.LastDaemonRun(ctx)
	if err != nil {
		return fmt.Errorf("read previous daemon run: %w", err)
	}

	if previousRun != nil && !previousRun.ShutdownClean {
		fmt.Printf(
			"Previous daemon run %d (PID %d) did not shut down cleanly\n",
			previousRun.ID,
			previousRun.PID,
		)
	}

	interrupted, err := stateDB.InterruptRunningExecutions(ctx)
	if err != nil {
		return fmt.Errorf("reconcile interrupted executions: %w", err)
	}

	if interrupted > 0 {
		fmt.Printf(
			"Marked %d orphaned execution(s) as interrupted\n",
			interrupted,
		)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	go func() {
		<-signals
		cancel()
		listener.Close()
	}()

	fmt.Printf("Forgehand daemon running\n")
	fmt.Printf("Socket: %s\n", socketPath)
	fmt.Printf("PID: %d\n", os.Getpid())

	runID, err := stateDB.StartDaemonRun(
		ctx,
		os.Getpid(),
		version.Version,
	)
	if err != nil {
		return fmt.Errorf("record daemon run: %w", err)
	}

	var workers sync.WaitGroup

	defer func() {
		cancel()
		workers.Wait()

		if err := stateDB.FinishDaemonRun(context.Background(), runID); err != nil {
			fmt.Fprintf(os.Stderr, "forgehand: record clean shutdown: %v\n", err)
		}
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				fmt.Println("Forgehand daemon stopped")
				return nil
			}

			return fmt.Errorf("accept connection: %w", err)
		}

		go handleConnection(ctx, stateDB, &workers, conn)
	}
}

func handleConnection(
	ctx context.Context,
	stateDB *state.Database,
	workers *sync.WaitGroup,
	conn net.Conn,
) {
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

	case "project-add":
		discovered, err := repository.Discover(
			context.Background(),
			request.Path,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		project, err := stateDB.CreateProject(
			context.Background(),
			discovered.Name,
			discovered.RootPath,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		_ = encoder.Encode(ipc.Response{
			OK: true,
			Project: &ipc.Project{
				ID:       project.ID,
				Name:     project.Name,
				RootPath: project.RootPath,
			},
		})

	case "project-observe":
		project, err := stateDB.GetProject(
			context.Background(),
			request.ProjectID,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		observed, err := repository.Observe(
			context.Background(),
			project.RootPath,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		snapshot, err := stateDB.CreateRepositorySnapshot(
			context.Background(),
			state.RepositorySnapshot{
				ProjectID:      project.ID,
				HeadCommit:     observed.HeadCommit,
				Branch:         observed.Branch,
				Detached:       observed.Detached,
				Dirty:          observed.Dirty,
				TrackedFiles:   observed.TrackedFiles,
				UntrackedFiles: observed.UntrackedFiles,
			},
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		_ = encoder.Encode(ipc.Response{
			OK: true,
			Snapshot: &ipc.RepositorySnapshot{
				ID:             snapshot.ID,
				ProjectID:      snapshot.ProjectID,
				HeadCommit:     snapshot.HeadCommit,
				Branch:         snapshot.Branch,
				Detached:       snapshot.Detached,
				Dirty:          snapshot.Dirty,
				TrackedFiles:   snapshot.TrackedFiles,
				UntrackedFiles: snapshot.UntrackedFiles,
			},
		})

	case "projects":
		projects, err := stateDB.ListProjects(context.Background())
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		responseProjects := make([]ipc.Project, 0, len(projects))

		for _, project := range projects {
			responseProjects = append(responseProjects, ipc.Project{
				ID:       project.ID,
				Name:     project.Name,
				RootPath: project.RootPath,
			})
		}

		_ = encoder.Encode(ipc.Response{
			OK:       true,
			Projects: responseProjects,
		})

	case "status":
		_ = encoder.Encode(ipc.Response{
			OK:      true,
			Message: "running",
			Version: version.Version,
			PID:     os.Getpid(),
		})

	case "session-start":
		session, err := stateDB.CreateSession(
			context.Background(),
			request.Title,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		_ = encoder.Encode(ipc.Response{
			OK: true,
			Session: &ipc.Session{
				ID:    session.ID,
				Title: session.Title,
				State: session.State,
			},
		})

	case "session-run":
		execution, err := stateDB.StartSessionExecution(
			context.Background(),
			request.SessionID,
			fakeWorkerSteps,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		workers.Add(1)
		go func() {
			defer workers.Done()
			runFakeWorker(ctx, stateDB, execution)
		}()

		_ = encoder.Encode(ipc.Response{
			OK: true,
			Execution: &ipc.Execution{
				ID:          execution.ID,
				SessionID:   execution.SessionID,
				Status:      execution.Status,
				CurrentStep: execution.CurrentStep,
				TotalSteps:  execution.TotalSteps,
			},
		})

	case "session-resume":
		execution, err := stateDB.ResumeSessionExecution(
			context.Background(),
			request.SessionID,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		workers.Add(1)
		go func() {
			defer workers.Done()
			runFakeWorker(ctx, stateDB, execution)
		}()

		_ = encoder.Encode(ipc.Response{
			OK: true,
			Execution: &ipc.Execution{
				ID:          execution.ID,
				SessionID:   execution.SessionID,
				Status:      execution.Status,
				CurrentStep: execution.CurrentStep,
				TotalSteps:  execution.TotalSteps,
			},
		})

	case "sessions":
		sessions, err := stateDB.ListSessions(context.Background())
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		response := ipc.Response{
			OK:       true,
			Sessions: make([]ipc.Session, 0, len(sessions)),
		}

		for _, session := range sessions {
			response.Sessions = append(
				response.Sessions,
				ipc.Session{
					ID:    session.ID,
					Title: session.Title,
					State: session.State,
				},
			)
		}

		_ = encoder.Encode(response)

	default:
		_ = encoder.Encode(ipc.Response{
			OK:      false,
			Message: "unknown command",
		})
	}
}

func runFakeWorker(
	ctx context.Context,
	stateDB *state.Database,
	execution state.SessionExecution,
) {
	currentStep := execution.CurrentStep

	for step := currentStep + 1; step <= execution.TotalSteps; step++ {
		timer := time.NewTimer(fakeWorkerDelay)

		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			fmt.Printf(
				"Execution %d stopped at %d/%d\n",
				execution.ID,
				currentStep,
				execution.TotalSteps,
			)
			return

		case <-timer.C:
		}

		if err := stateDB.AdvanceSessionExecution(
			context.Background(),
			execution.ID,
			step,
		); err != nil {
			fmt.Fprintf(
				os.Stderr,
				"forgehand: execution %d step %d: %v\n",
				execution.ID,
				step,
				err,
			)
			return
		}

		currentStep = step

		fmt.Printf(
			"Execution %d progress: %d/%d\n",
			execution.ID,
			currentStep,
			execution.TotalSteps,
		)
	}

	if err := stateDB.CompleteSessionExecution(
		context.Background(),
		execution.ID,
	); err != nil {
		fmt.Fprintf(
			os.Stderr,
			"forgehand: complete execution %d: %v\n",
			execution.ID,
			err,
		)
		return
	}

	fmt.Printf("Execution %d completed\n", execution.ID)
}
