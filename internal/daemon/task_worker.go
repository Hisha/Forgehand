package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hisha/Forgehand/internal/config"
	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/provider"
	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
	"github.com/Hisha/Forgehand/internal/task"
	"github.com/Hisha/Forgehand/internal/workspace"
)

// taskRuntime holds the dependencies a controlled coding-task worker needs. It
// reuses the configured provider client and carries the limits and workspaces
// directory resolved from configuration.
type taskRuntime struct {
	provider      provider.Provider
	providerID    string
	defaultModel  string
	workspacesDir string
	limits        task.Limits
	// validate enables the opt-in post-apply Go format-and-test stage. It is
	// disabled by default: running project tests executes repository content.
	validate        bool
	validateTimeout time.Duration
}

// buildTaskRuntime constructs a task runtime from the model runtime. It returns
// nil when no provider is configured, so task executions fail closed rather than
// silently doing nothing.
func buildTaskRuntime(model *modelRuntime, taskConfig config.TaskConfig) *taskRuntime {
	if model == nil {
		return nil
	}
	return &taskRuntime{
		provider:      model.provider,
		providerID:    model.providerID,
		defaultModel:  model.defaultModel,
		workspacesDir: taskConfig.WorkspacesDir,
		limits: task.Limits{
			MaxIterations:     taskConfig.MaxIterations,
			MaxFileBytes:      taskConfig.MaxFileBytes,
			MaxTotalReadBytes: taskConfig.MaxTotalReadBytes,
			MaxChanges:        taskConfig.MaxChanges,
			MaxSearchResults:  taskConfig.MaxSearchResults,
		},
		validate:        taskConfig.Validate,
		validateTimeout: taskConfig.ValidateTimeout,
	}
}

// runTaskWorker executes a single controlled coding-task execution. It is
// phase-aware: a run whose proposal is awaiting approval is resolved rather than
// rerun, and an already-terminal run is never re-executed. The worker never
// applies changes without a recorded human authorization that is bound to the
// exact persisted proposal.
func (r *taskRuntime) runTaskWorker(
	ctx context.Context,
	store *state.Database,
	execution state.SessionExecution,
) {
	session, err := store.GetSession(ctx, execution.SessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: load task session %d: %v\n", execution.SessionID, err)
		failExecutionWithReason(store, execution.ID, state.FailureKindWorker, "load task session: "+err.Error())
		return
	}
	if session.ProjectID == nil {
		fmt.Fprintf(os.Stderr, "forgehand: task session %d has no project\n", session.ID)
		failExecutionWithReason(store, execution.ID, state.FailureKindValidation, "task session has no project")
		return
	}
	project, err := store.GetProject(ctx, *session.ProjectID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: load project %d: %v\n", *session.ProjectID, err)
		failExecutionWithReason(store, execution.ID, state.FailureKindRepositoryContext, "load task project: "+err.Error())
		return
	}

	existing, err := store.GetTaskRunByExecution(ctx, execution.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: load task run for execution %d: %v\n", execution.ID, err)
		failExecutionWithReason(store, execution.ID, state.FailureKindWorker, "load task run: "+err.Error())
		return
	}

	if existing != nil {
		switch existing.Phase {
		case state.TaskPhaseProposed:
			// The proposal was already recorded and (possibly) answered; never
			// rerun the model loop for an existing proposal.
			r.resolveProposal(ctx, store, execution, *existing)
			return
		case state.TaskPhaseApplied, state.TaskPhaseCompleted:
			r.finalizeCompleted(ctx, store, execution, *existing)
			return
		case state.TaskPhaseRejected:
			r.finalizeRejected(ctx, store, execution, *existing)
			return
		case state.TaskPhaseFailed:
			failTask(store, execution, *existing, existing.Error)
			return
		}
		// PREPARED or EXECUTING: no durable side effect yet, so it is safe to
		// (re)run the loop.
	}

	r.executeTask(ctx, store, execution, session, project, existing)
}

