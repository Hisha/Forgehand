package state

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	SessionStateReady          = "READY"
	SessionStateRunning        = "RUNNING"
	SessionStateWaitingForUser = "WAITING_FOR_USER"
	SessionStateCompleted      = "COMPLETED"
	SessionStateInterrupted    = "INTERRUPTED"
	SessionStateFailed         = "FAILED"
)

type Session struct {
	ID        int64
	Title     string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func trimTitle(title string) string {
	return strings.TrimSpace(title)
}

func (d *Database) CreateSession(ctx context.Context, title string) (Session, error) {
	return d.CreateSessionWithEvent(ctx, title)
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
