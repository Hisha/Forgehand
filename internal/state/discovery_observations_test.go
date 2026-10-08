package state

import (
	"context"
	"reflect"
	"testing"

	"github.com/Hisha/Forgehand/internal/discovery"
)

func TestCreateAndListDiscoveryObservationsRetainsHistory(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	project, err := db.CreateProject(ctx, "Forgehand", "/tmp/forgehand-discovery")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	firstSummary := discovery.Summary{
		CommitHash:        "first-commit",
		TrackedFiles:      2,
		UnclassifiedFiles: 1,
		Languages: []discovery.LanguageCount{
			{Language: "Go", Count: 1},
		},
		BuildSystems: []discovery.BuildSystemIndicator{
			{Name: "Go modules", Evidence: discovery.EvidenceReference{Path: "go.mod"}},
		},
	}
	secondSummary := discovery.Summary{
		CommitHash:        "second-commit",
		TrackedFiles:      3,
		UnclassifiedFiles: 1,
		Languages: []discovery.LanguageCount{
			{Language: "Go", Count: 2},
		},
		BuildSystems: firstSummary.BuildSystems,
	}
	first, err := db.CreateDiscoveryObservation(ctx, project.ID, firstSummary)
	if err != nil {
		t.Fatalf("create first observation: %v", err)
	}
	second, err := db.CreateDiscoveryObservation(ctx, project.ID, secondSummary)
	if err != nil {
		t.Fatalf("create second observation: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("historical observations have the same ID")
	}

	observations, err := db.ListDiscoveryObservations(ctx, project.ID)
	if err != nil {
		t.Fatalf("list discovery observations: %v", err)
	}
	if len(observations) != 2 {
		t.Fatalf("observation count = %d, want 2", len(observations))
	}
	if !reflect.DeepEqual(observations[0].Summary, firstSummary) {
		t.Fatalf("first summary = %#v, want %#v", observations[0].Summary, firstSummary)
	}
	if !reflect.DeepEqual(observations[1].Summary, secondSummary) {
		t.Fatalf("second summary = %#v, want %#v", observations[1].Summary, secondSummary)
	}
	if observations[0].ObservedAt.IsZero() || observations[1].ObservedAt.IsZero() {
		t.Fatal("persisted discovery timestamp is zero")
	}
}

func TestDiscoveryObservationEnforcesProjectForeignKey(t *testing.T) {
	db := openTestDatabase(t)
	_, err := db.CreateDiscoveryObservation(
		context.Background(),
		999999,
		discovery.Summary{CommitHash: "missing-project", TrackedFiles: 1},
	)
	if err == nil {
		t.Fatal("observation for missing project unexpectedly succeeded")
	}
}
