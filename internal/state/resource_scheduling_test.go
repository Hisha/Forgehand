package state

import (
	"context"
	"strings"
	"testing"
)

func requireResources(
	t *testing.T,
	got []string,
	want []string,
	label string,
) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("%s resources = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s resources = %v, want %v", label, got, want)
		}
	}
}

// TestQueueSessionExecutionPersistsNormalizedResources verifies that resource
// requirements are trimmed, deduplicated, and sorted on the way in, survive a
// status read, and are recorded in the durable queued event.
func TestQueueSessionExecutionPersistsNormalizedResources(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "resource session")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	execution, err := db.QueueSessionExecution(
		ctx,
		session.ID,
		5,
		" gpu ", "cpu", "gpu",
	)
	if err != nil {
		t.Fatalf("QueueSessionExecution: %v", err)
	}
	requireResources(t, execution.Resources, []string{"cpu", "gpu"}, "returned")

	_, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatalf("SessionExecutionStatus: %v", err)
	}
	if latest == nil {
		t.Fatal("latest execution is nil")
	}
	requireResources(t, latest.Resources, []string{"cpu", "gpu"}, "stored")

	events, _, err := db.ListSessionEvents(ctx, session.ID, 0, 1000)
	if err != nil {
		t.Fatalf("ListSessionEvents: %v", err)
	}
	var queued map[string]any
	for _, event := range events {
		if event.Type == EventTypeExecutionQueued {
			queued = event.Details
		}
	}
	if queued == nil {
		t.Fatal("no execution.queued event recorded")
	}
	encoded, ok := queued["resources"].([]any)
	if !ok {
		t.Fatalf("queued event resources = %#v, want []any", queued["resources"])
	}
	decoded := make([]string, 0, len(encoded))
	for _, value := range encoded {
		name, ok := value.(string)
		if !ok {
			t.Fatalf("queued event resource entry = %#v, want string", value)
		}
		decoded = append(decoded, name)
	}
	requireResources(t, decoded, []string{"cpu", "gpu"}, "queued event")
}

func TestQueueSessionExecutionRejectsInvalidResources(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "invalid resources")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := db.QueueSessionExecution(ctx, session.ID, 5, " "); err == nil {
		t.Fatal("empty resource name was accepted")
	}
	oversized := strings.Repeat("x", MaxResourceNameLength+1)
	if _, err := db.QueueSessionExecution(ctx, session.ID, 5, oversized); err == nil {
		t.Fatal("oversized resource name was accepted")
	}

	// The failed calls must not have consumed the queue or left a partial
	// execution behind.
	queued, err := db.CountQueuedExecutions(ctx)
	if err != nil {
		t.Fatalf("CountQueuedExecutions: %v", err)
	}
	if queued != 0 {
		t.Fatalf("queued executions = %d, want 0", queued)
	}
}

// TestClaimSkipsResourceBlockedExecutionAndPreservesFIFOAmongEligible proves
// that a queued execution whose resource is held is skipped while a later
// execution with disjoint resources is claimed, and that the blocked
// execution is claimed once its resource frees.
func TestClaimSkipsResourceBlockedExecutionAndPreservesFIFOAmongEligible(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	queue := func(title string, resources ...string) SessionExecution {
		t.Helper()
		session, err := db.CreateSession(ctx, title)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		execution, err := db.QueueSessionExecution(ctx, session.ID, 5, resources...)
		if err != nil {
			t.Fatalf("QueueSessionExecution: %v", err)
		}
		return execution
	}

	first := queue("first", "gpu")
	second := queue("second", "gpu")
	third := queue("third", "cpu")

	// Someone else holds "gpu": first is skipped, third (disjoint) is claimed.
	claimed, ok, err := db.ClaimNextQueuedExecution(ctx, []string{"gpu"})
	if err != nil {
		t.Fatalf("ClaimNextQueuedExecution: %v", err)
	}
	if !ok {
		t.Fatal("no execution claimed while an independent resource was free")
	}
	if claimed.ID != third.ID {
		t.Fatalf("claimed execution %d, want %d (third, cpu)", claimed.ID, third.ID)
	}
	requireResources(t, claimed.Resources, []string{"cpu"}, "claimed")

	// Now nothing queued is eligible: first and second both need "gpu".
	_, ok, err = db.ClaimNextQueuedExecution(ctx, []string{"gpu"})
	if err != nil {
		t.Fatalf("ClaimNextQueuedExecution: %v", err)
	}
	if ok {
		t.Fatal("claimed a resource-blocked execution")
	}

	// With the resource free, FIFO among eligible restores first-before-second.
	firstClaimed, ok, err := db.ClaimNextQueuedExecution(ctx, nil)
	if err != nil {
		t.Fatalf("ClaimNextQueuedExecution: %v", err)
	}
	if !ok || firstClaimed.ID != first.ID {
		t.Fatalf("claimed execution = %#v (ok=%v), want %d", firstClaimed, ok, first.ID)
	}

	// While first holds "gpu", second stays blocked even though capacity is
	// otherwise free.
	_, ok, err = db.ClaimNextQueuedExecution(ctx, []string{"gpu"})
	if err != nil {
		t.Fatalf("ClaimNextQueuedExecution: %v", err)
	}
	if ok {
		t.Fatal("claimed second while first's gpu was held")
	}

	// Once first finishes and releases "gpu", second is claimed.
	secondClaimed, ok, err := db.ClaimNextQueuedExecution(ctx, nil)
	if err != nil {
		t.Fatalf("ClaimNextQueuedExecution: %v", err)
	}
	if !ok || secondClaimed.ID != second.ID {
		t.Fatalf("claimed execution = %#v (ok=%v), want %d", secondClaimed, ok, second.ID)
	}
}

