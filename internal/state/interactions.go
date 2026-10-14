package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	InteractionStatusPending  = "PENDING"
	InteractionStatusAnswered = "ANSWERED"
)

// HumanInteraction is a durable worker-to-human question. At most one
// interaction may be PENDING for a given execution at a time; the partial
// unique index in migration 10 enforces this. An answered interaction retains
// its answer and timestamp forever.
type HumanInteraction struct {
	ID          int64
	SessionID   int64
	ExecutionID int64
	Question    string
	Context     map[string]any
	Status      string
	CreatedAt   time.Time
	Answer      *string
	AnsweredAt  *time.Time
}

type interactionScanner interface {
	Scan(dest ...any) error
}

const humanInteractionColumns = `
SELECT
	id,
	session_id,
	execution_id,
	question,
	context_json,
	status,
	created_at,
	answer,
	answered_at
FROM human_interactions
`

func scanHumanInteraction(scanner interactionScanner) (HumanInteraction, error) {
	var (
		interaction  HumanInteraction
		contextJSON  sql.NullString
		createdText  string
		answer       sql.NullString
		answeredText sql.NullString
	)

	if err := scanner.Scan(
		&interaction.ID,
		&interaction.SessionID,
		&interaction.ExecutionID,
		&interaction.Question,
		&contextJSON,
		&interaction.Status,
		&createdText,
		&answer,
		&answeredText,
	); err != nil {
		return HumanInteraction{}, err
	}

	createdAt, err := time.Parse(time.RFC3339Nano, createdText)
	if err != nil {
		return HumanInteraction{}, fmt.Errorf(
			"parse interaction created_at: %w",
			err,
		)
	}
	interaction.CreatedAt = createdAt

	if contextJSON.Valid && contextJSON.String != "" && contextJSON.String != "{}" {
		decoded := map[string]any{}
		if err := json.Unmarshal([]byte(contextJSON.String), &decoded); err == nil {
			interaction.Context = decoded
		}
	}

	if answer.Valid {
		value := answer.String
		interaction.Answer = &value
	}

	if answeredText.Valid {
		answeredAt, err := time.Parse(time.RFC3339Nano, answeredText.String)
		if err != nil {
			return HumanInteraction{}, fmt.Errorf(
				"parse interaction answered_at: %w",
				err,
			)
		}
		interaction.AnsweredAt = &answeredAt
	}

	return interaction, nil
}

func pendingInteractionForExecution(
	ctx context.Context,
	tx *sql.Tx,
	executionID int64,
) (HumanInteraction, error) {
	row := tx.QueryRowContext(ctx, humanInteractionColumns+`
WHERE execution_id = ? AND status = ?
ORDER BY id DESC
LIMIT 1
`, executionID, InteractionStatusPending)

	interaction, err := scanHumanInteraction(row)
	if err == sql.ErrNoRows {
		return HumanInteraction{}, sql.ErrNoRows
	}
	if err != nil {
		return HumanInteraction{}, fmt.Errorf(
			"read pending interaction for execution %d: %w",
			executionID,
			err,
		)
	}
	return interaction, nil
}

func latestInteractionForExecution(
	ctx context.Context,
	tx *sql.Tx,
	executionID int64,
) (HumanInteraction, error) {
	row := tx.QueryRowContext(ctx, humanInteractionColumns+`
WHERE execution_id = ?
ORDER BY id DESC
LIMIT 1
`, executionID)

	interaction, err := scanHumanInteraction(row)
	if err == sql.ErrNoRows {
		return HumanInteraction{}, fmt.Errorf(
			"execution %d has no human interaction",
			executionID,
		)
	}
	if err != nil {
		return HumanInteraction{}, fmt.Errorf(
			"read latest interaction for execution %d: %w",
			executionID,
			err,
		)
	}
	return interaction, nil
}

func interactionByID(
	ctx context.Context,
	tx *sql.Tx,
	interactionID int64,
) (HumanInteraction, error) {
	row := tx.QueryRowContext(ctx, humanInteractionColumns+`
WHERE id = ?
`, interactionID)

	interaction, err := scanHumanInteraction(row)
	if err == sql.ErrNoRows {
		return HumanInteraction{}, fmt.Errorf(
			"interaction %d does not exist",
			interactionID,
		)
	}
	if err != nil {
		return HumanInteraction{}, fmt.Errorf(
			"read interaction %d: %w",
			interactionID,
			err,
		)
	}
	return interaction, nil
}

