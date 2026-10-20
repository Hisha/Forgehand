package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// rowQuerier is satisfied by both *sql.DB and *sql.Tx, so a queue helper can
// run inside or outside a transaction.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// executionResources reads the persisted exclusive resource requirements for a
// single execution, sorted for deterministic scheduling and display.
func executionResources(
	ctx context.Context,
	q rowQuerier,
	executionID int64,
) ([]string, error) {
	rows, err := q.QueryContext(ctx, `
SELECT resource
FROM execution_resources
WHERE execution_id = ?
ORDER BY resource ASC
`, executionID)
	if err != nil {
		return nil, fmt.Errorf(
			"read resources for execution %d: %w",
			executionID,
			err,
		)
	}
	defer rows.Close()

	var resources []string
	for rows.Next() {
		var resource string
		if err := rows.Scan(&resource); err != nil {
			return nil, fmt.Errorf(
				"scan resource for execution %d: %w",
				executionID,
				err,
			)
		}
		resources = append(resources, resource)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate resources for execution %d: %w",
			executionID,
			err,
		)
	}
	return resources, nil
}

// queuedExecutionsWithResources reads every QUEUED execution in FIFO order
// (queue sequence, then row id) together with its resource requirements. It is
// the selection input for resource-aware claiming and for the resource-blocked
// status count. A single joined query avoids nested queries on one transaction
// connection.
func queuedExecutionsWithResources(
	ctx context.Context,
	q rowQuerier,
) ([]SessionExecution, error) {
	rows, err := q.QueryContext(ctx, `
SELECT
	e.id,
	e.session_id,
	e.status,
	e.current_step,
	e.total_steps,
	e.started_at,
	e.queue_seq,
	e.kind,
	r.resource
FROM session_executions e
LEFT JOIN execution_resources r ON r.execution_id = e.id
WHERE e.status = ?
ORDER BY e.queue_seq ASC, e.id ASC, r.resource ASC
`, ExecutionStatusQueued)
	if err != nil {
		return nil, fmt.Errorf("read queued executions: %w", err)
	}
	defer rows.Close()

	var executions []SessionExecution
	indices := make(map[int64]int)
	for rows.Next() {
		var (
			execution   SessionExecution
			startedText string
			queueSeq    sql.NullInt64
			kind        sql.NullString
			resource    sql.NullString
		)
		if err := rows.Scan(
			&execution.ID,
			&execution.SessionID,
			&execution.Status,
			&execution.CurrentStep,
			&execution.TotalSteps,
			&startedText,
			&queueSeq,
			&kind,
			&resource,
		); err != nil {
			return nil, fmt.Errorf("scan queued execution: %w", err)
		}

		if index, ok := indices[execution.ID]; ok {
			if resource.Valid {
				executions[index].Resources = append(
					executions[index].Resources,
					resource.String,
				)
			}
			continue
		}

		startedAt, err := time.Parse(time.RFC3339Nano, startedText)
		if err != nil {
			return nil, fmt.Errorf(
				"parse execution %d start time: %w",
				execution.ID,
				err,
			)
		}
		execution.StartedAt = startedAt
		if queueSeq.Valid {
			value := queueSeq.Int64
			execution.QueueSeq = &value
		}
		if kind.Valid {
			execution.Kind = kind.String
		}
		if resource.Valid {
			execution.Resources = append(execution.Resources, resource.String)
		}

		indices[execution.ID] = len(executions)
		executions = append(executions, execution)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate queued executions: %w", err)
	}
	return executions, nil
}

// resourcesConflict reports whether an execution's required resources overlap
// the set of resources already held by running executions.
func resourcesConflict(required []string, held map[string]struct{}) bool {
	for _, resource := range required {
		if _, ok := held[resource]; ok {
			return true
		}
	}
	return false
}

func heldResourceSet(heldResources []string) map[string]struct{} {
	held := make(map[string]struct{}, len(heldResources))
	for _, resource := range heldResources {
		held[resource] = struct{}{}
	}
	return held
}

