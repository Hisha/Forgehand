package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	ExecutionStatusQueued         = "QUEUED"
	ExecutionStatusRunning        = "RUNNING"
	ExecutionStatusWaitingForUser = "WAITING_FOR_USER"
	ExecutionStatusCompleted      = "COMPLETED"
	ExecutionStatusInterrupted    = "INTERRUPTED"
	ExecutionStatusFailed         = "FAILED"
	ExecutionStatusCanceled       = "CANCELED"
)

// MaxCancelReasonLength bounds the user-supplied cancellation reason so the
// durable event payload and execution row cannot grow without limit.
const MaxCancelReasonLength = 200

// CancellationOutcome describes the result of a CancelSessionExecution request.
// It distinguishes a newly applied cancellation from an idempotent repeat and
// from a refusal to cancel an already-terminal execution.
type CancellationOutcome string

const (
	// CancellationApplied means the execution and session transitioned to
	// CANCELED in this call and an execution.canceled event was appended.
	CancellationApplied CancellationOutcome = "canceled"
	// CancellationAlreadyCanceled means the execution was already CANCELED; no
	// state change or event was made.
	CancellationAlreadyCanceled CancellationOutcome = "already_canceled"
	// CancellationRejectedTerminal means the execution is COMPLETED or FAILED
	// and may not be canceled.
	CancellationRejectedTerminal CancellationOutcome = "rejected_terminal"
)

// CancellationResult reports the durable state after a cancellation request and
// how the request was resolved.
type CancellationResult struct {
	Session   Session
	Execution SessionExecution
	Outcome   CancellationOutcome
}

type SessionExecution struct {
	ID           int64
	SessionID    int64
	Status       string
	CurrentStep  int
	TotalSteps   int
	StartedAt    time.Time
	CompletedAt  *time.Time
	CanceledAt   *time.Time
	CancelReason *string

	// QueueSeq is the durable, globally monotonic sequence assigned when an
	// execution is enqueued (QUEUED). It is nil for executions that never
	// entered the queue (for example, legacy rows or direct-start executions)
	// and is retained after dispatch so historical ordering stays auditable.
	QueueSeq *int64
	// QueuePosition is the 1-based position among currently QUEUED executions
	// at the time the execution was read. It is 0 when the execution is not
	// currently QUEUED.
	QueuePosition int
}

func (d *Database) StartSessionExecution(
	ctx context.Context,
	sessionID int64,
	totalSteps int,
) (SessionExecution, error) {
	if totalSteps <= 0 {
		return SessionExecution{}, fmt.Errorf("total steps must be greater than zero")
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionExecution{}, fmt.Errorf("begin session execution: %w", err)
	}
	defer tx.Rollback()

	var state string
	err = tx.QueryRowContext(
		ctx,
		`SELECT state FROM sessions WHERE id = ?`,
		sessionID,
	).Scan(&state)

	if err == sql.ErrNoRows {
		return SessionExecution{}, fmt.Errorf("session %d does not exist", sessionID)
	}
	if err != nil {
		return SessionExecution{}, fmt.Errorf("read session %d: %w", sessionID, err)
	}

	if state != SessionStateReady {
		return SessionExecution{}, fmt.Errorf(
			"session %d is %s, expected %s",
			sessionID,
			state,
			SessionStateReady,
		)
	}

	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)

	result, err := tx.ExecContext(ctx, `
INSERT INTO session_executions (
	session_id,
	status,
	current_step,
	total_steps,
	started_at
)
VALUES (?, ?, 0, ?, ?)
`,
		sessionID,
		ExecutionStatusRunning,
		totalSteps,
		nowText,
	)
	if err != nil {
		return SessionExecution{}, fmt.Errorf(
			"create execution for session %d: %w",
			sessionID,
			err,
		)
	}

	executionID, err := result.LastInsertId()
	if err != nil {
		return SessionExecution{}, fmt.Errorf("read execution id: %w", err)
	}

	result, err = tx.ExecContext(ctx, `
UPDATE sessions
SET state = ?,
    updated_at = ?
WHERE id = ?
  AND state = ?
`,
		SessionStateRunning,
		nowText,
		sessionID,
		SessionStateReady,
	)
	if err != nil {
		return SessionExecution{}, fmt.Errorf(
			"mark session %d running: %w",
			sessionID,
			err,
		)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return SessionExecution{}, fmt.Errorf(
			"check session %d transition: %w",
			sessionID,
			err,
		)
	}

	if rowsAffected != 1 {
		return SessionExecution{}, fmt.Errorf(
			"session %d could not transition from %s to %s",
			sessionID,
			SessionStateReady,
			SessionStateRunning,
		)
	}

	execID := executionID
	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&execID,
		EventTypeExecutionStarted,
		map[string]any{
			"execution_id": executionID,
			"total_steps":  totalSteps,
			"status":       ExecutionStatusRunning,
		},
	); err != nil {
		return SessionExecution{}, err
	}

	if err := tx.Commit(); err != nil {
		return SessionExecution{}, fmt.Errorf(
			"commit execution resume: %w",
			err,
		)
	}

	return SessionExecution{
		ID:          executionID,
		SessionID:   sessionID,
		Status:      ExecutionStatusRunning,
		CurrentStep: 0,
		TotalSteps:  totalSteps,
		StartedAt:   now,
	}, nil
}

