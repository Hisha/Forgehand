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
	for _, want := range []string{
		"Evidence files:",
		"main.go\tsource\tGo\t10\tblob",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("evidence inspection missing %q:\n%s", want, output.String())
		}
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
	if strings.Contains(output.String(), "KIND\tSOURCE\tTARGET\tMODULE") {
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
