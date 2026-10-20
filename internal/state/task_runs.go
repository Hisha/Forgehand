package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Task run phases. They describe how far a controlled coding-task execution
// progressed and are the durable basis for phase-aware recovery: a restarted
// worker must not blindly rerun a task whose proposal is awaiting approval or
// whose changes were already applied.
const (
	// TaskPhasePrepared means the isolated workspace was created at the base
	// commit and the worker has not produced a proposal yet.
	TaskPhasePrepared = "PREPARED"
	// TaskPhaseExecuting means the worker is inspecting the workspace.
	TaskPhaseExecuting = "EXECUTING"
	// TaskPhaseProposed means a proposal is durably recorded and awaiting human
	// authorization.
	TaskPhaseProposed = "PROPOSED"
	// TaskPhaseApplied means the authorized proposal was written to the
	// workspace and the resulting diff was recorded.
	TaskPhaseApplied = "APPLIED"
	// TaskPhaseRejected means the proposal was declined; no change was written.
	TaskPhaseRejected = "REJECTED"
	// TaskPhaseCompleted means the task finished without changes to apply.
	TaskPhaseCompleted = "COMPLETED"
	// TaskPhaseFailed means the task terminated on an error.
	TaskPhaseFailed = "FAILED"
)

// TaskOperation outcomes.
const (
	TaskOutcomeOK            = "ok"
	TaskOutcomeError         = "error"
	TaskOutcomeRejected      = "rejected"
	TaskOutcomeNotAuthorized = "not_authorized"
)

// Task run operation kinds recorded for observability.
const (
	TaskOpList    = "list"
	TaskOpRead    = "read"
	TaskOpSearch  = "search"
	TaskOpPropose = "propose"
	TaskOpApply   = "apply"
)

// Task validation statuses. They record the outcome of the optional,
// explicitly-enabled post-apply Go format-and-test stage. A run with Phase
// APPLIED and ValidationStatus FAILED means the authorized changes were written
// but did not pass validation; the changes are preserved for inspection.
const (
	// TaskValidationSkipped means validation was not enabled or the proposal
	// produced no code to validate.
	TaskValidationSkipped = "skipped"
	// TaskValidationPassed means the format-and-test stage completed cleanly.
	TaskValidationPassed = "passed"
	// TaskValidationFailed means a validation command reported failure (for
	// example, a non-zero go test exit code).
	TaskValidationFailed = "failed"
	// TaskValidationError means the stage could not run at all (for example,
	// the toolchain is unavailable or the stage was canceled).
	TaskValidationError = "error"
)

