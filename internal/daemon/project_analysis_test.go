package daemon

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/components"
	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/state"
)

func TestAnalyzeRegisteredProjectPersistsObservation(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)

	result, err := analyzeRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatalf("analyzeRegisteredProject(): %v", err)
	}
	commit := strings.TrimSpace(string(projectIntakeGitOutput(t, root, "rev-parse", "HEAD")))
	if result.Analysis.CommitHash != commit || result.Analysis.HeadCommit != commit {
		t.Fatalf("analysis commits = %q/%q, want %q", result.Analysis.CommitHash, result.Analysis.HeadCommit, commit)
	}
	if result.Status != InspectionStatusCurrent {
		t.Fatalf("analysis status = %q, want current", result.Status)
	}
	if result.Analysis.IsDirty {
		t.Fatal("analysis of clean repository reported dirty")
	}
	if result.Analysis.EngineVersion != components.EngineVersion {
		t.Fatalf("engine version = %d", result.Analysis.EngineVersion)
	}

	latest, err := db.LatestComponentObservation(ctx, project.ID, true)
	if err != nil {
		t.Fatalf("LatestComponentObservation(): %v", err)
	}
	if latest == nil {
		t.Fatal("analysis was not persisted")
	}
	if !hasComponent(latest.Components, "example.com/app/internal/greeting") {
		t.Fatalf("persisted components = %#v", latest.Components)
	}
	if !hasDependency(latest.Dependencies, string(components.DependencyInternal), "example.com/app/cmd/tool", "example.com/app/internal/greeting") {
		t.Fatalf("persisted dependencies = %#v", latest.Dependencies)
	}
}

func TestAnalyzeRegisteredProjectRejectsUnbornHEAD(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newUnbornProjectIntakeRepository(t, true)
	project := createAnalysisProject(t, ctx, db, root)

	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err == nil {
		t.Fatal("analysis of unborn-HEAD repository succeeded")
	} else if !strings.Contains(err.Error(), "unborn HEAD") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInspectNeverRunsAnalysis(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)

	// Inventory only: no analysis has been requested or run.
	if _, err := inventoryRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("inventoryRegisteredProject(): %v", err)
	}

	inspection, err := inspectRegisteredProject(ctx, db, project.ID, inspectionOptions{
		IncludeComponents:   true,
		IncludeDependencies: true,
	})
	if err != nil {
		t.Fatalf("inspectRegisteredProject(): %v", err)
	}
	if inspection.Analysis != nil {
		t.Fatalf("inspection implicitly produced analysis: %#v", inspection.Analysis)
	}

	latest, err := db.LatestComponentObservation(ctx, project.ID, true)
	if err != nil {
		t.Fatalf("LatestComponentObservation(): %v", err)
	}
	if latest != nil {
		t.Fatalf("inspection implicitly persisted analysis: %#v", latest)
	}
}

func TestInspectAnalysisFreshnessIsIndependentOfInventory(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)

	analyzed, err := analyzeRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatalf("analyzeRegisteredProject(): %v", err)
	}
	analyzedCommit := analyzed.Analysis.CommitHash

	// Advance HEAD, then inventory at the new HEAD so the inventory is current
	// while the analysis is stale.
	writeProjectIntakeFile(t, root, "extra/extra.go", "package extra\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "advance head")
	if _, err := inventoryRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("inventoryRegisteredProject(): %v", err)
	}
	head := strings.TrimSpace(string(projectIntakeGitOutput(t, root, "rev-parse", "HEAD")))

	inspection, err := inspectRegisteredProject(ctx, db, project.ID, inspectionOptions{
		IncludeComponents:   true,
		IncludeDependencies: true,
	})
	if err != nil {
		t.Fatalf("inspectRegisteredProject(): %v", err)
	}
	if inspection.Status != InspectionStatusCurrent {
		t.Fatalf("inventory status = %q, want current", inspection.Status)
	}
	if inspection.AnalysisStatus != InspectionStatusStale {
		t.Fatalf("analysis status = %q, want stale", inspection.AnalysisStatus)
	}
	if inspection.Analysis == nil || inspection.Analysis.CommitHash != analyzedCommit {
		t.Fatalf("stored analysis = %#v, want commit %q", inspection.Analysis, analyzedCommit)
	}
	if inspection.Head != head {
		t.Fatalf("inspection head = %q, want %q", inspection.Head, head)
	}
}

