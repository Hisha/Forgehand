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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Hisha/Forgehand/internal/config"
	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/planner"
	"github.com/Hisha/Forgehand/internal/provider"
	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
	"github.com/Hisha/Forgehand/internal/version"

	"github.com/Hisha/Forgehand/internal/workspace"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	fakeWorkerSteps             = 5
	fakeWorkerDelay             = 3 * time.Second
	maxQualificationRepetitions = 20

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
	FailSessionExecutionWithReason(ctx context.Context, executionID int64, failureKind string, failureReason string) error
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
	mu        sync.Mutex
	group     sync.WaitGroup
	stopped   bool
	owners    map[int64]context.CancelFunc
	resources map[int64][]string
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

// acquireResources records exclusive resource ownership for a claimed
// execution. It is all-or-nothing: when any required resource is already held
// by another execution, nothing is recorded and it returns false, so a claim
// can never partially hold its resource set. Callers pair it with claim and
// rely on release to free the resources with the execution ownership record.
func (w *workerGroup) acquireResources(executionID int64, resources []string) bool {
	if len(resources) == 0 {
		return true
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	for _, held := range w.resources {
		for _, have := range held {
			for _, want := range resources {
				if have == want {
					return false
				}
			}
		}
	}

	if w.resources == nil {
		w.resources = make(map[int64][]string)
	}
	w.resources[executionID] = append([]string(nil), resources...)
	return true
}

// heldResources returns a sorted snapshot of every exclusive resource currently
// held by running workers. The dispatcher passes it into claiming so a blocked
// execution is skipped, and status reporting uses it to name blockers.
func (w *workerGroup) heldResources() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	seen := make(map[string]struct{})
	for _, resources := range w.resources {
		for _, resource := range resources {
			seen[resource] = struct{}{}
		}
	}

	sorted := make([]string, 0, len(seen))
	for resource := range seen {
		sorted = append(sorted, resource)
	}
	sort.Strings(sorted)
	return sorted
}

// resourceBlock pairs a held exclusive resource with the execution that holds
// it, for status annotation of resource-blocked queued executions.
type resourceBlock struct {
	resource string
	holder   int64
}

// resourceBlocks reports which of the required resources are currently held by
// running executions, paired with the holding execution's ID and sorted by
// resource name for deterministic status output.
func (w *workerGroup) resourceBlocks(required []string) []resourceBlock {
	w.mu.Lock()
	defer w.mu.Unlock()

	holders := make(map[string]int64)
	for executionID, resources := range w.resources {
		for _, resource := range resources {
			holders[resource] = executionID
		}
	}

	var blocks []resourceBlock
	for _, resource := range required {
		if holder, ok := holders[resource]; ok {
			blocks = append(blocks, resourceBlock{resource: resource, holder: holder})
		}
	}
	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].resource < blocks[j].resource
	})
	return blocks
}

