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
	// Topics names the investigation topics this record supports. It is empty
	// for evidence produced by operations that are not topic-driven.
	Topics []string `json:"topics,omitempty"`
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
	ID                  int64
	ProjectID           int64
	SessionID           int64
	Objective           string
	OriginalRequest     string
	ReadOnly            bool
	Status              string
	Clarification       string
	Constraints         []string
	UnresolvedQuestions []string
	Revision            int
	CommitHash          string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Tasks               []WorkPlanTask
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
	Constraints, UnresolvedQuestions          []string
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
	constraints, _ := json.Marshal(spec.Constraints)
	questions, _ := json.Marshal(spec.UnresolvedQuestions)
	result, err := tx.ExecContext(ctx, `INSERT INTO work_plans
(project_id, session_id, objective, original_request, read_only, status, clarification, constraints_json, unresolved_questions_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, projectID, session.ID, spec.Objective, spec.OriginalRequest, boolToInt(spec.ReadOnly), status, spec.Clarification, string(constraints), string(questions), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
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
	row := d.db.QueryRowContext(ctx, `SELECT id, project_id, session_id, objective, original_request, read_only, status, clarification, constraints_json, unresolved_questions_json, revision, commit_hash, created_at, updated_at FROM work_plans WHERE session_id = ?`, sessionID)
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
	var constraints, questions string
	var created, updated string
	if err := row.Scan(&p.ID, &p.ProjectID, &p.SessionID, &p.Objective, &p.OriginalRequest, &ro, &p.Status, &p.Clarification, &constraints, &questions, &p.Revision, &p.CommitHash, &created, &updated); err != nil {
		return p, err
	}
	p.ReadOnly = ro != 0
	if err := json.Unmarshal([]byte(constraints), &p.Constraints); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(questions), &p.UnresolvedQuestions); err != nil {
		return p, err
	}
	var err error
	p.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return p, err
	}
	p.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return p, err
}

// ExpandWorkPlan appends a validated semantic proposal to the existing plan.
// Existing task rows are never updated or deleted, so completed results and
// evidence survive every revision. Root tasks in the proposal are ordered
// after the planning task that triggered the revision.
func (d *Database) ExpandWorkPlan(ctx context.Context, planID int64, triggerTaskKey string, spec WorkPlanSpec, proposalJSON string, maxTasks, maxRevisions int) error {
	if len(spec.Tasks) == 0 {
		return fmt.Errorf("work plan expansion requires at least one task")
	}
	if maxTasks <= 0 {
		maxTasks = 24
	}
	if maxRevisions <= 0 {
		maxRevisions = 3
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sessionID int64
	var revision, existingCount int
	if err := tx.QueryRowContext(ctx, `SELECT session_id, revision FROM work_plans WHERE id = ?`, planID).Scan(&sessionID, &revision); err != nil {
		return err
	}
	if revision >= maxRevisions {
		return fmt.Errorf("work plan revision limit of %d reached", maxRevisions)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_plan_tasks WHERE plan_id = ?`, planID).Scan(&existingCount); err != nil {
		return err
	}
	if existingCount+len(spec.Tasks) > maxTasks {
		return fmt.Errorf("work plan expansion would create %d tasks; maximum is %d", existingCount+len(spec.Tasks), maxTasks)
	}
	var triggerStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM work_plan_tasks WHERE plan_id = ? AND task_key = ?`, planID, triggerTaskKey).Scan(&triggerStatus); err != nil {
		return err
	}
	if triggerStatus != WorkTaskStatusCompleted {
		return fmt.Errorf("planning trigger task %q is %s, not completed", triggerTaskKey, triggerStatus)
	}
	existing := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT task_key FROM work_plan_tasks WHERE plan_id = ?`, planID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		existing[key] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	proposed := map[string]int{}
	for i, task := range spec.Tasks {
		_, duplicated := proposed[task.Key]
		if task.Key == "" || existing[task.Key] || duplicated {
			return fmt.Errorf("work plan expansion task key %q is empty or already used", task.Key)
		}
		proposed[task.Key] = i + 1
	}
	for i, task := range spec.Tasks {
		deps := append([]string(nil), task.Dependencies...)
		if len(deps) == 0 {
			deps = []string{triggerTaskKey}
		}
		for _, dep := range deps {
			proposedPosition, isProposed := proposed[dep]
			if !existing[dep] && !isProposed {
				return fmt.Errorf("task %s has unknown dependency %s", task.Key, dep)
			}
			if isProposed && proposedPosition > i {
				return fmt.Errorf("task %s depends on later task %s", task.Key, dep)
			}
		}
		depsJSON, _ := json.Marshal(deps)
		inputsJSON, _ := json.Marshal(task.Inputs)
		status := WorkTaskStatusPending
		if task.BlockedReason != "" {
			status = WorkTaskStatusBlocked
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO work_plan_tasks
(plan_id, task_key, position, objective, execution_method, dependencies_json, inputs_json, required_evidence, completion_criteria, status, requires_model, blocked_reason)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, planID, task.Key, existingCount+i+1, task.Objective, task.ExecutionMethod, string(depsJSON), string(inputsJSON), task.RequiredEvidence, task.CompletionCriteria, status, boolToInt(task.RequiresModel), task.BlockedReason)
		if err != nil {
			return fmt.Errorf("append work plan task %s: %w", task.Key, err)
		}
	}
	constraints, _ := json.Marshal(spec.Constraints)
	questions, _ := json.Marshal(spec.UnresolvedQuestions)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	newRevision := revision + 1
	if _, err = tx.ExecContext(ctx, `UPDATE work_plans SET objective = ?, read_only = ?, constraints_json = ?, unresolved_questions_json = ?, clarification = ?, revision = ?, updated_at = ? WHERE id = ?`, spec.Objective, boolToInt(spec.ReadOnly), string(constraints), string(questions), spec.Clarification, newRevision, now, planID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_plan_revisions (plan_id, revision, trigger_task_key, proposal_json, created_at) VALUES (?, ?, ?, ?, ?)`, planID, newRevision, triggerTaskKey, proposalJSON, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE session_executions SET total_steps = ? WHERE session_id = ? AND status = 'RUNNING'`, existingCount+len(spec.Tasks), sessionID); err != nil {
		return err
	}
	return tx.Commit()
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