func TestInspectPartialAnalysisStatusIsIndependent(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newPartialAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)

	if _, err := analyzeRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("analyzeRegisteredProject(): %v", err)
	}

	inspection, err := inspectRegisteredProject(ctx, db, project.ID, inspectionOptions{
		IncludeComponents: true,
	})
	if err != nil {
		t.Fatalf("inspectRegisteredProject(): %v", err)
	}
	if inspection.Analysis == nil {
		t.Fatal("analysis missing")
	}
	if inspection.AnalysisStatus != InspectionStatusCurrent {
		t.Fatalf("analysis status = %q, want current", inspection.AnalysisStatus)
	}
	if inspection.Analysis.Summary.Complete {
		t.Fatal("partial analysis reported complete")
	}
	if inspection.Analysis.Summary.Skipped == 0 {
		t.Fatal("partial analysis recorded no skipped sources")
	}
}

func TestInspectWithoutAnalysisReportsMissing(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)

	if _, err := inventoryRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("inventoryRegisteredProject(): %v", err)
	}

	inspection, err := inspectRegisteredProject(ctx, db, project.ID, inspectionOptions{
		IncludeComponents: true,
	})
	if err != nil {
		t.Fatalf("inspectRegisteredProject(): %v", err)
	}
	if inspection.Analysis != nil {
		t.Fatalf("inspection returned unexpected analysis: %#v", inspection.Analysis)
	}
}

func TestAnalysisStatus(t *testing.T) {
	tests := []struct {
		name         string
		analysisHash string
		head         string
		hasHead      bool
		expected     string
	}{
		{name: "matching head", analysisHash: "abc", head: "abc", hasHead: true, expected: InspectionStatusCurrent},
		{name: "advanced head", analysisHash: "abc", head: "def", hasHead: true, expected: InspectionStatusStale},
		{name: "no head", analysisHash: "abc", head: "", hasHead: false, expected: InspectionStatusUnavailable},
	}
	for _, test := range tests {
		if got := analysisStatus(test.analysisHash, test.head, test.hasHead); got != test.expected {
			t.Fatalf("%s: analysisStatus() = %q, want %q", test.name, got, test.expected)
		}
	}
}

func TestAnalysisToIPCFiltersDetails(t *testing.T) {
	record := state.ComponentObservation{
		ID:         4,
		ProjectID:  2,
		CommitHash: "commit",
		HeadCommit: "commit",
		Components: []state.ComponentRecord{
			{Kind: "package", Identity: "internal/greeting", Path: "internal/greeting", Language: "Go", Internal: true,
				Evidence: []components.SourceEvidence{{Path: "internal/greeting/greeting.go", BlobHash: "blob", Declaration: "package greeting", Line: 1}}},
		},
		Dependencies: []state.DependencyRecord{
			{Kind: "internal", Source: "example.com/app", Target: "internal/greeting", Language: "Go",
				Evidence: []components.SourceEvidence{{Path: "app.go", BlobHash: "blob", Declaration: "internal/greeting", Line: 3}}},
		},
	}

	both := analysisToIPC(record, InspectionStatusCurrent, "commit", false, true, true)
	if len(both.Components) != 1 || len(both.Dependencies) != 1 {
		t.Fatalf("both details = %#v", both)
	}
	if len(both.Components[0].Evidence) != 1 || both.Components[0].Evidence[0].BlobHash != "blob" {
		t.Fatalf("component evidence lost: %#v", both.Components)
	}
	if len(both.Dependencies[0].Evidence) != 1 || both.Dependencies[0].Evidence[0].BlobHash != "blob" {
		t.Fatalf("dependency evidence lost: %#v", both.Dependencies)
	}

	componentsOnly := analysisToIPC(record, InspectionStatusCurrent, "commit", false, true, false)
	if len(componentsOnly.Components) != 1 || componentsOnly.Dependencies != nil {
		t.Fatalf("components-only details = %#v", componentsOnly)
	}

	dependenciesOnly := analysisToIPC(record, InspectionStatusCurrent, "commit", false, false, true)
	if dependenciesOnly.Components != nil || len(dependenciesOnly.Dependencies) != 1 {
		t.Fatalf("dependencies-only details = %#v", dependenciesOnly)
	}
}

