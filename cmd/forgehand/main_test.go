package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/ipc"
)

func TestFormatWorkingTreeChangeUntracked(t *testing.T) {
	status, path := formatWorkingTreeChange(ipc.WorkingTreeChange{
		Path:      "new.txt",
		Untracked: true,
	})
	if status != "??" || path != "new.txt" {
		t.Fatalf("formatted untracked change = %q %q", status, path)
	}
}

func TestRequestUsesConfiguredSocketPath(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".fh-cli-sock-")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(abs) })
	socketPath := filepath.Join(abs, "forgehand.sock")
	t.Setenv("FORGEHAND_SOCKET_PATH", socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		var request ipc.Request
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			serverErr <- err
			return
		}
		if request.Command != "status" {
			serverErr <- fmt.Errorf("command = %q, want status", request.Command)
			return
		}
		serverErr <- json.NewEncoder(conn).Encode(ipc.Response{OK: true, Message: "configured"})
	}()

	response, err := request(ipc.Request{Command: "status"})
	if err != nil {
		t.Fatalf("request configured socket: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("configured socket server: %v", err)
	}
	if response.Message != "configured" {
		t.Fatalf("response message = %q", response.Message)
	}
}

func TestFormatProjectDiscovery(t *testing.T) {
	result := ipc.ProjectDiscovery{
		ObservationID: 7,
		Project: ipc.Project{
			ID:       3,
			Name:     "Example",
			RootPath: "/tmp/example",
		},
		CommitHash:        "abc123",
		TrackedFiles:      4,
		UnclassifiedFiles: 1,
		Languages: []ipc.LanguageCount{
			{Language: "Go", Count: 2},
		},
		BuildSystems: []ipc.BuildSystemIndicator{
			{Name: "Go modules", Evidence: ipc.EvidenceReference{Path: "go.mod"}},
		},
	}
	var output bytes.Buffer
	if err := formatProjectDiscovery(&output, result); err != nil {
		t.Fatalf("format project discovery: %v", err)
	}
	for _, want := range []string{
		"Discovered project 3",
		"Observation: 7",
		"Commit: abc123",
		"Tracked files: 4",
		"Unclassified files: 1",
		"Go: 2",
		"Go modules: go.mod",
		"no builds or tests were run",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("formatted discovery missing %q:\n%s", want, output.String())
		}
	}
}

func TestFormatWorkingTreeChangeUsesShortStatusAndRename(t *testing.T) {
	status, path := formatWorkingTreeChange(ipc.WorkingTreeChange{
		Path:         "new.txt",
		OriginalPath: "old.txt",
		IndexStatus:  "R",
		WorkStatus:   ".",
	})
	if status != "R " || path != "old.txt -> new.txt" {
		t.Fatalf("formatted rename = %q %q", status, path)
	}
}

func TestFormatProjectInspectionCurrent(t *testing.T) {
	inspection := ipc.Inspection{
		Status: "current",
		Head:   "abc123",
		Dirty:  false,
		Inventory: &ipc.Inventory{
			ID:            5,
			ProjectID:     3,
			CommitHash:    "abc123",
			HeadCommit:    "abc123",
			IsDirty:       false,
			EngineVersion: 1,
			TrackedFiles:  2,
			ObservedAt:    "2026-01-01T00:00:00Z",
			Summary: ipc.InventorySummary{
				TrackedFiles:      2,
				UnclassifiedFiles: 0,
				Languages:         []ipc.InventoryLanguage{{Language: "Go", Count: 2}},
				BuildSystems: []ipc.InventoryBuildSystem{
					{Name: "Go modules", Evidence: ipc.EvidenceReference{Path: "go.mod"}},
				},
				Categories: []ipc.InventoryCategory{
					{Category: "source", Count: 2},
				},
				Directories: []ipc.InventoryDirectory{
					{Path: "cmd", Files: 1},
				},
			},
		},
	}
	var output bytes.Buffer
	if err := formatProjectInspection(&output, inspection, inspectionFormatOptions{}); err != nil {
		t.Fatalf("format current inspection: %v", err)
	}
	for _, want := range []string{
		"Inspected project 3",
		"Status: current",
		"Commit analyzed: abc123",
		"HEAD now: abc123",
		"Working tree dirty (now): no",
		"Go: 2",
		"Go modules: go.mod",
		"source: 2",
		"cmd: 1",
		"no builds or tests were run",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("current inspection missing %q:\n%s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "Evidence files:") {
		t.Fatalf("current inspection unexpectedly contained evidence:\n%s", output.String())
	}
}

func TestFormatProjectInspectionStale(t *testing.T) {
	inspection := ipc.Inspection{
		Status: "stale",
		Head:   "def456",
		Dirty:  true,
		Inventory: &ipc.Inventory{
			ID:         5,
			ProjectID:  3,
			CommitHash: "abc123",
			HeadCommit: "abc123",
			IsDirty:    true,
			ObservedAt: "2026-01-01T00:00:00Z",
			Summary: ipc.InventorySummary{
				TrackedFiles: 0,
			},
		},
	}
	var output bytes.Buffer
	if err := formatProjectInspection(&output, inspection, inspectionFormatOptions{}); err != nil {
		t.Fatalf("format stale inspection: %v", err)
	}
	for _, want := range []string{
		"Status: stale",
		"HEAD now: def456",
		"Working tree dirty (now): yes",
		"has advanced to def456",
		"forgehand project inventory 3",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("stale inspection missing %q:\n%s", want, output.String())
		}
	}
}

