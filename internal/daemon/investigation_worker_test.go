package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/provider"
	"github.com/Hisha/Forgehand/internal/repocontext"
	"github.com/Hisha/Forgehand/internal/state"
)

func cppInvestigationFixture(t *testing.T, db *state.Database) (state.Project, string) {
	t.Helper()
	root := newProjectIntakeRepository(t)
	files := map[string]string{
		"CMakeLists.txt":       "cmake_minimum_required(VERSION 3.20)\nproject(greeter)\nadd_library(greeter src/greet.cpp)\ntarget_include_directories(greeter PUBLIC include)\nadd_executable(greet_test tests/greet_test.cpp)\n",
		"include/greet.h":      "#pragma once\n#include <string>\nstd::string greet(const std::string& name);\n",
		"src/greet.cpp":        "#include \"greet.h\"\nstd::string greet(const std::string& name) {\n  return \"Hello, \" + name; // committed-greeting-v1\n}\n",
		"tests/greet_test.cpp": "#include \"greet.h\"\n#include <cassert>\nint main() { assert(greet(\"Ada\") == \"Hello, Ada\"); }\n",
		"config/greeting.conf": "punctuation=!\n",
		"README.md":            "Small C++ greeting library built with CMake.\n",
	}
	for path, content := range files {
		writeProjectIntakeFile(t, root, path, content)
	}
	runProjectIntakeGit(t, root, "add", ".")
	runProjectIntakeGit(t, root, "commit", "-m", "C++ fixture")
	project, err := db.CreateProject(context.Background(), "C++ fixture", root)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := inventoryRegisteredProject(context.Background(), db, project.ID); err != nil {
		t.Fatalf("inventoryRegisteredProject: %v", err)
	}
	return project, root
}