func sessionIDFromExec(d *Database, tx *sql.Tx, ctx context.Context, executionID int64) int64 {
	var sessionID int64
	_ = tx.QueryRowContext(ctx, `SELECT session_id FROM session_executions WHERE id = ?`, executionID).Scan(&sessionID)
	return sessionID
}

func (d *Database) AdvanceSessionExecution(
	ctx context.Context,
	executionID int64,
	step int,
) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin execution progress update: %w", err)
	}
	defer tx.Rollback()

	var (
		status      string
		currentStep int
		totalSteps  int
		sessionID   int64
	)

	err = tx.QueryRowContext(ctx, `
SELECT
	session_id,
	status,
	current_step,
	total_steps
FROM session_executions
WHERE id = ?
`,
		executionID,
	).Scan(
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

	if step != currentStep+1 {
		return fmt.Errorf(
			"execution %d cannot advance from step %d to %d",
			executionID,
			currentStep,
			step,
		)
	}

	if step > totalSteps {
		return fmt.Errorf(
			"execution %d step %d exceeds total steps %d",
			executionID,
			step,
			totalSteps,
		)
	}

	result, err := tx.ExecContext(ctx, `
UPDATE session_executions
SET current_step = ?
WHERE id = ?
  AND status = ?
  AND current_step = ?
`,
		step,
		executionID,
		ExecutionStatusRunning,
		currentStep,
	)
	if err != nil {
		return fmt.Errorf("advance execution %d: %w", executionID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf(
			"check execution %d progress update: %w",
			executionID,
			err,
		)
	}

	if rowsAffected != 1 {
		return fmt.Errorf(
			"execution %d progress changed concurrently",
			executionID,
		)
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&executionID,
		EventTypeExecutionCheckpoint,
		map[string]any{
			"execution_id": executionID,
			"step":         step,
			"current_step": step,
			"total_steps":  totalSteps,
		},
	); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf(
			"commit execution %d progress: %w",
			executionID,
			err,
		)
	}

	return nil
}

func (d *Database) CompleteSessionExecution(
	ctx context.Context,
	executionID int64,
) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin execution completion: %w", err)
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
`,
		executionID,
	).Scan(
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

	if currentStep != totalSteps {
		return fmt.Errorf(
			"execution %d is at step %d of %d",
			executionID,
			currentStep,
			totalSteps,
		)
	}

	nowText := time.Now().UTC().Format(time.RFC3339Nano)

	result, err := tx.ExecContext(ctx, `
UPDATE session_executions
SET status = ?,
    completed_at = ?
WHERE id = ?
  AND status = ?
