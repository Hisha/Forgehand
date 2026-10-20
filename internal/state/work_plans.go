package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const (
	WorkPlanStatusPending   = "PENDING"
	WorkPlanStatusRunning   = "RUNNING"
	WorkPlanStatusCompleted = "COMPLETED"
	WorkPlanStatusBlocked   = "BLOCKED"
	WorkPlanStatusFailed    = "FAILED"

	WorkTaskStatusPending   = "PENDING"
	WorkTaskStatusReady     = "READY"
	WorkTaskStatusRunning   = "RUNNING"
	WorkTaskStatusCompleted = "COMPLETED"
	WorkTaskStatusBlocked   = "BLOCKED"
	WorkTaskStatusFailed    = "FAILED"
)

type WorkEvidence struct {
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"`
	BlobHash string `json:"blob_hash,omitempty"`
	Line     int    `json:"line,omitempty"`
	EndLine  int    `json:"end_line,omitempty"`
	Excerpt  string `json:"excerpt,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type WorkPlanTask struct {
	ID                 int64
	PlanID             int64
	Key                string
	Position           int
	Objective          string
	ExecutionMethod    string
	Dependencies       []string
	Inputs             map[string]string
	RequiredEvidence   string
	CompletionCriteria string
	Status             string
	RequiresModel      bool
	Result             string
	Evidence           []WorkEvidence
	BlockedReason      string
	FailureReason      string
	StartedAt          *time.Time
	CompletedAt        *time.Time
}

type WorkPlan struct {
	ID              int64
	ProjectID       int64
	SessionID       int64
	Objective       string
	OriginalRequest string
	ReadOnly        bool
	Status          string
	Clarification   string
	CommitHash      string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Tasks           []WorkPlanTask
}

type WorkPlanTaskSpec struct {
	Key, Objective, ExecutionMethod, RequiredEvidence, CompletionCriteria string
	Dependencies                                                          []string
	Inputs                                                                map[string]string
	RequiresModel                                                         bool
	BlockedReason                                                         string
}

type WorkPlanSpec struct {
	Objective, OriginalRequest, Clarification string
	ReadOnly                                  bool
	Tasks                                     []WorkPlanTaskSpec
}

// CreateWorkPlanSession creates the owning session, plan, and task graph. Root
// tasks start READY; semantic tasks explicitly rejected by the
// interpreter start BLOCKED and are never dispatched to a model implicitly.
func (d *Database) CreateWorkPlanSession(ctx context.Context, title string, projectID int64, spec WorkPlanSpec) (Session, WorkPlan, error) {
	if projectID <= 0 || len(spec.Tasks) == 0 {
		return Session{}, WorkPlan{}, fmt.Errorf("work plan requires a project and at least one task")
	}
	if _, err := d.GetProject(ctx, projectID); err != nil {
		return Session{}, WorkPlan{}, err
	}
	// Use the existing session creation path so the same lifecycle and event
	// conventions apply. The following plan insert is compensating-cleaned if it
	// fails; SQLite foreign keys prevent a plan without its session.
	pid, request := projectID, spec.OriginalRequest
	session, err := d.CreateSessionWithDetails(ctx, title, &pid, &request, "work-plan", "", "")
	if err != nil {
		return Session{}, WorkPlan{}, err
	}

	now := time.Now().UTC()
	status := WorkPlanStatusPending
	allBlocked := true
	for _, task := range spec.Tasks {
		if task.BlockedReason == "" {
			allBlocked = false
			break
		}
	}
	if allBlocked {
		status = WorkPlanStatusBlocked
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, WorkPlan{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO work_plans
(project_id, session_id, objective, original_request, read_only, status, clarification, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, projectID, session.ID, spec.Objective, spec.OriginalRequest, boolToInt(spec.ReadOnly), status, spec.Clarification, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return Session{}, WorkPlan{}, fmt.Errorf("create work plan: %w", err)
	}
	planID, _ := result.LastInsertId()
	keys := map[string]bool{}
	for _, task := range spec.Tasks {
		if task.Key == "" || keys[task.Key] {
			return Session{}, WorkPlan{}, fmt.Errorf("work plan task keys must be non-empty and unique")
		}
		keys[task.Key] = true
	}
	for i, task := range spec.Tasks {
		for _, dep := range task.Dependencies {
			if !keys[dep] {
				return Session{}, WorkPlan{}, fmt.Errorf("task %s has unknown dependency %s", task.Key, dep)
			}
		}
		deps, _ := json.Marshal(task.Dependencies)
		inputs, _ := json.Marshal(task.Inputs)
		taskStatus := WorkTaskStatusPending
		if task.BlockedReason != "" {
			taskStatus = WorkTaskStatusBlocked
		} else if len(task.Dependencies) == 0 {
			taskStatus = WorkTaskStatusReady
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO work_plan_tasks
(plan_id, task_key, position, objective, execution_method, dependencies_json, inputs_json, required_evidence, completion_criteria, status, requires_model, blocked_reason)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, planID, task.Key, i+1, task.Objective, task.ExecutionMethod, string(deps), string(inputs), task.RequiredEvidence, task.CompletionCriteria, taskStatus, boolToInt(task.RequiresModel), task.BlockedReason)
		if err != nil {
			return Session{}, WorkPlan{}, fmt.Errorf("create work plan task %s: %w", task.Key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Session{}, WorkPlan{}, fmt.Errorf("commit work plan: %w", err)
	}
	plan, err := d.GetWorkPlanBySession(ctx, session.ID)
	return session, *plan, err
}

func (d *Database) GetWorkPlanBySession(ctx context.Context, sessionID int64) (*WorkPlan, error) {
	row := d.db.QueryRowContext(ctx, `SELECT id, project_id, session_id, objective, original_request, read_only, status, clarification, commit_hash, created_at, updated_at FROM work_plans WHERE session_id = ?`, sessionID)
	plan, err := scanWorkPlan(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read work plan for session %d: %w", sessionID, err)
	}
	plan.Tasks, err = d.listWorkPlanTasks(ctx, plan.ID)
	if err != nil {
		return nil, err
	}
	return &plan, nil
}

func scanWorkPlan(row interface{ Scan(...any) error }) (WorkPlan, error) {
	var p WorkPlan
	var ro int
	var created, updated string
	if err := row.Scan(&p.ID, &p.ProjectID, &p.SessionID, &p.Objective, &p.OriginalRequest, &ro, &p.Status, &p.Clarification, &p.CommitHash, &created, &updated); err != nil {
		return p, err
	}
	p.ReadOnly = ro != 0
	var err error
	p.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return p, err
	}
	p.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return p, err
}

func (d *Database) listWorkPlanTasks(ctx context.Context, planID int64) ([]WorkPlanTask, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, plan_id, task_key, position, objective, execution_method, dependencies_json, inputs_json, required_evidence, completion_criteria, status, requires_model, result, evidence_json, blocked_reason, failure_reason, started_at, completed_at FROM work_plan_tasks WHERE plan_id = ? ORDER BY position`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []WorkPlanTask
	for rows.Next() {
		var t WorkPlanTask
		var deps, inputs, evidence string
		var model int
		var started, completed sql.NullString
		if err := rows.Scan(&t.ID, &t.PlanID, &t.Key, &t.Position, &t.Objective, &t.ExecutionMethod, &deps, &inputs, &t.RequiredEvidence, &t.CompletionCriteria, &t.Status, &model, &t.Result, &evidence, &t.BlockedReason, &t.FailureReason, &started, &completed); err != nil {
			return nil, err
		}
		t.RequiresModel = model != 0
		if err := json.Unmarshal([]byte(deps), &t.Dependencies); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(inputs), &t.Inputs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(evidence), &t.Evidence); err != nil {
			return nil, err
		}
		if started.Valid {
			v, err := time.Parse(time.RFC3339Nano, started.String)
			if err != nil {
				return nil, err
			}
			t.StartedAt = &v
		}
		if completed.Valid {
			v, err := time.Parse(time.RFC3339Nano, completed.String)
			if err != nil {
				return nil, err
			}
			t.CompletedAt = &v
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (d *Database) SetWorkPlanCommit(ctx context.Context, planID int64, commit string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE work_plans SET commit_hash = CASE WHEN commit_hash = '' THEN ? ELSE commit_hash END, updated_at = ? WHERE id = ?`, commit, time.Now().UTC().Format(time.RFC3339Nano), planID)
	return err
}

func (d *Database) StartWorkPlan(ctx context.Context, planID int64) error {
	_, err := d.db.ExecContext(ctx, `UPDATE work_plans SET status = ?, updated_at = ? WHERE id = ? AND status IN (?, ?)`, WorkPlanStatusRunning, time.Now().UTC().Format(time.RFC3339Nano), planID, WorkPlanStatusPending, WorkPlanStatusRunning)
	return err
}

func (d *Database) StartWorkPlanTask(ctx context.Context, taskID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := d.db.ExecContext(ctx, `UPDATE work_plan_tasks SET status = ?, started_at = COALESCE(started_at, ?) WHERE id = ? AND status = ?`, WorkTaskStatusRunning, now, taskID, WorkTaskStatusReady)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("work plan task %d is not ready", taskID)
	}
	return nil
}

func (d *Database) FinishWorkPlanTask(ctx context.Context, taskID int64, status, resultText, reason string, evidence []WorkEvidence) error {
	if status != WorkTaskStatusCompleted && status != WorkTaskStatusBlocked && status != WorkTaskStatusFailed {
		return fmt.Errorf("invalid terminal work task status %s", status)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	blocked, failed := "", ""
	if status == WorkTaskStatusBlocked {
		blocked = reason
	}
	if status == WorkTaskStatusFailed {
		failed = reason
	}
	_, err = d.db.ExecContext(ctx, `UPDATE work_plan_tasks SET status = ?, result = ?, evidence_json = ?, blocked_reason = ?, failure_reason = ?, completed_at = ? WHERE id = ? AND status = ?`, status, resultText, string(encoded), blocked, failed, time.Now().UTC().Format(time.RFC3339Nano), taskID, WorkTaskStatusRunning)
	return err
}

// RefreshWorkPlanReadiness advances pending tasks whose prerequisites
// completed and blocks tasks whose prerequisite terminated unsuccessfully.
func (d *Database) RefreshWorkPlanReadiness(ctx context.Context, planID int64) error {
	tasks, err := d.listWorkPlanTasks(ctx, planID)
	if err != nil {
		return err
	}
	statuses := map[string]string{}
	for _, task := range tasks {
		statuses[task.Key] = task.Status
	}
	for _, task := range tasks {
		if task.Status != WorkTaskStatusPending {
			continue
		}
		ready, bad := true, ""
		for _, dep := range task.Dependencies {
			switch statuses[dep] {
			case WorkTaskStatusCompleted:
			default:
				ready = false
				if statuses[dep] == WorkTaskStatusBlocked || statuses[dep] == WorkTaskStatusFailed {
					bad = dep
				}
			}
		}
		if bad != "" {
			_, err = d.db.ExecContext(ctx, `UPDATE work_plan_tasks SET status = ?, blocked_reason = ?, completed_at = ? WHERE id = ? AND status = ?`, WorkTaskStatusBlocked, "prerequisite "+bad+" did not complete", time.Now().UTC().Format(time.RFC3339Nano), task.ID, WorkTaskStatusPending)
		} else if ready {
			_, err = d.db.ExecContext(ctx, `UPDATE work_plan_tasks SET status = ? WHERE id = ? AND status = ?`, WorkTaskStatusReady, task.ID, WorkTaskStatusPending)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *Database) FinishWorkPlan(ctx context.Context, planID int64) error {
	tasks, err := d.listWorkPlanTasks(ctx, planID)
	if err != nil {
		return err
	}
	status := WorkPlanStatusCompleted
	for _, task := range tasks {
		if task.Status == WorkTaskStatusFailed {
			status = WorkPlanStatusFailed
			break
		}
		if task.Status == WorkTaskStatusBlocked {
			status = WorkPlanStatusBlocked
		}
	}
	_, err = d.db.ExecContext(ctx, `UPDATE work_plans SET status = ?, updated_at = ? WHERE id = ?`, status, time.Now().UTC().Format(time.RFC3339Nano), planID)
	return err
}

// RecoverRunningWorkPlanTasks makes deterministic read-only work resumable
// after the daemon has converted orphaned RUNNING executions to INTERRUPTED.
func (d *Database) RecoverRunningWorkPlanTasks(ctx context.Context) (int64, error) {
	result, err := d.db.ExecContext(ctx, `UPDATE work_plan_tasks SET status = ?, started_at = NULL WHERE status = ?`, WorkTaskStatusReady, WorkTaskStatusRunning)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