// TaskRun is the durable record of a controlled coding task. It binds the
// execution to its isolated workspace, the exact proposal that was authorized,
// and the resulting diff. A completed task with Applied=false produced only a
// response or proposal; Applied=true means code was changed.
type TaskRun struct {
	ID                   int64
	SessionID            int64
	ExecutionID          int64
	ProjectID            int64
	RepoRoot             string
	WorkspacePath        string
	BaseCommit           string
	Phase                string
	Applied              bool
	Summary              string
	WorkspaceFingerprint string
	ProposalJSON         string
	ProposalHash         string
	WorkspaceDiff        string
	Error                string
	// ValidationStatus records the outcome of the post-apply validation stage
	// (one of the TaskValidation* constants), or empty when it has not run.
	ValidationStatus string
	// ValidationJSON is the persisted validation result detail (command,
	// exit code, duration, bounded output). It is valid when
	// ValidationStatus is non-empty.
	ValidationJSON string
	CleanedAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TaskOperation is one recorded step of a task run. It is the durable,
// inspectable trail of what the worker asked for and what was done.
type TaskOperation struct {
	ID        int64
	RunID     int64
	Sequence  int
	Kind      string
	Path      string
	Detail    string
	Outcome   string
	Error     string
	CreatedAt time.Time
}

const taskRunColumns = `
SELECT
	id,
	session_id,
	execution_id,
	project_id,
	repo_root,
	workspace_path,
	base_commit,
	phase,
	applied,
	summary,
	workspace_fingerprint,
	proposal_json,
	proposal_hash,
	workspace_diff,
	error,
	validation_status,
	validation_json,
	cleaned_at,
	created_at,
	updated_at
FROM task_runs
`

func scanTaskRun(scanner interactionScanner) (TaskRun, error) {
	var (
		run         TaskRun
		applied     int
		cleanedText sql.NullString
		createdText string
		updatedText string
	)

	if err := scanner.Scan(
		&run.ID,
		&run.SessionID,
		&run.ExecutionID,
		&run.ProjectID,
		&run.RepoRoot,
		&run.WorkspacePath,
		&run.BaseCommit,
		&run.Phase,
		&applied,
		&run.Summary,
		&run.WorkspaceFingerprint,
		&run.ProposalJSON,
		&run.ProposalHash,
		&run.WorkspaceDiff,
		&run.Error,
		&run.ValidationStatus,
		&run.ValidationJSON,
		&cleanedText,
		&createdText,
		&updatedText,
	); err != nil {
		return TaskRun{}, err
	}

	run.Applied = applied != 0

	createdAt, err := time.Parse(time.RFC3339Nano, createdText)
	if err != nil {
		return TaskRun{}, fmt.Errorf("parse task run created_at: %w", err)
	}
	run.CreatedAt = createdAt

	updatedAt, err := time.Parse(time.RFC3339Nano, updatedText)
	if err != nil {
		return TaskRun{}, fmt.Errorf("parse task run updated_at: %w", err)
	}
	run.UpdatedAt = updatedAt

	if cleanedText.Valid {
		cleanedAt, err := time.Parse(time.RFC3339Nano, cleanedText.String)
		if err != nil {
			return TaskRun{}, fmt.Errorf("parse task run cleaned_at: %w", err)
		}
		run.CleanedAt = &cleanedAt
	}

	return run, nil
}

// GetTaskRunByExecution returns the task run for an execution, or nil when the
// execution has no task run. It is read-only.
func (d *Database) GetTaskRunByExecution(
	ctx context.Context,
	executionID int64,
) (*TaskRun, error) {
	row := d.db.QueryRowContext(
		ctx,
		taskRunColumns+` WHERE execution_id = ?`,
		executionID,
	)

	run, err := scanTaskRun(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf(
			"read task run for execution %d: %w",
			executionID,
			err,
		)
	}
	return &run, nil
}

// GetTaskRunBySession returns the most recent task run for a session, or nil
// when the session has no task run. It is read-only.
func (d *Database) GetTaskRunBySession(
	ctx context.Context,
	sessionID int64,
) (*TaskRun, error) {
	if err := d.ensureSessionExists(ctx, sessionID); err != nil {
		return nil, err
	}

	row := d.db.QueryRowContext(
		ctx,
		taskRunColumns+` WHERE session_id = ? ORDER BY id DESC LIMIT 1`,
		sessionID,
	)

	run, err := scanTaskRun(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf(
			"read task run for session %d: %w",
			sessionID,
			err,
		)
	}
	return &run, nil
}

// PrepareTaskRun records the isolated workspace assignment for an execution and
// appends the task.prepared event when the run is first created. Re-running on
// a restart updates the existing row without duplicating the event.
func (d *Database) PrepareTaskRun(
	ctx context.Context,
	run TaskRun,
) (TaskRun, error) {
	if run.ExecutionID <= 0 || run.SessionID <= 0 {
		return TaskRun{}, fmt.Errorf(
			"task run requires a session and an execution",
		)
	}

	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskRun{}, fmt.Errorf("begin task run prepare: %w", err)
	}
	defer tx.Rollback()

	var existingID int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM task_runs WHERE execution_id = ?`,
		run.ExecutionID,
	).Scan(&existingID)

	switch {
	case err == sql.ErrNoRows:
		if _, err := tx.ExecContext(ctx, `
INSERT INTO task_runs (
	session_id,
	execution_id,
	project_id,
	repo_root,
	workspace_path,
	base_commit,
	phase,
	applied,
	summary,
	workspace_fingerprint,
	proposal_json,
	proposal_hash,
	workspace_diff,
	error,
	created_at,
	updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, '', ?, '[]', '', '', '', ?, ?)
`,
			run.SessionID,
			run.ExecutionID,
			run.ProjectID,
			run.RepoRoot,
			run.WorkspacePath,
			run.BaseCommit,
			run.Phase,
			run.WorkspaceFingerprint,
			nowText,
			nowText,
		); err != nil {
			return TaskRun{}, fmt.Errorf(
				"prepare task run for execution %d: %w",
				run.ExecutionID,
				err,
			)
		}

		if err := d.appendSessionEvent(
			ctx,
			tx,
			run.SessionID,
			&run.ExecutionID,
			EventTypeTaskPrepared,
			map[string]any{
				"execution_id": run.ExecutionID,
				"workspace":    run.WorkspacePath,
				"base_commit":  run.BaseCommit,
				"phase":        run.Phase,
			},
		); err != nil {
			return TaskRun{}, err
		}

	case err != nil:
		return TaskRun{}, fmt.Errorf(
			"read task run for execution %d: %w",
			run.ExecutionID,
			err,
		)

	default:
		if _, err := tx.ExecContext(ctx, `
UPDATE task_runs
SET workspace_path = ?,
    base_commit = ?,
    phase = ?,
    workspace_fingerprint = ?,
    updated_at = ?
WHERE execution_id = ?
`,
			run.WorkspacePath,
			run.BaseCommit,
			run.Phase,
			run.WorkspaceFingerprint,
			nowText,
			run.ExecutionID,
		); err != nil {
			return TaskRun{}, fmt.Errorf(
				"update task run for execution %d: %w",
				run.ExecutionID,
				err,
			)
		}
	}

	if err := tx.Commit(); err != nil {
		return TaskRun{}, fmt.Errorf(
			"commit task run prepare for execution %d: %w",
			run.ExecutionID,
			err,
		)
	}

	saved, err := d.GetTaskRunByExecution(ctx, run.ExecutionID)
	if err != nil {
		return TaskRun{}, err
	}
	if saved == nil {
		return TaskRun{}, fmt.Errorf(
			"task run for execution %d not found after prepare",
			run.ExecutionID,
		)
	}
	return *saved, nil
}

// RecordTaskOperation appends one operation to a task run's durable trail,
// assigning the next per-run sequence. It is best-effort by design at call
// sites but returns errors so a caller can decide.
func (d *Database) RecordTaskOperation(
	ctx context.Context,
	runID int64,
	op TaskOperation,
) error {
	if runID <= 0 {
		return fmt.Errorf("task operation requires a run id")
	}

	nowText := op.CreatedAt
	if nowText.IsZero() {
		nowText = time.Now().UTC()
	}

	_, err := d.db.ExecContext(ctx, `
INSERT INTO task_operations (
	run_id,
	sequence,
	kind,
	path,
	detail,
	outcome,
	error,
	created_at
)
VALUES (
	?,
	(SELECT COALESCE(MAX(sequence), 0) + 1 FROM task_operations WHERE run_id = ?),
	?,
	?,
	?,
	?,
	?,
	?
)
`,
		runID,
		runID,
		op.Kind,
		op.Path,
		op.Detail,
		op.Outcome,
		op.Error,
		nowText.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf(
			"record task operation for run %d: %w",
			runID,
			err,
		)
	}
	return nil
}

// ListTaskOperations returns a task run's operations in sequence order. It is
// read-only.
func (d *Database) ListTaskOperations(
	ctx context.Context,
	runID int64,
) ([]TaskOperation, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT
	id,
	run_id,
	sequence,
	kind,
	path,
	detail,
	outcome,
	error,
	created_at
FROM task_operations
WHERE run_id = ?
ORDER BY sequence ASC, id ASC
`, runID)
	if err != nil {
		return nil, fmt.Errorf("list task operations: %w", err)
	}
	defer rows.Close()

	var operations []TaskOperation

	for rows.Next() {
		var (
			op          TaskOperation
			createdText string
		)
		if err := rows.Scan(
			&op.ID,
			&op.RunID,
			&op.Sequence,
			&op.Kind,
			&op.Path,
			&op.Detail,
			&op.Outcome,
			&op.Error,
			&createdText,
		); err != nil {
			return nil, fmt.Errorf("scan task operation: %w", err)
		}

		createdAt, err := time.Parse(time.RFC3339Nano, createdText)
		if err != nil {
			return nil, fmt.Errorf("parse task operation created_at: %w", err)
		}
		op.CreatedAt = createdAt

		operations = append(operations, op)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task operations: %w", err)
	}

	return operations, nil
}

