package state

import (
	"context"
	"errors"
	"testing"
)

func TestSessionExecutionRecordsPreserveHistoryAndResults(t *testing.T) {
	db := openTestDatabase(t)
	ctx := context.Background()
	project, err := db.CreateProject(ctx, "History", t.TempDir())
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	session, err := db.CreateCodingSession(ctx, "Inspect", project.ID, "do it", "provider", "model")
	if err != nil {
		t.Fatalf("CreateCodingSession: %v", err)
	}

	first, err := db.StartSessionExecution(ctx, session.ID, 1)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	if err := db.CompleteModelExecutionWithMetrics(ctx, first.ID, "provider-a", "model-a", "first\nresult", ModelMetrics{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5, LatencyMS: 17}); err != nil {
		t.Fatalf("CompleteModelExecutionWithMetrics: %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE sessions SET state = ? WHERE id = ?`, SessionStateReady, session.ID); err != nil {
		t.Fatalf("reset session fixture: %v", err)
	}
	second, err := db.StartSessionExecution(ctx, session.ID, 1)
	if err != nil {
		t.Fatalf("StartSessionExecution second: %v", err)
	}
	if err := db.FailSessionExecutionWithReason(ctx, second.ID, "provider", "provider unavailable"); err != nil {
		t.Fatalf("FailSessionExecutionWithReason: %v", err)
	}

	records, truncated, err := db.ListSessionExecutionRecords(ctx, session.ID, 10)
	if err != nil {
		t.Fatalf("ListSessionExecutionRecords: %v", err)
	}
	if truncated || len(records) != 2 {
		t.Fatalf("history len=%d truncated=%v", len(records), truncated)
	}
	if records[0].Execution.ID != second.ID || records[0].Result != nil || records[0].Execution.Status != ExecutionStatusFailed {
		t.Fatalf("unexpected latest record: %#v", records[0])
	}
	if records[1].Execution.ID != first.ID || records[1].Result == nil || records[1].Result.Content != "" {
		t.Fatalf("history should expose result metadata without content: %#v", records[1])
	}

	detail, err := db.GetSessionExecutionRecord(ctx, session.ID, first.ID, 0, 1024)
	if err != nil {
		t.Fatalf("GetSessionExecutionRecord: %v", err)
	}
	if detail.Result == nil || detail.Result.Content != "first\nresult" || detail.Result.TotalTokens != 5 || detail.ResultTotalCharacters != 12 {
		t.Fatalf("unexpected detail result: %#v", detail.Result)
	}
	limited, wasTruncated, err := db.ListSessionExecutionRecords(ctx, session.ID, 1)
	if err != nil || !wasTruncated || len(limited) != 1 || limited[0].Execution.ID != second.ID {
		t.Fatalf("bounded history=%#v truncated=%v err=%v", limited, wasTruncated, err)
	}
}

func TestSessionExecutionRecordIsScopedToSession(t *testing.T) {
	db := openTestDatabase(t)
	ctx := context.Background()
	project, _ := db.CreateProject(ctx, "Scope", t.TempDir())
	first, _ := db.CreateCodingSession(ctx, "One", project.ID, "one", "provider", "model")
	second, _ := db.CreateCodingSession(ctx, "Two", project.ID, "two", "provider", "model")
	execution, err := db.StartSessionExecution(ctx, first.ID, 1)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	if _, err := db.GetSessionExecutionRecord(ctx, second.ID, execution.ID, 0, 1024); !errors.Is(err, ErrSessionExecutionNotFound) {
		t.Fatalf("cross-session lookup error=%v", err)
	}
}
