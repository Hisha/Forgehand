package state

import (
	"context"
	"testing"
)

const testQualificationResult = `{"case":"repository-information","attempt":1,"passed":true,"infrastructure_failure":false,"criteria":{"schema_valid":true}}`

func TestQualificationJobPersistsProgressAndResults(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	job, err := db.CreateQualificationJob(ctx, "worker", "sha256:test", "planning", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := db.ClaimNextQueuedExecution(ctx, nil)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if claimed.ID != job.ExecutionID || claimed.Kind != "qualification" {
		t.Fatalf("claimed = %#v", claimed)
	}
	if _, ok, err := db.ClaimNextQueuedExecution(ctx, nil); err != nil || ok {
		t.Fatalf("duplicate claim ok=%v err=%v", ok, err)
	}
	if err := db.MarkQualificationRunning(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.AddQualificationResult(ctx, job.ID, 0, testQualificationResult); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetQualificationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != QualificationStatusRunning || stored.CompletedEvaluations != 1 || len(stored.Results) != 1 {
		t.Fatalf("stored = %#v", stored)
	}
	if err := db.AddQualificationResult(ctx, job.ID, 1, testQualificationResult); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteQualificationJob(ctx, job.ID, `{"aggregate":{"passing_runs":2}}`, "QUALIFIED"); err != nil {
		t.Fatal(err)
	}
	stored, _ = db.GetQualificationJob(ctx, job.ID)
	if stored.Status != QualificationStatusCompleted || stored.Recommendation != "QUALIFIED" || stored.CompletedEvaluations != 2 {
		t.Fatalf("completed = %#v", stored)
	}
}

func TestQualificationCancellationAndInterruptedRetry(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	cancelled, err := db.CreateQualificationJob(ctx, "worker", "fp", "planning", 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err = db.CancelQualificationJob(ctx, cancelled.ID, "operator request")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != QualificationStatusCancelled {
		t.Fatalf("status=%s", cancelled.Status)
	}
	job, err := db.CreateQualificationJob(ctx, "worker", "fp", "planning", 2, 14)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := db.ClaimNextQueuedExecution(ctx, nil)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if claimed.ID != job.ExecutionID {
		t.Fatalf("claimed %d want %d", claimed.ID, job.ExecutionID)
	}
	if err = db.MarkQualificationRunning(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.AddQualificationResult(ctx, job.ID, 0, testQualificationResult); err != nil {
		t.Fatal(err)
	}
	if err = db.InterruptQualificationJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := db.RetryQualificationJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != QualificationStatusQueued || retried.CompletedEvaluations != 1 || len(retried.Results) != 1 {
		t.Fatalf("retried=%#v", retried)
	}
}