func executionInteractionTarget(
	ctx context.Context,
	tx *sql.Tx,
	executionID int64,
) (sessionID int64, status string, currentStep int, totalSteps int, err error) {
	err = tx.QueryRowContext(ctx, `
SELECT session_id, status, current_step, total_steps
FROM session_executions
WHERE id = ?
`, executionID).Scan(&sessionID, &status, &currentStep, &totalSteps)

	if err == sql.ErrNoRows {
		return 0, "", 0, 0, fmt.Errorf("execution %d does not exist", executionID)
	}
	if err != nil {
		return 0, "", 0, 0, fmt.Errorf("read execution %d: %w", executionID, err)
	}
	return sessionID, status, currentStep, totalSteps, nil
}

func setExecutionStatus(
	ctx context.Context,
	tx *sql.Tx,
	executionID int64,
	from string,
	to string,
	action string,
) error {
	result, err := tx.ExecContext(ctx, `
UPDATE session_executions
SET status = ?
WHERE id = ?
  AND status = ?
`, to, executionID, from)
	if err != nil {
		return fmt.Errorf("%s: execution %d: %w", action, executionID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: check execution %d: %w", action, executionID, err)
	}
	if rowsAffected != 1 {
		return fmt.Errorf(
			"execution %d could not transition from %s to %s",
			executionID,
			from,
			to,
		)
	}
	return nil
}

func setSessionState(
	ctx context.Context,
	tx *sql.Tx,
	sessionID int64,
	from string,
	to string,
	nowText string,
	action string,
) error {
	result, err := tx.ExecContext(ctx, `
UPDATE sessions
SET state = ?,
    updated_at = ?
WHERE id = ?
  AND state = ?
`, to, nowText, sessionID, from)
	if err != nil {
		return fmt.Errorf("%s: session %d: %w", action, sessionID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: check session %d: %w", action, sessionID, err)
	}
	if rowsAffected != 1 {
		return fmt.Errorf(
			"session %d could not transition from %s to %s",
			sessionID,
			from,
			to,
		)
	}
	return nil
}

func (d *Database) ensureSessionExists(ctx context.Context, sessionID int64) error {
	var exists int
	err := d.db.QueryRowContext(ctx,
		`SELECT 1 FROM sessions WHERE id = ?`,
		sessionID,
	).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("session %d does not exist", sessionID)
	}
	if err != nil {
		return fmt.Errorf("check session %d: %w", sessionID, err)
	}
	return nil
}

// RequestHumanInput atomically transitions a RUNNING execution and its session
// to WAITING_FOR_USER, persists the pending question, and appends the
// interaction.requested and execution.waiting_for_user events. It is
// idempotent: if a PENDING interaction already exists for the execution it is
// returned unchanged with created=false, so retries, restarts, and repeated
// worker calls cannot create duplicate questions or overwrite the existing one.
func (d *Database) RequestHumanInput(
	ctx context.Context,
	executionID int64,
	question string,
	contextDetails map[string]any,
) (HumanInteraction, bool, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return HumanInteraction{}, false, fmt.Errorf("question must not be empty")
	}

	var contextJSON sql.NullString
	if len(contextDetails) > 0 {
		encoded, err := json.Marshal(contextDetails)
		if err != nil {
			return HumanInteraction{}, false, fmt.Errorf(
				"marshal interaction context: %w",
				err,
			)
		}
		contextJSON = sql.NullString{String: string(encoded), Valid: true}
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return HumanInteraction{}, false, fmt.Errorf("begin human input request: %w", err)
	}
	defer tx.Rollback()

	existing, err := pendingInteractionForExecution(ctx, tx, executionID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return HumanInteraction{}, false, fmt.Errorf(
				"commit idempotent human input request: %w",
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
			return HumanInteraction{}, false, fmt.Errorf("session %d does not exist", sessionID)
		}
		return HumanInteraction{}, false, fmt.Errorf("read session %d: %w", sessionID, err)
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

	result, err := tx.ExecContext(ctx, `
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
		return HumanInteraction{}, false, fmt.Errorf("create human interaction: %w", err)
	}

	interactionID, err := result.LastInsertId()
	if err != nil {
		return HumanInteraction{}, false, fmt.Errorf("read human interaction id: %w", err)
	}

	if err := setExecutionStatus(
		ctx,
		tx,
		executionID,
		ExecutionStatusRunning,
		ExecutionStatusWaitingForUser,
		"wait for user input",
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
		"wait for user input",
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
			"commit human input request: %w",
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

// AnswerHumanInput atomically records the answer to a PENDING interaction and
// appends the interaction.answered event. Only a PENDING interaction may be
// answered. A duplicate submission returns the already-answered interaction
// with applied=false and appends no event, so an answer can never be lost or
// replaced.
//
// A PENDING interaction whose execution has already been CANCELED is rejected:
// the interaction stays PENDING, no answer is recorded, and no event is
// appended. An interaction answered before cancellation keeps its recorded
// answer. Because the execution status is read inside the answer transaction,
// concurrent answer/cancel requests resolve deterministically.
func (d *Database) AnswerHumanInput(
	ctx context.Context,
	interactionID int64,
	answer string,
) (HumanInteraction, bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return HumanInteraction{}, false, fmt.Errorf("begin human input answer: %w", err)
	}
	defer tx.Rollback()

	interaction, err := interactionByID(ctx, tx, interactionID)
	if err != nil {
		return HumanInteraction{}, false, err
	}

	switch interaction.Status {
	case InteractionStatusAnswered:
		if err := tx.Commit(); err != nil {
			return HumanInteraction{}, false, fmt.Errorf(
				"commit idempotent human input answer: %w",
				err,
			)
		}
		return interaction, false, nil
	case InteractionStatusPending:
	default:
		return HumanInteraction{}, false, fmt.Errorf(
			"interaction %d has unexpected status %s",
			interactionID,
			interaction.Status,
		)
	}

	// A canceled execution is terminal and intentional; its pending question
	// must never be answered. The interaction row is left untouched (still
	// PENDING) for historical inspection, and no interaction.answered event is
	// appended. This read shares the answer transaction, so concurrent
	// answer/cancel requests are serialized deterministically: if cancellation
	// committed first the answer is rejected here, and if the answer committed
	// first cancellation still succeeds.
	_, executionStatus, _, _, err := executionInteractionTarget(
		ctx,
		tx,
		interaction.ExecutionID,
	)
	if err != nil {
		return HumanInteraction{}, false, err
	}
	if executionStatus == ExecutionStatusCanceled {
		return HumanInteraction{}, false, fmt.Errorf(
			"interaction %d cannot be answered: execution %d is %s",
			interactionID,
			interaction.ExecutionID,
			ExecutionStatusCanceled,
		)
	}

	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)

	result, err := tx.ExecContext(ctx, `
UPDATE human_interactions
SET status = ?,
    answer = ?,
    answered_at = ?
WHERE id = ?
  AND status = ?
`,
		InteractionStatusAnswered,
		answer,
		nowText,
		interactionID,
		InteractionStatusPending,
	)
	if err != nil {
		return HumanInteraction{}, false, fmt.Errorf("answer interaction %d: %w", interactionID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return HumanInteraction{}, false, fmt.Errorf(
			"check interaction %d answer: %w",
			interactionID,
			err,
		)
	}
	if rowsAffected != 1 {
		return HumanInteraction{}, false, fmt.Errorf(
			"interaction %d could not transition from %s to %s",
			interactionID,
			InteractionStatusPending,
			InteractionStatusAnswered,
		)
	}

	executionID := interaction.ExecutionID
	if err := d.appendSessionEvent(
		ctx,
		tx,
		interaction.SessionID,
		&executionID,
		EventTypeInteractionAnswered,
		map[string]any{
			"interaction_id": interactionID,
			"execution_id":   executionID,
			"status":         InteractionStatusAnswered,
		},
	); err != nil {
		return HumanInteraction{}, false, err
	}

	if err := tx.Commit(); err != nil {
		return HumanInteraction{}, false, fmt.Errorf(
			"commit interaction %d answer: %w",
			interactionID,
			err,
		)
	}

	answeredAt := now
	answerValue := answer
	interaction.Status = InteractionStatusAnswered
	interaction.Answer = &answerValue
	interaction.AnsweredAt = &answeredAt

	return interaction, true, nil
}

// ResumeAfterHumanInput transitions an answered WAITING_FOR_USER execution and
// its session back to RUNNING and appends the execution.resumed_after_input
// event. It rejects a waiting execution whose interaction is not yet answered,
// preserving the last committed checkpoint (the current step is not changed).
func (d *Database) ResumeAfterHumanInput(
	ctx context.Context,
	sessionID int64,
) (SessionExecution, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionExecution{}, fmt.Errorf("begin resume after input: %w", err)
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
`, sessionID).Scan(
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

	if execution.Status != ExecutionStatusWaitingForUser {
		return SessionExecution{}, fmt.Errorf(
			"execution %d is %s, expected %s",
			execution.ID,
			execution.Status,
			ExecutionStatusWaitingForUser,
		)
	}

	interaction, err := latestInteractionForExecution(ctx, tx, execution.ID)
	if err != nil {
		return SessionExecution{}, err
	}
	if interaction.Status != InteractionStatusAnswered {
		return SessionExecution{}, fmt.Errorf(
			"execution %d is waiting for an answer; interaction %d is %s",
			execution.ID,
			interaction.ID,
			interaction.Status,
		)
	}

	var sessionState string
	if err := tx.QueryRowContext(ctx, `
SELECT state FROM sessions WHERE id = ?
`, sessionID).Scan(&sessionState); err != nil {
		if err == sql.ErrNoRows {
			return SessionExecution{}, fmt.Errorf("session %d does not exist", sessionID)
		}
		return SessionExecution{}, fmt.Errorf("read session %d: %w", sessionID, err)
	}
	if sessionState != SessionStateWaitingForUser {
		return SessionExecution{}, fmt.Errorf(
			"session %d is %s, expected %s",
			sessionID,
			sessionState,
			SessionStateWaitingForUser,
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

	nowText := time.Now().UTC().Format(time.RFC3339Nano)

	if err := setExecutionStatus(
		ctx,
		tx,
		execution.ID,
		ExecutionStatusWaitingForUser,
		ExecutionStatusRunning,
		"resume execution after input",
	); err != nil {
		return SessionExecution{}, err
	}

	if err := setSessionState(
		ctx,
		tx,
		sessionID,
		SessionStateWaitingForUser,
		SessionStateRunning,
		nowText,
		"resume session after input",
	); err != nil {
		return SessionExecution{}, err
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&execution.ID,
		EventTypeExecutionResumedAfterInput,
		map[string]any{
			"execution_id":   execution.ID,
			"interaction_id": interaction.ID,
			"current_step":   execution.CurrentStep,
			"total_steps":    execution.TotalSteps,
			"status":         ExecutionStatusRunning,
		},
	); err != nil {
		return SessionExecution{}, err
	}

	if err := tx.Commit(); err != nil {
		return SessionExecution{}, fmt.Errorf(
			"commit execution %d resume after input: %w",
			execution.ID,
			err,
		)
	}

	execution.Status = ExecutionStatusRunning
	return execution, nil
}

// GetPendingHumanInput returns the single pending interaction for a session, or
// nil when the session has no pending question. It is read-only.
func (d *Database) GetPendingHumanInput(
	ctx context.Context,
	sessionID int64,
) (*HumanInteraction, error) {
	if err := d.ensureSessionExists(ctx, sessionID); err != nil {
		return nil, err
	}

	row := d.db.QueryRowContext(ctx, humanInteractionColumns+`
WHERE session_id = ? AND status = ?
ORDER BY id DESC
LIMIT 1
`, sessionID, InteractionStatusPending)

	interaction, err := scanHumanInteraction(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf(
			"read pending interaction for session %d: %w",
			sessionID,
			err,
		)
	}

	return &interaction, nil
}

// ListPendingHumanInteractions returns every pending interaction for a session
// in creation order. It is read-only.
func (d *Database) ListPendingHumanInteractions(
	ctx context.Context,
	sessionID int64,
) ([]HumanInteraction, error) {
	if err := d.ensureSessionExists(ctx, sessionID); err != nil {
		return nil, err
	}

	rows, err := d.db.QueryContext(ctx, humanInteractionColumns+`
WHERE session_id = ? AND status = ?
ORDER BY id ASC
`, sessionID, InteractionStatusPending)
	if err != nil {
		return nil, fmt.Errorf("list pending interactions: %w", err)
	}
	defer rows.Close()

	var interactions []HumanInteraction

	for rows.Next() {
		interaction, err := scanHumanInteraction(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pending interaction: %w", err)
		}
		interactions = append(interactions, interaction)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending interactions: %w", err)
	}

	return interactions, nil
}

// GetHumanInteraction returns a single interaction by ID. It is read-only.
func (d *Database) GetHumanInteraction(
	ctx context.Context,
	interactionID int64,
) (*HumanInteraction, error) {
	row := d.db.QueryRowContext(ctx, humanInteractionColumns+`
WHERE id = ?
`, interactionID)

	interaction, err := scanHumanInteraction(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("interaction %d does not exist", interactionID)
	}
	if err != nil {
		return nil, fmt.Errorf("read interaction %d: %w", interactionID, err)
	}

	return &interaction, nil
}