// release removes a worker's ownership record and frees its held resources so
// a later run or resume of the same execution is not blocked. Every worker
// exit path ends here, so finish, failure, cancellation, and human-wait all
// release resources at the same single point.
func (w *workerGroup) release(executionID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	delete(w.owners, executionID)
	delete(w.resources, executionID)
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
	Task                 config.TaskConfig
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
	taskConfig, err := config.Task(stateDir)
	if err != nil {
		return Config{}, err
	}
	return Config{
		StateDir:             stateDir,
		SocketPath:           socketPath,
		SocketMode:           socketMode,
		MaxConcurrentWorkers: maxWorkers,
		Provider:             providerConfig,
		Task:                 taskConfig,
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

	if err := prepareSocketPath(config.SocketPath); err != nil {
		return err
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
	if _, err := stateDB.RecoverRunningWorkPlanTasks(ctx); err != nil {
		return fmt.Errorf("recover interrupted work plan tasks: %w", err)
	}
	if _, err := stateDB.ReconcileInterruptedQualificationJobs(ctx); err != nil {
		return fmt.Errorf("recover interrupted qualification jobs: %w", err)
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

	task := buildTaskRuntime(model, config.Task)
	if task == nil {
		fmt.Printf("Task worker not configured; task sessions will fail\n")
	} else {
		fmt.Printf("Task workspaces: %s\n", task.workspacesDir)
	}

	var workers workerGroup
	dispatcher := newDispatcher(stateDB, &workers, config.MaxConcurrentWorkers, model, task)

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
	if request.ProtocolVersion != 0 && request.ProtocolVersion != ipc.ProtocolVersion {
		_ = encoder.Encode(ipc.Response{
			OK:      false,
			Message: fmt.Sprintf("incompatible IPC protocol: daemon supports %d, client requested %d", ipc.ProtocolVersion, request.ProtocolVersion),
		})
		return
	}
	if request.Command != "status" && request.ClientBuildIdentity != "" && request.ClientBuildIdentity != version.BuildIdentity() {
		_ = encoder.Encode(ipc.Response{
			OK:      false,
			Message: fmt.Sprintf("incompatible Forgehand build: daemon is %q, client is %q; update or restart the daemon", version.BuildIdentity(), request.ClientBuildIdentity),
		})
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

		blocked, err := stateDB.CountResourceBlockedQueuedExecutions(
			context.Background(),
			workers.heldResources(),
		)
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

		executable, _ := os.Executable()
		if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
			executable = resolved
		}
		socketPath, _ := config.SocketPath()
		stateDir, _ := config.StateDir()
		_ = encoder.Encode(ipc.Response{
			OK:                        true,
			Message:                   "running",
			Version:                   version.Version,
			BuildIdentity:             version.BuildIdentity(),
			PID:                       os.Getpid(),
			Executable:                executable,
			SocketPath:                socketPath,
			StateDir:                  stateDir,
			WorkerCapacity:            capacity,
			ActiveWorkers:             workers.ownerCount(),
			QueuedExecutions:          queued,
			ResourceBlockedExecutions: blocked,
		})

	case "models":
		configuration := ipc.ProviderConfiguration{Provider: provider.ProviderID}
		if disp != nil && disp.model != nil {
			configuration.Configured = true
			configuration.Endpoint = disp.model.endpoint
			configuration.Model = disp.model.defaultModel
		}
		_ = encoder.Encode(ipc.Response{OK: true, ProviderConfiguration: &configuration})

	case "qualification-create":
		if disp == nil || disp.model == nil {
			_ = encoder.Encode(ipc.Response{OK: false, Message: "no model provider is configured"})
			return
		}
		capability := strings.ToLower(strings.TrimSpace(request.Capability))
		if capability != planner.CapabilityPlanning {
			_ = encoder.Encode(ipc.Response{OK: false, Message: "only planning qualification is currently supported"})
			return
		}
		if request.Repetitions < 1 || request.Repetitions > maxQualificationRepetitions {
			_ = encoder.Encode(ipc.Response{OK: false, Message: fmt.Sprintf("repetitions must be between 1 and %d", maxQualificationRepetitions)})
			return
		}
		modelID := strings.TrimSpace(request.Model)
		if modelID == "" {
			modelID = disp.model.defaultModel
		}
		job, err := stateDB.CreateQualificationJob(context.Background(), modelID, disp.model.configuration, capability, request.Repetitions, len(planner.RepresentativeQualificationCases())*request.Repetitions)
		if err != nil {
			_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
			return
		}
		disp.notify()
		encoded := qualificationToIPC(job)
		_ = encoder.Encode(ipc.Response{OK: true, Qualification: &encoded})

	case "qualification-list":
		jobs, err := stateDB.ListQualificationJobs(context.Background(), request.Limit)
		if err != nil {
			_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
			return
		}
		encoded := make([]ipc.QualificationJob, 0, len(jobs))
		for _, job := range jobs {
			encoded = append(encoded, qualificationToIPC(job))
		}
		_ = encoder.Encode(ipc.Response{OK: true, Qualifications: encoded})

	case "qualification-get":
		job, err := stateDB.GetQualificationJob(context.Background(), request.QualificationID)
		if err != nil {
			_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
			return
		}
		encoded := qualificationToIPC(job)
		_ = encoder.Encode(ipc.Response{OK: true, Qualification: &encoded})

	case "qualification-cancel":
		job, err := stateDB.CancelQualificationJob(context.Background(), request.QualificationID, request.Reason)
		if err != nil {
			_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
			return
		}
		workers.cancel(job.ExecutionID)
		encoded := qualificationToIPC(job)
		_ = encoder.Encode(ipc.Response{OK: true, Qualification: &encoded, Canceled: true})

	case "qualification-retry":
		job, err := stateDB.RetryQualificationJob(context.Background(), request.QualificationID)
		if err != nil {
			_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
			return
		}
		if disp != nil {
			disp.notify()
		}
		encoded := qualificationToIPC(job)
		_ = encoder.Encode(ipc.Response{OK: true, Qualification: &encoded})

	case "session-start":
		if request.Kind == "work-plan" {
			if request.ProjectID <= 0 || strings.TrimSpace(request.Task) == "" {
				_ = encoder.Encode(ipc.Response{OK: false, Message: "a work plan requires a project id and request"})
				return
			}
			interpreted := planner.Interpret(request.Task)
			if len(interpreted.Tasks) == 1 && interpreted.Tasks[0].Method == planner.MethodSemanticPlanning {
				modelAvailable := disp != nil && disp.model != nil && disp.model.planningQualified
				decision := planner.AssessInvestigation(request.Task, request.Constraints)
				if decision.Eligible || decision.NeedsClarification {
					interpreted, _ = planner.InvestigationBootstrap(request.Task, request.Constraints, modelAvailable)
				} else {
					interpreted = planner.SemanticBootstrap(request.Task, modelAvailable)
				}
			}
			spec := state.WorkPlanSpec{Objective: interpreted.Objective, OriginalRequest: interpreted.Original, ReadOnly: interpreted.ReadOnly, Clarification: interpreted.Clarification, Constraints: append([]string(nil), request.Constraints...)}
			for _, task := range interpreted.Tasks {
				spec.Tasks = append(spec.Tasks, state.WorkPlanTaskSpec{Key: task.Key, Objective: task.Objective, ExecutionMethod: task.Method, Dependencies: task.DependsOn, Inputs: task.Inputs, RequiredEvidence: task.RequiredEvidence, CompletionCriteria: task.CompletionCriteria, RequiresModel: task.RequiresModel, BlockedReason: task.BlockedReason})
			}
			session, plan, err := stateDB.CreateWorkPlanSession(context.Background(), codingSessionTitle(request.Title, request.Task), request.ProjectID, spec)
			if err != nil {
				_ = encoder.Encode(ipc.Response{OK: false, Message: err.Error()})
				return
			}
			encodedSession, encodedPlan := sessionToIPC(session), workPlanToIPC(plan)
			_ = encoder.Encode(ipc.Response{OK: true, Session: &encodedSession, WorkPlan: &encodedPlan})
			return
		}
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
			var (
				session state.Session
				err     error
			)
			if request.Kind == "investigation" {
				if !modelRuntimeRef.investigationQualified {
					_ = encoder.Encode(ipc.Response{OK: false, Message: "configured model is not qualified for investigation; add investigation to FORGEHAND_PROVIDER_CAPABILITIES"})
					return
				}
				session, err = stateDB.CreateInvestigationSession(context.Background(), codingSessionTitle(request.Title, request.Task), request.ProjectID, request.Task, provider.ProviderID, codingModel(request.Model, modelRuntimeRef.defaultModel), request.Identity, request.AnalysisID)
			} else {
				session, err = stateDB.CreateCodingSession(context.Background(), codingSessionTitle(request.Title, request.Task), request.ProjectID, request.Task, provider.ProviderID, codingModel(request.Model, modelRuntimeRef.defaultModel))
			}
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
		totalSteps := fakeWorkerSteps
		if session, getErr := stateDB.GetSession(context.Background(), request.SessionID); getErr == nil && session.Kind == "investigation" {
			totalSteps = 1
		} else if getErr == nil && session.Kind == "work-plan" {
			plan, planErr := stateDB.GetWorkPlanBySession(context.Background(), request.SessionID)
			if planErr != nil || plan == nil {
				if planErr == nil {
					planErr = fmt.Errorf("session %d has no work plan", request.SessionID)
				}
				_ = encoder.Encode(ipc.Response{OK: false, Message: planErr.Error()})
				return
			}
			totalSteps = len(plan.Tasks)
		}
		execution, err := stateDB.QueueSessionExecution(
			context.Background(),
			request.SessionID,
			totalSteps,
			request.Resources...,
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
		annotateResourceBlocks(workers, &encoded)
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
		annotateResourceBlocks(workers, &encoded)
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
			// A task execution reports progress derived from its durable
			// phase rather than the placeholder step counter.
			if run, _ := stateDB.GetTaskRunByExecution(
				context.Background(),
				execution.ID,
			); run != nil {
				applyTaskProgress(&encoded, run.Phase)
			}
			annotateResourceBlocks(workers, &encoded)
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
		if session.Kind == "investigation" {
			if evidence, err := stateDB.InvestigationEvidence(context.Background(), session.ID); err == nil {
				encodedEvidence := investigationEvidenceToIPC(evidence)
				response.Evidence = &encodedEvidence
			}
		}
		if plan, err := stateDB.GetWorkPlanBySession(context.Background(), session.ID); err == nil && plan != nil {
			encodedPlan := workPlanToIPC(*plan)
			response.WorkPlan = &encodedPlan
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
		response := ipc.Response{OK: true, Result: &encoded}
		if evidence, err := stateDB.InvestigationEvidence(context.Background(), request.SessionID); err == nil {
			encodedEvidence := investigationEvidenceToIPC(evidence)
			response.Evidence = &encodedEvidence
		}
		_ = encoder.Encode(response)

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

	case "task-start":
		if request.ProjectID <= 0 {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: "a task requires a project id",
			})
			return
		}
		if strings.TrimSpace(request.Task) == "" {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: "a task requires a task description",
			})
			return
		}
		var taskRuntimeRef *taskRuntime
		if disp != nil {
			taskRuntimeRef = disp.task
		}
		if taskRuntimeRef == nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: "no model provider is configured",
			})
			return
		}
		if _, err := stateDB.GetProject(context.Background(), request.ProjectID); err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}
		projectID := request.ProjectID
		taskText := request.Task
		session, err := stateDB.CreateSessionWithDetails(
			context.Background(),
			codingSessionTitle(request.Title, request.Task),
			&projectID,
			&taskText,
			"task",
			provider.ProviderID,
			codingModel(request.Model, taskRuntimeRef.defaultModel),
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}
		execution, err := stateDB.QueueSessionExecution(
			context.Background(),
			session.ID,
			1,
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
		encodedSession := sessionToIPC(session)
		encodedExecution := executionToIPC(execution)
		applyTaskProgress(&encodedExecution, state.TaskPhasePrepared)
		_ = encoder.Encode(ipc.Response{
			OK:        true,
			Session:   &encodedSession,
			Execution: &encodedExecution,
		})

	case "task-status":
		run, err := stateDB.GetTaskRunBySession(
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
		if run == nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: fmt.Sprintf("session %d has no task run", request.SessionID),
			})
			return
		}
		operations, err := stateDB.ListTaskOperations(
			context.Background(),
			run.ID,
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}
		encodedRun := taskRunToIPC(*run)
		encodedOps := make([]ipc.TaskOperation, 0, len(operations))
		for _, op := range operations {
			encodedOps = append(encodedOps, taskOperationToIPC(op))
		}
		_ = encoder.Encode(ipc.Response{
			OK:             true,
			TaskRun:        &encodedRun,
			TaskOperations: encodedOps,
		})

	case "task-cleanup":
		run, err := stateDB.GetTaskRunBySession(
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
		if run == nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: fmt.Sprintf("session %d has no task run", request.SessionID),
			})
			return
		}
		if run.CleanedAt != nil || run.WorkspacePath == "" {
			_ = encoder.Encode(ipc.Response{
				OK:      true,
				Message: "workspace already cleaned",
			})
			return
		}
		// Never remove a workspace that a live worker is still using. Only
		// non-terminal executions can be actively reading or writing the
		// worktree; a completed/failed/canceled run's workspace is safe to
		// remove at the user's explicit request.
		_, latest, err := stateDB.SessionExecutionStatus(
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
		if latest != nil &&
			(latest.Status == state.ExecutionStatusRunning ||
				latest.Status == state.ExecutionStatusQueued) {
			_ = encoder.Encode(ipc.Response{
				OK: false,
				Message: fmt.Sprintf(
					"session %d has an active task execution; cancel it before cleaning up its workspace",
					request.SessionID,
				),
			})
			return
		}
		if err := workspace.Remove(
			context.Background(),
			run.RepoRoot,
			run.WorkspacePath,
		); err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}
		if err := stateDB.MarkTaskRunCleaned(
			context.Background(),
			run.ID,
		); err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}
		_ = encoder.Encode(ipc.Response{
			OK:      true,
			Message: fmt.Sprintf("removed workspace for task run %d", run.ID),
		})

	case "task-diff":
		run, err := stateDB.GetTaskRunBySession(
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
		if run == nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: fmt.Sprintf("session %d has no task run", request.SessionID),
			})
			return
		}
		encodedRun := taskRunToIPC(*run)
		response := ipc.Response{OK: true, TaskRun: &encodedRun}
		switch {
		case run.Applied || run.Phase == state.TaskPhaseApplied:
			// The authoritative applied diff is the persisted workspace diff.
			response.Diff = run.WorkspaceDiff
			response.DiffSource = "applied"
		case run.Phase == state.TaskPhaseProposed:
			diff, derr := renderProposedDiff(context.Background(), *run)
			if derr != nil {
				_ = encoder.Encode(ipc.Response{
					OK:      false,
					Message: derr.Error(),
				})
				return
			}
			response.Diff = diff
			response.DiffSource = "proposed"
		case run.Phase == state.TaskPhaseRejected:
			// A rejected proposal was never applied; show the proposal that was
			// declined so the history stays inspectable.
			diff, derr := renderProposedDiff(context.Background(), *run)
			if derr != nil {
				_ = encoder.Encode(ipc.Response{
					OK:      false,
					Message: derr.Error(),
				})
				return
			}
			response.Diff = diff
			response.DiffSource = "proposed"
		default:
			_ = encoder.Encode(ipc.Response{
				OK: false,
				Message: fmt.Sprintf(
					"session %d task is %s; no proposal or applied diff to show",
					request.SessionID,
					run.Phase,
				),
			})
			return
		}
		_ = encoder.Encode(response)

	case "task-approve":
		run, err := stateDB.GetTaskRunBySession(
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
		if run == nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: fmt.Sprintf("session %d has no task run", request.SessionID),
			})
			return
		}
		if run.Phase != state.TaskPhaseProposed {
			_ = encoder.Encode(ipc.Response{
				OK: false,
				Message: fmt.Sprintf(
					"session %d task is %s, not awaiting approval",
					request.SessionID,
					run.Phase,
				),
			})
			return
		}
		interaction, err := stateDB.GetPendingHumanInput(
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
		if interaction == nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: fmt.Sprintf("session %d has no pending approval interaction", request.SessionID),
			})
			return
		}
		// Guard the approval binding: the pending interaction must authorize
		// exactly the currently persisted proposal. A changed proposal is
		// refused so the user must review it again.
		if err := verifyApprovalBinding(*run, *interaction); err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}
		_, applied, err := stateDB.AnswerHumanInput(
			context.Background(),
			interaction.ID,
			"approve",
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}
		if !applied {
			// The interaction was already answered; report the current state
			// rather than silently authorizing a different proposal.
			_ = encoder.Encode(ipc.Response{
				OK:            true,
				AnswerApplied: false,
				Message:       fmt.Sprintf("interaction %d was already answered", interaction.ID),
			})
			return
		}
		if _, err := stateDB.QueueResumeAfterHumanInput(
			context.Background(),
			request.SessionID,
		); err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}
		if disp != nil {
			disp.notify()
		}
		updated, err := stateDB.GetTaskRunBySession(
			context.Background(),
			request.SessionID,
		)
		if err != nil || updated == nil {
			_ = encoder.Encode(ipc.Response{
				OK:      true,
				Message: "approved; resuming task",
			})
			return
		}
		encodedRun := taskRunToIPC(*updated)
		_ = encoder.Encode(ipc.Response{
			OK:            true,
			AnswerApplied: true,
			TaskRun:       &encodedRun,
			Message:       "approved; resuming task",
		})

	case "task-reject":
		run, err := stateDB.GetTaskRunBySession(
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
		if run == nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: fmt.Sprintf("session %d has no task run", request.SessionID),
			})
			return
		}
		if run.Phase != state.TaskPhaseProposed {
			_ = encoder.Encode(ipc.Response{
				OK: false,
				Message: fmt.Sprintf(
					"session %d task is %s, not awaiting approval",
					request.SessionID,
					run.Phase,
				),
			})
			return
		}
		interaction, err := stateDB.GetPendingHumanInput(
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
		if interaction == nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: fmt.Sprintf("session %d has no pending approval interaction", request.SessionID),
			})
			return
		}
		_, applied, err := stateDB.AnswerHumanInput(
			context.Background(),
			interaction.ID,
			"reject",
		)
		if err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}
		if !applied {
			_ = encoder.Encode(ipc.Response{
				OK:            true,
				AnswerApplied: false,
				Message:       fmt.Sprintf("interaction %d was already answered", interaction.ID),
			})
			return
		}
		if _, err := stateDB.QueueResumeAfterHumanInput(
			context.Background(),
			request.SessionID,
		); err != nil {
			_ = encoder.Encode(ipc.Response{
				OK:      false,
				Message: err.Error(),
			})
			return
		}
		if disp != nil {
			disp.notify()
		}
		updated, err := stateDB.GetTaskRunBySession(
			context.Background(),
			request.SessionID,
		)
		if err != nil || updated == nil {
			_ = encoder.Encode(ipc.Response{
				OK:      true,
				Message: "rejected; resuming task",
			})
			return
		}
		encodedRun := taskRunToIPC(*updated)
		_ = encoder.Encode(ipc.Response{
			OK:            true,
			AnswerApplied: true,
			TaskRun:       &encodedRun,
			Message:       "rejected; resuming task",
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
	task *taskRuntime,
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

	if !workers.acquireResources(execution.ID, execution.Resources) {
		workers.release(execution.ID)
		workers.done()
		cancel()
		return fmt.Errorf(
			"execution %d could not acquire resources %v",
			execution.ID,
			execution.Resources,
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
				failExecutionWithReason(stateDB, execution.ID, state.FailureKindProvider, "provider is not configured")
			} else {
				model.runModelWorker(workerCtx, stateDB, execution)
			}
		case "investigation":
			if model == nil {
				fmt.Fprintf(os.Stderr, "forgehand: execution %d is an investigation but no provider is configured\n", execution.ID)
				failExecutionWithReason(stateDB, execution.ID, state.FailureKindProvider, "provider is not configured")
			} else {
				model.runInvestigationWorker(workerCtx, stateDB, execution)
			}
		case "work-plan":
			runWorkPlanWorkerWithModel(workerCtx, stateDB, model, execution)
		case "qualification":
			if model == nil {
				if job, err := stateDB.GetQualificationJobByExecution(context.Background(), execution.ID); err == nil {
					_ = stateDB.MarkQualificationRunning(context.Background(), job.ID)
					_ = stateDB.FailQualificationJob(context.Background(), job.ID, state.FailureKindProvider, "provider is not configured")
				} else {
					failExecutionWithReason(stateDB, execution.ID, state.FailureKindProvider, "provider is not configured")
				}
			} else {
				model.runQualificationWorker(workerCtx, stateDB, execution)
			}
		case "task":
			if task == nil {
				fmt.Fprintf(
					os.Stderr,
					"forgehand: execution %d is a task execution but no provider is configured\n",
					execution.ID,
				)
				failExecutionWithReason(stateDB, execution.ID, state.FailureKindProvider, "provider is not configured")
			} else {
				task.runTaskWorker(workerCtx, stateDB, execution)
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

// annotateResourceBlocks attaches to a QUEUED execution's IPC encoding the
// currently held resources that keep it from dispatching, paired with the
// holding execution's ID. It is a no-op for executions in any other status.
// A block naming the execution itself is skipped: that only occurs in the
// instant between a state read and the dispatcher's claim.
func annotateResourceBlocks(workers *workerGroup, execution *ipc.Execution) {
	if execution == nil || execution.Status != state.ExecutionStatusQueued {
		return
	}

	for _, block := range workers.resourceBlocks(execution.Resources) {
		if block.holder == execution.ID {
			continue
		}
		execution.BlockedBy = append(execution.BlockedBy, ipc.ResourceBlock{
			Resource:          block.resource,
			HeldByExecutionID: block.holder,
		})
	}
}

func workPlanToIPC(plan state.WorkPlan) ipc.WorkPlan {
	out := ipc.WorkPlan{ID: plan.ID, ProjectID: plan.ProjectID, SessionID: plan.SessionID, Objective: plan.Objective, OriginalRequest: plan.OriginalRequest, ReadOnly: plan.ReadOnly, Status: plan.Status, Clarification: plan.Clarification, Constraints: append([]string(nil), plan.Constraints...), UnresolvedQuestions: append([]string(nil), plan.UnresolvedQuestions...), Revision: plan.Revision, CommitHash: plan.CommitHash}
	for _, task := range plan.Tasks {
		encoded := ipc.WorkPlanTask{ID: task.ID, Key: task.Key, Position: task.Position, Objective: task.Objective, ExecutionMethod: task.ExecutionMethod, Dependencies: append([]string(nil), task.Dependencies...), RequiredEvidence: task.RequiredEvidence, CompletionCriteria: task.CompletionCriteria, Status: task.Status, RequiresModel: task.RequiresModel, Result: task.Result, BlockedReason: task.BlockedReason, FailureReason: task.FailureReason}
		for _, evidence := range task.Evidence {
			encoded.Evidence = append(encoded.Evidence, ipc.WorkEvidence{Kind: evidence.Kind, Path: evidence.Path, BlobHash: evidence.BlobHash, Line: evidence.Line, EndLine: evidence.EndLine, Excerpt: evidence.Excerpt, Detail: evidence.Detail, Topics: append([]string(nil), evidence.Topics...)})
		}
		out.Tasks = append(out.Tasks, encoded)
	}
	return out
}

func qualificationToIPC(job state.QualificationJob) ipc.QualificationJob {
	encoded := ipc.QualificationJob{ID: job.ID, Model: job.Model, ProviderFingerprint: job.ProviderFingerprint, Capability: job.Capability, Repetitions: job.Repetitions, TotalEvaluations: job.TotalEvaluations, CompletedEvaluations: job.CompletedEvaluations, Status: job.Status, Recommendation: job.Recommendation, FailureKind: job.FailureKind, FailureReason: job.FailureReason}
	if job.StartedAt != nil {
		encoded.StartedAt = job.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if job.CompletedAt != nil {
		encoded.CompletedAt = job.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if job.AggregateJSON != "" {
		var aggregate struct {
			Aggregate   ipc.QualificationAggregate     `json:"aggregate"`
			Consistency []ipc.QualificationConsistency `json:"consistency"`
		}
		if json.Unmarshal([]byte(job.AggregateJSON), &aggregate) == nil {
			encoded.Aggregate, encoded.Consistency = aggregate.Aggregate, aggregate.Consistency
		}
	}
	for _, stored := range job.Results {
		var result ipc.QualificationResult
		if json.Unmarshal([]byte(stored.ResultJSON), &result) == nil {
			encoded.Results = append(encoded.Results, result)
		}
	}
	return encoded
}

func executionToIPC(execution state.SessionExecution) ipc.Execution {
	encoded := ipc.Execution{
		ID:            execution.ID,
		SessionID:     execution.SessionID,
		Status:        execution.Status,
		CurrentStep:   execution.CurrentStep,
		TotalSteps:    execution.TotalSteps,
		FailureKind:   execution.FailureKind,
		FailureReason: execution.FailureReason,
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
	encoded.Resources = append([]string(nil), execution.Resources...)
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
		SessionID:        result.SessionID,
		ExecutionID:      result.ExecutionID,
		Provider:         result.Provider,
		Model:            result.Model,
		Content:          result.Content,
		CreatedAt:        result.CreatedAt.UTC().Format(time.RFC3339Nano),
		PromptTokens:     result.PromptTokens,
		CompletionTokens: result.CompletionTokens,
		TotalTokens:      result.TotalTokens,
		LatencyMS:        result.LatencyMS,
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
			handleWorkerStopWithFailure(store, execution, ctx, state.FailureKindWorker, fmt.Sprintf("advance execution step %d: %v", step, err))
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
		handleWorkerStopWithFailure(store, execution, ctx, state.FailureKindWorker, "complete execution: "+err.Error())
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
	handleWorkerStopWithFailure(
		store,
		execution,
		ctx,
		state.FailureKindWorker,
		"worker failed without a more specific diagnostic",
	)
}

func handleWorkerStopWithFailure(
	store executionStore,
	execution state.SessionExecution,
	ctx context.Context,
	failureKind string,
	failureReason string,
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
			failExecutionWithReason(store, execution.ID, failureKind, failureReason)
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
		handleWorkerStopWithFailure(store, execution, ctx, state.FailureKindWorker, "request human input: "+err.Error())
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
	failExecutionWithReason(
		store,
		executionID,
		state.FailureKindWorker,
		"worker failed without a more specific diagnostic",
	)
}

func failExecutionWithReason(
	store executionStore,
	executionID int64,
	failureKind string,
	failureReason string,
) {
	if err := store.FailSessionExecutionWithReason(
		context.Background(),
		executionID,
		failureKind,
		failureReason,
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
