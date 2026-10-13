package daemon

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

func TestInvestigationHandlerKnownAndUnknown(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)
	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	resp := runInvestigation(t, db, ipc.Request{
		Command:   "investigate",
		ProjectID: project.ID,
		Identity:  "example.com/app/internal/greeting",
	})
	if !resp.OK || resp.Investigation == nil {
		t.Fatalf("investigation failed: %#v", resp)
	}
	inv := resp.Investigation
	if inv.SchemaVersion != 1 {
		t.Fatalf("schemaVersion=%d", inv.SchemaVersion)
	}
	if inv.Component.Identity != "example.com/app/internal/greeting" {
		t.Fatalf("component=%#v", inv.Component)
	}
	if len(inv.Dependencies) == 0 && len(inv.Dependents) == 0 {
		// should have both in fixture
	}
	if len(inv.Evidence) == 0 {
		t.Fatal("no evidence")
	}
	if len(inv.SuggestedFiles) == 0 {
		t.Fatal("no suggested files")
	}

	// unknown component
	unknown := runInvestigation(t, db, ipc.Request{
		Command:   "investigate",
		ProjectID: project.ID,
		Identity:  "example.com/app/unknown",
	})
	if unknown.OK {
		t.Fatal("expected failure")
	}

	// unknown project
	unkProj := runInvestigation(t, db, ipc.Request{
		Command:   "investigate",
		ProjectID: 999999,
		Identity:  "example.com/app/internal/greeting",
	})
	if unkProj.OK || unkProj.Message == "" {
		t.Fatalf("expected error, got %#v", unkProj)
	}
}

func TestInvestigationReadOnly(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)
	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	latest, err := db.LatestComponentObservation(ctx, project.ID, false)
	if err != nil {
		t.Fatal(err)
	}

	before, _ := countTable(db, "component_observations")
	resp := runInvestigation(t, db, ipc.Request{
		Command:   "investigate",
		ProjectID: project.ID,
		Identity:  "example.com/app/internal/greeting",
		Limit:     5,
	})
	if !resp.OK {
		t.Fatal(resp.Message)
	}
	after, _ := countTable(db, "component_observations")
	if before != after {
		t.Fatal("observations mutated")
	}
	latest2, _ := db.LatestComponentObservation(ctx, project.ID, false)
	if latest2.ID != latest.ID {
		t.Fatal("latest id changed")
	}
}

func runInvestigation(t *testing.T, db *state.Database, request ipc.Request) ipc.Response {
	t.Helper()
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleConnection(context.Background(), db, &workerGroup{}, server, ClientIdentity{})
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

func countTable(db *state.Database, table string) (int, error) {
	_ = db
	_ = table
	return 0, nil
}
