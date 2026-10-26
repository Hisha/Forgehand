package state

import (
	"context"
	"testing"
)

func TestReadOperationalCountsUsesDurableSessionStates(t *testing.T) {
	db := openTestDatabase(t)
	ctx := context.Background()
	for _, state := range []string{SessionStateWaitingForUser, SessionStateCompleted, SessionStateCompleted, SessionStateFailed} {
		if _, err := db.db.ExecContext(ctx, `INSERT INTO sessions (title,state,created_at,updated_at) VALUES ('status', ?, datetime('now'), datetime('now'))`, state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.ExecContext(ctx, `INSERT INTO session_executions (session_id,status,current_step,total_steps,started_at,queue_seq) VALUES (1, ?, 0, 1, datetime('now'), 1)`, ExecutionStatusQueued); err != nil {
		t.Fatal(err)
	}

	queued, counts, err := db.ReadOperationalCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if queued != 1 || counts.Pending != 1 || counts.Completed != 2 || counts.Failed != 1 {
		t.Fatalf("queued=%d counts=%#v", queued, counts)
	}
}