`,
		ExecutionStatusCompleted,
		nowText,
		executionID,
		ExecutionStatusRunning,
	)
	if err != nil {
		return fmt.Errorf("complete execution %d: %w", executionID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf(
			"check execution %d completion: %w",
			executionID,
			err,
		)
	}

	if rowsAffected != 1 {
		return fmt.Errorf(
			"execution %d could not transition to %s",
			executionID,
			ExecutionStatusCompleted,
		)
	}

	result, err = tx.ExecContext(ctx, `
UPDATE sessions
SET state = ?,
    updated_at = ?
WHERE id = ?
  AND state = ?
`,
		SessionStateCompleted,
		nowText,
		sessionID,
		SessionStateRunning,
	)
	if err != nil {
		return fmt.Errorf(
			"complete session %d: %w",
			sessionID,
			err,
		)
	}

	rowsAffected, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf(
			"check session %d completion: %w",
			sessionID,
			err,
		)
	}

	if rowsAffected != 1 {
		return fmt.Errorf(
			"session %d could not transition from %s to %s",
			sessionID,
			SessionStateRunning,
			SessionStateCompleted,
		)
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&executionID,
		EventTypeExecutionCompleted,
		map[string]any{
			"execution_id": executionID,
			"current_step": currentStep,
			"total_steps":  totalSteps,
			"status":       ExecutionStatusCompleted,
		},
	); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf(
			"commit execution %d completion: %w",
			executionID,
			err,
		)
	}

	return nil
}

// InterruptSessionExecution marks a single running execution and its session as
// INTERRUPTED, preserving the last committed checkpoint. It is the recoverable
// interruption path (graceful worker cancellation); the execution remains
// resumable through ResumeSessionExecution.
func (d *Database) InterruptSessionExecution(
	ctx context.Context,
	executionID int64,
) error {
	return d.transitionRunningExecution(
		ctx,
		executionID,
		ExecutionStatusInterrupted,
		SessionStateInterrupted,
	)
}

// FailSessionExecution marks a single running execution and its session as
// FAILED, preserving the last committed checkpoint. It is the terminal failure
// path for an unexpected worker error and, unlike an interruption, is not
// resumable.
func (d *Database) FailSessionExecution(
	ctx context.Context,
	executionID int64,
) error {
	return d.transitionRunningExecution(
		ctx,
		executionID,
		ExecutionStatusFailed,
		SessionStateFailed,
	)
}

func (d *Database) transitionRunningExecution(
	ctx context.Context,
	executionID int64,
	executionStatus string,
	sessionStatus string,
) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin execution transition: %w", err)
	}
	defer tx.Rollback()

	var (
		sessionID int64
		status    string
	)

	err = tx.QueryRowContext(ctx, `
SELECT session_id, status
FROM session_executions
WHERE id = ?
`,
		executionID,
	).Scan(
		&sessionID,
		&status,
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

	nowText := time.Now().UTC().Format(time.RFC3339Nano)

	result, err := tx.ExecContext(ctx, `
UPDATE session_executions
SET status = ?
WHERE id = ?
  AND status = ?
`,
		executionStatus,
		executionID,
		ExecutionStatusRunning,
	)
	if err != nil {
		return fmt.Errorf(
			"transition execution %d to %s: %w",
			executionID,
			executionStatus,
			err,
		)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf(
			"check execution %d transition: %w",
			executionID,
			err,
		)
	}

	if rowsAffected != 1 {
		return fmt.Errorf(
			"execution %d could not transition to %s",
			executionID,
			executionStatus,
		)
	}

	result, err = tx.ExecContext(ctx, `
UPDATE sessions
SET state = ?,
    updated_at = ?
WHERE id = ?
  AND state = ?
