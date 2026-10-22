package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	SessionStateReady          = "READY"
	SessionStateQueued         = "QUEUED"
	SessionStateRunning        = "RUNNING"
	SessionStateWaitingForUser = "WAITING_FOR_USER"
	SessionStateCompleted      = "COMPLETED"
	SessionStateInterrupted    = "INTERRUPTED"
	SessionStateFailed         = "FAILED"
	SessionStateCanceled       = "CANCELED"
)

type Session struct {
	ID        int64
	Title     string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
	ProjectID *int64
	Task      string
	Kind      string
	Provider  string
	Model     string
}

func trimTitle(title string) string {
	return strings.TrimSpace(title)
}

func (d *Database) CreateSession(ctx context.Context, title string) (Session, error) {
	return d.CreateSessionWithEvent(ctx, title)
}

// MaxSessionsForProject bounds the number of sessions a single project query
// returns, so that a project with a very long history cannot produce an
// unbounded response.
const MaxSessionsForProject = 1000

// ListSessionsForProject returns every session that belongs to a project,
// ordered by identifier. Sessions that are not bound to a project are never
// returned by this query.
func (d *Database) ListSessionsForProject(
	ctx context.Context,
	projectID int64,
) ([]Session, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("project ID must be positive")
	}

	rows, err := d.db.QueryContext(ctx, `
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
WHERE project_id = ?
ORDER BY id
LIMIT ?
`, projectID, MaxSessionsForProject)
	if err != nil {
		return nil, fmt.Errorf("list sessions for project: %w", err)
	}
	defer rows.Close()

	var sessions []Session

	for rows.Next() {
		var (
			session     Session
			createdText string
			updatedText string
		)

		var (
			projectIDValue sql.NullInt64
			task           sql.NullString
			kind           sql.NullString
			provider       sql.NullString
			model          sql.NullString
		)

		if err := rows.Scan(
			&session.ID,
			&session.Title,
			&session.State,
			&createdText,
			&updatedText,
			&projectIDValue,
			&task,
			&kind,
			&provider,
			&model,
		); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}

		session.CreatedAt, err = time.Parse(time.RFC3339Nano, createdText)
		if err != nil {
			return nil, fmt.Errorf("parse created_at for session %d: %w", session.ID, err)
		}

		session.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedText)
		if err != nil {
			return nil, fmt.Errorf("parse updated_at for session %d: %w", session.ID, err)
		}

		if projectIDValue.Valid {
			pid := projectIDValue.Int64
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

		sessions = append(sessions, session)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions for project: %w", err)
	}

	return sessions, nil
}

func (d *Database) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := d.db.QueryContext(ctx, `
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

		var (
			projectID sql.NullInt64
			task      sql.NullString
			kind      sql.NullString
			provider  sql.NullString
			model     sql.NullString
		)

		if err := rows.Scan(
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

		sessions = append(sessions, session)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}

	return sessions, nil
}
