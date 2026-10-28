package state

import (
	"context"
	"fmt"
)

// RestartWorkState is the authoritative durable execution state used by the
// daemon lifecycle coordinator. It intentionally contains counts only: callers
// do not need session, project, model, or task details to decide readiness.
type RestartWorkState struct {
	QueuedExecutions         int
	RunningExecutions        int
	WaitingExecutions        int
	RunningWorkPlans         int
	RunningQualificationJobs int
	UnknownExecutions        int
}

// InspectRestartWorkState reads all restart-relevant durable state in one
// read transaction. Any failed query makes the entire inspection unusable.
func (d *Database) InspectRestartWorkState(ctx context.Context) (RestartWorkState, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return RestartWorkState{}, fmt.Errorf("begin restart work inspection: %w", err)
	}
	defer tx.Rollback()

	var result RestartWorkState
	queries := []struct {
		name   string
		query  string
		status string
		count  *int
	}{
		{"queued executions", `SELECT COUNT(*) FROM session_executions WHERE status = ?`, ExecutionStatusQueued, &result.QueuedExecutions},
		{"running executions", `SELECT COUNT(*) FROM session_executions WHERE status = ?`, ExecutionStatusRunning, &result.RunningExecutions},
		{"waiting executions", `SELECT COUNT(*) FROM session_executions WHERE status = ?`, ExecutionStatusWaitingForUser, &result.WaitingExecutions},
		{"running work plans", `SELECT COUNT(*) FROM work_plans WHERE status = ?`, WorkPlanStatusRunning, &result.RunningWorkPlans},
		{"running qualification jobs", `SELECT COUNT(*) FROM qualification_jobs WHERE status = ?`, QualificationStatusRunning, &result.RunningQualificationJobs},
	}
	for _, item := range queries {
		if err := tx.QueryRowContext(ctx, item.query, item.status).Scan(item.count); err != nil {
			return RestartWorkState{}, fmt.Errorf("count %s: %w", item.name, err)
		}
	}
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM session_executions
WHERE status IS NULL OR status NOT IN (?, ?, ?, ?, ?, ?, ?)
`, ExecutionStatusQueued, ExecutionStatusRunning, ExecutionStatusWaitingForUser,
		ExecutionStatusInterrupted, ExecutionStatusCompleted, ExecutionStatusFailed,
		ExecutionStatusCanceled).Scan(&result.UnknownExecutions); err != nil {
		return RestartWorkState{}, fmt.Errorf("count executions with unknown state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RestartWorkState{}, fmt.Errorf("commit restart work inspection: %w", err)
	}
	return result, nil
}
