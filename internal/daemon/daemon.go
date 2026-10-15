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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Hisha/Forgehand/internal/config"
	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/provider"
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
	RequestHumanInput(ctx context.Context, executionID int64, question string, contextDetails map[string]any) (state.HumanInteraction, bool, error)
	SessionExecutionStatus(ctx context.Context, sessionID int64) (state.Session, *state.SessionExecution, error)
}

// workerGroup tracks daemon-side fake workers. It rejects new work once the
// daemon starts shutting down, so a worker can never be registered in the wait
// set at the same time as the shutdown wait (which would race a WaitGroup).
//
// It also owns the daemon-local execution ownership map: at most one live
// worker may own a given execution ID at a time. Ownership is in-memory only
// and therefore cannot survive a daemon restart, which is exactly the intended
// lifetime for Milestone 2E (no distributed leases).
type workerGroup struct {
	mu      sync.Mutex
	group   sync.WaitGroup
	stopped bool
	owners  map[int64]context.CancelFunc
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

// claim records ownership of an execution for a worker and registers its
// cancellation function. It fails when the daemon is shutting down or another
// worker already owns the execution, preventing duplicate workers for one
// execution.
func (w *workerGroup) claim(executionID int64, cancel context.CancelFunc) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.stopped {
		return false
	}
	if w.owners == nil {
		w.owners = make(map[int64]context.CancelFunc)
	}
	if _, exists := w.owners[executionID]; exists {
		return false
	}

	w.owners[executionID] = cancel
	return true
}

// release removes a worker's ownership record so a later run or resume of the
// same execution is not blocked.
func (w *workerGroup) release(executionID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	delete(w.owners, executionID)
}

// cancel signals the worker that owns an execution, if any. It reports whether
// an owner was found.
func (w *workerGroup) cancel(executionID int64) bool {
	w.mu.Lock()
	cancel, ok := w.owners[executionID]
	w.mu.Unlock()

	if !ok {
		return false
	}

	cancel()
	return true
}

func (w *workerGroup) ownerCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return len(w.owners)
}

func (w *workerGroup) stopAndWait() {
	w.mu.Lock()
	w.stopped = true
	cancelers := make([]context.CancelFunc, 0, len(w.owners))
	for _, cancel := range w.owners {
		cancelers = append(cancelers, cancel)
	}
	w.mu.Unlock()

	for _, cancel := range cancelers {
		cancel()
	}

	w.group.Wait()
}

func SocketPath() (string, error) {
	return config.SocketPath()
}

type Config struct {
	StateDir             string
	SocketPath           string
	SocketMode           os.FileMode
	MaxConcurrentWorkers int
	Provider             config.ProviderConfig
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
	maxWorkers, err := config.MaxConcurrentWorkers()
	if err != nil {
		return Config{}, err
	}
	providerConfig, err := config.Provider()
	if err != nil {
		return Config{}, err
	}
	return Config{
		StateDir:             stateDir,
		SocketPath:           socketPath,
		SocketMode:           socketMode,
		MaxConcurrentWorkers: maxWorkers,
		Provider:             providerConfig,
	}, nil
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

	model := buildModelRuntime(config.Provider, stateDB)
	if model == nil {
		fmt.Printf("Model provider not configured; coding sessions will fail\n")
	} else {
		fmt.Printf("Model provider configured: %s (model %s)\n", provider.ProviderID, config.Provider.Model)
	}

	var workers workerGroup
	dispatcher := newDispatcher(stateDB, &workers, config.MaxConcurrentWorkers, model)

	defer func() {
		cancel()
		workers.stopAndWait()
		// Join the dispatcher so it cannot touch the database while it is
		// being closed below.
		<-dispatcher.done

		if err := stateDB.FinishDaemonRun(context.Background(), runID); err != nil {
			fmt.Fprintf(os.Stderr, "forgehand: record clean shutdown: %v\n", err)
		}
	}()

	go dispatcher.run(ctx)

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

		go handleConnection(ctx, stateDB, &workers, dispatcher, conn, client)
	}
}