func TestDaemonProjectAnalyzeAndInspectIPC(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)

	analyzeResponse := runDaemonRequest(t, db, ipc.Request{
		Command:   "project-analyze",
		ProjectID: project.ID,
	})
	if !analyzeResponse.OK {
		t.Fatalf("project-analyze failed: %s", analyzeResponse.Message)
	}
	if analyzeResponse.Analysis == nil {
		t.Fatal("project-analyze returned no analysis")
	}
	if len(analyzeResponse.Analysis.Components) == 0 {
		t.Fatal("project-analyze returned no components")
	}
	if len(analyzeResponse.Analysis.Dependencies) == 0 {
		t.Fatal("project-analyze returned no dependencies")
	}

	inspectResponse := runDaemonRequest(t, db, ipc.Request{
		Command:             "project-inspect",
		ProjectID:           project.ID,
		IncludeComponents:   true,
		IncludeDependencies: true,
		IncludeFiles:        true,
	})
	if !inspectResponse.OK {
		t.Fatalf("project-inspect failed: %s", inspectResponse.Message)
	}
	if inspectResponse.Inspection == nil || inspectResponse.Inspection.Analysis == nil {
		t.Fatal("project-inspect returned no analysis")
	}
	analysis := inspectResponse.Inspection.Analysis
	if analysis.Status != "current" {
		t.Fatalf("inspection analysis status = %q, want current", analysis.Status)
	}
	if len(analysis.Components) == 0 || len(analysis.Dependencies) == 0 {
		t.Fatalf("inspection analysis omitted details: %#v", analysis)
	}
	var hasEvidence bool
	for _, dependency := range analysis.Dependencies {
		if len(dependency.Evidence) > 0 && dependency.Evidence[0].BlobHash != "" {
			hasEvidence = true
		}
	}
	if !hasEvidence {
		t.Fatalf("inspection analysis omitted committed blob evidence: %#v", analysis.Dependencies)
	}
}

func TestDaemonProjectInspectWithoutAnalysisIPC(t *testing.T) {
	ctx := context.Background()
	db := openProjectIntakeTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)

	if _, err := inventoryRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("inventoryRegisteredProject(): %v", err)
	}

	response := runDaemonRequest(t, db, ipc.Request{
		Command:           "project-inspect",
		ProjectID:         project.ID,
		IncludeComponents: true,
	})
	if !response.OK {
		t.Fatalf("project-inspect failed: %s", response.Message)
	}
	if response.Inspection == nil {
		t.Fatal("project-inspect returned no inspection")
	}
	if response.Inspection.Analysis != nil {
		t.Fatalf("project-inspect implicitly produced analysis: %#v", response.Inspection.Analysis)
	}
	if response.Inspection.Inventory == nil {
		t.Fatal("project-inspect omitted stored inventory")
	}
}

func runDaemonRequest(t *testing.T, db *state.Database, request ipc.Request) ipc.Response {
	t.Helper()

	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleConnection(context.Background(), db, &workerGroup{}, nil, server, ClientIdentity{}, nil)
	}()

	if err := json.NewEncoder(client).Encode(request); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var response ipc.Response
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	_ = client.Close()
	<-done

	return response
}

func newAnalysisRepository(t *testing.T) string {
	t.Helper()
	root := newProjectIntakeRepository(t)
	files := map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.22\n\nrequire github.com/example/dep v1.2.3\n",
		"app.go": `package app

import (
	"fmt"

	"example.com/app/internal/greeting"
	"github.com/example/dep/pkg"
	_ "github.com/unknown/missing"
)

func Use() {
	_ = greeting.Hello
	fmt.Println(pkg.Value)
}
`,
		"internal/greeting/greeting.go": `package greeting

import "fmt"

var Hello = fmt.Sprint("hi")
`,
		"cmd/tool/main.go": `package main

import "example.com/app/internal/greeting"

func main() {
	_ = greeting.Hello
}
`,
	}
	for path, content := range files {
		writeProjectIntakeFile(t, root, path, content)
	}
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add go module")
	return root
}

func newPartialAnalysisRepository(t *testing.T) string {
	t.Helper()
	root := newProjectIntakeRepository(t)
	writeProjectIntakeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.22\n")
	writeProjectIntakeFile(t, root, "broken.go", "package app\n\nfunc {\n")
	runProjectIntakeGit(t, root, "add", "--all")
	runProjectIntakeGit(t, root, "commit", "-m", "add malformed go source")
	return root
}

func createAnalysisProject(t *testing.T, ctx context.Context, db *state.Database, root string) state.Project {
	t.Helper()
	project, err := db.CreateProject(ctx, "Analysis Test", root)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

func hasComponent(records []state.ComponentRecord, identity string) bool {
	for _, record := range records {
		if record.Identity == identity {
			return true
		}
	}
	return false
}

func hasDependency(records []state.DependencyRecord, kind, source, target string) bool {
	for _, record := range records {
		if record.Kind == kind && record.Source == source && record.Target == target {
			return true
		}
	}
	return false
}
