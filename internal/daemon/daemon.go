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

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	fakeWorkerSteps = 5
	fakeWorkerDelay = 3 * time.Second

	// busyRetryAttempts bounds application-level retries when SQLite reports a
	// transient lock contention error despite the connection's busy_timeout.
	busyRetryAttempts = 5
	busyRetryDelay    = 50 * time.Millisecond
)

// executionStore is the subset of the state database a fake worker needs. It
// exists so worker failure handling can be tested with an injected error.
type executionStore interface {
	AdvanceSessionExecution(ctx context.Context, executionID int64, step int) error
	CompleteSessionExecution(ctx context.Context, executionID int64) error
	InterruptSessionExecution(ctx context.Context, executionID int64) error
	FailSessionExecution(ctx context.Context, executionID int64) error
}

// workerGroup tracks daemon-side fake workers. It rejects new work once the
// daemon starts shutting down, so a worker can never be registered in the wait
// set at the same time as the shutdown wait (which would race a WaitGroup).
type workerGroup struct {
	mu      sync.Mutex
	group   sync.WaitGroup
	stopped bool
}

func (w *workerGroup) start() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.stopped {
		return false
	}

	w.group.Add(1)
	return true
}

func (w *workerGroup) done() {
	w.group.Done()
}

func (w *workerGroup) stopAndWait() {
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()

	w.group.Wait()
}

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

	var workers workerGroup

	defer func() {
		cancel()
		workers.stopAndWait()

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
	workers *workerGroup,
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

	case "project-inventory":
		result, err := inventoryRegisteredProject(
			context.Background(),
			stateDB,
			request.ProjectID,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
			return
		}

		record := result.Inventory
		if request.IncludeFiles {
			loaded, err := stateDB.GetProjectInventory(
				context.Background(),
				record.ID,
				true,
			)
			if err != nil {
				_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
				return
			}
			if loaded != nil {
				record = *loaded
			}
		}

		inv := inventoryToIPC(record)
		_ = encoder.Encode(ipc.Response{OK: true, Inventory: &inv})

	case "project-analyze":
		response, err := analyzeRegisteredProject(context.Background(), stateDB, request.ProjectID)
		if err != nil {
			_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
			return
		}
		analysis := analysisToIPC(response.Analysis, response.Status, response.Head, response.Dirty, true, true)
		_ = encoder.Encode(ipc.Response{OK: true, Analysis: &analysis})

	case "project-inspect":
		inspection, err := inspectRegisteredProject(
			context.Background(),
			stateDB,
			request.ProjectID,
			inspectionOptions{
				IncludeFiles:        request.IncludeFiles,
				IncludeComponents:   request.IncludeComponents,
				IncludeDependencies: request.IncludeDependencies,
			},
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
			return
		}

		response := ipc.Inspection{
			Status: inspection.Status,
			Head:   inspection.Head,
			Dirty:  inspection.Dirty,
		}
		if inspection.Inventory != nil {
			inv := inventoryToIPC(*inspection.Inventory)
			response.Inventory = &inv
		}
		if inspection.Analysis != nil {
			analysis := analysisToIPC(
				*inspection.Analysis,
				inspection.AnalysisStatus,
				inspection.Head,
				inspection.Dirty,
				request.IncludeComponents,
				request.IncludeDependencies,
			)
			response.Analysis = &analysis
		}

		_ = encoder.Encode(ipc.Response{OK: true, Inspection: &response})

	case "query-components", "query-dependencies", "query-dependents", "query-evidence", "query-changes":
		result, err := handleQuery(context.Background(), stateDB, request)
		if err != nil {
			_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
			return
		}
		_ = encoder.Encode(ipc.Response{OK: true, Query: &result})

	case "investigate":
		result, err := handleInvestigation(context.Background(), stateDB, request)
		if err != nil {
			_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
			return
		}
		_ = encoder.Encode(ipc.Response{OK: true, Investigation: &result})

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
		if !workers.start() {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: "daemon is shutting down",
			})
			return
		}

		execution, err := stateDB.StartSessionExecution(
			context.Background(),
			request.SessionID,
			fakeWorkerSteps,
		)
		if err != nil {
			workers.done()
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		go func() {
			defer workers.done()
			runFakeWorker(ctx, stateDB, execution, fakeWorkerDelay)
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
		if !workers.start() {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: "daemon is shutting down",
			})
			return
		}

		execution, err := stateDB.ResumeSessionExecution(
			context.Background(),
			request.SessionID,
		)
		if err != nil {
			workers.done()
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		go func() {
			defer workers.done()
			runFakeWorker(ctx, stateDB, execution, fakeWorkerDelay)
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

	case "session-status":
		session, execution, err := stateDB.SessionExecutionStatus(
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

		response := ipc.Response{
			OK: true,
			Session: &ipc.Session{
				ID:    session.ID,
				Title: session.Title,
				State: session.State,
			},
		}

		if execution != nil {
			response.Execution = &ipc.Execution{
				ID:          execution.ID,
				SessionID:   execution.SessionID,
				Status:      execution.Status,
				CurrentStep: execution.CurrentStep,
				TotalSteps:  execution.TotalSteps,
			}
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
	store executionStore,
	execution state.SessionExecution,
	delay time.Duration,
) {
	currentStep := execution.CurrentStep

	for step := currentStep + 1; step <= execution.TotalSteps; step++ {
		if !waitForStep(ctx, delay) {
			fmt.Printf(
				"Execution %d stopped at %d/%d\n",
				execution.ID,
				currentStep,
				execution.TotalSteps,
			)
			interruptExecution(store, execution.ID)
			return
		}

		if err := retryOnBusy(ctx, func() error {
			return store.AdvanceSessionExecution(
				context.Background(),
				execution.ID,
				step,
			)
		}); err != nil {
			fmt.Fprintf(
				os.Stderr,
				"forgehand: execution %d step %d: %v\n",
				execution.ID,
				step,
				err,
			)
			if ctx.Err() != nil {
				interruptExecution(store, execution.ID)
			} else {
				failExecution(store, execution.ID)
			}
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

	if err := retryOnBusy(ctx, func() error {
		return store.CompleteSessionExecution(
			context.Background(),
			execution.ID,
		)
	}); err != nil {
		fmt.Fprintf(
			os.Stderr,
			"forgehand: complete execution %d: %v\n",
			execution.ID,
			err,
		)
		if ctx.Err() != nil {
			interruptExecution(store, execution.ID)
		} else {
			failExecution(store, execution.ID)
		}
		return
	}

	fmt.Printf("Execution %d completed\n", execution.ID)
}

// waitForStep blocks for the worker's step delay. It returns false when the
// daemon context is cancelled first, so the worker can stop at the last
// committed checkpoint.
func waitForStep(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// interruptExecution records a recoverable interruption at the current
// checkpoint so the execution is never abandoned silently in RUNNING.
func interruptExecution(store executionStore, executionID int64) {
	if err := store.InterruptSessionExecution(
		context.Background(),
		executionID,
	); err != nil {
		fmt.Fprintf(
			os.Stderr,
			"forgehand: interrupt execution %d: %v\n",
			executionID,
			err,
		)
	}
}

// failExecution records a terminal execution failure, preserving the last
// committed checkpoint rather than leaving the execution in RUNNING.
func failExecution(store executionStore, executionID int64) {
	if err := store.FailSessionExecution(
		context.Background(),
		executionID,
	); err != nil {
		fmt.Fprintf(
			os.Stderr,
			"forgehand: mark execution %d failed: %v\n",
			executionID,
			err,
		)
	}
}

// retryOnBusy re-runs a state operation a bounded number of times when SQLite
// reports lock contention. Each attempt is a whole transaction, and the driver
// rolls back a transaction whose commit returns busy, so a retry cannot double
// apply a transition.
func retryOnBusy(ctx context.Context, op func() error) error {
	var err error

	for attempt := 0; attempt < busyRetryAttempts; attempt++ {
		if err = op(); err == nil {
			return nil
		}
		if !isBusy(err) || attempt == busyRetryAttempts-1 {
			return err
		}

		select {
		case <-ctx.Done():
			return err
		case <-time.After(busyRetryDelay * time.Duration(attempt+1)):
		}
	}

	return err
}

func isBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}

	primary := sqliteErr.Code() & 0xff
	return primary == sqlite3.SQLITE_BUSY || primary == sqlite3.SQLITE_LOCKED
}