func handleConnection(
	ctx context.Context,
	stateDB *state.Database,
	workers *workerGroup,
	disp *dispatcher,
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
		queued, err := stateDB.CountQueuedExecutions(context.Background())
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		capacity := 0
		if disp != nil {
			capacity = disp.capacity()
		}

		_ = encoder.Encode(ipc.Response{
			OK:               true,
			Message:          "running",
			Version:          version.Version,
			PID:              os.Getpid(),
			WorkerCapacity:   capacity,
			ActiveWorkers:    workers.ownerCount(),
			QueuedExecutions: queued,
		})

	case "session-start":
		if request.Task != "" || request.ProjectID > 0 {
			if request.ProjectID <= 0 {
				_ = encoder.Encode(ipc.Response{
					OK:      false,
					Message: "a coding session requires a project id",
				})
				return
			}
			if strings.TrimSpace(request.Task) == "" {
				_ = encoder.Encode(ipc.Response{
					OK:      false,
					Message: "a coding session requires a task",
				})
				return
			}
			var modelRuntimeRef *modelRuntime
			if disp != nil {
				modelRuntimeRef = disp.model
			}
			if modelRuntimeRef == nil {
				_ = encoder.Encode(ipc.Response{
					OK:      false,
					Message: "no model provider is configured",
				})
				return
			}
			session, err := stateDB.CreateCodingSession(
				context.Background(),
				codingSessionTitle(request.Title, request.Task),
				request.ProjectID,
				request.Task,
				provider.ProviderID,
				codingModel(request.Model, modelRuntimeRef.defaultModel),
			)
			if err != nil {
				_ = encoder.Encode(ipc.Response{
					OK:      false,
					Message: err.Error(),
				})
				return
			}
			encoded := sessionToIPC(session)
			_ = encoder.Encode(ipc.Response{OK: true, Session: &encoded})
			return
		}

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

		encoded := sessionToIPC(session)
		_ = encoder.Encode(ipc.Response{
			OK:      true,
			Session: &encoded,
		})

	case "session-run":
		execution, err := stateDB.QueueSessionExecution(
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

		if disp != nil {
			disp.notify()
		}

		encoded := executionToIPC(execution)
		_ = encoder.Encode(ipc.Response{
			OK:        true,
			Execution: &encoded,
		})

	case "session-resume":
		session, _, err := stateDB.SessionExecutionStatus(
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

		var execution state.SessionExecution
		if session.State == state.SessionStateWaitingForUser {
			execution, err = stateDB.QueueResumeAfterHumanInput(
				context.Background(),
				request.SessionID,
			)
		} else {
			execution, err = stateDB.QueueResumeExecution(
				context.Background(),
				request.SessionID,
			)
		}
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		if disp != nil {
			disp.notify()
		}

		encoded := executionToIPC(execution)
		_ = encoder.Encode(ipc.Response{
			OK:        true,
			Execution: &encoded,
		})

	case "session-cancel":
		result, err := stateDB.CancelSessionExecution(
			context.Background(),
			request.SessionID,
			request.Reason,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		if result.Outcome == state.CancellationRejectedTerminal {
			_ = encoder.Encode(ipc.Response{
				OK:            false,
				CancelOutcome: string(result.Outcome),
				Message: fmt.Sprintf(
					"execution %d is %s; cancellation is not permitted",
					result.Execution.ID,
					result.Execution.Status,
				),
			})
			return
		}

		// Cancellation is durably committed by now. Signal the owning worker,
		// if one is live, so it stops at a safe point. The database is
		// authoritative: a worker that misses the signal still cannot advance
		// the canceled execution.
		workers.cancel(result.Execution.ID)

		encoded := executionToIPC(result.Execution)
		encodedSession := sessionToIPC(result.Session)
		_ = encoder.Encode(ipc.Response{
			OK:            true,
			Canceled:      true,
			CancelOutcome: string(result.Outcome),
			Session:       &encodedSession,
			Execution:     &encoded,
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
				sessionToIPC(session),
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

		encodedSession := sessionToIPC(session)
		response := ipc.Response{
			OK:      true,
			Session: &encodedSession,
		}

		if execution != nil {
			encoded := executionToIPC(*execution)
			response.Execution = &encoded
		}

		if session.State == state.SessionStateWaitingForUser {
			pending, err := stateDB.GetPendingHumanInput(
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
			if pending != nil {
				interaction := interactionToIPC(*pending)
				response.Interaction = &interaction
			}
		}

		_ = encoder.Encode(response)

	case "session-result":
		result, err := stateDB.LatestModelResult(
			context.Background(),
			request.SessionID,
		)
		if err != nil {
			if errors.Is(err, state.ErrNoModelResult) {
				_ = encoder.Encode(ipc.Response{
					OK:      false,
					Message: fmt.Sprintf("session %d has no model result", request.SessionID),
				})
				return
			}
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		encoded := modelResultToIPC(result)
		_ = encoder.Encode(ipc.Response{OK: true, Result: &encoded})

	case "session-questions":
		pending, err := stateDB.ListPendingHumanInteractions(
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

		interactions := make([]ipc.Interaction, 0, len(pending))
		for _, interaction := range pending {
			interactions = append(interactions, interactionToIPC(interaction))
		}

		_ = encoder.Encode(ipc.Response{
			OK:           true,
			Interactions: interactions,
		})

	case "interaction-get":
		interaction, err := stateDB.GetHumanInteraction(
			context.Background(),
			request.InteractionID,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		encoded := interactionToIPC(*interaction)
		_ = encoder.Encode(ipc.Response{
			OK:          true,
			Interaction: &encoded,
		})

	case "interaction-answer":
		interaction, applied, err := stateDB.AnswerHumanInput(
			context.Background(),
			request.InteractionID,
			request.Answer,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		encoded := interactionToIPC(interaction)
		_ = encoder.Encode(ipc.Response{
			OK:            true,
			Interaction:   &encoded,
			AnswerApplied: applied,
		})

	case "session-events":
		limit := request.Limit
		if limit <= 0 {
			limit = 100
		}
		if limit > 1000 {
			limit = 1000
		}

		events, nextCursor, err := stateDB.ListSessionEvents(
			context.Background(),
			request.SessionID,
			request.AfterEventID,
			limit,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}

		ipcEvents := make([]ipc.SessionEvent, 0, len(events))
		for _, ev := range events {
			ipcEv := ipc.SessionEvent{
				ID:        ev.ID,
				SessionID: ev.SessionID,
				Type:      ev.Type,
				Timestamp: ev.Timestamp.UTC().Format(time.RFC3339Nano),
				Details:   ev.Details,
			}
			if ev.ExecutionID != nil {
				id := *ev.ExecutionID
				ipcEv.ExecutionID = &id
			}
			ipcEvents = append(ipcEvents, ipcEv)
		}

		_ = encoder.Encode(ipc.Response{
			OK:          true,
			Events:      ipcEvents,
			NextEventID: nextCursor,
		})

	default:
		_ = encoder.Encode(ipc.Response{
			OK:      false,
			Message: "unknown command",
		})
	}
}

// launchExecutionWorker reserves a worker slot and starts the deterministic
// fake worker for an execution under daemon-local ownership. Ownership is
// claimed before the goroutine is launched so a concurrent cancellation can
// always find and signal it. When the worker finishes it releases the slot and
// invokes notify so the dispatcher can start the next queued execution.
//
// On error the reserved slot is released; the caller remains responsible for
// reconciling the execution, which is already committed as RUNNING.
func launchExecutionWorker(
	ctx context.Context,
	stateDB *state.Database,
	workers *workerGroup,
	model *modelRuntime,
	execution state.SessionExecution,
	notify func(),
) error {
	workerCtx, cancel := context.WithCancel(ctx)

	if !workers.start() {
		cancel()
		return fmt.Errorf("daemon is shutting down")
	}

	if !workers.claim(execution.ID, cancel) {
		workers.done()
		cancel()
		return fmt.Errorf(
			"execution %d already has an active worker",
			execution.ID,
		)
	}

	go func() {
		defer cancel()
		switch execution.Kind {
		case "coding":
			if model == nil {
				fmt.Fprintf(
					os.Stderr,
					"forgehand: execution %d is a coding execution but no provider is configured\n",
					execution.ID,
				)
				failExecution(stateDB, execution.ID)
			} else {
				model.runModelWorker(workerCtx, stateDB, execution)
			}
		default:
			runFakeWorkerWithPause(
				workerCtx,
				stateDB,
				execution,
				fakeWorkerDelay,
				config.FakeWorkerPauseStep(),
			)
		}
		workers.release(execution.ID)
		workers.done()
		if notify != nil {
			notify()
		}
	}()

	return nil
}

func executionToIPC(execution state.SessionExecution) ipc.Execution {
	encoded := ipc.Execution{
		ID:          execution.ID,
		SessionID:   execution.SessionID,
		Status:      execution.Status,
		CurrentStep: execution.CurrentStep,
		TotalSteps:  execution.TotalSteps,
	}
	if execution.CanceledAt != nil {
		canceledAt := execution.CanceledAt.UTC().Format(time.RFC3339Nano)
		encoded.CanceledAt = &canceledAt
	}
	if execution.CancelReason != nil {
		encoded.CancelReason = execution.CancelReason
	}
	if execution.QueueSeq != nil {
		queueSeq := *execution.QueueSeq
		encoded.QueueSeq = &queueSeq
	}
	encoded.QueuePosition = execution.QueuePosition
	encoded.Kind = execution.Kind
	return encoded
}

func sessionToIPC(session state.Session) ipc.Session {
	encoded := ipc.Session{
		ID:       session.ID,
		Title:    session.Title,
		State:    session.State,
		Task:     session.Task,
		Kind:     session.Kind,
		Provider: session.Provider,
		Model:    session.Model,
	}
	if session.ProjectID != nil {
		projectID := *session.ProjectID
		encoded.ProjectID = &projectID
	}
	return encoded
}

// codingSessionTitle derives a non-empty title for a coding session, falling
// back to a truncated form of the task when no explicit title is provided.
func codingSessionTitle(title, task string) string {
	if trimmed := strings.TrimSpace(title); trimmed != "" {
		return trimmed
	}
	task = strings.TrimSpace(task)
	if len(task) > 80 {
		task = task[:80]
	}
	if task == "" {
		return "coding session"
	}
	return task
}

// codingModel selects the model for a coding session, preferring the request's
// explicit model and otherwise using the configured provider default.
func codingModel(requestModel, defaultModel string) string {
	if trimmed := strings.TrimSpace(requestModel); trimmed != "" {
		return trimmed
	}
	return defaultModel
}

func modelResultToIPC(result state.ModelResult) ipc.ModelResult {
	return ipc.ModelResult{
		SessionID:   result.SessionID,
		ExecutionID: result.ExecutionID,
		Provider:    result.Provider,
		Model:       result.Model,
		Content:     result.Content,
		CreatedAt:   result.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func interactionToIPC(interaction state.HumanInteraction) ipc.Interaction {
	encoded := ipc.Interaction{
		ID:          interaction.ID,
		SessionID:   interaction.SessionID,
		ExecutionID: interaction.ExecutionID,
		Question:    interaction.Question,
		Context:     interaction.Context,
		Status:      interaction.Status,
		CreatedAt:   interaction.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if interaction.Answer != nil {
		encoded.Answer = interaction.Answer
	}
	if interaction.AnsweredAt != nil {
		answeredAt := interaction.AnsweredAt.UTC().Format(time.RFC3339Nano)
		encoded.AnsweredAt = &answeredAt
	}
	return encoded
}

func runFakeWorker(
	ctx context.Context,
	store executionStore,
	execution state.SessionExecution,
	delay time.Duration,
) {
	runFakeWorkerWithPause(ctx, store, execution, delay, 0)
}

// runFakeWorkerWithPause drives a deterministic fake execution. When
// pauseAfterStep is greater than zero the worker requests human input after
// committing that checkpoint and returns, releasing its worker slot. It never
// stays alive while waiting and never repeats a committed checkpoint; the
// resumed worker continues from execution.CurrentStep.
func runFakeWorkerWithPause(
	ctx context.Context,
	store executionStore,
	execution state.SessionExecution,
	delay time.Duration,
	pauseAfterStep int,
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
			handleWorkerStop(store, execution, ctx)
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
			handleWorkerStop(store, execution, ctx)
			return
		}

		currentStep = step

		fmt.Printf(
			"Execution %d progress: %d/%d\n",
			execution.ID,
			currentStep,
			execution.TotalSteps,
		)

		if pauseAfterStep > 0 && step == pauseAfterStep {
			requestHumanInput(store, execution, step, execution.TotalSteps, ctx)
			return
		}
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
		handleWorkerStop(store, execution, ctx)
		return
	}

	fmt.Printf("Execution %d completed\n", execution.ID)
}

// handleWorkerStop reconciles an execution when a worker leaves its loop after
// a stop signal or an error. The database status is authoritative: a CANCELED
// (or otherwise terminal) execution is never overwritten. A still-RUNNING
// execution is interrupted when the stop was a daemon/worker cancellation, and
// failed when the worker hit a real error.
func handleWorkerStop(
	store executionStore,
	execution state.SessionExecution,
	ctx context.Context,
) {
	_, latest, err := store.SessionExecutionStatus(
		context.Background(),
		execution.SessionID,
	)
	if err != nil {
		fmt.Fprintf(
			os.Stderr,
			"forgehand: read execution %d status: %v\n",
			execution.ID,
			err,
		)
		return
	}
	if latest == nil {
		return
	}

	switch latest.Status {
	case state.ExecutionStatusRunning:
		if ctx.Err() != nil {
			interruptExecution(store, execution.ID)
		} else {
			failExecution(store, execution.ID)
		}
	default:
		// CANCELED, COMPLETED, FAILED, INTERRUPTED, or WAITING_FOR_USER are
		// already durable and must not be overwritten by a departing worker.
	}
}

// requestHumanInput persists the worker's pending question and transitions the
// execution to WAITING_FOR_USER. The execution is left resumable; the worker
// goroutine returns so waiting work consumes no active worker capacity.
func requestHumanInput(
	store executionStore,
	execution state.SessionExecution,
	step int,
	totalSteps int,
	ctx context.Context,
) {
	err := retryOnBusy(context.Background(), func() error {
		_, _, err := store.RequestHumanInput(
			context.Background(),
			execution.ID,
			fmt.Sprintf(
				"Fake worker paused after checkpoint %d of %d. Provide input to continue.",
				step,
				totalSteps,
			),
			map[string]any{
				"step":        step,
				"total_steps": totalSteps,
				"source":      "fake-worker",
			},
		)
		return err
	})
	if err != nil {
		fmt.Fprintf(
			os.Stderr,
			"forgehand: request human input for execution %d: %v\n",
			execution.ID,
			err,
		)
		handleWorkerStop(store, execution, ctx)
		return
	}

	fmt.Printf("Execution %d waiting for user input\n", execution.ID)
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
