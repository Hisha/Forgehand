package main

import (
	"bytes"
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
