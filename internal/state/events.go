package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const (
	EventTypeSessionCreated             = "session.created"
	EventTypeExecutionStarted           = "execution.started"
	EventTypeExecutionQueued            = "execution.queued"
	EventTypeExecutionDispatched        = "execution.dispatched"
	EventTypeExecutionCheckpoint        = "execution.checkpoint"
	EventTypeExecutionCompleted         = "execution.completed"
	EventTypeExecutionInterrupted       = "execution.interrupted"
	EventTypeExecutionFailed            = "execution.failed"
	EventTypeExecutionCanceled          = "execution.canceled"
	EventTypeExecutionResumed           = "execution.resumed"
	EventTypeInteractionRequested       = "interaction.requested"
	EventTypeInteractionAnswered        = "interaction.answered"
	EventTypeExecutionWaitingForUser    = "execution.waiting_for_user"
	EventTypeExecutionResumedAfterInput = "execution.resumed_after_input"
)

type SessionEvent struct {
	ID          int64
	SessionID   int64
	ExecutionID *int64
	Type        string
	Timestamp   time.Time
	Details     map[string]any
}

type sessionEventRow struct {
	ID            int64
	SessionID     int64
	ExecutionID   sql.NullInt64
	EventType     string
	TimestampText string
	DetailsJSON   string
}

func (d *Database) appendSessionEvent(
	ctx context.Context,
	tx *sql.Tx,
	sessionID int64,
	executionID *int64,
	eventType string,
	details map[string]any,
) error {
	if details == nil {
		details = map[string]any{}
	}

	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("marshal event details: %w", err)
	}

	nowText := time.Now().UTC().Format(time.RFC3339Nano)

	_, err = tx.ExecContext(ctx, `
INSERT INTO session_events (
	session_id,
	execution_id,
	event_type,
	timestamp,
	details_json
)
VALUES (?, ?, ?, ?, ?)
`,
		sessionID,
		executionID,
		eventType,
		nowText,
		string(detailsJSON),
	)
	if err != nil {
		return fmt.Errorf("insert session event: %w", err)
	}

	return nil
}

func (d *Database) CreateSessionWithEvent(
	ctx context.Context,
	title string,
) (Session, error) {
	title = trimTitle(title)
	if title == "" {
		return Session{}, fmt.Errorf("session title must not be empty")
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, fmt.Errorf("begin create session: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)

	result, err := tx.ExecContext(ctx, `
INSERT INTO sessions (
	title,
	state,
	created_at,
	updated_at
)
VALUES (?, ?, ?, ?)
`,
		title,
		SessionStateReady,
		nowText,
		nowText,
	)
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Session{}, fmt.Errorf("read session id: %w", err)
	}

	if err := d.appendSessionEvent(
		ctx,
		tx,
		id,
		nil,
		EventTypeSessionCreated,
		map[string]any{
			"title": title,
			"state": SessionStateReady,
		},
	); err != nil {
		return Session{}, err
	}

	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit create session: %w", err)
	}

	return Session{
		ID:        id,
		Title:     title,
		State:     SessionStateReady,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (d *Database) ListSessionEvents(
	ctx context.Context,
	sessionID int64,
	afterEventID int64,
	limit int,
) ([]SessionEvent, int64, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	var exists int
	if err := d.db.QueryRowContext(ctx,
		`SELECT 1 FROM sessions WHERE id = ?`,
		sessionID,
	).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return nil, 0, fmt.Errorf("session %d does not exist", sessionID)
		}
		return nil, 0, fmt.Errorf("check session %d: %w", sessionID, err)
	}

	rows, err := d.db.QueryContext(ctx, `
SELECT
	id,
	session_id,
	execution_id,
	event_type,
	timestamp,
	details_json
FROM session_events
WHERE session_id = ? AND id > ?
ORDER BY id ASC
LIMIT ?
`,
		sessionID,
		afterEventID,
		limit,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list session events: %w", err)
	}
	defer rows.Close()

	var events []SessionEvent
	var lastID int64
	for rows.Next() {
		var row sessionEventRow
		if err := rows.Scan(
			&row.ID,
			&row.SessionID,
			&row.ExecutionID,
			&row.EventType,
			&row.TimestampText,
			&row.DetailsJSON,
		); err != nil {
			return nil, lastID, fmt.Errorf("scan event: %w", err)
		}

		timestamp, err := time.Parse(time.RFC3339Nano, row.TimestampText)
		if err != nil {
			return nil, lastID, fmt.Errorf("parse event timestamp: %w", err)
		}

		details := map[string]any{}
		if row.DetailsJSON != "" && row.DetailsJSON != "{}" {
			if err := json.Unmarshal([]byte(row.DetailsJSON), &details); err != nil {
				// best effort - keep empty details on parse error
				details = map[string]any{}
			}
		}

		event := SessionEvent{
			ID:        row.ID,
			SessionID: row.SessionID,
			Type:      row.EventType,
			Timestamp: timestamp,
			Details:   details,
		}
		if row.ExecutionID.Valid {
			execID := row.ExecutionID.Int64
			event.ExecutionID = &execID
		}

		events = append(events, event)
		lastID = row.ID
	}

	if err := rows.Err(); err != nil {
		return nil, lastID, fmt.Errorf("iterate events: %w", err)
	}

	nextCursor := lastID
	return events, nextCursor, nil
}