`,
		sessionStatus,
		nowText,
		sessionID,
		SessionStateRunning,
	)
	if err != nil {
		return fmt.Errorf(
			"transition session %d to %s: %w",
			sessionID,
			sessionStatus,
			err,
		)
	}

	rowsAffected, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf(
			"check session %d transition: %w",
			sessionID,
			err,
		)
	}

	if rowsAffected != 1 {
		return fmt.Errorf(
			"session %d could not transition to %s",
			sessionID,
			sessionStatus,
		)
	}

	eventType := EventTypeExecutionInterrupted
	if executionStatus == ExecutionStatusFailed {
		eventType = EventTypeExecutionFailed
	}
	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&executionID,
		eventType,
		map[string]any{
			"execution_id":  executionID,
			"status":        executionStatus,
			"session_state": sessionStatus,
		},
	); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf(
			"commit execution %d transition: %w",
			executionID,
			err,
		)
	}

	return nil
}

// SessionExecutionStatus reports a session's durable state and its most recent
// execution, if one has been started. It is read-only and is the backing path
// for the session-status IPC command.
func (d *Database) SessionExecutionStatus(
	ctx context.Context,
	sessionID int64,
) (Session, *SessionExecution, error) {
	var (
		session     Session
		createdText string
		updatedText string
	)

	err := d.db.QueryRowContext(ctx, `
SELECT
	id,
	title,
	state,
	created_at,
	updated_at
FROM sessions
WHERE id = ?
`,
		sessionID,
	).Scan(
		&session.ID,
		&session.Title,
		&session.State,
		&createdText,
		&updatedText,
	)
	if err == sql.ErrNoRows {
		return Session{}, nil, fmt.Errorf("session %d does not exist", sessionID)
	}
	if err != nil {
		return Session{}, nil, fmt.Errorf("read session %d: %w", sessionID, err)
	}

	session.CreatedAt, err = time.Parse(time.RFC3339Nano, createdText)
	if err != nil {
		return Session{}, nil, fmt.Errorf(
			"parse created_at for session %d: %w",
			sessionID,
			err,
		)
	}

	session.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedText)
	if err != nil {
		return Session{}, nil, fmt.Errorf(
			"parse updated_at for session %d: %w",
			sessionID,
			err,
		)
	}

	var (
		execution     SessionExecution
		startedText   string
		completedText sql.NullString
		canceledText  sql.NullString
		cancelReason  sql.NullString
		queueSeq      sql.NullInt64
	)

	err = d.db.QueryRowContext(ctx, `
SELECT
	id,
	session_id,
	status,
	current_step,
	total_steps,
	started_at,
	completed_at,
	canceled_at,
	cancel_reason,
	queue_seq
