package state

import (
	"context"
	"fmt"
)

// OperationalSessionCounts is a cheap aggregate over the indexed session
// state column. RUNNING is intentionally not included: live daemon worker
// ownership is the authoritative source for current running activity.
type OperationalSessionCounts struct {
	Pending   int
	Completed int
	Failed    int
}

// ReadOperationalCounts returns durable queue and session history metrics in
// one read-only query. The existing state indexes keep this bounded by the
// number of distinct state values rather than result history size.
func (d *Database) ReadOperationalCounts(ctx context.Context) (int, OperationalSessionCounts, error) {
	var queued int
	if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM session_executions
WHERE status = ?
`, ExecutionStatusQueued).Scan(&queued); err != nil {
		return 0, OperationalSessionCounts{}, fmt.Errorf("count queued executions: %w", err)
	}

	rows, err := d.db.QueryContext(ctx, `
SELECT state, COUNT(*)
FROM sessions
WHERE state IN (?, ?, ?)
GROUP BY state
`, SessionStateWaitingForUser, SessionStateCompleted, SessionStateFailed)
	if err != nil {
		return 0, OperationalSessionCounts{}, fmt.Errorf("count operational sessions: %w", err)
	}
	defer rows.Close()

	var counts OperationalSessionCounts
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return 0, OperationalSessionCounts{}, fmt.Errorf("scan operational session count: %w", err)
		}
		switch state {
		case SessionStateWaitingForUser:
			counts.Pending = count
		case SessionStateCompleted:
			counts.Completed = count
		case SessionStateFailed:
			counts.Failed = count
		}
	}
	if err := rows.Err(); err != nil {
		return 0, OperationalSessionCounts{}, fmt.Errorf("read operational session counts: %w", err)
	}
	return queued, counts, nil
}