// nextQueueSeq returns the next durable, globally monotonic queue sequence.
// Because every writer takes the write lock at BEGIN (txlock=immediate), two
// concurrent enqueues cannot observe the same maximum.
func nextQueueSeq(ctx context.Context, tx *sql.Tx) (int64, error) {
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(queue_seq), 0) + 1 FROM session_executions`,
	).Scan(&seq); err != nil {
		return 0, fmt.Errorf("compute next queue sequence: %w", err)
	}
	return seq, nil
}

// queuePosition returns the 1-based position of a queued execution among all
// currently QUEUED executions, ordered by queue sequence. A position of 0 means
// the execution is no longer queued.
func queuePosition(ctx context.Context, q rowQuerier, queueSeq int64) (int, error) {
	var position int
	if err := q.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM session_executions
WHERE status = ? AND queue_seq <= ?
`, ExecutionStatusQueued, queueSeq).Scan(&position); err != nil {
		return 0, fmt.Errorf("compute queue position: %w", err)
	}
	return position, nil
}

// latestExecutionForQueue reads a session's most recent execution together with
// its queue sequence. It returns sql.ErrNoRows when the session has no
// execution.
func latestExecutionForQueue(
	ctx context.Context,
	tx *sql.Tx,
	sessionID int64,
) (SessionExecution, error) {
	var (
		execution   SessionExecution
		startedText string
		queueSeq    sql.NullInt64
		kind        sql.NullString
	)

	err := tx.QueryRowContext(ctx, `
SELECT
	id,
	session_id,
	status,
	current_step,
	total_steps,
	started_at,
	queue_seq,
	kind
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
		&startedText,
		&queueSeq,
		&kind,
	)
	if err != nil {
		return SessionExecution{}, err
	}

	startedAt, err := time.Parse(time.RFC3339Nano, startedText)
	if err != nil {
		return SessionExecution{}, fmt.Errorf(
			"parse execution %d start time: %w",
			execution.ID,
			err,
		)
	}
	execution.StartedAt = startedAt
	if queueSeq.Valid {
		value := queueSeq.Int64
		execution.QueueSeq = &value
	}
	if kind.Valid {
		execution.Kind = kind.String
	}

	resources, err := executionResources(ctx, tx, execution.ID)
	if err != nil {
		return SessionExecution{}, err
	}
	execution.Resources = resources

	return execution, nil
}

// QueueSessionExecution places a READY session on the durable FIFO queue as a
// new QUEUED execution and appends the execution.queued event in one
// transaction. The dispatcher later claims it and transitions it to RUNNING.
// Only a READY session can be queued; a session already QUEUED or RUNNING is
// rejected. Optional exclusive resource requirements are persisted alongside
// the execution; the dispatcher acquires them for the execution's whole run.
func (d *Database) QueueSessionExecution(
	ctx context.Context,
	sessionID int64,
	totalSteps int,
	resources ...string,
) (SessionExecution, error) {
	if totalSteps <= 0 {
		return SessionExecution{}, fmt.Errorf("total steps must be greater than zero")
	}

	normalizedResources, err := normalizeResources(resources)
	if err != nil {
		return SessionExecution{}, fmt.Errorf("queue session execution: %w", err)
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionExecution{}, fmt.Errorf("begin queue session execution: %w", err)
	}
	defer tx.Rollback()

	var (
		state string
		kind  sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT state, kind FROM sessions WHERE id = ?`,
		sessionID,
	).Scan(&state, &kind)
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

	executionKind := "fake"
	if kind.Valid && kind.String != "" {
		executionKind = kind.String
	}

	seq, err := nextQueueSeq(ctx, tx)
	if err != nil {
		return SessionExecution{}, err
	}

	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)

	result, err := tx.ExecContext(ctx, `
INSERT INTO session_executions (
	session_id,
	status,
	current_step,
	total_steps,
	started_at,
	queue_seq,
	kind
)
VALUES (?, ?, 0, ?, ?, ?, ?)
`,
		sessionID,
		ExecutionStatusQueued,
		totalSteps,
		nowText,
		seq,
		executionKind,
	)
	if err != nil {
		return SessionExecution{}, fmt.Errorf(
			"queue execution for session %d: %w",
			sessionID,
			err,
		)
	}

	executionID, err := result.LastInsertId()
	if err != nil {
		return SessionExecution{}, fmt.Errorf("read execution id: %w", err)
	}

	for _, resource := range normalizedResources {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO execution_resources (execution_id, resource)
VALUES (?, ?)
`,
			executionID,
			resource,
		); err != nil {
			return SessionExecution{}, fmt.Errorf(
				"record resource %q for execution %d: %w",
				resource,
				executionID,
				err,
			)
		}
	}

	if err := setSessionState(
		ctx,
		tx,
		sessionID,
		SessionStateReady,
		SessionStateQueued,
		nowText,
		"queue session",
	); err != nil {
		return SessionExecution{}, err
	}

	execID := executionID
	queuedDetails := map[string]any{
		"execution_id": executionID,
		"current_step": 0,
		"total_steps":  totalSteps,
		"queue_seq":    seq,
		"status":       ExecutionStatusQueued,
	}
	if len(normalizedResources) > 0 {
		queuedDetails["resources"] = normalizedResources
	}
	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&execID,
		EventTypeExecutionQueued,
		queuedDetails,
	); err != nil {
		return SessionExecution{}, err
	}

	position, err := queuePosition(ctx, tx, seq)
	if err != nil {
		return SessionExecution{}, err
	}

	if err := tx.Commit(); err != nil {
		return SessionExecution{}, fmt.Errorf("commit queue execution: %w", err)
	}

	queueSeq := seq
	return SessionExecution{
		ID:            executionID,
		SessionID:     sessionID,
		Status:        ExecutionStatusQueued,
		CurrentStep:   0,
		TotalSteps:    totalSteps,
		StartedAt:     now,
		QueueSeq:      &queueSeq,
		QueuePosition: position,
		Kind:          executionKind,
		Resources:     normalizedResources,
	}, nil
}

// QueueResumeExecution places an INTERRUPTED session's execution on the queue,
// preserving its committed checkpoint. It is idempotent: if the execution is
// already QUEUED it is returned unchanged without appending a second event.
func (d *Database) QueueResumeExecution(
	ctx context.Context,
	sessionID int64,
) (SessionExecution, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionExecution{}, fmt.Errorf("begin queue execution resume: %w", err)
	}
	defer tx.Rollback()

	execution, err := latestExecutionForQueue(ctx, tx, sessionID)
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

	if execution.Status == ExecutionStatusQueued {
		if err := tx.Commit(); err != nil {
			return SessionExecution{}, fmt.Errorf("commit idempotent queue resume: %w", err)
		}
		return execution, nil
	}

	if execution.Status != ExecutionStatusInterrupted {
		return SessionExecution{}, fmt.Errorf(
			"execution %d is %s, expected %s",
			execution.ID,
			execution.Status,
			ExecutionStatusInterrupted,
		)
	}

	var sessionState string
	if err := tx.QueryRowContext(ctx,
		`SELECT state FROM sessions WHERE id = ?`,
		sessionID,
	).Scan(&sessionState); err != nil {
		if err == sql.ErrNoRows {
			return SessionExecution{}, fmt.Errorf("session %d does not exist", sessionID)
		}
		return SessionExecution{}, fmt.Errorf("read session %d: %w", sessionID, err)
	}
	if sessionState != SessionStateInterrupted {
		return SessionExecution{}, fmt.Errorf(
			"session %d is %s, expected %s",
			sessionID,
			sessionState,
			SessionStateInterrupted,
		)
	}

	seq, err := nextQueueSeq(ctx, tx)
	if err != nil {
		return SessionExecution{}, err
	}

	nowText := time.Now().UTC().Format(time.RFC3339Nano)

	if err := setExecutionStatus(
		ctx,
		tx,
		execution.ID,
		ExecutionStatusInterrupted,
		ExecutionStatusQueued,
		"queue execution resume",
	); err != nil {
		return SessionExecution{}, err
	}
	if err := setSessionState(
		ctx,
		tx,
		sessionID,
		SessionStateInterrupted,
		SessionStateQueued,
		nowText,
		"queue session resume",
	); err != nil {
		return SessionExecution{}, err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE session_executions SET queue_seq = ? WHERE id = ?`,
		seq,
		execution.ID,
	); err != nil {
		return SessionExecution{}, fmt.Errorf(
			"record resume queue sequence for execution %d: %w",
			execution.ID,
			err,
		)
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&execution.ID,
		EventTypeExecutionQueued,
		map[string]any{
			"execution_id": execution.ID,
			"current_step": execution.CurrentStep,
			"total_steps":  execution.TotalSteps,
			"queue_seq":    seq,
			"status":       ExecutionStatusQueued,
			"resumed":      true,
		},
	); err != nil {
		return SessionExecution{}, err
	}

	position, err := queuePosition(ctx, tx, seq)
	if err != nil {
		return SessionExecution{}, err
	}

	if err := tx.Commit(); err != nil {
		return SessionExecution{}, fmt.Errorf(
			"commit queue execution %d resume: %w",
			execution.ID,
			err,
		)
	}

	execution.Status = ExecutionStatusQueued
	queueSeq := seq
	execution.QueueSeq = &queueSeq
	execution.QueuePosition = position
	return execution, nil
}