// MarkTaskRunCleaned records that the isolated workspace for a task run was
// removed at the user's explicit request. The durable diff is preserved.
func (d *Database) MarkTaskRunCleaned(
	ctx context.Context,
	runID int64,
) error {
	nowText := time.Now().UTC().Format(time.RFC3339Nano)

	result, err := d.db.ExecContext(ctx, `
UPDATE task_runs
SET cleaned_at = ?,
    workspace_path = '',
    updated_at = ?
WHERE id = ?
`, nowText, nowText, runID)
	if err != nil {
		return fmt.Errorf("mark task run %d cleaned: %w", runID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check task run %d cleanup: %w", runID, err)
	}
	if rowsAffected != 1 {
		return fmt.Errorf("task run %d does not exist", runID)
	}
	return nil
}

// finishTaskExecution atomically finalizes a running task execution: it records
// the terminal task-run phase and diff, transitions the execution and session,
// and appends the task and execution events. It is only permitted while the
// execution is RUNNING, so a canceled execution can never commit a task result.
func (d *Database) finishTaskExecution(
	ctx context.Context,
	executionID int64,
	run TaskRun,
	executionStatus string,
	sessionState string,
	taskEventType string,
	execEventType string,
) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task execution finalize: %w", err)
	}
	defer tx.Rollback()

	var (
		sessionID   int64
		status      string
		currentStep int
		totalSteps  int
	)

	err = tx.QueryRowContext(ctx, `
SELECT
	session_id,
	status,
	current_step,
	total_steps
FROM session_executions
WHERE id = ?
`, executionID).Scan(
		&sessionID,
		&status,
		&currentStep,
		&totalSteps,
	)
	if err == sql.ErrNoRows {
		return fmt.Errorf("execution %d does not exist", executionID)
	}
	if err != nil {
		return fmt.Errorf("read execution %d: %w", executionID, err)
	}

	if status != ExecutionStatusRunning {
		return fmt.Errorf(
			"execution %d is %s, expected %s",
			executionID,
			status,
			ExecutionStatusRunning,
		)
	}

	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)

	proposalJSON := run.ProposalJSON
	if proposalJSON == "" {
		proposalJSON = "[]"
	}

	result, err := tx.ExecContext(ctx, `
UPDATE task_runs
SET phase = ?,
    applied = ?,
    summary = ?,
    workspace_fingerprint = ?,
    proposal_json = ?,
    proposal_hash = ?,
    workspace_diff = ?,
    error = ?,
    validation_status = ?,
    validation_json = ?,
    updated_at = ?
WHERE execution_id = ?
`,
		run.Phase,
		boolToInt(run.Applied),
		run.Summary,
		run.WorkspaceFingerprint,
		proposalJSON,
		run.ProposalHash,
		run.WorkspaceDiff,
		run.Error,
		run.ValidationStatus,
		run.ValidationJSON,
		nowText,
		executionID,
	)
	if err != nil {
		return fmt.Errorf(
			"finalize task run for execution %d: %w",
			executionID,
			err,
		)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf(
			"check task run finalize for execution %d: %w",
			executionID,
			err,
		)
	}
	if rowsAffected != 1 {
		return fmt.Errorf(
			"task run for execution %d does not exist",
			executionID,
		)
	}

	if err := setExecutionStatus(
		ctx,
		tx,
		executionID,
		ExecutionStatusRunning,
		executionStatus,
		"finalize task execution",
	); err != nil {
		return err
	}

	if executionStatus == ExecutionStatusCompleted {
		if _, err := tx.ExecContext(ctx,
			`UPDATE session_executions SET completed_at = ? WHERE id = ?`,
			nowText,
			executionID,
		); err != nil {
			return fmt.Errorf(
				"record execution %d completion time: %w",
				executionID,
				err,
			)
		}
	}

	if err := setSessionState(
		ctx,
		tx,
		sessionID,
		SessionStateRunning,
		sessionState,
		nowText,
		"finalize task session",
	); err != nil {
		return err
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&executionID,
		taskEventType,
		map[string]any{
			"execution_id": executionID,
			"phase":        run.Phase,
			"applied":      run.Applied,
			"summary":      run.Summary,
		},
	); err != nil {
		return err
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&executionID,
		execEventType,
		map[string]any{
			"execution_id":  executionID,
			"status":        executionStatus,
			"session_state": sessionState,
			"phase":         run.Phase,
		},
	); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf(
			"commit task execution %d finalize: %w",
			executionID,
			err,
		)
	}

	return nil
}