func TestFormatProjectInspectionEvidence(t *testing.T) {
	inspection := ipc.Inspection{
		Status: "current",
		Head:   "abc123",
		Inventory: &ipc.Inventory{
			ID:           5,
			ProjectID:    3,
			CommitHash:   "abc123",
			HeadCommit:   "abc123",
			TrackedFiles: 1,
			ObservedAt:   "2026-01-01T00:00:00Z",
			Summary:      ipc.InventorySummary{TrackedFiles: 1},
			Files: []ipc.InventoryFile{
				{Path: "main.go", BlobHash: "blob", Mode: "100644", Size: 10, Language: "Go", Category: "source"},
			},
		},
	}
	var output bytes.Buffer
	if err := formatProjectInspection(&output, inspection, inspectionFormatOptions{
		includeInventoryFiles: true,
		includeEvidence:       true,
	}); err != nil {
		t.Fatalf("format evidence inspection: %v", err)
	}
	if !strings.Contains(output.String(), "Evidence files:") {
		t.Fatalf("evidence inspection missing header:\n%s", output.String())
	}
	if !hasTabulatedRow(output.String(), []string{"main.go", "source", "Go", "10", "blob"}) {
		t.Fatalf("evidence inspection missing file row:\n%s", output.String())
	}
}

func TestFormatProjectInspectionComponentsDependenciesEvidence(t *testing.T) {
	inspection := analysisInspectionFixture()
	var output bytes.Buffer
	if err := formatProjectInspection(&output, inspection, inspectionFormatOptions{
		includeComponents:   true,
		includeDependencies: true,
		includeEvidence:     true,
	}); err != nil {
		t.Fatalf("format analysis inspection: %v", err)
	}
	for _, want := range []string{
		"Analysis: 9",
		"Analysis status: current",
		"Analysis complete: yes",
		"Declared module requirements:",
		"github.com/example/dep",
		"Components:",
		"example.com/app/internal/greeting",
		"Component evidence:",
		"internal/greeting/greeting.go",
		"gblob",
		"Dependencies:",
		"Dependency evidence:",
		"ablob",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("analysis inspection missing %q:\n%s", want, output.String())
		}
	}
}

func TestFormatProjectInspectionComponentsWithoutEvidence(t *testing.T) {
	inspection := analysisInspectionFixture()
	var output bytes.Buffer
	if err := formatProjectInspection(&output, inspection, inspectionFormatOptions{
		includeComponents: true,
	}); err != nil {
		t.Fatalf("format analysis inspection: %v", err)
	}
	if !strings.Contains(output.String(), "example.com/app/internal/greeting") {
		t.Fatalf("component missing:\n%s", output.String())
	}
	if strings.Contains(output.String(), "Component evidence:") {
		t.Fatalf("evidence printed without --evidence:\n%s", output.String())
	}
	if hasTabulatedRow(output.String(), []string{"KIND", "SOURCE", "TARGET", "MODULE"}) {
		t.Fatalf("dependency detail printed without --dependencies:\n%s", output.String())
	}
}

