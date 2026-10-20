package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrNoInvestigationEvidence = errors.New("no investigation evidence")

type InvestigationRequest struct {
	ComponentID string `json:"component_id,omitempty"`
	AnalysisID  int64  `json:"analysis_id,omitempty"`
}

type InvestigationSourceLocation struct {
	Line        int    `json:"line"`
	Declaration string `json:"declaration,omitempty"`
}

type InvestigationEvidenceFile struct {
	Path      string                        `json:"path"`
	BlobHash  string                        `json:"blob_hash"`
	Size      int                           `json:"size"`
	Reasons   []string                      `json:"reasons,omitempty"`
	Locations []InvestigationSourceLocation `json:"locations,omitempty"`
}

// InvestigationEvidence is persisted in the existing session event log. The
// path/blob pairs are the immutable selection boundary used by retries.
type InvestigationEvidence struct {
	ProjectID   int64                       `json:"project_id"`
	InventoryID int64                       `json:"inventory_id"`
	AnalysisID  int64                       `json:"analysis_id,omitempty"`
	Commit      string                      `json:"commit"`
	ComponentID string                      `json:"component_id,omitempty"`
	PromptTask  string                      `json:"prompt_task"`
	Files       []InvestigationEvidenceFile `json:"files"`
	BudgetChars int                         `json:"budget_chars"`
	UsedChars   int                         `json:"used_chars"`
	Truncated   bool                        `json:"truncated"`
	Warnings    []string                    `json:"warnings,omitempty"`
}

func (d *Database) CreateInvestigationSession(ctx context.Context, title string, projectID int64, question, provider, model, componentID string, analysisID int64) (Session, error) {
	pid := projectID
	task := question
	details := map[string]any{
		"investigation": true,
	}
	if componentID = strings.TrimSpace(componentID); componentID != "" {
		details["component_id"] = componentID
	}
	if analysisID > 0 {
		details["analysis_id"] = analysisID
	}
	return d.createSessionWithDetails(ctx, title, &pid, &task, "investigation", provider, model, details)
}

func (d *Database) InvestigationRequest(ctx context.Context, sessionID int64) (InvestigationRequest, error) {
	var encoded string
	err := d.db.QueryRowContext(ctx, `
SELECT details_json
FROM session_events
WHERE session_id = ? AND event_type = ?
ORDER BY id ASC LIMIT 1
`, sessionID, EventTypeSessionCreated).Scan(&encoded)
	if err != nil {
		return InvestigationRequest{}, fmt.Errorf("read investigation request: %w", err)
	}
	var details struct {
		ComponentID string `json:"component_id"`
		AnalysisID  int64  `json:"analysis_id"`
	}
	if err := json.Unmarshal([]byte(encoded), &details); err != nil {
		return InvestigationRequest{}, fmt.Errorf("decode investigation request: %w", err)
	}
	return InvestigationRequest{ComponentID: details.ComponentID, AnalysisID: details.AnalysisID}, nil
}

// RecordInvestigationEvidence appends the immutable selection while the
// execution is RUNNING. A prior selection wins, making retries idempotent.
func (d *Database) RecordInvestigationEvidence(ctx context.Context, executionID int64, evidence InvestigationEvidence) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin investigation evidence: %w", err)
	}
	defer tx.Rollback()

	var sessionID int64
	var status, kind string
	if err := tx.QueryRowContext(ctx, `
SELECT e.session_id, e.status, s.kind
FROM session_executions e JOIN sessions s ON s.id = e.session_id
WHERE e.id = ?
`, executionID).Scan(&sessionID, &status, &kind); err != nil {
		return fmt.Errorf("read investigation execution: %w", err)
	}
	if status != ExecutionStatusRunning || kind != "investigation" {
		return fmt.Errorf("execution %d is not a running investigation", executionID)
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM session_events WHERE session_id = ? AND event_type = ? LIMIT 1`, sessionID, EventTypeInvestigationEvidence).Scan(&exists)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("check investigation evidence: %w", err)
	}

	encoded, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("encode investigation evidence: %w", err)
	}
	var details map[string]any
	if err := json.Unmarshal(encoded, &details); err != nil {
		return fmt.Errorf("prepare investigation evidence event: %w", err)
	}
	if err := d.appendSessionEvent(ctx, tx, sessionID, &executionID, EventTypeInvestigationEvidence, details); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit investigation evidence: %w", err)
	}
	return nil
}

func (d *Database) InvestigationEvidence(ctx context.Context, sessionID int64) (InvestigationEvidence, error) {
	var encoded string
	err := d.db.QueryRowContext(ctx, `
SELECT details_json FROM session_events
WHERE session_id = ? AND event_type = ?
ORDER BY id ASC LIMIT 1
`, sessionID, EventTypeInvestigationEvidence).Scan(&encoded)
	if err == sql.ErrNoRows {
		return InvestigationEvidence{}, ErrNoInvestigationEvidence
	}
	if err != nil {
		return InvestigationEvidence{}, fmt.Errorf("read investigation evidence: %w", err)
	}
	var evidence InvestigationEvidence
	if err := json.Unmarshal([]byte(encoded), &evidence); err != nil {
		return InvestigationEvidence{}, fmt.Errorf("decode investigation evidence: %w", err)
	}
	return evidence, nil
}
