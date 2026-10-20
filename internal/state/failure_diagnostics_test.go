package state

import (
	"context"
	"strings"
	"testing"
)

func TestFailureDiagnosticPersistsAcrossReopenAndIsRedacted(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := OpenAt(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	session, err := db.CreateSession(ctx, "durable failure")
	if err != nil {
		t.Fatal(err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	unsafe := `provider rejected request: Authorization: Bearer secret-token API_KEY=sk-abcdefghijk password=hunter2 client_secret="provider-private"`
	if err := db.FailSessionExecutionWithReason(ctx, execution.ID, FailureKindProvider, unsafe); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenAt(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, got, err := reopened.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.FailureKind != FailureKindProvider {
		t.Fatalf("persisted failure = %#v", got)
	}
	for _, secret := range []string{"secret-token", "sk-abcdefghijk", "hunter2", "provider-private"} {
		if strings.Contains(got.FailureReason, secret) {
			t.Fatalf("failure reason leaked %q: %q", secret, got.FailureReason)
		}
	}
	if !strings.Contains(got.FailureReason, "provider rejected request") || !strings.Contains(got.FailureReason, "[REDACTED]") {
		t.Fatalf("failure reason lost useful context: %q", got.FailureReason)
	}

	events, _, err := reopened.ListSessionEvents(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var failed *SessionEvent
	for i := range events {
		if events[i].Type == EventTypeExecutionFailed {
			failed = &events[i]
		}
	}
	if failed == nil || failed.Details["failure_kind"] != FailureKindProvider || failed.Details["failure_reason"] != got.FailureReason {
		t.Fatalf("execution.failed diagnostic = %#v", failed)
	}
}
