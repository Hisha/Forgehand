package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNoModelResult is returned when a session has no persisted model result.
var ErrNoModelResult = errors.New("no model result")

// ModelResult is the durable response from a model-backed execution. Coding
// results are verbatim; investigation results are schema-validated and
// normalized before persistence.
type ModelResult struct {
	ID               int64
	SessionID        int64
	ExecutionID      int64
	Provider         string
	Model            string
	Content          string
	CreatedAt        time.Time
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	LatencyMS        int64
}

type ModelMetrics struct {
	PromptTokens, CompletionTokens, TotalTokens int
	LatencyMS                                   int64
}

// CreateCodingSession creates a READY session of kind "coding" tied to a
// project and task, recording provider/model metadata.
func (d *Database) CreateCodingSession(
	ctx context.Context,
	title string,
	projectID int64,
	task string,
	provider string,
	model string,
) (Session, error) {
	pid := projectID
	t := task
	return d.CreateSessionWithDetails(ctx, title, &pid, &t, "coding", provider, model)
}

// GetSession returns a single session by id.
func (d *Database) GetSession(ctx context.Context, sessionID int64) (Session, error) {
	var (
		session     Session
		createdText string
		updatedText string
		projectID   sql.NullInt64
		task        sql.NullString
		kind        sql.NullString
		provider    sql.NullString
		model       sql.NullString
	)

	err := d.db.QueryRowContext(ctx, `
SELECT
	id,
	title,
	state,
	created_at,
	updated_at,
	project_id,
	task,
	kind,
	provider,
	model
FROM sessions
WHERE id = ?
`, sessionID).Scan(
		&session.ID,
		&session.Title,
		&session.State,
		&createdText,
		&updatedText,
		&projectID,
		&task,
		&kind,
		&provider,
		&model,
	)
	if err == sql.ErrNoRows {
		return Session{}, fmt.Errorf("session %d does not exist", sessionID)
	}
	if err != nil {
		return Session{}, fmt.Errorf("read session %d: %w", sessionID, err)
	}

	session.CreatedAt, err = time.Parse(time.RFC3339Nano, createdText)
	if err != nil {
		return Session{}, fmt.Errorf("parse created_at for session %d: %w", sessionID, err)
	}
	session.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedText)
	if err != nil {
		return Session{}, fmt.Errorf("parse updated_at for session %d: %w", sessionID, err)
	}
	if projectID.Valid {
		pid := projectID.Int64
		session.ProjectID = &pid
	}
	if task.Valid {
		session.Task = task.String
	}
	if kind.Valid {
		session.Kind = kind.String
	}
	if provider.Valid {
		session.Provider = provider.String
	}
	if model.Valid {
		session.Model = model.String
	}

	return session, nil
}

// CompleteModelExecution atomically persists a model result, appends the
// model.response.completed event, and transitions the execution and its session
// to COMPLETED. It is only permitted while the execution is RUNNING, so a
// canceled or failed execution can never commit a model result.
func (d *Database) CompleteModelExecution(
	ctx context.Context,
	executionID int64,
	provider string,
	model string,
	content string,
) error {
	return d.CompleteModelExecutionWithMetrics(ctx, executionID, provider, model, content, ModelMetrics{})
}

// CompleteModelExecutionWithMetrics performs the same atomic completion while
// preserving provider-reported token counts and observed wall-clock latency.
func (d *Database) CompleteModelExecutionWithMetrics(
	ctx context.Context,
	executionID int64,
	provider string,
	model string,
	content string,
	metrics ModelMetrics,
) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin model execution completion: %w", err)
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

	nowText := time.Now().UTC().Format(time.RFC3339Nano)

	result, err := tx.ExecContext(ctx, `
UPDATE session_executions
SET status = ?,
    current_step = total_steps,
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
		return fmt.Errorf("check execution %d completion: %w", executionID, err)
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
		return fmt.Errorf("complete session %d: %w", sessionID, err)
	}
	rowsAffected, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check session %d completion: %w", sessionID, err)
	}
	if rowsAffected != 1 {
		return fmt.Errorf(
			"session %d could not transition from %s to %s",
			sessionID,
			SessionStateRunning,
			SessionStateCompleted,
		)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO model_results (
	session_id,
	execution_id,
	provider,
	model,
	content,
	created_at,
	prompt_tokens,
	completion_tokens,
	total_tokens,
	latency_ms
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		sessionID,
		executionID,
		provider,
		model,
		content,
		nowText,
		metrics.PromptTokens,
		metrics.CompletionTokens,
		metrics.TotalTokens,
		metrics.LatencyMS,
	); err != nil {
		return fmt.Errorf("insert model result for execution %d: %w", executionID, err)
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&executionID,
		EventTypeModelResponseCompleted,
		map[string]any{
			"execution_id": executionID,
			"provider":     provider,
			"model":        model,
			"status":       ExecutionStatusCompleted,
		},
	); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model execution %d completion: %w", executionID, err)
	}

	return nil
}

// LatestModelResult returns the most recent model result for a session together
// with the execution it belongs to. It returns sql.ErrNoRows when the session
// has no model result.
func (d *Database) LatestModelResult(ctx context.Context, sessionID int64) (ModelResult, error) {
	var (
		result      ModelResult
		provider    sql.NullString
		model       sql.NullString
		createdText string
	)

	err := d.db.QueryRowContext(ctx, `
SELECT
	id,
	session_id,
	execution_id,
	provider,
	model,
	content,
	created_at,
	prompt_tokens,
	completion_tokens,
	total_tokens,
	latency_ms
FROM model_results
WHERE session_id = ?
ORDER BY id DESC
LIMIT 1
`, sessionID).Scan(
		&result.ID,
		&result.SessionID,
		&result.ExecutionID,
		&provider,
		&model,
		&result.Content,
		&createdText,
		&result.PromptTokens,
		&result.CompletionTokens,
		&result.TotalTokens,
		&result.LatencyMS,
	)
	if err == sql.ErrNoRows {
		return ModelResult{}, ErrNoModelResult
	}
	if err != nil {
		return ModelResult{}, fmt.Errorf("read latest model result for session %d: %w", sessionID, err)
	}

	if provider.Valid {
		result.Provider = provider.String
	}
	if model.Valid {
		result.Model = model.String
	}
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdText)
	if err != nil {
		return ModelResult{}, fmt.Errorf("parse model result created_at: %w", err)
	}

	return result, nil
}
