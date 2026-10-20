package task

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/workspace"
)

// newGoModuleWorkspace creates a git worktree backed by a minimal, passing Go
// module and returns an opened workspace rooted at it. Tests use it to exercise
// formatting and testing against the real workspace resolution rules.
func newGoModuleWorkspace(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not available")
	}

	root := t.TempDir()
	runGitTest(t, root, "init")
	runGitTest(t, root, "config", "user.email", "forgehand-test@example.invalid")
	runGitTest(t, root, "config", "user.name", "Forgehand Test")

	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("go.mod", "module example.com/validate\n\ngo 1.22\n")
	write("sum.go", "package validate\n\nfunc Add(a, b int) int { return a + b }\n")
	write("sum_test.go", "package validate\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	runGitTest(t, root, "add", ".")
	runGitTest(t, root, "commit", "-m", "baseline")

	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	return ws, root
}

func runGitTest(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestValidateFormatsUnformattedGoFile(t *testing.T) {
	ws, root := newGoModuleWorkspace(t)

	// An intentionally unformatted Go file applied to the workspace.
	unformatted := "package validate\n\nfunc Mul(a, b int) int {\nreturn a * b\n}\n"
	if err := ws.Apply([]workspace.Change{
		{Op: "write", Path: "mul.go", Content: unformatted},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	changes := []workspace.Change{{Op: "write", Path: "mul.go"}}
	validator := &Validator{Timeout: 30 * time.Second}
	result, err := validator.Validate(context.Background(), ws, changes)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Status != "passed" {
		t.Fatalf("status = %q, want passed; checks = %+v", result.Status, result.Checks)
	}

	// The file on disk must now be formatted (gofmt -w wrote it back).
	formatted, err := os.ReadFile(filepath.Join(root, "mul.go"))
	if err != nil {
		t.Fatalf("read formatted file: %v", err)
	}
	if string(formatted) == unformatted {
		t.Fatalf("file was not formatted in place:\n%s", formatted)
	}
	if !strings.Contains(string(formatted), "\treturn a * b") {
		t.Fatalf("formatting did not apply gofmt style:\n%s", formatted)
	}
	if strings.Contains(string(formatted), "\nreturn a * b") {
		t.Fatalf("unformatted body still present:\n%s", formatted)
	}
}

func TestValidateFormattedFilesReported(t *testing.T) {
	ws, _ := newGoModuleWorkspace(t)
	unformatted := "package validate\n\nfunc Mul(a, b int) int {\nreturn a * b\n}\n"
	if err := ws.Apply([]workspace.Change{
		{Op: "write", Path: "mul.go", Content: unformatted},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	validator := &Validator{Timeout: 30 * time.Second}
	result, err := validator.Validate(
		context.Background(),
		ws,
		[]workspace.Change{{Op: "write", Path: "mul.go"}},
	)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(result.Checks) == 0 || result.Checks[0].Name != "gofmt" {
		t.Fatalf("expected a gofmt check first, got %+v", result.Checks)
	}
	if len(result.Checks[0].Formatted) != 1 || result.Checks[0].Formatted[0] != "mul.go" {
		t.Fatalf("formatted list = %v, want [mul.go]", result.Checks[0].Formatted)
	}
}

func TestValidateExcludesDeletedGoFiles(t *testing.T) {
	ws, root := newGoModuleWorkspace(t)

	// Track a second go file then delete it, alongside a write. The deleted
	// file must not be handed to gofmt.
	if err := os.WriteFile(filepath.Join(root, "gone.go"), []byte("package validate\n"), 0o644); err != nil {
		t.Fatalf("write gone.go: %v", err)
	}
	runGitTest(t, root, "add", "gone.go")
	runGitTest(t, root, "commit", "-m", "add gone")

	if err := ws.Apply([]workspace.Change{
		{Op: "write", Path: "mul.go", Content: "package validate\n\nfunc Mul(a, b int) int {\nreturn a * b\n}\n"},
		{Op: "delete", Path: "gone.go"},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	changes := []workspace.Change{
		{Op: "write", Path: "mul.go"},
		{Op: "delete", Path: "gone.go"},
	}
	eligible, err := eligibleGoFiles(ws, changes)
	if err != nil {
		t.Fatalf("eligibleGoFiles: %v", err)
	}
	for _, path := range eligible {
		if path == "gone.go" {
			t.Fatalf("deleted file included in format set: %v", eligible)
		}
	}
	if len(eligible) != 1 || eligible[0] != "mul.go" {
		t.Fatalf("eligible = %v, want [mul.go]", eligible)
	}

	validator := &Validator{Timeout: 30 * time.Second}
	result, err := validator.Validate(context.Background(), ws, changes)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Status != "passed" {
		t.Fatalf("status = %q, want passed", result.Status)
	}
}

func TestEligibleGoFilesRejectsPathEscape(t *testing.T) {
	ws, _ := newGoModuleWorkspace(t)

	// A proposal path that escapes the workspace must be refused rather than
	// silently skipped, so gofmt can never be pointed outside the worktree.
	_, err := eligibleGoFiles(ws, []workspace.Change{
		{Op: "write", Path: "../escape.go"},
	})
	if err == nil {
		t.Fatal("eligibleGoFiles accepted an escaping path")
	}
	if !strings.Contains(err.Error(), "escape") {
		t.Fatalf("error = %v, want a path-escape error", err)
	}
}

func TestEligibleGoFilesSkipsNonExistentWrite(t *testing.T) {
	ws, _ := newGoModuleWorkspace(t)
	// A write that was never applied leaves no file; it must be skipped, not
	// passed to gofmt.
	eligible, err := eligibleGoFiles(ws, []workspace.Change{
		{Op: "write", Path: "absent.go"},
	})
	if err != nil {
		t.Fatalf("eligibleGoFiles: %v", err)
	}
	if len(eligible) != 0 {
		t.Fatalf("eligible = %v, want empty", eligible)
	}
}

func TestValidateNoChangesSkips(t *testing.T) {
	ws, _ := newGoModuleWorkspace(t)
	validator := &Validator{}
	result, err := validator.Validate(context.Background(), ws, nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Status != "skipped" {
		t.Fatalf("status = %q, want skipped", result.Status)
	}
}

func TestValidateFailingTestReportsFailure(t *testing.T) {
	ws, root := newGoModuleWorkspace(t)
	// Break the test so go test fails.
	if err := os.WriteFile(
		filepath.Join(root, "sum_test.go"),
		[]byte("package validate\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 6 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n"),
		0o644,
	); err != nil {
		t.Fatalf("write failing test: %v", err)
	}

	validator := &Validator{Timeout: 30 * time.Second}
	result, err := validator.Validate(
		context.Background(),
		ws,
		[]workspace.Change{{Op: "write", Path: "sum.go"}},
	)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	for _, check := range result.Checks {
		if check.Name == "go-test" && check.ExitCode == 0 {
			t.Fatalf("go-test reported exit 0 despite failing test: %+v", check)
		}
	}
}

func TestMinimalValidationEnvExcludesSecrets(t *testing.T) {
	t.Setenv("FORGEHAND_TEST_SECRET_TOKEN", "super-secret")
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("PATH", "/usr/bin")

	env := minimalValidationEnv()
	joined := strings.Join(env, "\n")
	for _, leaked := range []string{
		"FORGEHAND_TEST_SECRET_TOKEN",
		"OPENAI_API_KEY",
		"AWS_SECRET_ACCESS_KEY",
	} {
		if strings.Contains(joined, leaked) {
			t.Fatalf("minimalValidationEnv leaked %s: %v", leaked, env)
		}
	}
	// PATH must survive so the toolchain can be located.
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Fatalf("minimalValidationEnv dropped PATH: %v", env)
	}
	// It must be an allowlist, not the full environment.
	if len(env) > 20 {
		t.Fatalf("minimalValidationEnv is not minimal: %d vars", len(env))
	}
}

func TestValidateTimeoutCancels(t *testing.T) {
	ws, _ := newGoModuleWorkspace(t)
	// A 1ns timeout forces the command to be canceled immediately.
	validator := &Validator{Timeout: time.Nanosecond}
	result, err := validator.Validate(
		context.Background(),
		ws,
		[]workspace.Change{{Op: "write", Path: "sum.go"}},
	)
	if err == nil && result.Status == "passed" {
		t.Fatalf("expected timeout to be reported; result = %+v", result)
	}
}

func TestValidateBoundsOutput(t *testing.T) {
	s := strings.Repeat("x", validateStdoutLimit+100)
	got := truncateOutput(s, validateStdoutLimit)
	if len(got) > validateStdoutLimit+50 {
		t.Fatalf("truncateOutput did not bound output: len = %d", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("truncateOutput missing truncation marker: %q", got)
	}
}
