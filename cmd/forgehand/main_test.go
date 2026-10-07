package main

import (
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