// CompleteTaskExecution finalizes a task execution as COMPLETED. The task run's
// phase records whether code was actually changed (APPLIED) or the task merely
// finished (COMPLETED), so a reader can distinguish a proposal from a change.
func (d *Database) CompleteTaskExecution(
	ctx context.Context,
	executionID int64,
	run TaskRun,
) error {
	taskEventType := EventTypeTaskApplied
	if !run.Applied {
		taskEventType = EventTypeTaskCompleted
	}

	return d.finishTaskExecution(
		ctx,
		executionID,
		run,
		ExecutionStatusCompleted,
		SessionStateCompleted,
		taskEventType,
		EventTypeExecutionCompleted,
	)
}

// FailTaskExecution finalizes a task execution as FAILED, recording the cause
// on both the execution-facing task run and the task.failed event.
func (d *Database) FailTaskExecution(
	ctx context.Context,
	executionID int64,
	run TaskRun,
	cause string,
) error {
	run.Error = cause
	run.Phase = TaskPhaseFailed

	return d.finishTaskExecution(
		ctx,
		executionID,
		run,
		ExecutionStatusFailed,
		SessionStateFailed,
		EventTypeTaskFailed,
		EventTypeExecutionFailed,
	)
}

// RejectTaskExecution finalizes a task execution whose proposal was declined.
// The execution completes successfully (the task finished) but no change was
// applied.
func (d *Database) RejectTaskExecution(
	ctx context.Context,
	executionID int64,
	run TaskRun,
	reason string,
) error {
	run.Applied = false
	run.Phase = TaskPhaseRejected
	run.Error = reason
	run.WorkspaceDiff = ""

	return d.finishTaskExecution(
		ctx,
		executionID,
		run,
		ExecutionStatusCompleted,
		SessionStateCompleted,
		EventTypeTaskRejected,
		EventTypeExecutionCompleted,
	)
}

