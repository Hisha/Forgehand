package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/provider"
	"github.com/Hisha/Forgehand/internal/state"
)

type qualificationFakeProvider func(context.Context, provider.ChatRequest) (provider.ChatResponse, error)

func (f qualificationFakeProvider) Complete(ctx context.Context, request provider.ChatRequest) (provider.ChatResponse, error) {
	return f(ctx, request)
}

func TestQualificationWorkerCompletesModelFailuresWithoutInfrastructureFailure(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	job, err := db.CreateQualificationJob(ctx, "worker", "fp", "planning", 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	execution, ok, err := db.ClaimNextQueuedExecution(ctx, nil)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	runtime := newModelRuntime(qualificationFakeProvider(func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Content: `{}`}, nil
	}), nil)
	runtime.defaultModel = "worker"
	runtime.configuration = "fp"
	runtime.maxTokens = 2048
	runtime.runQualificationWorker(ctx, db, execution)
	stored, err := db.GetQualificationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != state.QualificationStatusCompleted || stored.Recommendation != "NOT_QUALIFIED" || stored.CompletedEvaluations != 7 {
		t.Fatalf("stored=%#v", stored)
	}
}

func TestQualificationWorkerClassifiesProviderFailureAsInfrastructure(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	job, err := db.CreateQualificationJob(ctx, "worker", "fp", "planning", 2, 14)
	if err != nil {
		t.Fatal(err)
	}
	execution, _, _ := db.ClaimNextQueuedExecution(ctx, nil)
	runtime := newModelRuntime(qualificationFakeProvider(func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{}, errors.New("provider unavailable")
	}), nil)
	runtime.defaultModel = "worker"
	runtime.maxTokens = 2048
	runtime.runQualificationWorker(ctx, db, execution)
	stored, _ := db.GetQualificationJob(ctx, job.ID)
	if stored.Status != state.QualificationStatusFailed || stored.Recommendation != "INCONCLUSIVE" || stored.FailureKind != "provider" || stored.CompletedEvaluations != 1 {
		t.Fatalf("stored=%#v", stored)
	}
}

func TestQualificationWorkerClassifiesTimeoutAsInfrastructure(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx := context.Background()
	job, err := db.CreateQualificationJob(ctx, "worker", "fp", "planning", 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	execution, _, _ := db.ClaimNextQueuedExecution(ctx, nil)
	runtime := newModelRuntime(qualificationFakeProvider(func(callCtx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
		<-callCtx.Done()
		return provider.ChatResponse{}, callCtx.Err()
	}), nil)
	runtime.defaultModel = "worker"
	runtime.qualificationTimeout = 10 * time.Millisecond
	runtime.runQualificationWorker(ctx, db, execution)
	stored, _ := db.GetQualificationJob(ctx, job.ID)
	if stored.Status != state.QualificationStatusFailed || stored.FailureKind != "timeout" || stored.Recommendation != "INCONCLUSIVE" {
		t.Fatalf("stored=%#v", stored)
	}
}

func TestQualificationWorkerInterruptionPreservesCompletedResults(t *testing.T) {
	db := openWorkerTestDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	job, err := db.CreateQualificationJob(ctx, "worker", "fp", "planning", 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	execution, _, _ := db.ClaimNextQueuedExecution(ctx, nil)
	started := make(chan struct{})
	runtime := newModelRuntime(qualificationFakeProvider(func(callCtx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
		close(started)
		<-callCtx.Done()
		return provider.ChatResponse{}, callCtx.Err()
	}), nil)
	runtime.defaultModel = "worker"
	done := make(chan struct{})
	go func() { runtime.runQualificationWorker(ctx, db, execution); close(done) }()
	<-started
	cancel()
	<-done
	stored, _ := db.GetQualificationJob(context.Background(), job.ID)
	if stored.Status != state.QualificationStatusInterrupted || stored.CompletedEvaluations != 0 {
		t.Fatalf("stored=%#v", stored)
	}
}

func TestQualificationIPC_CreateListGetAndCancel(t *testing.T) {
	db := openWorkerTestDatabase(t)
	workers := &workerGroup{}
	runtime := newModelRuntime(qualificationFakeProvider(func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Content: `{}`}, nil
	}), nil)
	runtime.defaultModel = "worker"
	runtime.endpoint = "http://127.0.0.1:8000/v1"
	runtime.configuration = "sha256:test"
	dispatcher := newDispatcher(db, workers, 1, runtime, nil)
	models := qualificationRoundTrip(t, db, workers, dispatcher, ipc.Request{Command: "models"})
	if models.ProviderConfiguration == nil || models.ProviderConfiguration.Model != "worker" || models.ProviderConfiguration.Endpoint == "" {
		t.Fatalf("models=%#v", models)
	}
	created := qualificationRoundTrip(t, db, workers, dispatcher, ipc.Request{Command: "qualification-create", Model: "worker", Capability: "planning", Repetitions: 1})
	if !created.OK || created.Qualification == nil || created.Qualification.TotalEvaluations != 7 {
		t.Fatalf("created=%#v", created)
	}
	listed := qualificationRoundTrip(t, db, workers, dispatcher, ipc.Request{Command: "qualification-list"})
	if len(listed.Qualifications) != 1 {
		t.Fatalf("listed=%#v", listed)
	}
	got := qualificationRoundTrip(t, db, workers, dispatcher, ipc.Request{Command: "qualification-get", QualificationID: created.Qualification.ID})
	if got.Qualification == nil || got.Qualification.ID != created.Qualification.ID {
		t.Fatalf("got=%#v", got)
	}
	cancelled := qualificationRoundTrip(t, db, workers, dispatcher, ipc.Request{Command: "qualification-cancel", QualificationID: created.Qualification.ID})
	if !cancelled.OK || cancelled.Qualification == nil || cancelled.Qualification.Status != state.QualificationStatusCancelled {
		t.Fatalf("cancelled=%#v", cancelled)
	}
}

func qualificationRoundTrip(t *testing.T, db *state.Database, workers *workerGroup, dispatcher *dispatcher, request ipc.Request) ipc.Response {
	t.Helper()
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleConnection(context.Background(), db, workers, dispatcher, server, ClientIdentity{}, nil)
	}()
	if err := json.NewEncoder(client).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response ipc.Response
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	client.Close()
	<-done
	return response
}