FROM session_executions
WHERE session_id = ?
ORDER BY id DESC
LIMIT 1
`,
		sessionID,
	).Scan(
		&execution.ID,
		&execution.SessionID,
		&execution.Status,
		&execution.CurrentStep,
		&execution.TotalSteps,
		&startedText,
		&completedText,
		&canceledText,
		&cancelReason,
		&queueSeq,
	)
	if err == sql.ErrNoRows {
		return session, nil, nil
	}
	if err != nil {
		return Session{}, nil, fmt.Errorf(
			"read latest execution for session %d: %w",
			sessionID,
			err,
		)
	}

	execution.StartedAt, err = time.Parse(time.RFC3339Nano, startedText)
	if err != nil {
		return Session{}, nil, fmt.Errorf(
			"parse execution %d start time: %w",
			execution.ID,
			err,
		)
	}

	if completedText.Valid {
		completedAt, err := time.Parse(time.RFC3339Nano, completedText.String)
		if err != nil {
			return Session{}, nil, fmt.Errorf(
				"parse execution %d completion time: %w",
				execution.ID,
				err,
			)
		}
		execution.CompletedAt = &completedAt
	}

	if canceledText.Valid {
		canceledAt, err := time.Parse(time.RFC3339Nano, canceledText.String)
		if err != nil {
			return Session{}, nil, fmt.Errorf(
				"parse execution %d cancellation time: %w",
				execution.ID,
				err,
			)
		}
		execution.CanceledAt = &canceledAt
	}

	if cancelReason.Valid {
		value := cancelReason.String
		execution.CancelReason = &value
	}

	if queueSeq.Valid {
		value := queueSeq.Int64
		execution.QueueSeq = &value
		if execution.Status == ExecutionStatusQueued {
			position, err := queuePosition(ctx, d.db, value)
			if err != nil {
				return Session{}, nil, err
			}
			execution.QueuePosition = position
		}
	}

	return session, &execution, nil
}

// CancelSessionExecution transitions a session's most recent execution and the
// session to the terminal CANCELED state and appends an execution.canceled
// event in the same transaction. It is the intentional-stop path, distinct from
// COMPLETED (success), FAILED (error), INTERRUPTED (recoverable), and
// WAITING_FOR_USER (awaiting a human).
//
// Cancellation is accepted from QUEUED, RUNNING, WAITING_FOR_USER, and
// INTERRUPTED. QUEUED is cancellable so a user can abandon scheduled work
// before it starts. INTERRUPTED is cancellable so a user can permanently
// abandon work they no longer wish to resume. COMPLETED and FAILED are terminal
// and rejected with
// CancellationRejectedTerminal. An execution that is already CANCELED returns
// CancellationAlreadyCanceled without writing a second event or changing the
// recorded reason, making repeated requests idempotent. A session with no
// execution is rejected.
//
// The entire state change and event append occur in one transaction. A failure
// to append the event rolls the whole cancellation back.
func (d *Database) CancelSessionExecution(
	ctx context.Context,
	sessionID int64,
	reason string,
) (CancellationResult, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) > MaxCancelReasonLength {
		reason = reason[:MaxCancelReasonLength]
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return CancellationResult{}, fmt.Errorf("begin session cancellation: %w", err)
	}
	defer tx.Rollback()

	var (
		session     Session
		createdText string
		updatedText string
	)

	err = tx.QueryRowContext(ctx, `
SELECT
	id,
	title,
	state,
	created_at,
	updated_at
FROM sessions
WHERE id = ?
`,
		sessionID,
	).Scan(
		&session.ID,
		&session.Title,
		&session.State,
		&createdText,
		&updatedText,
	)
	if err == sql.ErrNoRows {
		return CancellationResult{}, fmt.Errorf("session %d does not exist", sessionID)
	}
	if err != nil {
		return CancellationResult{}, fmt.Errorf("read session %d: %w", sessionID, err)
	}

	session.CreatedAt, err = time.Parse(time.RFC3339Nano, createdText)
	if err != nil {
		return CancellationResult{}, fmt.Errorf(
			"parse created_at for session %d: %w",
			sessionID,
			err,
		)
	}
	session.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedText)
	if err != nil {
		return CancellationResult{}, fmt.Errorf(
			"parse updated_at for session %d: %w",
			sessionID,
			err,
		)
	}

	var (
		execution     SessionExecution
		startedText   string
		completedText sql.NullString
		canceledText  sql.NullString
		cancelReason  sql.NullString
	)

	err = tx.QueryRowContext(ctx, `
SELECT
	id,
	session_id,
	status,
	current_step,
	total_steps,
	started_at,
	completed_at,
	canceled_at,
	cancel_reason
