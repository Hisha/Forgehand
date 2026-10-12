package main

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/ipc"
)

func TestInvestigateCommandJSONAndText(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".fh-cli-invest-")
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(abs) })
	socketPath := filepath.Join(abs, "forgehand.sock")
	t.Setenv("FORGEHAND_SOCKET_PATH", socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan ipc.Request, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var req ipc.Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			return
		}
		received <- req
		inv := &ipc.InvestigationResponse{
			SchemaVersion: 1,
			ProjectID:     7,
			AnalysisID:    9,
			Commit:        "abc123",
			Status:        "current",
			Complete:      true,
			Component:     ipc.AnalysisComponent{Kind: "package", Identity: "example.com/app/internal/greeting", Path: "internal/greeting", Internal: true},
			Dependencies:  []ipc.AnalysisDependency{{Kind: "internal", Source: "example.com/app", Target: "example.com/app/internal/greeting"}},
			Dependents:    []ipc.QueryDependent{{Kind: "internal", Source: "example.com/app/cmd/tool", Target: "example.com/app/internal/greeting"}},
			Evidence:      []ipc.SourceEvidence{{Path: "internal/greeting/greeting.go", Line: 1, Declaration: "package greeting", BlobHash: "gblob"}},
			SuggestedFiles: []ipc.InvestigationFile{{Path: "internal/greeting/greeting.go", Reasons: []string{"component-declaration", "incoming-dependency-declaration"}}},
		}
		_ = json.NewEncoder(conn).Encode(ipc.Response{OK: true, Investigation: inv})
	}()

	oldStdout := os.Stdout
	oldStderr := os.Stderr
	outReader, outWriter, _ := os.Pipe()
	errReader, errWriter, _ := os.Pipe()
	os.Stdout = outWriter
	os.Stderr = errWriter
	if err := investigateCommand([]string{"7", "--component", "example.com/app/internal/greeting", "--format", "json", "--analysis", "9", "--limit", "10"}); err != nil {
		t.Fatal(err)
	}
	outWriter.Close()
	errWriter.Close()
	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	_, _ = stdoutBuf.ReadFrom(outReader)
	_, _ = stderrBuf.ReadFrom(errReader)
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	listener.Close()

	req := <-received
	if req.Command != "investigate" || req.ProjectID != 7 || req.Identity != "example.com/app/internal/greeting" {
		t.Fatalf("bad req: %#v", req)
	}
	if req.AnalysisID != 9 || req.Format != "json" || req.Limit != 10 {
		t.Fatalf("bad req fields: %#v", req)
	}

	var invOut ipc.InvestigationResponse
	if err := json.Unmarshal(stdoutBuf.Bytes(), &invOut); err != nil {
		t.Fatalf("json decode: %v\n%s", err, stdoutBuf.String())
	}
	if invOut.SchemaVersion != 1 || invOut.Component.Identity == "" {
		t.Fatalf("bad json: %#v", invOut)
	}
	if strings.Contains(stdoutBuf.String(), "warning:") {
		t.Fatalf("diagnostics should be on stderr, not stdout: %s", stdoutBuf.String())
	}

	// Text
	_ = os.Remove(socketPath)
	listener2, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := listener2.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var req ipc.Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			return
		}
		inv := &ipc.InvestigationResponse{
			SchemaVersion:  1,
			ProjectID:      7,
			AnalysisID:     9,
			Commit:         "abc123",
			Status:         "current",
			Complete:       true,
			Component:      ipc.AnalysisComponent{Kind: "package", Identity: "example.com/app/internal/greeting", Path: "internal/greeting", Internal: true},
			Dependencies:   []ipc.AnalysisDependency{{Kind: "internal", Source: "example.com/app", Target: "example.com/app/internal/greeting"}},
			Dependents:     []ipc.QueryDependent{{Kind: "internal", Source: "example.com/app/cmd/tool", Target: "example.com/app/internal/greeting"}},
			Evidence:       []ipc.SourceEvidence{{Path: "internal/greeting/greeting.go", Line: 1, Declaration: "package greeting", BlobHash: "gblob"}},
			SuggestedFiles: []ipc.InvestigationFile{{Path: "internal/greeting/greeting.go", Reasons: []string{"component-declaration"}}},
		}
		_ = json.NewEncoder(conn).Encode(ipc.Response{OK: true, Investigation: inv})
	}()
	var textOut bytes.Buffer
	r, w, _ := os.Pipe()
	os.Stdout = w
	if err := investigateCommand([]string{"7", "--component", "example.com/app/internal/greeting"}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	_, _ = textOut.ReadFrom(r)
	os.Stdout = oldStdout
	listener2.Close()
	if !strings.Contains(textOut.String(), "Investigation of component") || !strings.Contains(textOut.String(), "Suggested investigation files") {
		t.Fatalf("text output missing expected content:\n%s", textOut.String())
	}
}

func TestInvestigateValidation(t *testing.T) {
	cases := []struct {
		args []string
	}{
		{[]string{"7"}},
		{[]string{"abc", "--component", "x"}},
		{[]string{"7", "--component", "x", "--format", "xml"}},
		{[]string{"7", "--component", "x", "--limit", "-1"}},
		{[]string{"7", "--component", "x", "--limit", "bad"}},
		{[]string{"7", "--component", "x", "--analysis", "0"}},
		{[]string{"7", "--analysis", "1"}},
	}
	for _, c := range cases {
		if err := investigateCommand(c.args); err == nil {
			t.Fatalf("expected error for %v", c.args)
		}
	}
}
