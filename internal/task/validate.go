package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Hisha/Forgehand/internal/workspace"
)

// Bounded output for a single validation check. Truncation keeps a failed or
// verbose test run from flooding the database or the task-status output.
const (
	// validateStdoutLimit bounds the captured standard output of one check.
	validateStdoutLimit = 8 * 1024
	// validateStderrLimit bounds the captured standard error of one check.
	validateStderrLimit = 8 * 1024
)

// ValidationCheck is the recorded outcome of one validation command (for
// example, running gofmt or go test). It is persisted verbatim so a human can
// see exactly what ran and why it passed or failed.
type ValidationCheck struct {
	Name     string `json:"name"`
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Duration string `json:"duration"`
	// Skipped is set when the check was considered but not run (for example,
	// the toolchain is unavailable). Skipped is not a failure.
	Skipped bool   `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
	// Formatted lists the workspace-relative Go files that gofmt rewrote. It is
	// only populated for the gofmt check.
	Formatted []string `json:"formatted,omitempty"`
}

// ValidationResult is the aggregate outcome of the validation stage.
type ValidationResult struct {
	// Status is one of the state.TaskValidation* constants: passed, failed,
	// error, or skipped. Callers map it onto the task run.
	Status string
	Checks []ValidationCheck
}

// Validator runs the opt-in, bounded post-apply validation stage inside an
// isolated worktree.
//
// SECURITY: This stage is NOT a sandbox. It runs the project's own toolchain
// (`go test ./...`) inside a Git worktree, which executes repository-controlled
// code with the daemon's user privileges. A Git worktree isolates file layout,
// not the process: tests can read and write files the daemon user can, make
// network calls, and spawn processes. Enable it (FORGEHAND_TASK_VALIDATE) only
// for repositories whose contents the operator trusts. It is disabled by
// default. Every command is invoked with explicit arguments through
// exec.CommandContext (never a shell) and a minimal environment, and is
// canceled when the per-command timeout expires.
type Validator struct {
	// Timeout bounds each individual validation command. Zero means
	// DefaultValidateTimeout.
	Timeout time.Duration
	// GoTool is the go binary to invoke. Empty means "go" from PATH.
	GoTool string
	// GoFmtTool is the gofmt binary to invoke. Empty means "gofmt" from PATH.
	GoFmtTool string
}

// DefaultValidateTimeout is the per-command bound applied when the validator
// has no explicit timeout.
const DefaultValidateTimeout = 2 * time.Minute

// Validate formats and tests the modified Go files of a proposal inside the
// given isolated worktree. It runs, in order:
//
//  1. gofmt -w on each eligible .go file, writing the formatted result back
//     into the workspace so the recorded diff includes it. Only files that
//     exist after apply and were created or overwritten by the proposal are
//     formatted; deleted files are never touched. Every path is resolved
//     through the workspace, so a path outside the worktree is refused.
//  2. go test ./... when the worktree root contains a go.mod.
//
// Because formatting mutates the workspace, callers must run Validate before
// computing the applied workspace diff so the persisted diff reflects the
// formatted result. It returns a non-nil result describing every check it
// considered, and a non-nil error only when the stage itself could not run at
// all (for example, the toolchain is missing or the context was canceled).
func (v *Validator) Validate(
	ctx context.Context,
	ws *workspace.Workspace,
	changes []workspace.Change,
) (ValidationResult, error) {
	result := ValidationResult{Status: "passed"}
	if len(changes) == 0 {
		result.Status = "skipped"
		return result, nil
	}

	goTool := v.GoTool
	if goTool == "" {
		goTool = "go"
	}
	gofmtTool := v.GoFmtTool
	if gofmtTool == "" {
		gofmtTool = "gofmt"
	}
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = DefaultValidateTimeout
	}

	// Stage 1: format the eligible modified Go files in place. Deleted files
	// are excluded because they no longer exist post-apply.
	goFiles, err := eligibleGoFiles(ws, changes)
	if err != nil {
		result.Status = "error"
		result.Checks = append(result.Checks, ValidationCheck{
			Name:  "gofmt",
			Error: err.Error(),
		})
		return result, err
	}
	if len(goFiles) > 0 {
		if _, err := exec.LookPath(gofmtTool); err != nil {
			result.Status = "error"
			result.Checks = append(result.Checks, ValidationCheck{
				Name:    "gofmt",
				Skipped: true,
				Error:   fmt.Sprintf("%s is not available on PATH", gofmtTool),
			})
			return result, fmt.Errorf("%s is not available: %w", gofmtTool, err)
		}
		check := runValidationCommand(
			ctx, ws.Root(), gofmtTool, append([]string{"-w", "--"}, goFiles...),
			"gofmt", timeout, minimalValidationEnv(),
		)
		if check.ExitCode == 0 {
			check.Formatted = goFiles
		}
		result.Checks = append(result.Checks, check)
		if check.ExitCode != 0 {
			result.Status = "failed"
		}
	}

	// Stage 2: go test ./... when the project is a Go module.
	if hasGoMod(ws.Root()) {
		if _, err := exec.LookPath(goTool); err != nil {
			result.Status = "error"
			result.Checks = append(result.Checks, ValidationCheck{
				Name:    "go-test",
				Skipped: true,
				Error:   fmt.Sprintf("%s is not available on PATH", goTool),
			})
			return result, fmt.Errorf("%s is not available: %w", goTool, err)
		}
		check := runValidationCommand(
			ctx, ws.Root(), goTool, []string{"test", "./..."},
			"go-test", timeout, minimalValidationEnv(),
		)
		result.Checks = append(result.Checks, check)
		if check.ExitCode != 0 {
			result.Status = "failed"
		}
	}

	// A canceled context means the stage did not finish; report it as an error
	// rather than a silent pass.
	if ctx.Err() != nil {
		result.Status = "error"
		return result, ctx.Err()
	}

	return result, nil
}

// eligibleGoFiles returns the sorted, de-duplicated set of workspace-relative
// .go file paths that should be formatted: those a "write" change created or
// overwrote and that actually exist as regular files in the workspace after
// apply. Deleted files and paths that escape the workspace are excluded. Every
// path is validated through the workspace so an unsafe proposal path can never
// reach gofmt.
func eligibleGoFiles(
	ws *workspace.Workspace,
	changes []workspace.Change,
) ([]string, error) {
	seen := make(map[string]struct{})
	for _, change := range changes {
		// A delete removes the file; there is nothing to format.
		if strings.TrimSpace(change.Op) != "write" {
			continue
		}
		if !strings.HasSuffix(change.Path, ".go") {
			continue
		}
		if _, ok := seen[change.Path]; ok {
			continue
		}

		info, err := ws.Stat(change.Path)
		if err != nil {
			// A proposal path that escapes the workspace or names reserved
			// metadata is a hard error: refuse rather than silently skipping.
			if errors.Is(err, workspace.ErrPathEscape) ||
				errors.Is(err, workspace.ErrReservedPath) {
				return nil, fmt.Errorf("format %q: %w", change.Path, err)
			}
			// The file is not present as a regular file (for example, it was
			// written as a directory or removed concurrently); skip it.
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		seen[change.Path] = struct{}{}
	}

	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

// hasGoMod reports whether the root contains a go.mod file.
func hasGoMod(root string) bool {
	info, err := os.Stat(filepath.Join(root, "go.mod"))
	return err == nil && !info.IsDir()
}

// runValidationCommand executes one validation command inside the worktree,
// capturing bounded stdout/stderr and the exit code. It never invokes a shell.
func runValidationCommand(
	ctx context.Context,
	dir string,
	binary string,
	args []string,
	name string,
	timeout time.Duration,
	env []string,
) ValidationCheck {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(commandCtx, binary, args...)
	cmd.Dir = dir
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &boundedWriter{buf: &stdout, limit: validateStdoutLimit}
	cmd.Stderr = &boundedWriter{buf: &stderr, limit: validateStderrLimit}

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	check := ValidationCheck{
		Name:     name,
		Command:  strings.Join(append([]string{binary}, args...), " "),
		Duration: duration.Round(time.Millisecond).String(),
		Stdout:   truncateOutput(stdout.String(), validateStdoutLimit),
		Stderr:   truncateOutput(stderr.String(), validateStderrLimit),
	}

	if err != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			check.ExitCode = exitErr.ExitCode()
		case commandCtx.Err() != nil:
			check.ExitCode = -1
			check.Error = fmt.Sprintf("validation timed out after %s", timeout)
		default:
			check.ExitCode = -1
			check.Error = err.Error()
		}
	}
	return check
}

// minimalValidationEnv returns a deliberately small environment for validation
// commands. It forwards only the variables the Go toolchain requires for
// compilation, module resolution, and caching, and drops everything else the
// daemon may hold (API keys, provider tokens, cloud credentials, and so on).
//
// SECURITY: This reduces the secrets exposed to repository-controlled test
// code; it does not sandbox that code. A Go test can still read the filesystem
// as the daemon user, open sockets, and inspect its own (now minimal)
// environment.
func minimalValidationEnv() []string {
	// Start from an empty environment and add only a vetted allowlist.
	env := make([]string, 0, 12)

	for _, key := range []string{
		"PATH",   // locate go, git, and other build tools
		"HOME",   // default GOPATH/GOCACHE resolution
		"GOPATH", // module cache and workspace location
		"GOCACHE",
		"GOMODCACHE",
		"GOROOT",
		"GOFLAGS",
		"GOPROXY",
		"GONOSUMDB",
		"GONOSUMCHECK",
		"GOSUMDB",
		"GOPRIVATE",
		"GOTOOLCHAIN",
		"TMPDIR", // scratch space for the toolchain
		"LANG",
		"LC_ALL",
		"TZ",
	} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}

	return env
}

// boundedWriter captures at most limit bytes and discards the rest, so a
// chatty test run cannot exhaust memory or bloat the database.
type boundedWriter struct {
	buf   *bytes.Buffer
	limit int
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = w.buf.Write(p[:remaining])
		return len(p), nil
	}
	return w.buf.Write(p)
}

// truncateOutput trims output that exceeded the capture limit.
func truncateOutput(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "\n... [output truncated]"
}
