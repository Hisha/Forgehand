package state

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	SessionStateReady     = "READY"
	SessionStateRunning   = "RUNNING"
	SessionStateCompleted = "COMPLETED"
)

type Session struct {
	ID        int64
	Title     string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (d *Database) CreateSession(ctx context.Context, title string) (Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Session{}, fmt.Errorf("session title must not be empty")
	}

	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)

	result, err := d.db.ExecContext(ctx, `
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

	return Session{
		ID:        id,
		Title:     title,
		State:     SessionStateReady,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (d *Database) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT
	id,
	title,
	state,
	created_at,
	updated_at
FROM sessions
ORDER BY id
`)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []Session

	for rows.Next() {
		var (
			session     Session
			createdText string
			updatedText string
		)

		if err := rows.Scan(
			&session.ID,
			&session.Title,
			&session.State,
			&createdText,
			&updatedText,
		); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}

		session.CreatedAt, err = time.Parse(time.RFC3339Nano, createdText)
		if err != nil {
			return nil, fmt.Errorf(
				"parse created_at for session %d: %w",
				session.ID,
				err,
			)
		}

		session.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedText)
		if err != nil {
			return nil, fmt.Errorf(
				"parse updated_at for session %d: %w",
				session.ID,
				err,
			)
		}

		sessions = append(sessions, session)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}

	return sessions, nil
}
