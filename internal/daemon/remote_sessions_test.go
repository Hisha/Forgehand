package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/Hisha/Forgehand/internal/provider"
	"github.com/Hisha/Forgehand/internal/remoteapi"
	"github.com/Hisha/Forgehand/internal/state"
)

func TestRemoteSessionOperationsUseAuthoritativeStoreAndDispatcher(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenAt(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	project, err := db.CreateProject(ctx, "project", t.TempDir())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	var workers workerGroup
	dispatcher := newDispatcher(db, &workers, 1, &modelRuntime{defaultModel: "configured-model"}, nil)
	operations := &remoteSessionOperations{db: db, dispatcher: dispatcher}

	session, err := operations.CreateCodingSession(ctx, project.ID, "Remote work", "Implement it")
	if err != nil {
		t.Fatalf("create coding session: %v", err)
	}
	if session.Kind != "coding" || session.Provider != provider.ProviderID || session.Model != "configured-model" {
		t.Fatalf("session metadata = %+v", session)
	}

	execution, err := operations.QueueSessionExecution(ctx, session.ID)
	if err != nil {
		t.Fatalf("queue execution: %v", err)
	}
	if execution.Status != state.ExecutionStatusQueued || execution.TotalSteps != fakeWorkerSteps {
		t.Fatalf("execution = %+v", execution)
	}
	if len(dispatcher.wake) != 1 {
		t.Fatalf("dispatcher wake count = %d, want 1", len(dispatcher.wake))
	}

	_, err = operations.QueueSessionExecution(ctx, session.ID)
	if !errors.Is(err, remoteapi.ErrSessionConflict) {
		t.Fatalf("duplicate queue error = %v, want conflict", err)
	}
}

func TestRemoteSessionCreationFailsClosedWithoutModelRuntime(t *testing.T) {
	operations := &remoteSessionOperations{dispatcher: &dispatcher{}}
	_, err := operations.CreateCodingSession(context.Background(), 1, "Title", "Task")
	if !errors.Is(err, remoteapi.ErrExecutionUnavailable) {
		t.Fatalf("error = %v, want execution unavailable", err)
	}
}