func TestFormatProjectInspectionMissingAnalysis(t *testing.T) {
	inspection := ipc.Inspection{
		Status: "current",
		Head:   "abc123",
		Inventory: &ipc.Inventory{
			ID:           5,
			ProjectID:    7,
			CommitHash:   "abc123",
			HeadCommit:   "abc123",
			ObservedAt:   "2026-01-01T00:00:00Z",
			TrackedFiles: 1,
			Summary:      ipc.InventorySummary{TrackedFiles: 1},
		},
	}
	var output bytes.Buffer
	if err := formatProjectInspection(&output, inspection, inspectionFormatOptions{includeComponents: true}); err != nil {
		t.Fatalf("format missing analysis inspection: %v", err)
	}
	if !strings.Contains(output.String(), "No component analysis recorded") {
		t.Fatalf("missing analysis guidance absent:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "forgehand project analyze 7") {
		t.Fatalf("missing analysis command hint absent:\n%s", output.String())
	}
}

func TestFormatProjectInspectionStalePartialAnalysis(t *testing.T) {
	inspection := analysisInspectionFixture()
	inspection.Analysis.Status = "stale"
	inspection.Analysis.Summary.Complete = false
	inspection.Analysis.SkippedFiles = []ipc.AnalysisSkippedFile{
		{Path: "internal/broken/broken.go", BlobHash: "bblob", Reason: "malformed source"},
	}
	var output bytes.Buffer
	if err := formatProjectInspection(&output, inspection, inspectionFormatOptions{includeComponents: true}); err != nil {
		t.Fatalf("format stale analysis inspection: %v", err)
	}
	for _, want := range []string{
		"Analysis status: stale",
		"Analysis complete: no (partial)",
		"forgehand project analyze 7",
		"Skipped sources:",
		"internal/broken/broken.go",
		"bblob",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("stale/partial inspection missing %q:\n%s", want, output.String())
		}
	}
}

func TestInspectProjectForwardsAnalysisFlags(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".fh-cli-inspect-")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(abs) })
	socketPath := filepath.Join(abs, "forgehand.sock")
	t.Setenv("FORGEHAND_SOCKET_PATH", socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	received := make(chan ipc.Request, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request ipc.Request
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			return
		}
		received <- request
		_ = json.NewEncoder(conn).Encode(ipc.Response{OK: true, Inspection: &ipc.Inspection{
			Status:   "current",
			Head:     "abc123",
			Analysis: analysisInspectionFixture().Analysis,
		}})
	}()

	output := captureStdout(t, func() {
		if err := inspectProject([]string{"7", "--components", "--dependencies", "--evidence"}); err != nil {
			t.Fatalf("inspectProject(): %v", err)
		}
	})

	request := <-received
	if request.Command != "project-inspect" || request.ProjectID != 7 {
		t.Fatalf("request = %#v", request)
	}
	if !request.IncludeComponents || !request.IncludeDependencies || !request.IncludeFiles {
		t.Fatalf("analysis flags not forwarded: %#v", request)
	}
	if !strings.Contains(output, "Components:") || !strings.Contains(output, "Dependencies:") {
		t.Fatalf("inspection output missing analysis sections:\n%s", output)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = original }()

	fn()
	_ = writer.Close()
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	_ = reader.Close()
	return string(out)
}