func TestInvestigationImmediateValidationFailurePersistsReason(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	session, err := db.CreateSessionWithDetails(ctx, "invalid investigation", nil, nil, "investigation", "stub", "stub")
	if err != nil {
		t.Fatal(err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newModelRuntime(&stubProvider{}, repocontext.NewBuilder(db))
	runtime.runInvestigationWorker(ctx, db, execution)

	_, latest, err := db.SessionExecutionStatus(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.Status != state.ExecutionStatusFailed || latest.FailureKind != state.FailureKindValidation || latest.FailureReason != "investigation session has no project" {
		t.Fatalf("immediate failure diagnostic = %#v", latest)
	}
}

func TestInvestigationSessionCppFixturePersistsEvidenceAndFlagsInventedReference(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	db, err := state.OpenAt(ctx, stateDir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	project, root := cppInvestigationFixture(t, db)
	headBefore := strings.TrimSpace(string(projectIntakeGitOutput(t, root, "rev-parse", "HEAD")))

	p := &stubProvider{fn: func(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		if !strings.Contains(req.User, "committed-greeting-v1") || !strings.Contains(req.User, "src/greet.cpp") {
			t.Fatalf("model request missing relevant committed C++ source:\n%s", req.User)
		}
		return provider.ChatResponse{Model: "stub", Content: investigationJSON("The greeting is assembled in source.", "src/greet.cpp", 3)}, nil
	}}
	runtime := newModelRuntime(p, repocontext.NewBuilder(db).WithLimits(3, 4096, 1800))
	runtime.defaultModel = "stub"
	session, err := db.CreateInvestigationSession(ctx, "greeting investigation", project.ID, "Where is the greeting assembled and tested?", "stub", "stub", "", 0)
	if err != nil {
		t.Fatalf("CreateInvestigationSession: %v", err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 1)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	runtime.runInvestigationWorker(ctx, db, execution)

	result, err := db.LatestModelResult(ctx, session.ID)
	if err != nil {
		t.Fatalf("LatestModelResult: %v", err)
	}
	if !strings.Contains(result.Content, "Observed repository facts") || !strings.Contains(result.Content, "[src/greet.cpp:3]") {
		t.Fatalf("validated report missing:\n%s", result.Content)
	}
	evidence, err := db.InvestigationEvidence(ctx, session.ID)
	if err != nil {
		t.Fatalf("InvestigationEvidence: %v", err)
	}
	if evidence.Commit != headBefore || len(evidence.Files) == 0 {
		t.Fatalf("unexpected evidence: %#v", evidence)
	}
	if evidence.UsedChars > evidence.BudgetChars || len(evidence.Files) > 3 {
		t.Fatalf("evidence exceeded budget: %#v", evidence)
	}
	for _, file := range evidence.Files {
		if file.Path == "" || file.BlobHash == "" {
			t.Fatalf("evidence lacks path/blob identity: %#v", file)
		}
	}
	assertProjectIntakeClean(t, root)
	if headAfter := strings.TrimSpace(string(projectIntakeGitOutput(t, root, "rev-parse", "HEAD"))); headAfter != headBefore {
		t.Fatalf("investigation changed HEAD: %s -> %s", headBefore, headAfter)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := state.OpenAt(ctx, stateDir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	gotEvidence, err := reopened.InvestigationEvidence(ctx, session.ID)
	if err != nil || gotEvidence.Commit != evidence.Commit || len(gotEvidence.Files) != len(evidence.Files) {
		t.Fatalf("evidence after reopen = %#v, %v", gotEvidence, err)
	}
	gotResult, err := reopened.LatestModelResult(ctx, session.ID)
	if err != nil || gotResult.Content != result.Content {
		t.Fatalf("result after reopen = %#v, %v", gotResult, err)
	}
}

func TestInterruptedInvestigationReusesRecordedBlobSelection(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	project, root := cppInvestigationFixture(t, db)

	started := make(chan struct{})
	var mu sync.Mutex
	var requests []provider.ChatRequest
	p := &stubProvider{fn: func(callCtx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		mu.Lock()
		requests = append(requests, req)
		call := len(requests)
		mu.Unlock()
		if call == 1 {
			close(started)
			<-callCtx.Done()
			return provider.ChatResponse{}, callCtx.Err()
		}
		return provider.ChatResponse{Model: "stub", Content: investigationJSON("Original behavior.", "src/greet.cpp", 3)}, nil
	}}
	runtime := newModelRuntime(p, repocontext.NewBuilder(db).WithLimits(4, 4096, 2200))
	session, err := db.CreateInvestigationSession(ctx, "retry", project.ID, "Inspect the greeting implementation", "stub", "stub", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.runInvestigationWorker(workerCtx, db, execution)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	cancel()
	<-done
	firstEvidence, err := db.InvestigationEvidence(ctx, session.ID)
	if err != nil {
		t.Fatalf("evidence before resume: %v", err)
	}

	newSource := "#include \"greet.h\"\nstd::string greet(const std::string& name) {\n  return \"Hi, \" + name; // newer-greeting-v2\n}\n"
	if err := os.WriteFile(filepath.Join(root, "src/greet.cpp"), []byte(newSource), 0o644); err != nil {
		t.Fatal(err)
	}
	runProjectIntakeGit(t, root, "add", "src/greet.cpp")
	runProjectIntakeGit(t, root, "commit", "-m", "newer source")
	if _, err := inventoryRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatalf("new inventory: %v", err)
	}

	resumed, err := db.ResumeSessionExecution(ctx, session.ID)
	if err != nil {
		t.Fatalf("ResumeSessionExecution: %v", err)
	}
	runtime.runInvestigationWorker(ctx, db, resumed)
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(requests))
	}
	if !strings.Contains(requests[1].User, "committed-greeting-v1") || strings.Contains(requests[1].User, "newer-greeting-v2") {
		t.Fatalf("resume substituted newer content:\n%s", requests[1].User)
	}
	secondEvidence, err := db.InvestigationEvidence(ctx, session.ID)
	if err != nil || secondEvidence.Commit != firstEvidence.Commit || secondEvidence.Files[0].BlobHash != firstEvidence.Files[0].BlobHash {
		t.Fatalf("evidence selection changed on resume: before=%#v after=%#v err=%v", firstEvidence, secondEvidence, err)
	}
	assertProjectIntakeClean(t, root)
}

func TestComponentInvestigationIncludesRelationshipsAndSourceLocations(t *testing.T) {
	ctx := context.Background()
	db := openWorkerTestDatabase(t)
	root := newAnalysisRepository(t)
	project := createAnalysisProject(t, ctx, db, root)
	if _, err := inventoryRegisteredProject(ctx, db, project.ID); err != nil {
		t.Fatal(err)
	}
	analysis, err := analyzeRegisteredProject(ctx, db, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	component := "example.com/app/internal/greeting"
	p := &stubProvider{fn: func(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		if !strings.Contains(req.User, "KNOWN COMPONENT: "+component) || !strings.Contains(req.User, "Incoming relationship:") || !strings.Contains(req.User, "internal/greeting/greeting.go") {
			t.Fatalf("component context omitted relationships/source:\n%s", req.User)
		}
		return provider.ChatResponse{Model: "stub", Content: investigationJSON("The component declares Hello.", "internal/greeting/greeting.go", 5)}, nil
	}}
	runtime := newModelRuntime(p, repocontext.NewBuilder(db).WithLimits(8, 4096, 5000))
	session, err := db.CreateInvestigationSession(ctx, "component", project.ID, "Who uses this component?", "stub", "stub", component, analysis.Analysis.ID)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := db.StartSessionExecution(ctx, session.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	runtime.runInvestigationWorker(ctx, db, execution)
	evidence, err := db.InvestigationEvidence(ctx, session.ID)
	if err != nil || evidence.AnalysisID != analysis.Analysis.ID || evidence.ComponentID != component {
		t.Fatalf("component evidence = %#v, %v", evidence, err)
	}
	foundLocation := false
	for _, file := range evidence.Files {
		if file.Path == "internal/greeting/greeting.go" && len(file.Locations) > 0 {
			foundLocation = true
		}
	}
	if !foundLocation {
		t.Fatalf("component source locations not persisted: %#v", evidence.Files)
	}
}

func investigationJSON(statement, path string, line int) string {
	return fmt.Sprintf(`{"observed_facts":[{"statement":%q,"citations":[{"path":%q,"line":%d}]}],"reasoned_conclusions":[{"statement":"The cited fact supports the requested interpretation.","citations":[{"path":%q,"line":%d}]}],"uncertainties":["Additional evidence may exist outside the bounded package."],"suggested_next_steps":["Inspect adjacent committed code if more detail is needed."]}`, statement, path, line, path, line)
}
