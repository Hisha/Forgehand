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