func analysisInspectionFixture() ipc.Inspection {
	return ipc.Inspection{
		Status: "current",
		Head:   "abc123",
		Inventory: &ipc.Inventory{
			ID:            5,
			ProjectID:     7,
			CommitHash:    "abc123",
			HeadCommit:    "abc123",
			EngineVersion: 1,
			TrackedFiles:  2,
			ObservedAt:    "2026-01-01T00:00:00Z",
			Summary:       ipc.InventorySummary{TrackedFiles: 2},
		},
		Analysis: &ipc.ComponentAnalysis{
			ID:            9,
			ProjectID:     7,
			CommitHash:    "abc123",
			HeadCommit:    "abc123",
			EngineVersion: 1,
			ObservedAt:    "2026-01-01T00:00:00Z",
			Status:        "current",
			Head:          "abc123",
			Summary: ipc.AnalysisSummary{
				Complete: true,
				Sources:  2,
				Analyzed: 2,
				Modules:  1,
				Packages: 1,
				Commands: 1,
				Dependencies: []ipc.AnalysisDependencyCount{
					{Kind: "internal", Count: 1},
				},
			},
			Components: []ipc.AnalysisComponent{
				{
					Kind:     "package",
					Identity: "example.com/app/internal/greeting",
					Path:     "internal/greeting",
					Language: "Go",
					Internal: true,
					Evidence: []ipc.SourceEvidence{
						{Path: "internal/greeting/greeting.go", BlobHash: "gblob", Declaration: "package greeting", Line: 1},
					},
				},
			},
			Dependencies: []ipc.AnalysisDependency{
				{
					Kind:     "internal",
					Source:   "example.com/app",
					Target:   "example.com/app/internal/greeting",
					Language: "Go",
					Evidence: []ipc.SourceEvidence{
						{Path: "app.go", BlobHash: "ablob", Declaration: "example.com/app/internal/greeting", Line: 3},
					},
				},
			},
			ModuleRequirements: []ipc.ModuleRequirement{
				{Path: "github.com/example/dep", Version: "v1.2.3"},
			},
		},
	}
}

func TestFormatProjectInventory(t *testing.T) {
	summary := ipc.InventorySummary{
		TrackedFiles:      1,
		UnclassifiedFiles: 1,
		Languages:         []ipc.InventoryLanguage{{Language: "Go", Count: 1}},
		Categories:        []ipc.InventoryCategory{{Category: "source", Count: 1}},
	}
	var output bytes.Buffer
	if err := formatInventorySummary(&output, summary); err != nil {
		t.Fatalf("format inventory summary: %v", err)
	}
	for _, want := range []string{
		"Unclassified files: 1",
		"Languages:",
		"Go: 1",
		"File classifications:",
		"source: 1",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("inventory summary missing %q:\n%s", want, output.String())
		}
	}
}

func TestQueryComponentsForwardsFlags(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".fh-cli-query-")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(abs) })
	socketPath := filepath.Join(abs, "forgehand.sock")
	t.Setenv("FORGEHAND_SOCKET_PATH", socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	received := make(chan ipc.Request, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request ipc.Request
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			return
		}
		received <- request
		_ = json.NewEncoder(conn).Encode(ipc.Response{OK: true, Query: &ipc.QueryResponse{
			ProjectID: 7,
			Query:     "components",
			Observation: &ipc.QueryObservation{
				AnalysisID: 9,
				ProjectID:  7,
				Commit:     "abc123",
				Head:       "abc123",
				Status:     "current",
				Complete:   true,
			},
			Components: []ipc.AnalysisComponent{
				{Kind: "package", Identity: "example.com/app", Path: ".", Internal: true},
			},
			Total: 1,
		}})
	}()

	output := captureStdout(t, func() {
		if err := queryCommand([]string{
			"components", "7",
			"--kind", "package",
			"--identity", "example.com/app",
			"--path", "internal",
			"--contains", "app",
			"--analysis", "9",
			"--limit", "20",
			"--evidence",
		}); err != nil {
			t.Fatalf("queryCommand(): %v", err)
		}
	})

	request := <-received
	if request.Command != "query-components" || request.ProjectID != 7 {
		t.Fatalf("request = %#v", request)
	}
	if request.Kind != "package" || request.Identity != "example.com/app" {
		t.Fatalf("kind/identity not forwarded: %#v", request)
	}
	if request.ComponentPath != "internal" || request.Contains != "app" {
		t.Fatalf("path/contains not forwarded: %#v", request)
	}
	if request.AnalysisID != 9 || request.Limit != 20 || !request.IncludeEvidence {
		t.Fatalf("analysis/limit/evidence not forwarded: %#v", request)
	}
	for _, want := range []string{"Components: 1", "package", "example.com/app", "Analysis: 9"} {
		if !strings.Contains(output, want) {
			t.Fatalf("query output missing %q:\n%s", want, output)
		}
	}
}