// executeTask runs the bounded task loop inside a fresh isolated workspace and
// records either a completed (no-change) run or a proposal awaiting approval.
func (r *taskRuntime) executeTask(
	ctx context.Context,
	store *state.Database,
	execution state.SessionExecution,
	session state.Session,
	project state.Project,
	existing *state.TaskRun,
) {
	baseCommit, hasHead, err := repository.HeadCommit(ctx, project.RootPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: read base commit for project %d: %v\n", project.ID, err)
		failTask(store, execution, emptyRun(session, execution, project), err.Error())
		return
	}
	if !hasHead {
		failTask(store, execution, emptyRun(session, execution, project), "repository has no commits")
		return
	}

	run := emptyRun(session, execution, project)
	if existing != nil {
		run = *existing
	}
	if run.BaseCommit == "" {
		run.BaseCommit = baseCommit
	}
	run.WorkspacePath = filepath.Join(r.workspacesDir, fmt.Sprintf("session-%d", session.ID))

	if err := workspace.Ensure(ctx, project.RootPath, run.BaseCommit, run.WorkspacePath); err != nil {
		if ctx.Err() != nil {
			handleWorkerStop(store, execution, ctx)
			return
		}
		fmt.Fprintf(os.Stderr, "forgehand: prepare workspace for execution %d: %v\n", execution.ID, err)
		failTask(store, execution, run, err.Error())
		return
	}

	ws, err := workspace.Open(run.WorkspacePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: open workspace for execution %d: %v\n", execution.ID, err)
		failTask(store, execution, run, err.Error())
		return
	}

	fingerprint, err := repository.WorkingTreeFingerprint(ctx, run.WorkspacePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: fingerprint workspace for execution %d: %v\n", execution.ID, err)
		failTask(store, execution, run, err.Error())
		return
	}

	run.Phase = state.TaskPhaseExecuting
	run.WorkspaceFingerprint = fingerprint
	run.Error = ""

	saved, err := store.PrepareTaskRun(ctx, run)
	if err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: record task run for execution %d: %v\n", execution.ID, err)
		failExecutionWithReason(store, execution.ID, state.FailureKindWorker, "persist task run: "+err.Error())
		return
	}

	runner := &task.Runner{
		Provider: r.provider,
		Limits:   r.limits,
		OnOperation: func(op task.Operation) {
			if err := store.RecordTaskOperation(
				context.Background(),
				saved.ID,
				state.TaskOperation{
					Kind:    op.Kind,
					Path:    op.Path,
					Detail:  op.Detail,
					Outcome: op.Outcome,
					Error:   op.Error,
				},
			); err != nil {
				fmt.Fprintf(os.Stderr, "forgehand: record task operation for run %d: %v\n", saved.ID, err)
			}
		},
	}

	result, err := runner.Run(ctx, ws, task.Contract{
		ProjectID:  project.ID,
		RepoRoot:   project.RootPath,
		Task:       session.Task,
		BaseCommit: run.BaseCommit,
		Model:      session.Model,
	})
	if err != nil {
		if ctx.Err() != nil {
			handleWorkerStop(store, execution, ctx)
			return
		}
		fmt.Fprintf(os.Stderr, "forgehand: task execution %d failed: %v\n", execution.ID, err)
		failTask(store, execution, saved, err.Error())
		return
	}

	if len(result.Changes) == 0 {
		saved.Phase = state.TaskPhaseCompleted
		saved.Applied = false
		saved.Summary = result.Summary
		saved.WorkspaceDiff = ""
		if err := retryOnBusy(ctx, func() error {
			return store.CompleteTaskExecution(context.Background(), execution.ID, saved)
		}); err != nil {
			fmt.Fprintf(os.Stderr, "forgehand: complete task execution %d: %v\n", execution.ID, err)
			handleWorkerStopWithFailure(store, execution, ctx, state.FailureKindWorker, "persist completed task: "+err.Error())
			return
		}
		fmt.Printf("Execution %d completed with no changes\n", execution.ID)
		return
	}

	saved.Phase = state.TaskPhaseProposed
	saved.Applied = false
	saved.Summary = result.Summary
	saved.ProposalJSON = task.EncodeChanges(result.Changes)
	saved.ProposalHash = task.ProposalHash(result.Summary, result.Changes)
	// The proposal-time fingerprint is the clean workspace state that apply
	// re-verifies, so an approved proposal can never be applied to a workspace
	// that changed after the proposal was recorded.
	saved.WorkspaceFingerprint = fingerprint

	question := fmt.Sprintf(
		"Task %d proposes changes to %d file(s). Reply \"approve\" to apply or \"reject\" to discard.",
		session.ID,
		len(result.Changes),
	)
	contextDetails := map[string]any{
		"execution_id":          execution.ID,
		"session_id":            session.ID,
		"proposal_hash":         saved.ProposalHash,
		"base_commit":           saved.BaseCommit,
		"workspace_fingerprint": saved.WorkspaceFingerprint,
		"change_count":          len(result.Changes),
		"summary":               saved.Summary,
		"changes":               changesForIPC(saved.ProposalJSON),
	}

	interaction, created, err := store.RequestTaskApproval(
		ctx,
		execution.ID,
		saved,
		question,
		contextDetails,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: request task approval for execution %d: %v\n", execution.ID, err)
		handleWorkerStopWithFailure(store, execution, ctx, state.FailureKindWorker, "request task approval: "+err.Error())
		return
	}
	if created {
		fmt.Printf(
			"Execution %d proposed changes; awaiting approval (interaction %d)\n",
			execution.ID,
			interaction.ID,
		)
	}
}