FROM session_executions
WHERE session_id = ?
ORDER BY id DESC
LIMIT 1
`,
		sessionID,
	).Scan(
		&execution.ID,
		&execution.SessionID,
		&execution.Status,
		&execution.CurrentStep,
		&execution.TotalSteps,
		&startedText,
		&completedText,
		&canceledText,
		&cancelReason,
	)
	if err == sql.ErrNoRows {
		return CancellationResult{}, fmt.Errorf(
			"session %d has no execution to cancel",
			sessionID,
		)
	}
	if err != nil {
		return CancellationResult{}, fmt.Errorf(
			"read latest execution for session %d: %w",
			sessionID,
			err,
		)
	}

	execution.StartedAt, err = time.Parse(time.RFC3339Nano, startedText)
	if err != nil {
		return CancellationResult{}, fmt.Errorf(
			"parse execution %d start time: %w",
			execution.ID,
			err,
		)
	}
	if completedText.Valid {
		completedAt, err := time.Parse(time.RFC3339Nano, completedText.String)
		if err != nil {
			return CancellationResult{}, fmt.Errorf(
				"parse execution %d completion time: %w",
				execution.ID,
				err,
			)
		}
		execution.CompletedAt = &completedAt
	}
	if canceledText.Valid {
		canceledAt, err := time.Parse(time.RFC3339Nano, canceledText.String)
		if err != nil {
			return CancellationResult{}, fmt.Errorf(
				"parse execution %d cancellation time: %w",
				execution.ID,
				err,
			)
		}
		execution.CanceledAt = &canceledAt
	}
	if cancelReason.Valid {
		value := cancelReason.String
		execution.CancelReason = &value
	}

	switch execution.Status {
	case ExecutionStatusCanceled:
		if err := tx.Commit(); err != nil {
			return CancellationResult{}, fmt.Errorf(
				"commit idempotent session cancellation: %w",
				err,
			)
		}
		return CancellationResult{
			Session:   session,
			Execution: execution,
			Outcome:   CancellationAlreadyCanceled,
		}, nil

	case ExecutionStatusQueued, ExecutionStatusRunning, ExecutionStatusWaitingForUser, ExecutionStatusInterrupted:
		// Cancellable.

	case ExecutionStatusCompleted, ExecutionStatusFailed:
		if err := tx.Commit(); err != nil {
			return CancellationResult{}, fmt.Errorf(
				"commit rejected session cancellation: %w",
				err,
			)
		}
		return CancellationResult{
			Session:   session,
			Execution: execution,
			Outcome:   CancellationRejectedTerminal,
		}, nil

	default:
		return CancellationResult{}, fmt.Errorf(
			"execution %d has unknown status %s",
			execution.ID,
			execution.Status,
		)
	}

	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)

	var reasonValue any
	if reason != "" {
		reasonValue = reason
	}

	result, err := tx.ExecContext(ctx, `
UPDATE session_executions
SET status = ?,
    canceled_at = ?,
    cancel_reason = ?
WHERE id = ?
  AND status = ?
`,
		ExecutionStatusCanceled,
		nowText,
		reasonValue,
		execution.ID,
		execution.Status,
	)
	if err != nil {
		return CancellationResult{}, fmt.Errorf(
			"cancel execution %d: %w",
			execution.ID,
			err,
		)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return CancellationResult{}, fmt.Errorf(
			"check execution %d cancellation: %w",
			execution.ID,
			err,
		)
	}
	if rowsAffected != 1 {
		return CancellationResult{}, fmt.Errorf(
			"execution %d could not transition from %s to %s",
			execution.ID,
			execution.Status,
			ExecutionStatusCanceled,
		)
	}

	result, err = tx.ExecContext(ctx, `
UPDATE sessions
SET state = ?,
    updated_at = ?
WHERE id = ?
  AND state = ?
