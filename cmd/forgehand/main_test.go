package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	if err := formatProjectInspection(&output, inspection, false); err != nil {
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
	if err := formatProjectInspection(&output, inspection, false); err != nil {
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
	if err := formatProjectInspection(&output, inspection, true); err != nil {
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