// resolveProposal applies or discards an authorized proposal. It re-verifies
// that the authorization is bound to the exact persisted proposal and that the
// workspace is unchanged since the proposal was recorded, so a stale or
// mismatched approval can never authorize different changes.
func (r *taskRuntime) resolveProposal(
	ctx context.Context,
	store *state.Database,
	execution state.SessionExecution,
	run state.TaskRun,
) {
	interaction, err := store.LatestHumanInteractionForExecution(ctx, execution.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: load approval for execution %d: %v\n", execution.ID, err)
		failTask(store, execution, run, err.Error())
		return
	}
	if interaction == nil {
		failTask(store, execution, run, "proposal has no recorded approval interaction")
		return
	}
	if interaction.Status != state.InteractionStatusAnswered {
		failTask(store, execution, run, fmt.Sprintf(
			"proposal awaiting approval but interaction %d is %s",
			interaction.ID,
			interaction.Status,
		))
		return
	}

	answer := ""
	if interaction.Answer != nil {
		answer = strings.ToLower(strings.TrimSpace(*interaction.Answer))
	}

	if answer != "approve" {
		if err := retryOnBusy(ctx, func() error {
			return store.RejectTaskExecution(
				context.Background(),
				execution.ID,
				run,
				"proposal was not approved",
			)
		}); err != nil {
			fmt.Fprintf(os.Stderr, "forgehand: reject task execution %d: %v\n", execution.ID, err)
			handleWorkerStopWithFailure(store, execution, ctx, state.FailureKindWorker, "persist task rejection: "+err.Error())
			return
		}
		fmt.Printf("Execution %d rejected proposal; no changes applied\n", execution.ID)
		return
	}

	if err := r.verifyProposal(ctx, run, *interaction); err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: refuse stale task approval for execution %d: %v\n", execution.ID, err)
		if rerr := retryOnBusy(ctx, func() error {
			return store.RejectTaskExecution(context.Background(), execution.ID, run, err.Error())
		}); rerr != nil {
			fmt.Fprintf(os.Stderr, "forgehand: reject stale task execution %d: %v\n", execution.ID, rerr)
			handleWorkerStopWithFailure(store, execution, ctx, state.FailureKindWorker, "persist stale task rejection: "+rerr.Error())
		}
		return
	}

	changes, err := task.DecodeChanges(run.ProposalJSON)
	if err != nil {
		failTask(store, execution, run, err.Error())
		return
	}

	ws, err := workspace.Open(run.WorkspacePath)
	if err != nil {
		failTask(store, execution, run, err.Error())
		return
	}

	if err := ws.Apply(changes); err != nil {
		failTask(store, execution, run, "apply changes: "+err.Error())
		return
	}

	// Run the opt-in, bounded validation stage on the applied changes before
	// computing the diff: formatting rewrites files in place, so the diff must
	// be rendered afterwards to include those changes. A validation failure
	// does not roll the changes back: the workspace is preserved for inspection
	// and the failure is recorded on the run so `task status` can surface it.
	run.Phase = state.TaskPhaseApplied
	run.Applied = true
	run.Error = ""
	r.runValidation(ctx, ws, &run, changes)

	diff, err := ws.Diff(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: render diff for execution %d: %v\n", execution.ID, err)
		diff = ""
	}
	run.WorkspaceDiff = diff

	if err := retryOnBusy(ctx, func() error {
		return store.CompleteTaskExecution(context.Background(), execution.ID, run)
	}); err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: complete applied task execution %d: %v\n", execution.ID, err)
		handleWorkerStopWithFailure(store, execution, ctx, state.FailureKindWorker, "persist applied task: "+err.Error())
		return
	}

	switch run.ValidationStatus {
	case state.TaskValidationFailed, state.TaskValidationError:
		fmt.Printf(
			"Execution %d applied approved changes; validation %s\n",
			execution.ID,
			run.ValidationStatus,
		)
	default:
		fmt.Printf("Execution %d applied approved changes\n", execution.ID)
	}
}