func TestQueryDependentsForwardsIdentity(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".fh-cli-query-")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(abs) })
	socketPath := filepath.Join(abs, "forgehand.sock")
	t.Setenv("FORGEHAND_SOCKET_PATH", socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	received := make(chan ipc.Request, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request ipc.Request
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			return
		}
		received <- request
		_ = json.NewEncoder(conn).Encode(ipc.Response{OK: true, Query: &ipc.QueryResponse{
			ProjectID: 7,
			Query:     "dependents",
			Observation: &ipc.QueryObservation{
				AnalysisID: 9,
				Commit:     "abc123",
				Head:       "abc123",
				Status:     "stale",
				Complete:   true,
			},
			Dependents: []ipc.QueryDependent{
				{Kind: "internal", Source: "example.com/app", Target: "example.com/app/internal/greeting"},
			},
			Total:    1,
			Warnings: []string{"analysis is stale; refresh"},
		}})
	}()

	output := captureStdout(t, func() {
		if err := queryCommand([]string{"dependents", "7", "example.com/app/internal/greeting"}); err != nil {
			t.Fatalf("queryCommand(): %v", err)
		}
	})

	request := <-received
	if request.Command != "query-dependents" || request.ProjectID != 7 {
		t.Fatalf("request = %#v", request)
	}
	if request.Identity != "example.com/app/internal/greeting" {
		t.Fatalf("identity not forwarded: %#v", request)
	}
	for _, want := range []string{"Dependents: 1", "example.com/app", "stale", "refresh"} {
		if !strings.Contains(output, want) {
			t.Fatalf("query output missing %q:\n%s", want, output)
		}
	}
}

func TestQueryArgumentValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "no subcommand", args: nil},
		{name: "unknown subcommand", args: []string{"bogus"}},
		{name: "components missing id", args: []string{"components"}},
		{name: "components invalid id", args: []string{"components", "abc"}},
		{name: "components invalid limit", args: []string{"components", "7", "--limit", "x"}},
		{name: "components invalid analysis", args: []string{"components", "7", "--analysis", "0"}},
		{name: "components missing flag value", args: []string{"components", "7", "--kind"}},
		{name: "components unknown option", args: []string{"components", "7", "--bogus"}},
		{name: "dependencies missing identity", args: []string{"dependencies", "7"}},
		{name: "evidence missing identity", args: []string{"evidence", "7"}},
		{name: "dependents unknown option", args: []string{"dependents", "7", "id", "--bogus"}},
		{name: "changes missing range", args: []string{"changes", "7"}},
		{name: "changes missing to", args: []string{"changes", "7", "--from", "3"}},
		{name: "changes invalid from", args: []string{"changes", "7", "--from", "x", "--to", "3"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := queryCommand(testCase.args); err == nil {
				t.Fatalf("queryCommand(%v) succeeded, want error", testCase.args)
			}
		})
	}
}

func TestFormatQueryResponseComponents(t *testing.T) {
	query := ipc.QueryResponse{
		ProjectID: 7,
		Query:     "components",
		Observation: &ipc.QueryObservation{
			AnalysisID: 9,
			Commit:     "abc123",
			Head:       "abc123",
			Status:     "current",
			Complete:   true,
		},
		Components: []ipc.AnalysisComponent{
			{
				Kind:     "package",
				Identity: "example.com/app",
				Path:     ".",
				Internal: true,
				Evidence: []ipc.SourceEvidence{
					{Path: "app.go", Line: 1, Declaration: "package main", BlobHash: "ablob"},
				},
			},
		},
		Total:    1,
		Limited:  true,
		Warnings: []string{"analysis is stale; run project analyze to refresh"},
	}

	var output bytes.Buffer
	if err := formatQueryResponse(&output, query); err != nil {
		t.Fatalf("formatQueryResponse(): %v", err)
	}
	for _, want := range []string{
		"Queried project 7 components",
		"Analysis: 9",
		"Status: current",
		"Complete: yes",
		"Components: 1",
		"Component evidence:",
		"app.go",
		"Note: output truncated by --limit.",
		"Warnings:",
		"stale",
		"no builds or tests were run",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("query output missing %q:\n%s", want, output.String())
		}
	}
	if !hasTabulatedRow(output.String(), []string{"KIND", "IDENTITY", "PATH", "INTERNAL"}) {
		t.Fatalf("query output missing component header:\n%s", output.String())
	}
	if !hasTabulatedRow(output.String(), []string{"package", "example.com/app", ".", "true"}) {
		t.Fatalf("query output missing component row:\n%s", output.String())
	}
	if strings.Contains(output.String(), "\t") {
		t.Fatalf("query output contains unexpanded tabs:\n%s", output.String())
	}
}