// TestClaimRequiresWholeResourceSet verifies all-or-nothing acquisition: an
// execution requiring two resources is not claimed while only one of them is
// held.
func TestClaimRequiresWholeResourceSet(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "multi resource")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	execution, err := db.QueueSessionExecution(ctx, session.ID, 5, "gpu", "storage")
	if err != nil {
		t.Fatalf("QueueSessionExecution: %v", err)
	}

	for _, held := range [][]string{{"gpu"}, {"storage"}, {"storage", "gpu"}} {
		claimed, ok, err := db.ClaimNextQueuedExecution(ctx, held)
		if err != nil {
			t.Fatalf("ClaimNextQueuedExecution(%v): %v", held, err)
		}
		if ok {
			t.Fatalf("claimed %#v while %v was held", claimed, held)
		}
	}

	claimed, ok, err := db.ClaimNextQueuedExecution(ctx, []string{"cpu"})
	if err != nil {
		t.Fatalf("ClaimNextQueuedExecution: %v", err)
	}
	if !ok || claimed.ID != execution.ID {
		t.Fatalf("claimed execution = %#v (ok=%v), want %d", claimed, ok, execution.ID)
	}
}

func TestCountResourceBlockedQueuedExecutions(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	queue := func(title string, resources ...string) {
		t.Helper()
		session, err := db.CreateSession(ctx, title)
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if _, err := db.QueueSessionExecution(ctx, session.ID, 5, resources...); err != nil {
			t.Fatalf("QueueSessionExecution: %v", err)
		}
	}

	queue("a", "gpu")
	queue("b", "gpu")
	queue("c", "cpu")
	queue("d", "cpu", "gpu")

	count, err := db.CountResourceBlockedQueuedExecutions(ctx, nil)
	if err != nil {
		t.Fatalf("CountResourceBlockedQueuedExecutions: %v", err)
	}
	if count != 0 {
		t.Fatalf("blocked count with nothing held = %d, want 0", count)
	}

	count, err = db.CountResourceBlockedQueuedExecutions(ctx, []string{"gpu"})
	if err != nil {
		t.Fatalf("CountResourceBlockedQueuedExecutions: %v", err)
	}
	if count != 3 {
		t.Fatalf("blocked count with gpu held = %d, want 3", count)
	}

	count, err = db.CountResourceBlockedQueuedExecutions(ctx, []string{"tape"})
	if err != nil {
		t.Fatalf("CountResourceBlockedQueuedExecutions: %v", err)
	}
	if count != 0 {
		t.Fatalf("blocked count with unused resource held = %d, want 0", count)
	}
}

// TestResumePreservesResourceRequirements proves resources survive an
// interruption and requeue cycle, so a resumed execution still excludes
// conflicting work.
func TestResumePreservesResourceRequirements(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "resume resources")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	queued, err := db.QueueSessionExecution(ctx, session.ID, 5, "gpu")
	if err != nil {
		t.Fatalf("QueueSessionExecution: %v", err)
	}
	if _, ok, err := db.ClaimNextQueuedExecution(ctx, nil); err != nil || !ok {
		t.Fatalf("ClaimNextQueuedExecution ok=%v err=%v", ok, err)
	}
	if err := db.InterruptSessionExecution(ctx, queued.ID); err != nil {
		t.Fatalf("InterruptSessionExecution: %v", err)
	}
	resumed, err := db.QueueResumeExecution(ctx, session.ID)
	if err != nil {
		t.Fatalf("QueueResumeExecution: %v", err)
	}
	requireResources(t, resumed.Resources, []string{"gpu"}, "resumed")

	// The resumed execution must still be resource-blocked.
	claimed, ok, err := db.ClaimNextQueuedExecution(ctx, []string{"gpu"})
	if err != nil {
		t.Fatalf("ClaimNextQueuedExecution: %v", err)
	}
	if ok {
		t.Fatalf("claimed %#v, want resource-blocked skip", claimed)
	}

	claimed, ok, err = db.ClaimNextQueuedExecution(ctx, nil)
	if err != nil {
		t.Fatalf("ClaimNextQueuedExecution: %v", err)
	}
	if !ok || claimed.ID != queued.ID {
		t.Fatalf("claimed execution = %#v (ok=%v), want %d", claimed, ok, queued.ID)
	}
}