// runValidation executes the post-apply validation stage when it is enabled and
// records the outcome on the run. When validation is disabled the run is marked
// skipped so `task status` can distinguish "not validated" from "validated".
func (r *taskRuntime) runValidation(
	ctx context.Context,
	ws *workspace.Workspace,
	run *state.TaskRun,
	changes []workspace.Change,
) {
	if !r.validate {
		run.ValidationStatus = state.TaskValidationSkipped
		run.ValidationJSON = ""
		return
	}

	validator := &task.Validator{Timeout: r.validateTimeout}
	result, err := validator.Validate(ctx, ws, changes)

	detail, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		run.ValidationStatus = state.TaskValidationError
		run.ValidationJSON = ""
		fmt.Fprintf(os.Stderr, "forgehand: marshal validation result: %v\n", marshalErr)
		return
	}
	run.ValidationJSON = string(detail)

	switch {
	case err != nil:
		run.ValidationStatus = state.TaskValidationError
	case result.Status == "failed":
		run.ValidationStatus = state.TaskValidationFailed
	case result.Status == "skipped":
		run.ValidationStatus = state.TaskValidationSkipped
	default:
		run.ValidationStatus = state.TaskValidationPassed
	}
}

// verifyProposal checks that an approval authorizes exactly the persisted
// proposal and that the workspace is still in the state the proposal was made
// against. It fails closed on any mismatch.
func (r *taskRuntime) verifyProposal(
	ctx context.Context,
	run state.TaskRun,
	interaction state.HumanInteraction,
) error {
	if interaction.Context == nil {
		return fmt.Errorf("approval is not bound to a proposal")
	}
	hash, _ := interaction.Context["proposal_hash"].(string)
	if hash == "" {
		return fmt.Errorf("approval is missing the proposal hash")
	}
	if hash != run.ProposalHash {
		return fmt.Errorf("approval does not match the recorded proposal")
	}

	changes, err := task.DecodeChanges(run.ProposalJSON)
	if err != nil {
		return err
	}
	if task.ProposalHash(run.Summary, changes) != run.ProposalHash {
		return fmt.Errorf("recorded proposal was altered after approval")
	}

	if run.WorkspacePath == "" || !workspace.Exists(run.WorkspacePath) {
		return fmt.Errorf("workspace no longer exists")
	}

	head, hasHead, err := repository.HeadCommit(ctx, run.WorkspacePath)
	if err != nil {
		return err
	}
	if !hasHead || head != run.BaseCommit {
		return fmt.Errorf("workspace is no longer at the proposal base commit")
	}

	fingerprint, err := repository.WorkingTreeFingerprint(ctx, run.WorkspacePath)
	if err != nil {
		return err
	}
	if run.WorkspaceFingerprint != "" && fingerprint != run.WorkspaceFingerprint {
		return fmt.Errorf("workspace changed after the proposal was recorded")
	}

	return nil
}

