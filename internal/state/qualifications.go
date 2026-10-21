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
	QualificationStatusQueued      = "QUEUED"
	QualificationStatusRunning     = "RUNNING"
	QualificationStatusCompleted   = "COMPLETED"
	QualificationStatusFailed      = "FAILED"
	QualificationStatusCancelled   = "CANCELLED"
	QualificationStatusInterrupted = "INTERRUPTED"
)

type QualificationJob struct {
	ID                   int64
	SessionID            int64
	ExecutionID          int64
	Model                string
	ProviderFingerprint  string
	Capability           string
	Repetitions          int
	TotalEvaluations     int
	CompletedEvaluations int
	Status               string
	StartedAt            *time.Time
	CompletedAt          *time.Time
	AggregateJSON        string
	Recommendation       string
	FailureKind          string
	FailureReason        string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	Results              []QualificationResult
}

type QualificationResult struct {
	ID                    int64
	JobID                 int64
	EvaluationIndex       int
	CaseName              string
	Attempt               int
	Passed                bool
	InfrastructureFailure bool
	ResultJSON            string
	CreatedAt             time.Time
}

func (d *Database) CreateQualificationJob(ctx context.Context, model, fingerprint, capability string, repetitions, total int) (QualificationJob, error) {
	model, capability = strings.TrimSpace(model), strings.TrimSpace(capability)
	if model == "" || capability == "" {
		return QualificationJob{}, fmt.Errorf("qualification model and capability are required")
	}
	if repetitions < 1 || total < 1 {
		return QualificationJob{}, fmt.Errorf("qualification repetitions and total evaluations must be positive")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return QualificationJob{}, fmt.Errorf("begin qualification creation: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	title := fmt.Sprintf("Qualify %s for %s", model, capability)
	result, err := tx.ExecContext(ctx, `INSERT INTO sessions (title,state,created_at,updated_at,kind,provider,model) VALUES (?,?,?,?,?,?,?)`, title, SessionStateQueued, nowText, nowText, "qualification", "openai-compatible", model)
	if err != nil {
		return QualificationJob{}, fmt.Errorf("create qualification session: %w", err)
	}
	sessionID, err := result.LastInsertId()
	if err != nil {
		return QualificationJob{}, err
	}
	seq, err := nextQueueSeq(ctx, tx)
	if err != nil {
		return QualificationJob{}, err
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO session_executions (session_id,status,current_step,total_steps,started_at,queue_seq,kind) VALUES (?,?,?,?,?,?,?)`, sessionID, ExecutionStatusQueued, 0, total, nowText, seq, "qualification")
	if err != nil {
		return QualificationJob{}, fmt.Errorf("create qualification execution: %w", err)
	}
	executionID, err := result.LastInsertId()
	if err != nil {
		return QualificationJob{}, err
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO qualification_jobs (session_id,execution_id,model,provider_fingerprint,capability,repetitions,total_evaluations,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, sessionID, executionID, model, fingerprint, capability, repetitions, total, QualificationStatusQueued, nowText, nowText)
	if err != nil {
		return QualificationJob{}, fmt.Errorf("create qualification job: %w", err)
	}
	jobID, err := result.LastInsertId()
	if err != nil {
		return QualificationJob{}, err
	}
	if err := d.appendSessionEvent(ctx, tx, sessionID, nil, EventTypeSessionCreated, map[string]any{"title": title, "state": SessionStateQueued, "kind": "qualification", "model": model}); err != nil {
		return QualificationJob{}, err
	}
	if err := d.appendSessionEvent(ctx, tx, sessionID, &executionID, EventTypeExecutionQueued, map[string]any{"execution_id": executionID, "current_step": 0, "total_steps": total, "queue_seq": seq, "status": ExecutionStatusQueued}); err != nil {
		return QualificationJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return QualificationJob{}, err
	}
	return QualificationJob{ID: jobID, SessionID: sessionID, ExecutionID: executionID, Model: model, ProviderFingerprint: fingerprint, Capability: capability, Repetitions: repetitions, TotalEvaluations: total, Status: QualificationStatusQueued, AggregateJSON: "{}", CreatedAt: now, UpdatedAt: now}, nil
}

const qualificationSelect = `SELECT id,session_id,execution_id,model,provider_fingerprint,capability,repetitions,total_evaluations,completed_evaluations,status,started_at,completed_at,aggregate_json,recommendation,failure_kind,failure_reason,created_at,updated_at FROM qualification_jobs`

func scanQualificationJob(scanner interface{ Scan(...any) error }) (QualificationJob, error) {
	var job QualificationJob
	var started, completed sql.NullString
	var created, updated string
	err := scanner.Scan(&job.ID, &job.SessionID, &job.ExecutionID, &job.Model, &job.ProviderFingerprint, &job.Capability, &job.Repetitions, &job.TotalEvaluations, &job.CompletedEvaluations, &job.Status, &started, &completed, &job.AggregateJSON, &job.Recommendation, &job.FailureKind, &job.FailureReason, &created, &updated)
	if err != nil {
		return job, err
	}
	if job.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return job, err
	}
	if job.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return job, err
	}
	if started.Valid {
		value, e := time.Parse(time.RFC3339Nano, started.String)
		if e != nil {
			return job, e
		}
		job.StartedAt = &value
	}
	if completed.Valid {
		value, e := time.Parse(time.RFC3339Nano, completed.String)
		if e != nil {
			return job, e
		}
		job.CompletedAt = &value
	}
	return job, nil
}

func (d *Database) GetQualificationJob(ctx context.Context, id int64) (QualificationJob, error) {
	job, err := scanQualificationJob(d.db.QueryRowContext(ctx, qualificationSelect+` WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return job, fmt.Errorf("qualification %d does not exist", id)
	}
	if err != nil {
		return job, err
	}
	job.Results, err = d.ListQualificationResults(ctx, id)
	return job, err
}
func (d *Database) GetQualificationJobByExecution(ctx context.Context, executionID int64) (QualificationJob, error) {
	job, err := scanQualificationJob(d.db.QueryRowContext(ctx, qualificationSelect+` WHERE execution_id=?`, executionID))
	if err != nil {
		return job, err
	}
	job.Results, err = d.ListQualificationResults(ctx, job.ID)
	return job, err
}
func (d *Database) ListQualificationJobs(ctx context.Context, limit int) ([]QualificationJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := d.db.QueryContext(ctx, qualificationSelect+` ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []QualificationJob
	for rows.Next() {
		job, e := scanQualificationJob(rows)
		if e != nil {
			return nil, e
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}
func (d *Database) ListQualificationResults(ctx context.Context, jobID int64) ([]QualificationResult, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id,job_id,evaluation_index,case_name,attempt,passed,infrastructure_failure,result_json,created_at FROM qualification_results WHERE job_id=? ORDER BY evaluation_index`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QualificationResult
	for rows.Next() {
		var item QualificationResult
		var passed, infra int
		var created string
		if err := rows.Scan(&item.ID, &item.JobID, &item.EvaluationIndex, &item.CaseName, &item.Attempt, &passed, &infra, &item.ResultJSON, &created); err != nil {
			return nil, err
		}
		item.Passed = passed != 0
		item.InfrastructureFailure = infra != 0
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (d *Database) MarkQualificationRunning(ctx context.Context, jobID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := d.db.ExecContext(ctx, `UPDATE qualification_jobs SET status=?,started_at=COALESCE(started_at,?),updated_at=? WHERE id=? AND status IN (?,?)`, QualificationStatusRunning, now, now, jobID, QualificationStatusQueued, QualificationStatusRunning)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fmt.Errorf("qualification %d is not queued or running", jobID)
	}
	return nil
}
func (d *Database) AddQualificationResult(ctx context.Context, jobID int64, evaluationIndex int, resultJSON string) error {
	var envelope struct {
		Case                  string `json:"case"`
		Attempt               int    `json:"attempt"`
		Passed                bool   `json:"passed"`
		InfrastructureFailure bool   `json:"infrastructure_failure"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &envelope); err != nil {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var executionID, completed, total int
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT execution_id,completed_evaluations,total_evaluations,status FROM qualification_jobs WHERE id=?`, jobID).Scan(&executionID, &completed, &total, &status); err != nil {
		return err
	}
	if status != QualificationStatusRunning {
		return fmt.Errorf("qualification %d is %s", jobID, status)
	}
	if evaluationIndex != completed || evaluationIndex >= total {
		return fmt.Errorf("qualification %d expected evaluation index %d, got %d", jobID, completed, evaluationIndex)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO qualification_results (job_id,evaluation_index,case_name,attempt,passed,infrastructure_failure,result_json,created_at) VALUES (?,?,?,?,?,?,?,?)`, jobID, evaluationIndex, envelope.Case, envelope.Attempt, envelope.Passed, envelope.InfrastructureFailure, resultJSON, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE qualification_jobs SET completed_evaluations=completed_evaluations+1,updated_at=? WHERE id=?`, now, jobID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE session_executions SET current_step=current_step+1 WHERE id=? AND status=?`, executionID, ExecutionStatusRunning); err != nil {
		return err
	}
	return tx.Commit()
}
func (d *Database) CompleteQualificationJob(ctx context.Context, jobID int64, aggregateJSON, recommendation string) error {
	return d.finishQualification(ctx, jobID, QualificationStatusCompleted, aggregateJSON, recommendation, "", "")
}
func (d *Database) FailQualificationJob(ctx context.Context, jobID int64, kind, reason string) error {
	return d.finishQualification(ctx, jobID, QualificationStatusFailed, "{}", "INCONCLUSIVE", kind, reason)
}
func (d *Database) finishQualification(ctx context.Context, jobID int64, jobStatus, aggregateJSON, recommendation, failureKind, failureReason string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sessionID, executionID int64
	var completed, total int
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT session_id,execution_id,completed_evaluations,total_evaluations,status FROM qualification_jobs WHERE id=?`, jobID).Scan(&sessionID, &executionID, &completed, &total, &status); err != nil {
		return err
	}
	if status != QualificationStatusRunning {
		return fmt.Errorf("qualification %d is %s", jobID, status)
	}
	if jobStatus == QualificationStatusCompleted && completed != total {
		return fmt.Errorf("qualification %d has completed %d of %d evaluations", jobID, completed, total)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	execStatus, sessionStatus := ExecutionStatusCompleted, SessionStateCompleted
	if jobStatus == QualificationStatusFailed {
		execStatus, sessionStatus = ExecutionStatusFailed, SessionStateFailed
	}
	if _, err = tx.ExecContext(ctx, `UPDATE qualification_jobs SET status=?,completed_at=?,updated_at=?,aggregate_json=?,recommendation=?,failure_kind=?,failure_reason=? WHERE id=?`, jobStatus, now, now, aggregateJSON, recommendation, failureKind, failureReason, jobID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE session_executions SET status=?,completed_at=?,failure_kind=?,failure_reason=? WHERE id=? AND status=?`, execStatus, now, failureKind, failureReason, executionID, ExecutionStatusRunning); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET state=?,updated_at=? WHERE id=? AND state=?`, sessionStatus, now, sessionID, SessionStateRunning); err != nil {
		return err
	}
	return tx.Commit()
}
func (d *Database) CancelQualificationJob(ctx context.Context, jobID int64, reason string) (QualificationJob, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return QualificationJob{}, err
	}
	defer tx.Rollback()
	job, err := scanQualificationJob(tx.QueryRowContext(ctx, qualificationSelect+` WHERE id=?`, jobID))
	if err != nil {
		return job, err
	}
	if job.Status != QualificationStatusQueued && job.Status != QualificationStatusRunning {
		return job, fmt.Errorf("qualification %d is %s; cancellation is not permitted", jobID, job.Status)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	reason = strings.TrimSpace(reason)
	if len(reason) > MaxCancelReasonLength {
		reason = reason[:MaxCancelReasonLength]
	}
	if _, err = tx.ExecContext(ctx, `UPDATE qualification_jobs SET status=?,completed_at=?,updated_at=?,failure_reason=? WHERE id=?`, QualificationStatusCancelled, now, now, reason, jobID); err != nil {
		return job, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE session_executions SET status=?,canceled_at=?,cancel_reason=? WHERE id=? AND status IN (?,?)`, ExecutionStatusCanceled, now, reason, job.ExecutionID, ExecutionStatusQueued, ExecutionStatusRunning); err != nil {
		return job, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET state=?,updated_at=? WHERE id=? AND state IN (?,?)`, SessionStateCanceled, now, job.SessionID, SessionStateQueued, SessionStateRunning); err != nil {
		return job, err
	}
	if err = tx.Commit(); err != nil {
		return job, err
	}
	return d.GetQualificationJob(ctx, jobID)
}
func (d *Database) InterruptQualificationJob(ctx context.Context, jobID int64) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var sessionID, executionID int64
	if err = tx.QueryRowContext(ctx, `SELECT session_id,execution_id FROM qualification_jobs WHERE id=? AND status=?`, jobID, QualificationStatusRunning).Scan(&sessionID, &executionID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE qualification_jobs SET status=?,updated_at=? WHERE id=?`, QualificationStatusInterrupted, now, jobID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE session_executions SET status=? WHERE id=? AND status=?`, ExecutionStatusInterrupted, executionID, ExecutionStatusRunning); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET state=?,updated_at=? WHERE id=? AND state=?`, SessionStateInterrupted, now, sessionID, SessionStateRunning); err != nil {
		return err
	}
	return tx.Commit()
}
func (d *Database) ReconcileInterruptedQualificationJobs(ctx context.Context) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := d.db.ExecContext(ctx, `UPDATE qualification_jobs SET status=?,updated_at=? WHERE status=? AND execution_id IN (SELECT id FROM session_executions WHERE status=?)`, QualificationStatusInterrupted, now, QualificationStatusRunning, ExecutionStatusInterrupted)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
func (d *Database) RetryQualificationJob(ctx context.Context, jobID int64) (QualificationJob, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return QualificationJob{}, err
	}
	defer tx.Rollback()
	job, err := scanQualificationJob(tx.QueryRowContext(ctx, qualificationSelect+` WHERE id=?`, jobID))
	if err != nil {
		return job, err
	}
	if job.Status != QualificationStatusInterrupted {
		return job, fmt.Errorf("qualification %d is %s, expected INTERRUPTED", jobID, job.Status)
	}
	seq, err := nextQueueSeq(ctx, tx)
	if err != nil {
		return job, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `UPDATE qualification_jobs SET status=?,updated_at=?,failure_kind='',failure_reason='' WHERE id=?`, QualificationStatusQueued, now, jobID); err != nil {
		return job, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE session_executions SET status=?,queue_seq=? WHERE id=? AND status=?`, ExecutionStatusQueued, seq, job.ExecutionID, ExecutionStatusInterrupted); err != nil {
		return job, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET state=?,updated_at=? WHERE id=? AND state=?`, SessionStateQueued, now, job.SessionID, SessionStateInterrupted); err != nil {
		return job, err
	}
	if err = tx.Commit(); err != nil {
		return job, err
	}
	return d.GetQualificationJob(ctx, jobID)
}