`,
		SessionStateCanceled,
		nowText,
		sessionID,
		session.State,
	)
	if err != nil {
		return CancellationResult{}, fmt.Errorf(
			"cancel session %d: %w",
			sessionID,
			err,
		)
	}

	rowsAffected, err = result.RowsAffected()
	if err != nil {
		return CancellationResult{}, fmt.Errorf(
			"check session %d cancellation: %w",
			sessionID,
			err,
		)
	}
	if rowsAffected != 1 {
		return CancellationResult{}, fmt.Errorf(
			"session %d could not transition from %s to %s",
			sessionID,
			session.State,
			SessionStateCanceled,
		)
	}

	details := map[string]any{
		"execution_id":  execution.ID,
		"status":        ExecutionStatusCanceled,
		"session_state": SessionStateCanceled,
		"current_step":  execution.CurrentStep,
		"total_steps":   execution.TotalSteps,
	}
	if reason != "" {
		details["reason"] = reason
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&execution.ID,
		EventTypeExecutionCanceled,
		details,
	); err != nil {
		return CancellationResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return CancellationResult{}, fmt.Errorf(
			"commit execution %d cancellation: %w",
			execution.ID,
			err,
		)
	}

	execution.Status = ExecutionStatusCanceled
	execution.CanceledAt = &now
	if reason != "" {
		value := reason
		execution.CancelReason = &value
	}
	session.State = SessionStateCanceled
	session.UpdatedAt = now

	return CancellationResult{
		Session:   session,
		Execution: execution,
		Outcome:   CancellationApplied,
	}, nil
}

func (d *Database) InterruptRunningExecutions(
	ctx context.Context,
) (int64, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin execution interruption: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
SELECT id, session_id
FROM session_executions
WHERE status = ?
ORDER BY id
`,
		ExecutionStatusRunning,
	)
	if err != nil {
		return 0, fmt.Errorf("find running executions: %w", err)
	}

	type runningExecution struct {
		executionID int64
		sessionID   int64
	}

	var running []runningExecution

	for rows.Next() {
		var item runningExecution

		if err := rows.Scan(
			&item.executionID,
			&item.sessionID,
		); err != nil {
			rows.Close()
			return 0, fmt.Errorf("read running execution: %w", err)
		}

		running = append(running, item)
	}

	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate running executions: %w", err)
	}

	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close running executions: %w", err)
	}

	if len(running) == 0 {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("commit empty execution interruption: %w", err)
		}

		return 0, nil
	}

	nowText := time.Now().UTC().Format(time.RFC3339Nano)

	var interrupted int64

	for _, item := range running {
		result, err := tx.ExecContext(ctx, `
UPDATE session_executions
SET status = ?
WHERE id = ?
  AND status = ?
`,
			ExecutionStatusInterrupted,
			item.executionID,
			ExecutionStatusRunning,
		)
		if err != nil {
			return 0, fmt.Errorf(
				"interrupt execution %d: %w",
				item.executionID,
				err,
			)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf(
				"check execution %d interruption: %w",
				item.executionID,
				err,
			)
		}

		if rowsAffected != 1 {
			return 0, fmt.Errorf(
				"execution %d could not transition from %s to %s",
				item.executionID,
				ExecutionStatusRunning,
				ExecutionStatusInterrupted,
			)
		}

		result, err = tx.ExecContext(ctx, `
UPDATE sessions
SET state = ?,
    updated_at = ?
WHERE id = ?
  AND state = ?
`,
			SessionStateInterrupted,
			nowText,
			item.sessionID,
			SessionStateRunning,
		)
		if err != nil {
			return 0, fmt.Errorf(
				"interrupt session %d: %w",
				item.sessionID,
				err,
			)
		}

		rowsAffected, err = result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf(
				"check session %d interruption: %w",
				item.sessionID,
				err,
			)
		}

		if rowsAffected != 1 {
			return 0, fmt.Errorf(
				"session %d could not transition from %s to %s",
				item.sessionID,
				SessionStateRunning,
				SessionStateInterrupted,
			)
		}

		if err := d.appendSessionEvent(
			ctx,
			tx,
			item.sessionID,
			&item.executionID,
			EventTypeExecutionInterrupted,
			map[string]any{
				"execution_id":  item.executionID,
				"status":        ExecutionStatusInterrupted,
				"session_state": SessionStateInterrupted,
				"recovery":      true,
			},
		); err != nil {
			return 0, err
		}

		interrupted++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit execution interruption: %w", err)
	}

	return interrupted, nil
}