// finalizeCompleted reconciles an execution whose run is already applied or
// completed from a prior worker, without re-running the loop.
func (r *taskRuntime) finalizeCompleted(
	ctx context.Context,
	store *state.Database,
	execution state.SessionExecution,
	run state.TaskRun,
) {
	if err := retryOnBusy(ctx, func() error {
		return store.CompleteTaskExecution(context.Background(), execution.ID, run)
	}); err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: finalize completed task execution %d: %v\n", execution.ID, err)
		handleWorkerStopWithFailure(store, execution, ctx, state.FailureKindWorker, "finalize completed task: "+err.Error())
	}
}

// finalizeRejected reconciles an execution whose proposal was already rejected
// from a prior worker.
func (r *taskRuntime) finalizeRejected(
	ctx context.Context,
	store *state.Database,
	execution state.SessionExecution,
	run state.TaskRun,
) {
	if err := retryOnBusy(ctx, func() error {
		return store.RejectTaskExecution(context.Background(), execution.ID, run, run.Error)
	}); err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: finalize rejected task execution %d: %v\n", execution.ID, err)
		handleWorkerStopWithFailure(store, execution, ctx, state.FailureKindWorker, "finalize rejected task: "+err.Error())
	}
}

// failTask records a task failure, falling back to plain execution
// reconciliation when the run is not in a state that permits finalization.
func failTask(
	store *state.Database,
	execution state.SessionExecution,
	run state.TaskRun,
	cause string,
) {
	cause = strings.TrimSpace(cause)
	if cause == "" {
		cause = "task failed"
	}
	if err := retryOnBusy(context.Background(), func() error {
		return store.FailTaskExecution(context.Background(), execution.ID, run, cause)
	}); err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: fail task execution %d: %v\n", execution.ID, err)
		failExecutionWithReason(store, execution.ID, state.FailureKindWorker, "persist task failure: "+err.Error())
	}
}

func emptyRun(
	session state.Session,
	execution state.SessionExecution,
	project state.Project,
) state.TaskRun {
	return state.TaskRun{
		SessionID:   session.ID,
		ExecutionID: execution.ID,
		ProjectID:   project.ID,
		RepoRoot:    project.RootPath,
	}
}

// changesForIPC returns the proposed changes as a display-friendly slice.
func changesForIPC(proposalJSON string) []map[string]any {
	changes, err := task.DecodeChanges(proposalJSON)
	if err != nil {
		return nil
	}
	display := make([]map[string]any, 0, len(changes))
	for _, change := range changes {
		display = append(display, map[string]any{
			"op":   change.Op,
			"path": change.Path,
		})
	}
	return display
}

