package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/provider"
	"github.com/Hisha/Forgehand/internal/workspace"
)

type stubProvider struct {
	responses []provider.ChatResponse
	calls     int
	lastUser  string
}

func (s *stubProvider) Complete(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	s.lastUser = req.User
	index := s.calls
	s.calls++
	if index >= len(s.responses) {
		index = len(s.responses) - 1
	}
	return s.responses[index], nil
}

type errorProvider struct{ err error }

func (e errorProvider) Complete(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, e.err
}

func jsonResponse(body string) provider.ChatResponse {
	return provider.ChatResponse{Content: body}
}

func taskTestRepo(t *testing.T) (repoRoot, worktreePath string) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}

	repoRoot = t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.email", "forgehand-test@example.invalid")
	runGit(t, repoRoot, "config", "user.name", "Forgehand Test")

	if err := os.WriteFile(filepath.Join(repoRoot, "tracked.txt"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatalf("write tracked.txt: %v", err)
	}
	runGit(t, repoRoot, "add", "tracked.txt")
	runGit(t, repoRoot, "commit", "-m", "baseline")

	base := strings.TrimSpace(gitOutput(t, repoRoot, "rev-parse", "HEAD"))
	worktreePath = filepath.Join(t.TempDir(), "ws")
	if err := workspace.Ensure(context.Background(), repoRoot, base, worktreePath); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return repoRoot, worktreePath
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func TestParseResponseVariants(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantAct string
		wantErr bool
	}{
		{"plain", `{"action":"final","summary":"done"}`, "final", false},
		{"fenced", "```json\n{\"action\":\"propose\"}\n```", "propose", false},
		{"trailing prose", `Here you go: {"action":"inspect"} thanks`, "inspect", false},
		{"whitespace", "   \n {\"action\":\"final\"}\n", "final", false},
		{"no json", "I cannot help with that", "", true},
		{"missing action", `{"summary":"oops"}`, "", true},
		{"malformed", `{"action":}`, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseResponse(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseResponse(%q) succeeded, want error", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseResponse(%q): %v", tc.input, err)
			}
			if parsed.Action != tc.wantAct {
				t.Fatalf("action = %q, want %q", parsed.Action, tc.wantAct)
			}
		})
	}
}