// RequestTaskApproval records a task proposal and requests human authorization
// in a single transaction. It persists the proposal (phase, hash, workspace
// fingerprint) and then applies the same idempotent pending-interaction rules
// as RequestHumanInput, so retries and restarts cannot create duplicate
// questions or lose an existing one.
func (d *Database) RequestTaskApproval(
	ctx context.Context,
	executionID int64,
	run TaskRun,
	question string,
	contextDetails map[string]any,
) (HumanInteraction, bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return HumanInteraction{}, false, fmt.Errorf(
			"begin task approval request: %w",
			err,
		)
	}
	defer tx.Rollback()

	existing, err := pendingInteractionForExecution(ctx, tx, executionID)
	if err == nil {
		// A pending question already exists; do not overwrite the persisted
		// proposal or create a second question.
		if err := tx.Commit(); err != nil {
			return HumanInteraction{}, false, fmt.Errorf(
				"commit idempotent task approval request: %w",
				err,
			)
		}
		return existing, false, nil
	}
	if err != sql.ErrNoRows {
		return HumanInteraction{}, false, err
	}

	sessionID, status, currentStep, totalSteps, err := executionInteractionTarget(
		ctx,
		tx,
		executionID,
	)
	if err != nil {
		return HumanInteraction{}, false, err
	}
	if status != ExecutionStatusRunning {
		return HumanInteraction{}, false, fmt.Errorf(
			"execution %d is %s, expected %s",
			executionID,
			status,
			ExecutionStatusRunning,
		)
	}

	var sessionState string
	if err := tx.QueryRowContext(ctx,
		`SELECT state FROM sessions WHERE id = ?`,
		sessionID,
	).Scan(&sessionState); err != nil {
		if err == sql.ErrNoRows {
			return HumanInteraction{}, false, fmt.Errorf(
				"session %d does not exist",
				sessionID,
			)
		}
		return HumanInteraction{}, false, fmt.Errorf(
			"read session %d: %w",
			sessionID,
			err,
		)
	}
	if sessionState != SessionStateRunning {
		return HumanInteraction{}, false, fmt.Errorf(
			"session %d is %s, expected %s",
			sessionID,
			sessionState,
			SessionStateRunning,
		)
	}

	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)

	proposalJSON := run.ProposalJSON
	if proposalJSON == "" {
		proposalJSON = "[]"
	}

	result, err := tx.ExecContext(ctx, `
UPDATE task_runs
SET phase = ?,
    applied = 0,
    summary = ?,
    workspace_fingerprint = ?,
    proposal_json = ?,
    proposal_hash = ?,
    error = '',
    updated_at = ?
WHERE execution_id = ?
`,
		TaskPhaseProposed,
		run.Summary,
		run.WorkspaceFingerprint,
		proposalJSON,
		run.ProposalHash,
		nowText,
		executionID,
	)
	if err != nil {
		return HumanInteraction{}, false, fmt.Errorf(
			"record task proposal for execution %d: %w",
			executionID,
			err,
		)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return HumanInteraction{}, false, fmt.Errorf(
			"check task proposal for execution %d: %w",
			executionID,
			err,
		)
	}
	if rowsAffected != 1 {
		return HumanInteraction{}, false, fmt.Errorf(
			"task run for execution %d does not exist",
			executionID,
		)
	}

	var contextJSON sql.NullString
	if len(contextDetails) > 0 {
		encoded, err := json.Marshal(contextDetails)
		if err != nil {
			return HumanInteraction{}, false, fmt.Errorf(
				"marshal task approval context: %w",
				err,
			)
		}
		contextJSON = sql.NullString{String: string(encoded), Valid: true}
	}

	result, err = tx.ExecContext(ctx, `
INSERT INTO human_interactions (
	session_id,
	execution_id,
	question,
	context_json,
	status,
	created_at
)
VALUES (?, ?, ?, ?, ?, ?)
`,
		sessionID,
		executionID,
		question,
		contextJSON,
		InteractionStatusPending,
		nowText,
	)
	if err != nil {
		return HumanInteraction{}, false, fmt.Errorf(
			"create task approval interaction: %w",
			err,
		)
	}

	interactionID, err := result.LastInsertId()
	if err != nil {
		return HumanInteraction{}, false, fmt.Errorf(
			"read task approval interaction id: %w",
			err,
		)
	}

	if err := setExecutionStatus(
		ctx,
		tx,
		executionID,
		ExecutionStatusRunning,
		ExecutionStatusWaitingForUser,
		"wait for task approval",
	); err != nil {
		return HumanInteraction{}, false, err
	}

	if err := setSessionState(
		ctx,
		tx,
		sessionID,
		SessionStateRunning,
		SessionStateWaitingForUser,
		nowText,
		"wait for task approval",
	); err != nil {
		return HumanInteraction{}, false, err
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&executionID,
		EventTypeTaskProposed,
		map[string]any{
			"execution_id":   executionID,
			"interaction_id": interactionID,
			"proposal_hash":  run.ProposalHash,
			"summary":        run.Summary,
		},
	); err != nil {
		return HumanInteraction{}, false, err
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&executionID,
		EventTypeInteractionRequested,
		map[string]any{
			"interaction_id": interactionID,
			"execution_id":   executionID,
			"status":         InteractionStatusPending,
		},
	); err != nil {
		return HumanInteraction{}, false, err
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&executionID,
		EventTypeExecutionWaitingForUser,
		map[string]any{
			"interaction_id": interactionID,
			"execution_id":   executionID,
			"current_step":   currentStep,
			"total_steps":    totalSteps,
			"status":         ExecutionStatusWaitingForUser,
		},
	); err != nil {
		return HumanInteraction{}, false, err
	}

	if err := tx.Commit(); err != nil {
		return HumanInteraction{}, false, fmt.Errorf(
			"commit task approval request: %w",
			err,
		)
	}

	interaction := HumanInteraction{
		ID:          interactionID,
		SessionID:   sessionID,
		ExecutionID: executionID,
		Question:    question,
		Status:      InteractionStatusPending,
		CreatedAt:   now,
	}
	if contextJSON.Valid {
		decoded := map[string]any{}
		if err := json.Unmarshal([]byte(contextJSON.String), &decoded); err == nil {
			interaction.Context = decoded
		}
	}

	return interaction, true, nil
}

// LatestHumanInteractionForExecution returns the most recent interaction for an
// execution, or nil when none exists. It is read-only and is used to resolve
// whether a proposal was authorized before applying it.
func (d *Database) LatestHumanInteractionForExecution(
	ctx context.Context,
	executionID int64,
) (*HumanInteraction, error) {
	row := d.db.QueryRowContext(
		ctx,
		humanInteractionColumns+`
WHERE execution_id = ?
ORDER BY id DESC
LIMIT 1
`,
		executionID,
	)

	interaction, err := scanHumanInteraction(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf(
			"read latest interaction for execution %d: %w",
			executionID,
			err,
		)
	}
	return &interaction, nil
}
