package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrSessionExecutionNotFound is returned when an execution does not belong to
// the requested session. Callers should not distinguish that case from a
// missing execution.
var ErrSessionExecutionNotFound = errors.New("session execution not found")

// SessionExecutionRecord joins an execution to its optional persisted model
// result. Non-completed and non-model executions legitimately have no result.
type SessionExecutionRecord struct {
	Execution             SessionExecution
	Result                *ModelResult
	ResultTotalCharacters int
}

// ListSessionExecutionRecords returns a bounded, newest-first execution
// history. The boolean result reports whether older rows were omitted.
func (d *Database) ListSessionExecutionRecords(
	ctx context.Context,
	sessionID int64,
	limit int,
) ([]SessionExecutionRecord, bool, error) {
	if sessionID <= 0 || limit <= 0 {
		return nil, false, fmt.Errorf("session identifier and limit must be positive")
	}
	rows, err := d.db.QueryContext(ctx, executionRecordSelect(false)+`
WHERE e.session_id = ?
ORDER BY e.id DESC
LIMIT ?
`, sessionID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list executions for session %d: %w", sessionID, err)
	}
	defer rows.Close()

	records := make([]SessionExecutionRecord, 0, limit)
	for rows.Next() {
		record, err := scanSessionExecutionRecord(rows, false)
		if err != nil {
			return nil, false, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate executions for session %d: %w", sessionID, err)
	}
	truncated := len(records) > limit
	if truncated {
		records = records[:limit]
	}
	return records, truncated, nil
}

// GetSessionExecutionRecord returns one execution and an optional bounded page
// of its model result, scoped to the parent session.
func (d *Database) GetSessionExecutionRecord(
	ctx context.Context,
	sessionID, executionID int64,
	contentOffset, contentLimit int,
) (SessionExecutionRecord, error) {
	if sessionID <= 0 || executionID <= 0 || contentOffset < 0 || contentLimit <= 0 {
		return SessionExecutionRecord{}, ErrSessionExecutionNotFound
	}
	record, err := scanSessionExecutionRecord(d.db.QueryRowContext(ctx, executionRecordSelect(true)+`
WHERE e.session_id = ? AND e.id = ?
`, contentOffset+1, contentLimit, sessionID, executionID), true)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionExecutionRecord{}, ErrSessionExecutionNotFound
	}
	if err != nil {
		return SessionExecutionRecord{}, err
	}
	return record, nil
}

func executionRecordSelect(includeContent bool) string {
	contentColumn := ""
	if includeContent {
		contentColumn = ", substr(mr.content, ?, ?), length(mr.content)"
	}
	return `
SELECT
	e.id, e.session_id, e.status, e.current_step, e.total_steps,
	e.started_at, e.completed_at, e.canceled_at, e.cancel_reason,
	e.failure_kind, e.failure_reason, e.queue_seq, e.kind,
	mr.id, mr.session_id, mr.execution_id, mr.provider, mr.model,
	mr.created_at, mr.prompt_tokens, mr.completion_tokens,
	mr.total_tokens, mr.latency_ms` + contentColumn + `
FROM session_executions e
LEFT JOIN model_results mr ON mr.execution_id = e.id
`
}

type sqlScanner interface {
	Scan(dest ...any) error
}

func scanSessionExecutionRecord(scanner sqlScanner, includeContent bool) (SessionExecutionRecord, error) {
	var (
		record                                       SessionExecutionRecord
		started                                      string
		completed, canceled, cancelReason            sql.NullString
		failureKind, failureReason, executionKind    sql.NullString
		queueSeq                                     sql.NullInt64
		resultID, resultSessionID, resultExecutionID sql.NullInt64
		resultProvider, resultModel, resultCreated   sql.NullString
		promptTokens, completionTokens, totalTokens  sql.NullInt64
		latencyMS                                    sql.NullInt64
		content                                      sql.NullString
		contentLength                                sql.NullInt64
	)
	dest := []any{
		&record.Execution.ID, &record.Execution.SessionID, &record.Execution.Status,
		&record.Execution.CurrentStep, &record.Execution.TotalSteps, &started,
		&completed, &canceled, &cancelReason, &failureKind,
		&failureReason, &queueSeq, &executionKind,
		&resultID, &resultSessionID, &resultExecutionID, &resultProvider,
		&resultModel, &resultCreated, &promptTokens, &completionTokens,
		&totalTokens, &latencyMS,
	}
	if includeContent {
		dest = append(dest, &content, &contentLength)
	}
	if err := scanner.Scan(dest...); err != nil {
		return SessionExecutionRecord{}, err
	}

	var err error
	record.Execution.StartedAt, err = time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return SessionExecutionRecord{}, fmt.Errorf("parse started_at for execution %d: %w", record.Execution.ID, err)
	}
	if completed.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, completed.String)
		if parseErr != nil {
			return SessionExecutionRecord{}, fmt.Errorf("parse completed_at for execution %d: %w", record.Execution.ID, parseErr)
		}
		record.Execution.CompletedAt = &value
	}
	if canceled.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, canceled.String)
		if parseErr != nil {
			return SessionExecutionRecord{}, fmt.Errorf("parse canceled_at for execution %d: %w", record.Execution.ID, parseErr)
		}
		record.Execution.CanceledAt = &value
	}
	if cancelReason.Valid {
		value := cancelReason.String
		record.Execution.CancelReason = &value
	}
	if queueSeq.Valid {
		value := queueSeq.Int64
		record.Execution.QueueSeq = &value
	}
	record.Execution.FailureKind = failureKind.String
	record.Execution.FailureReason = failureReason.String
	record.Execution.Kind = executionKind.String

	if resultID.Valid {
		created, parseErr := time.Parse(time.RFC3339Nano, resultCreated.String)
		if parseErr != nil {
			return SessionExecutionRecord{}, fmt.Errorf("parse result created_at for execution %d: %w", record.Execution.ID, parseErr)
		}
		record.Result = &ModelResult{
			ID: resultID.Int64, SessionID: resultSessionID.Int64,
			ExecutionID: resultExecutionID.Int64, Provider: resultProvider.String,
			Model: resultModel.String, Content: content.String, CreatedAt: created,
			PromptTokens: int(promptTokens.Int64), CompletionTokens: int(completionTokens.Int64),
			TotalTokens: int(totalTokens.Int64), LatencyMS: latencyMS.Int64,
		}
		record.ResultTotalCharacters = int(contentLength.Int64)
	}
	return record, nil
}
