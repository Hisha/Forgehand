package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	ExecutionStatusRunning     = "RUNNING"
	ExecutionStatusCompleted   = "COMPLETED"
	ExecutionStatusInterrupted = "INTERRUPTED"
)

type SessionExecution struct {
	ID          int64
	SessionID   int64
	Status      string
	CurrentStep int
	TotalSteps  int
	StartedAt   time.Time
	CompletedAt *time.Time
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

	if err := tx.Commit(); err != nil {
		return SessionExecution{}, fmt.Errorf(
			"commit session execution: %w",
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
	)

	err = tx.QueryRowContext(ctx, `
SELECT
	status,
	current_step,
	total_steps
FROM session_executions
WHERE id = ?
`,
		executionID,
	).Scan(
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

	if err := tx.Commit(); err != nil {
		return fmt.Errorf(
			"commit execution %d completion: %w",
			executionID,
			err,
		)
	}

	return nil
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

		interrupted++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit execution interruption: %w", err)
	}

	return interrupted, nil
}