// taskRunToIPC converts a durable task run into its IPC representation.
func taskRunToIPC(run state.TaskRun) ipc.TaskRun {
	encoded := ipc.TaskRun{
		ID:            run.ID,
		SessionID:     run.SessionID,
		ExecutionID:   run.ExecutionID,
		ProjectID:     run.ProjectID,
		WorkspacePath: run.WorkspacePath,
		BaseCommit:    run.BaseCommit,
		Phase:         run.Phase,
		Applied:       run.Applied,
		Summary:       run.Summary,
		ProposalHash:  run.ProposalHash,
		WorkspaceDiff: run.WorkspaceDiff,
		Error:         run.Error,
		CreatedAt:     run.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:     run.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if run.ValidationStatus != "" {
		encoded.ValidationStatus = run.ValidationStatus
		if run.ValidationJSON != "" {
			encoded.Validation = json.RawMessage(run.ValidationJSON)
		}
	}
	if run.CleanedAt != nil {
		cleanedAt := run.CleanedAt.UTC().Format(time.RFC3339Nano)
		encoded.CleanedAt = &cleanedAt
	}

	if run.ProposalJSON != "" && run.ProposalJSON != "[]" {
		changes, err := task.DecodeChanges(run.ProposalJSON)
		if err == nil {
			encoded.Changes = make([]ipc.TaskChange, 0, len(changes))
			for _, change := range changes {
				encoded.Changes = append(encoded.Changes, ipc.TaskChange{
					Op:   change.Op,
					Path: change.Path,
				})
			}
		}
	}

	return encoded
}

// taskOperationToIPC converts a recorded task operation into its IPC form.
func taskOperationToIPC(op state.TaskOperation) ipc.TaskOperation {
	return ipc.TaskOperation{
		Sequence:  op.Sequence,
		Kind:      op.Kind,
		Path:      op.Path,
		Detail:    op.Detail,
		Outcome:   op.Outcome,
		Error:     op.Error,
		CreatedAt: op.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// renderProposedDiff renders a task run's persisted proposal as a unified diff
// against the current (clean) workspace content, without mutating the
// workspace. It is the authoritative preview of exactly what an approval would
// authorize.
func renderProposedDiff(ctx context.Context, run state.TaskRun) (string, error) {
	changes, err := task.DecodeChanges(run.ProposalJSON)
	if err != nil {
		return "", fmt.Errorf("decode proposal: %w", err)
	}
	if len(changes) == 0 {
		return "", nil
	}
	if run.WorkspacePath == "" || !workspace.Exists(run.WorkspacePath) {
		return "", fmt.Errorf(
			"workspace for task run %d is no longer available; cannot render a proposal preview",
			run.ID,
		)
	}
	ws, err := workspace.Open(run.WorkspacePath)
	if err != nil {
		return "", fmt.Errorf("open workspace: %w", err)
	}
	reader := func(path string) (string, bool, error) {
		data, err := ws.Read(path, 0)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", false, nil
			}
			return "", false, err
		}
		return string(data), true, nil
	}
	return task.RenderProposalDiff(ctx, changes, reader)
}

// verifyApprovalBinding checks that a pending approval interaction authorizes
// exactly the currently persisted proposal. It fails closed when the binding is
// missing or mismatched, so a stale approval can never authorize different
// changes.
func verifyApprovalBinding(run state.TaskRun, interaction state.HumanInteraction) error {
	if interaction.Context == nil {
		return fmt.Errorf("approval interaction is not bound to a proposal")
	}
	hash, _ := interaction.Context["proposal_hash"].(string)
	if hash == "" {
		return fmt.Errorf("approval interaction is missing the proposal hash")
	}
	if run.ProposalHash == "" {
		return fmt.Errorf("task run has no recorded proposal hash")
	}
	if hash != run.ProposalHash {
		return fmt.Errorf(
			"the proposal has changed since this approval was created; review it again with task diff",
		)
	}
	return nil
}

// applyTaskProgress overrides an execution's reported step progress with
// progress derived from its durable task phase. Task executions are not driven
// by artificial step increments (the worker never advances CurrentStep), so the
// phase is the source of truth for how far the task has progressed. Non-task
// executions keep their existing step-counter progress.
func applyTaskProgress(encoded *ipc.Execution, taskPhase string) {
	encoded.CurrentStep, encoded.TotalSteps = taskProgressFromPhase(taskPhase)
}

// taskProgressFromPhase maps a task run phase to (current, total) steps. A
// three-stage lifecycle is reported: prepare, propose/approve, apply. A
// terminal phase reports full progress so a completed task no longer shows
// 0/1.
func taskProgressFromPhase(phase string) (current int, total int) {
	const totalSteps = 3
	switch phase {
	case state.TaskPhasePrepared, state.TaskPhaseExecuting:
		return 1, totalSteps
	case state.TaskPhaseProposed:
		return 2, totalSteps
	case state.TaskPhaseApplied, state.TaskPhaseRejected,
		state.TaskPhaseCompleted, state.TaskPhaseFailed:
		return totalSteps, totalSteps
	default:
		return 0, totalSteps
	}
}