// QueueResumeAfterHumanInput places an answered WAITING_FOR_USER session's
// execution back on the queue, preserving its committed checkpoint. It rejects
// a waiting execution whose latest interaction is not yet answered, and is
// idempotent when the execution is already QUEUED.
func (d *Database) QueueResumeAfterHumanInput(
	ctx context.Context,
	sessionID int64,
) (SessionExecution, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionExecution{}, fmt.Errorf("begin queue resume after input: %w", err)
	}
	defer tx.Rollback()

	execution, err := latestExecutionForQueue(ctx, tx, sessionID)
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

	if execution.Status == ExecutionStatusQueued {
		if err := tx.Commit(); err != nil {
			return SessionExecution{}, fmt.Errorf("commit idempotent queue resume: %w", err)
		}
		return execution, nil
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
	if err := tx.QueryRowContext(ctx,
		`SELECT state FROM sessions WHERE id = ?`,
		sessionID,
	).Scan(&sessionState); err != nil {
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

	seq, err := nextQueueSeq(ctx, tx)
	if err != nil {
		return SessionExecution{}, err
	}

	nowText := time.Now().UTC().Format(time.RFC3339Nano)

	if err := setExecutionStatus(
		ctx,
		tx,
		execution.ID,
		ExecutionStatusWaitingForUser,
		ExecutionStatusQueued,
		"queue resume after input",
	); err != nil {
		return SessionExecution{}, err
	}
	if err := setSessionState(
		ctx,
		tx,
		sessionID,
		SessionStateWaitingForUser,
		SessionStateQueued,
		nowText,
		"queue resume after input",
	); err != nil {
		return SessionExecution{}, err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE session_executions SET queue_seq = ? WHERE id = ?`,
		seq,
		execution.ID,
	); err != nil {
		return SessionExecution{}, fmt.Errorf(
			"record resume queue sequence for execution %d: %w",
			execution.ID,
			err,
		)
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		sessionID,
		&execution.ID,
		EventTypeExecutionQueued,
		map[string]any{
			"execution_id":   execution.ID,
			"interaction_id": interaction.ID,
			"current_step":   execution.CurrentStep,
			"total_steps":    execution.TotalSteps,
			"queue_seq":      seq,
			"status":         ExecutionStatusQueued,
			"resumed":        true,
		},
	); err != nil {
		return SessionExecution{}, err
	}

	position, err := queuePosition(ctx, tx, seq)
	if err != nil {
		return SessionExecution{}, err
	}

	if err := tx.Commit(); err != nil {
		return SessionExecution{}, fmt.Errorf(
			"commit queue execution %d resume after input: %w",
			execution.ID,
			err,
		)
	}

	execution.Status = ExecutionStatusQueued
	queueSeq := seq
	execution.QueueSeq = &queueSeq
	execution.QueuePosition = position
	return execution, nil
}

// ClaimNextQueuedExecution atomically transitions the oldest eligible QUEUED
// execution (by queue sequence, then row id) to RUNNING and its session to
// RUNNING, appending the execution.dispatched event in the same transaction.
// An execution is eligible when none of its required exclusive resources is
// already in heldResources; blocked executions are skipped so unrelated work
// proceeds, while FIFO is preserved among eligible executions. It returns
// ok=false when no execution is queued or every queued execution is blocked.
// Running under the immediate write lock makes it safe for a single dispatcher
// and for competing test dispatchers alike.
func (d *Database) ClaimNextQueuedExecution(
	ctx context.Context,
	heldResources []string,
) (SessionExecution, bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionExecution{}, false, fmt.Errorf("begin claim queued execution: %w", err)
	}
	defer tx.Rollback()

	queued, err := queuedExecutionsWithResources(ctx, tx)
	if err != nil {
		return SessionExecution{}, false, err
	}

	held := heldResourceSet(heldResources)
	var execution *SessionExecution
	for index := range queued {
		if !resourcesConflict(queued[index].Resources, held) {
			execution = &queued[index]
			break
		}
	}
	if execution == nil {
		if err := tx.Commit(); err != nil {
			return SessionExecution{}, false, fmt.Errorf("commit empty claim: %w", err)
		}
		return SessionExecution{}, false, nil
	}

	nowText := time.Now().UTC().Format(time.RFC3339Nano)

	if err := setExecutionStatus(
		ctx,
		tx,
		execution.ID,
		ExecutionStatusQueued,
		ExecutionStatusRunning,
		"dispatch execution",
	); err != nil {
		return SessionExecution{}, false, err
	}
	if err := setSessionState(
		ctx,
		tx,
		execution.SessionID,
		SessionStateQueued,
		SessionStateRunning,
		nowText,
		"dispatch session",
	); err != nil {
		return SessionExecution{}, false, err
	}

	details := map[string]any{
		"execution_id": execution.ID,
		"current_step": execution.CurrentStep,
		"total_steps":  execution.TotalSteps,
		"status":       ExecutionStatusRunning,
	}
	if execution.QueueSeq != nil {
		details["queue_seq"] = *execution.QueueSeq
	}
	if len(execution.Resources) > 0 {
		details["resources"] = execution.Resources
	}
	if err := d.appendSessionEvent(
		ctx,
		tx,
		execution.SessionID,
		&execution.ID,
		EventTypeExecutionDispatched,
		details,
	); err != nil {
		return SessionExecution{}, false, err
	}

	if err := tx.Commit(); err != nil {
		return SessionExecution{}, false, fmt.Errorf(
			"commit dispatch of execution %d: %w",
			execution.ID,
			err,
		)
	}

	execution.Status = ExecutionStatusRunning
	return *execution, true, nil
}

// CountQueuedExecutions reports how many executions are currently QUEUED. It is
// read-only and backs the daemon status worker-capacity report.
func (d *Database) CountQueuedExecutions(ctx context.Context) (int, error) {
	var count int
	if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM session_executions
WHERE status = ?
`, ExecutionStatusQueued).Scan(&count); err != nil {
		return 0, fmt.Errorf("count queued executions: %w", err)
	}
	return count, nil
}

// CountResourceBlockedQueuedExecutions reports how many QUEUED executions are
// currently blocked because at least one of their required exclusive resources
// is held by a running execution. It is read-only and backs the daemon status
// resource-blocked report.
func (d *Database) CountResourceBlockedQueuedExecutions(
	ctx context.Context,
	heldResources []string,
) (int, error) {
	if len(heldResources) == 0 {
		return 0, nil
	}

	queued, err := queuedExecutionsWithResources(ctx, d.db)
	if err != nil {
		return 0, err
	}

	held := heldResourceSet(heldResources)
	count := 0
	for _, execution := range queued {
		if resourcesConflict(execution.Resources, held) {
			count++
		}
	}
	return count, nil
}