func TestValidatePathRejectsUnsafePaths(t *testing.T) {
	rejected := []string{
		"",
		"/etc/passwd",
		"../escape.txt",
		"a/../../escape.txt",
		".git/config",
		".git",
	}
	for _, path := range rejected {
		t.Run(fmt.Sprintf("reject %q", path), func(t *testing.T) {
			if _, err := validatePath(path); err == nil {
				t.Fatalf("validatePath(%q) succeeded, want error", path)
			}
		})
	}

	accepted := map[string]string{
		"file.txt":      "file.txt",
		"./nested/a.go": "nested/a.go",
		"dir/file.txt":  "dir/file.txt",
	}
	for input, want := range accepted {
		got, err := validatePath(input)
		if err != nil {
			t.Fatalf("validatePath(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("validatePath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeChangesValidation(t *testing.T) {
	limits := Limits{MaxChanges: 3, MaxFileBytes: 8}

	t.Run("valid changes are sorted", func(t *testing.T) {
		changes, err := normalizeChanges([]ChangeRequest{
			{Op: "write", Path: "b.txt", Content: "b"},
			{Op: "write", Path: "a.txt", Content: "a"},
		}, Limits{}.normalize())
		if err != nil {
			t.Fatalf("normalizeChanges: %v", err)
		}
		if len(changes) != 2 || changes[0].Path != "a.txt" || changes[1].Path != "b.txt" {
			t.Fatalf("changes not canonicalized: %+v", changes)
		}
	})

	rejected := []struct {
		name     string
		requests []ChangeRequest
	}{
		{"empty", nil},
		{"unsupported op", []ChangeRequest{{Op: "chmod", Path: "a.txt"}}},
		{"escaping", []ChangeRequest{{Op: "write", Path: "../x", Content: "x"}}},
		{"reserved", []ChangeRequest{{Op: "write", Path: ".git/config", Content: "x"}}},
		{"absolute", []ChangeRequest{{Op: "write", Path: "/x", Content: "x"}}},
		{"duplicate", []ChangeRequest{
			{Op: "write", Path: "a.txt", Content: "a"},
			{Op: "delete", Path: "a.txt"},
		}},
		{"too many", []ChangeRequest{
			{Op: "write", Path: "a.txt", Content: "a"},
			{Op: "write", Path: "b.txt", Content: "b"},
			{Op: "write", Path: "c.txt", Content: "c"},
			{Op: "write", Path: "d.txt", Content: "d"},
		}},
		{"oversized content", []ChangeRequest{{Op: "write", Path: "a.txt", Content: "0123456789"}}},
	}

	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := normalizeChanges(tc.requests, limits); err == nil {
				t.Fatalf("normalizeChanges accepted %s", tc.name)
			}
		})
	}
}

func TestProposalHashBindsSummaryAndChanges(t *testing.T) {
	a := []workspace.Change{{Op: "write", Path: "a.txt", Content: "a"}}
	b := []workspace.Change{{Op: "write", Path: "b.txt", Content: "b"}}

	base := ProposalHash("summary", a)
	if base != ProposalHash("summary", a) {
		t.Fatal("ProposalHash is not stable across calls")
	}

	// Order independence: the same change set in a different order hashes the
	// same way.
	reverse := []workspace.Change{b[0], a[0]}
	forward := []workspace.Change{a[0], b[0]}
	if ProposalHash("summary", reverse) != ProposalHash("summary", forward) {
		t.Fatal("ProposalHash depends on change ordering")
	}

	if ProposalHash("different", a) == base {
		t.Fatal("ProposalHash ignores the summary")
	}
	if ProposalHash("summary", b) == base {
		t.Fatal("ProposalHash ignores the change set")
	}
	changed := []workspace.Change{{Op: "write", Path: "a.txt", Content: "MUTATED"}}
	if ProposalHash("summary", changed) == base {
		t.Fatal("ProposalHash ignores file content")
	}
}

func TestEncodeDecodeChangesRoundTrip(t *testing.T) {
	changes := []workspace.Change{
		{Op: "write", Path: "b.txt", Content: "b"},
		{Op: "delete", Path: "a.txt"},
	}
	decoded, err := DecodeChanges(EncodeChanges(changes))
	if err != nil {
		t.Fatalf("DecodeChanges: %v", err)
	}
	if len(decoded) != 2 || decoded[0].Path != "a.txt" || decoded[1].Path != "b.txt" {
		t.Fatalf("round-trip mismatch: %+v", decoded)
	}
	if decoded[1].Content != "b" {
		t.Fatalf("content lost in round-trip: %+v", decoded[1])
	}

	if empty, err := DecodeChanges(""); err != nil || empty != nil {
		t.Fatalf("DecodeChanges(\"\") = %+v, %v; want nil, nil", empty, err)
	}
}

func TestRunnerInspectsThenProposesAndRecordsOperations(t *testing.T) {
	_, worktreePath := taskTestRepo(t)
	ws, err := workspace.Open(worktreePath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	stub := &stubProvider{responses: []provider.ChatResponse{
		jsonResponse(`{"action":"inspect","summary":"reading","operations":[
			{"op":"read","path":"tracked.txt"},
			{"op":"list","path":""}
		]}`),
		jsonResponse(`{"action":"propose","summary":"rewrite","changes":[
			{"op":"write","path":"tracked.txt","content":"changed\n"}
		]}`),
	}}

	var operations []Operation
	runner := &Runner{
		Provider:    stub,
		Limits:      Limits{MaxIterations: 5},
		OnOperation: func(op Operation) { operations = append(operations, op) },
	}

	result, err := runner.Run(context.Background(), ws, Contract{Task: "rewrite", BaseCommit: "HEAD"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stub.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", stub.calls)
	}
	if len(result.Changes) != 1 || result.Changes[0].Path != "tracked.txt" {
		t.Fatalf("unexpected proposal: %+v", result.Changes)
	}
	if result.Iterations != 2 {
		t.Fatalf("iterations = %d, want 2", result.Iterations)
	}
	if len(operations) != 2 {
		t.Fatalf("recorded operations = %d, want 2", len(operations))
	}
	// The proposal is not applied by the runner.
	content, err := os.ReadFile(filepath.Join(worktreePath, "tracked.txt"))
	if err != nil {
		t.Fatalf("read tracked.txt: %v", err)
	}
	if string(content) != "baseline\n" {
		t.Fatalf("runner applied a change; file = %q", content)
	}
}

func TestRunnerFinalWithoutChanges(t *testing.T) {
	_, worktreePath := taskTestRepo(t)
	ws, _ := workspace.Open(worktreePath)

	stub := &stubProvider{responses: []provider.ChatResponse{
		jsonResponse(`{"action":"final","summary":"nothing to do"}`),
	}}
	runner := &Runner{Provider: stub, Limits: Limits{MaxIterations: 3}}

	result, err := runner.Run(context.Background(), ws, Contract{Task: "noop"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Changes) != 0 {
		t.Fatalf("expected no changes, got %+v", result.Changes)
	}
	if result.Summary != "nothing to do" {
		t.Fatalf("summary = %q", result.Summary)
	}
}

func TestRunnerRecoversFromMalformedResponse(t *testing.T) {
	_, worktreePath := taskTestRepo(t)
	ws, _ := workspace.Open(worktreePath)

	stub := &stubProvider{responses: []provider.ChatResponse{
		jsonResponse("not json at all"),
		jsonResponse(`{"action":"final","summary":"recovered"}`),
	}}
	runner := &Runner{Provider: stub, Limits: Limits{MaxIterations: 4}}

	result, err := runner.Run(context.Background(), ws, Contract{Task: "noop"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Summary != "recovered" {
		t.Fatalf("summary = %q, want recovered", result.Summary)
	}
	if stub.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", stub.calls)
	}
}

func TestRunnerIterationLimit(t *testing.T) {
	_, worktreePath := taskTestRepo(t)
	ws, _ := workspace.Open(worktreePath)

	stub := &stubProvider{responses: []provider.ChatResponse{
		jsonResponse(`{"action":"inspect","summary":"looping","operations":[]}`),
	}}
	runner := &Runner{Provider: stub, Limits: Limits{MaxIterations: 3}}

	_, err := runner.Run(context.Background(), ws, Contract{Task: "loop"})
	if err == nil {
		t.Fatal("Run succeeded despite never reaching a terminal action")
	}
	if stub.calls != 3 {
		t.Fatalf("provider calls = %d, want 3 (the iteration limit)", stub.calls)
	}
}

func TestRunnerPropagatesProviderError(t *testing.T) {
	_, worktreePath := taskTestRepo(t)
	ws, _ := workspace.Open(worktreePath)

	sentinel := errors.New("provider unavailable")
	runner := &Runner{Provider: errorProvider{err: sentinel}, Limits: Limits{MaxIterations: 2}}

	_, err := runner.Run(context.Background(), ws, Contract{Task: "x"})
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("Run error = %v, want provider error", err)
	}
}

func TestRunnerRejectsEscapingProposalThenAcceptsFixed(t *testing.T) {
	_, worktreePath := taskTestRepo(t)
	ws, _ := workspace.Open(worktreePath)

	stub := &stubProvider{responses: []provider.ChatResponse{
		jsonResponse(`{"action":"propose","summary":"bad","changes":[
			{"op":"write","path":"../escape.txt","content":"x"}
		]}`),
		jsonResponse(`{"action":"propose","summary":"good","changes":[
			{"op":"write","path":"safe.txt","content":"ok\n"}
		]}`),
	}}
	runner := &Runner{Provider: stub, Limits: Limits{MaxIterations: 4}}

	result, err := runner.Run(context.Background(), ws, Contract{Task: "x"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Changes) != 1 || result.Changes[0].Path != "safe.txt" {
		t.Fatalf("proposal not corrected: %+v", result.Changes)
	}
}