func TestFormatQueryResponseEmptyComponents(t *testing.T) {
	query := ipc.QueryResponse{
		ProjectID: 7,
		Query:     "components",
		Empty:     true,
		Total:     0,
	}

	var output bytes.Buffer
	if err := formatQueryResponse(&output, query); err != nil {
		t.Fatalf("formatQueryResponse(): %v", err)
	}
	if !strings.Contains(output.String(), "Components: 0") {
		t.Fatalf("output missing component count:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "No components matched.") {
		t.Fatalf("output missing empty notice:\n%s", output.String())
	}
	if strings.Contains(output.String(), "Component evidence:") {
		t.Fatalf("output unexpectedly contains evidence:\n%s", output.String())
	}
}

func TestFormatQueryResponseDependenciesWithEvidence(t *testing.T) {
	query := ipc.QueryResponse{
		ProjectID: 7,
		Query:     "dependencies",
		Observation: &ipc.QueryObservation{
			AnalysisID: 9,
			Commit:     "abc123",
			Head:       "abc123",
			Status:     "current",
			Complete:   false,
			Sources:    3,
			Analyzed:   2,
			Skipped:    1,
		},
		Components: []ipc.AnalysisComponent{
			{Kind: "package", Identity: "example.com/app", Path: "."},
		},
		Dependencies: []ipc.AnalysisDependency{
			{
				Kind:     "internal",
				Source:   "example.com/app",
				Target:   "example.com/app/internal/greeting",
				Language: "Go",
				Evidence: []ipc.SourceEvidence{
					{Path: "app.go", Line: 3, Declaration: "example.com/app/internal/greeting", BlobHash: "ablob"},
				},
			},
		},
		Total: 1,
	}

	var output bytes.Buffer
	if err := formatQueryResponse(&output, query); err != nil {
		t.Fatalf("formatQueryResponse(): %v", err)
	}
	for _, want := range []string{
		"Source: example.com/app (package)",
		"Dependencies: 1",
		"Dependency evidence:",
		"Complete: no (partial; 1 of 3 source(s) skipped)",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("query output missing %q:\n%s", want, output.String())
		}
	}
	if !hasTabulatedRow(output.String(), []string{"KIND", "SOURCE", "TARGET", "MODULE"}) {
		t.Fatalf("query output missing dependency header:\n%s", output.String())
	}
	if !hasTabulatedRow(output.String(), []string{"internal", "example.com/app", "example.com/app/internal/greeting"}) {
		t.Fatalf("query output missing dependency row:\n%s", output.String())
	}
}

func TestFormatQueryResponseEvidence(t *testing.T) {
	query := ipc.QueryResponse{
		ProjectID: 7,
		Query:     "evidence",
		Components: []ipc.AnalysisComponent{
			{Kind: "package", Identity: "example.com/app/internal/greeting", Path: "internal/greeting"},
		},
		Evidence: []ipc.SourceEvidence{
			{Path: "internal/greeting/greeting.go", Line: 1, Declaration: "package greeting", BlobHash: "gblob"},
		},
		Total: 1,
	}

	var output bytes.Buffer
	if err := formatQueryResponse(&output, query); err != nil {
		t.Fatalf("formatQueryResponse(): %v", err)
	}
	for _, want := range []string{
		"Component: example.com/app/internal/greeting (package)",
		"Evidence: 1",
		"internal/greeting/greeting.go",
		"package greeting",
		"gblob",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("query output missing %q:\n%s", want, output.String())
		}
	}
	if !hasTabulatedRow(output.String(), []string{"PATH", "LINE", "DECLARATION", "BLOB"}) {
		t.Fatalf("query output missing evidence header:\n%s", output.String())
	}
	if strings.Contains(output.String(), "\t") {
		t.Fatalf("query output contains unexpanded tabs:\n%s", output.String())
	}
}

