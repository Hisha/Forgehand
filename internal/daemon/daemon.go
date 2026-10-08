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
	"sync"
	"syscall"
	"time"

	"github.com/Hisha/Forgehand/internal/config"
	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
	"github.com/Hisha/Forgehand/internal/version"
)

const (
	fakeWorkerSteps = 5
	fakeWorkerDelay = 3 * time.Second
)

func SocketPath() (string, error) {
	return config.SocketPath()
}

type Config struct {
	StateDir   string
	SocketPath string
	SocketMode os.FileMode
}

func LoadConfig() (Config, error) {
	stateDir, err := config.StateDir()
	if err != nil {
		return Config{}, err
	}
	socketPath, err := config.SocketPath()
	if err != nil {
		return Config{}, err
	}
	socketMode, err := config.SocketMode()
	if err != nil {
		return Config{}, err
	}
	return Config{StateDir: stateDir, SocketPath: socketPath, SocketMode: socketMode}, nil
}

func Run() error {
	realUID := os.Getuid()
	effectiveUID := os.Geteuid()
	if realUID == 0 || effectiveUID == 0 {
		return validateDaemonIdentity(realUID, effectiveUID, 0)
	}
	capabilities, err := effectiveCapabilities()
	if err != nil {
		return err
	}
	return runWithIdentity(realUID, effectiveUID, capabilities)
}

func runWithIdentity(realUID, effectiveUID int, capabilities uint64) error {
	if err := validateDaemonIdentity(realUID, effectiveUID, capabilities); err != nil {
		return err
	}
	configured, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load daemon configuration: %w", err)
	}
	return runConfigured(configured)
}

func runConfigured(config Config) error {
	if err := state.EnsureDirAt(config.StateDir); err != nil {
		return err
	}
	ownership, err := acquireStateOwnership(config.StateDir)
	if err != nil {
		return err
	}
	defer ownership.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stateDB, err := state.OpenAt(ctx, config.StateDir)
	if err != nil {
		return fmt.Errorf("open persistent state: %w", err)
	}
	defer stateDB.Close()

	if err := ensureRuntimeDir(config.SocketPath, config.SocketMode); err != nil {
		return err
	}

	if conn, err := net.Dial("unix", config.SocketPath); err == nil {
		conn.Close()
		return fmt.Errorf("Forgehand daemon is already running")
	}

	if err := os.Remove(config.SocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	listener, err := net.Listen("unix", config.SocketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", config.SocketPath, err)
	}

	cleanup := func() {
		listener.Close()
		os.Remove(config.SocketPath)
	}
	defer cleanup()

	if err := os.Chmod(config.SocketPath, config.SocketMode); err != nil {
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
	fmt.Printf("Socket: %s\n", config.SocketPath)
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

		client, err := captureClientIdentity(conn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "forgehand: identify local client: %v\n", err)
			conn.Close()
			continue
		}

		go handleConnection(ctx, stateDB, &workers, conn, client)
	}
}

func handleConnection(
	ctx context.Context,
	stateDB *state.Database,
	workers *sync.WaitGroup,
	conn net.Conn,
	client ClientIdentity,
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
		intake, err := inspectProjectIntake(
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

		if len(intake.Changes) != 0 {
			changes := make(
				[]ipc.WorkingTreeChange,
				0,
				len(intake.Changes),
			)

			for _, change := range intake.Changes {
				changes = append(
					changes,
					ipc.WorkingTreeChange{
						Path:         change.Path,
						OriginalPath: change.OriginalPath,
						IndexStatus:  change.IndexStatus,
						WorkStatus:   change.WorkStatus,
						Untracked:    change.Untracked,
					},
				)
			}

			_ = encoder.Encode(ipc.Response{
				OK: true,
				Intake: &ipc.ProjectIntake{
					RootPath:      intake.Repository.RootPath,
					Changes:       changes,
					ExpectedState: intake.Fingerprint,
				},
			})
			return
		}

		project, err := admitProject(
			context.Background(),
			stateDB,
			intake,
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

	case "project-resolve-intake":
		project, err := resolveProjectIntake(
			context.Background(),
			stateDB,
			request.Path,
			request.Action,
			request.ExpectedState,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
			return
		}
		if request.Action == projectIntakeCancel {
			_ = encoder.Encode(ipc.Response{OK: true})
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

	case "project-discover":
		result, err := discoverRegisteredProject(
			context.Background(),
			stateDB,
			request.ProjectID,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
			return
		}

		summary := result.Observation.Summary
		languages := make([]ipc.LanguageCount, 0, len(summary.Languages))
		for _, language := range summary.Languages {
			languages = append(languages, ipc.LanguageCount{
				Language: language.Language,
				Count:    language.Count,
			})
		}
		buildSystems := make([]ipc.BuildSystemIndicator, 0, len(summary.BuildSystems))
		for _, indicator := range summary.BuildSystems {
			buildSystems = append(buildSystems, ipc.BuildSystemIndicator{
				Name: indicator.Name,
				Evidence: ipc.EvidenceReference{
					Path: indicator.Evidence.Path,
				},
			})
		}

		_ = encoder.Encode(ipc.Response{
			OK: true,
			Discovery: &ipc.ProjectDiscovery{
				ObservationID: result.Observation.ID,
				Project: ipc.Project{
					ID:       result.Project.ID,
					Name:     result.Project.Name,
					RootPath: result.Project.RootPath,
				},
				CommitHash:        summary.CommitHash,
				TrackedFiles:      summary.TrackedFiles,
				UnclassifiedFiles: summary.UnclassifiedFiles,
				Languages:         languages,
				BuildSystems:      buildSystems,
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