func (d *Database) ResumeSessionExecution(
	ctx context.Context,
	sessionID int64,
) (SessionExecution, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionExecution{}, fmt.Errorf("begin execution resume: %w", err)
	}
	defer tx.Rollback()

	var (
		execution     SessionExecution
		startedAtText string
	)

	err = tx.QueryRowContext(ctx, `
SELECT
	id,
	session_id,
	status,
	current_step,
	total_steps,
	started_at
FROM session_executions
WHERE session_id = ?
ORDER BY id DESC
LIMIT 1
`,
		sessionID,
	).Scan(
		&execution.ID,
		&execution.SessionID,
		&execution.Status,
		&execution.CurrentStep,
		&execution.TotalSteps,
		&startedAtText,
	)

	if err == sql.ErrNoRows {
		return SessionExecution{}, fmt.Errorf(
			"session %d has no execution to resume",
			sessionID,
		)
	}
	if err != nil {
		return SessionExecution{}, fmt.Errorf(
			"read latest execution for session %d: %w",
			sessionID,
			err,
		)
	}

	if execution.Status != ExecutionStatusInterrupted {
		return SessionExecution{}, fmt.Errorf(
			"execution %d is %s, expected %s",
			execution.ID,
			execution.Status,
			ExecutionStatusInterrupted,
		)
	}

	startedAt, err := time.Parse(time.RFC3339Nano, startedAtText)
	if err != nil {
		return SessionExecution{}, fmt.Errorf(
			"parse execution %d start time: %w",
			execution.ID,
			err,
		)
	}
	execution.StartedAt = startedAt

	var sessionState string
	err = tx.QueryRowContext(ctx, `
SELECT state
FROM sessions
WHERE id = ?
`,
		sessionID,
	).Scan(&sessionState)

	if err == sql.ErrNoRows {
		return SessionExecution{}, fmt.Errorf(
			"session %d does not exist",
			sessionID,
		)
	}
	if err != nil {
		return SessionExecution{}, fmt.Errorf(
			"read session %d: %w",
			sessionID,
			err,
		)
	}

	if sessionState != SessionStateInterrupted {
		return SessionExecution{}, fmt.Errorf(
			"session %d is %s, expected %s",
			sessionID,
			sessionState,
			SessionStateInterrupted,
		)
	}

	nowText := time.Now().UTC().Format(time.RFC3339Nano)

	result, err := tx.ExecContext(ctx, `
UPDATE session_executions
SET status = ?
WHERE id = ?
  AND status = ?
`,
		ExecutionStatusRunning,
		execution.ID,
		ExecutionStatusInterrupted,
	)
	if err != nil {
		return SessionExecution{}, fmt.Errorf(
			"resume execution %d: %w",
			execution.ID,
			err,
		)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return SessionExecution{}, fmt.Errorf(
			"check execution %d resume: %w",
			execution.ID,
			err,
		)
	}

	if rowsAffected != 1 {
		return SessionExecution{}, fmt.Errorf(
			"execution %d could not transition from %s to %s",
			execution.ID,
			ExecutionStatusInterrupted,
			ExecutionStatusRunning,
		)
	}

	result, err = tx.ExecContext(ctx, `
UPDATE sessions
SET state = ?,
    updated_at = ?
WHERE id = ?
  AND state = ?
`,
		SessionStateRunning,
		nowText,
		sessionID,
		SessionStateInterrupted,
	)
	if err != nil {
		return SessionExecution{}, fmt.Errorf(
			"resume session %d: %w",
			sessionID,
			err,
		)
	}

	rowsAffected, err = result.RowsAffected()
	if err != nil {
		return SessionExecution{}, fmt.Errorf(
			"check session %d resume: %w",
			sessionID,
			err,
		)
	}

	if rowsAffected != 1 {
		return SessionExecution{}, fmt.Errorf(
			"session %d could not transition from %s to %s",
			sessionID,
			SessionStateInterrupted,
			SessionStateRunning,
		)
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&execution.ID,
		EventTypeExecutionResumed,
		map[string]any{
			"execution_id": execution.ID,
			"current_step": execution.CurrentStep,
			"total_steps":  execution.TotalSteps,
			"status":       ExecutionStatusRunning,
		},
	); err != nil {
		return SessionExecution{}, err
	}

	if err := tx.Commit(); err != nil {
		return SessionExecution{}, fmt.Errorf(
			"commit execution %d resume: %w",
			execution.ID,
			err,
		)
	}

	execution.Status = ExecutionStatusRunning
	return execution, nil
}