func TestFormatQueryResponseChanges(t *testing.T) {
	query := ipc.QueryResponse{
		ProjectID: 7,
		Query:     "changes",
		Changes: &ipc.QueryChanges{
			From: ipc.QueryObservation{AnalysisID: 1, Commit: "aaa", Status: "stale"},
			To:   ipc.QueryObservation{AnalysisID: 2, Commit: "bbb", Status: "current"},
			AddedComponents: []ipc.AnalysisComponent{
				{Kind: "package", Identity: "example.com/app/internal/word"},
			},
			RemovedComponents: []ipc.AnalysisComponent{
				{Kind: "command", Identity: "example.com/app/cmd/tool"},
			},
			AddedDependencies: []ipc.AnalysisDependency{
				{Kind: "internal", Source: "example.com/app/internal/greeting", Target: "example.com/app/internal/word"},
			},
			RemovedDependencies: []ipc.AnalysisDependency{
				{Kind: "internal", Source: "example.com/app/cmd/tool", Target: "example.com/app/internal/greeting"},
			},
			Reclassified: []ipc.QueryReclassification{
				{Source: "example.com/app", Target: "github.com/example/dep/pkg", FromKind: "external-module", ToKind: "unresolved"},
			},
		},
	}

	var output bytes.Buffer
	if err := formatQueryResponse(&output, query); err != nil {
		t.Fatalf("formatQueryResponse(): %v", err)
	}
	for _, want := range []string{
		"From analysis: 1 (status stale, commit aaa)",
		"To analysis: 2 (status current, commit bbb)",
		"Added components: 1",
		"example.com/app/internal/word",
		"Removed components: 1",
		"example.com/app/cmd/tool",
		"Added dependencies: 1",
		"Removed dependencies: 1",
		"Reclassified dependencies: 1",
		"external-module -> unresolved",
		"no builds or tests were run",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("query output missing %q:\n%s", want, output.String())
		}
	}
}

func TestFormatQueryResponseEvidenceSeparatesLongDeclarationFromBlobHash(t *testing.T) {
	longDeclaration := "example.com/app/" + strings.Repeat("verylongsegment/", 6) + "package"
	query := ipc.QueryResponse{
		ProjectID: 7,
		Query:     "evidence",
		Components: []ipc.AnalysisComponent{
			{Kind: "package", Identity: "example.com/app/internal/greeting"},
		},
		Evidence: []ipc.SourceEvidence{
			{Path: "internal/greeting/greeting.go", Line: 1, Declaration: longDeclaration, BlobHash: "gblob"},
		},
		Total: 1,
	}

	var output bytes.Buffer
	if err := formatQueryResponse(&output, query); err != nil {
		t.Fatalf("formatQueryResponse(): %v", err)
	}

	var row string
	for _, line := range strings.Split(output.String(), "\n") {
		if strings.Contains(line, longDeclaration) {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatalf("declaration row missing:\n%s", output.String())
	}
	blobIndex := strings.Index(row, "gblob")
	declarationEnd := strings.Index(row, longDeclaration) + len(longDeclaration)
	if blobIndex < declarationEnd {
		t.Fatalf("blob hash overlaps declaration in %q", row)
	}
	gap := row[declarationEnd:blobIndex]
	if len(gap) < 2 || strings.Trim(gap, " ") != "" {
		t.Fatalf("ambiguous separator %q between declaration and blob hash in %q", gap, row)
	}
}

func TestFormatQueryResponseSanitizesDeclarationWhitespace(t *testing.T) {
	query := ipc.QueryResponse{
		ProjectID: 7,
		Query:     "evidence",
		Components: []ipc.AnalysisComponent{
			{Kind: "package", Identity: "example.com/app"},
		},
		Evidence: []ipc.SourceEvidence{
			{Path: "app.go", Line: 1, Declaration: "package\tgreeting\nline", BlobHash: "gblob"},
		},
		Total: 1,
	}

	var output bytes.Buffer
	if err := formatQueryResponse(&output, query); err != nil {
		t.Fatalf("formatQueryResponse(): %v", err)
	}
	if strings.Contains(output.String(), "\t") {
		t.Fatalf("sanitized output contains tab:\n%s", output.String())
	}
	if !hasTabulatedRow(output.String(), []string{"app.go", "1", "package", "greeting", "line", "gblob"}) {
		t.Fatalf("sanitized row not tabulated:\n%s", output.String())
	}
}

func hasTabulatedRow(output string, want []string) bool {
	for _, line := range strings.Split(output, "\n") {
		if sameFields(strings.Fields(line), want) {
			return true
		}
	}
	return false
}

func sameFields(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
