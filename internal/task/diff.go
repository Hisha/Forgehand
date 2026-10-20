package task

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Hisha/Forgehand/internal/workspace"
)

// OriginalReader returns the pre-change content of a workspace-relative path.
// The bool reports whether the path existed before the change. It is used to
// render a proposal preview without mutating the workspace.
type OriginalReader func(path string) (string, bool, error)

// RenderProposalDiff renders a set of proposed changes as a standard unified
// Git-style diff, comparing each path's original content (as reported by
// original) against the proposed content. It never writes to the workspace:
// the comparison happens in a temporary directory.
//
// Paths whose original content cannot be read are reported as an error rather
// than silently omitted, so a preview always represents the full proposal.
func RenderProposalDiff(
	ctx context.Context,
	changes []workspace.Change,
	original OriginalReader,
) (string, error) {
	if len(changes) == 0 {
		return "", nil
	}

	// Stage the before/after trees in a temporary directory and let Git render
	// the unified diff. This reuses Git's exact formatting without touching the
	// workspace.
	tmp, err := os.MkdirTemp("", "forgehand-proposal-diff")
	if err != nil {
		return "", fmt.Errorf("create diff staging directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	beforeDir := filepath.Join(tmp, "before")
	afterDir := filepath.Join(tmp, "after")
	if err := os.MkdirAll(beforeDir, 0o700); err != nil {
		return "", fmt.Errorf("create before directory: %w", err)
	}
	if err := os.MkdirAll(afterDir, 0o700); err != nil {
		return "", fmt.Errorf("create after directory: %w", err)
	}

	for _, change := range changes {
		clean := filepath.FromSlash(change.Path)
		orig, existed, err := original(change.Path)
		if err != nil {
			return "", fmt.Errorf("read original %q: %w", change.Path, err)
		}

		switch strings.TrimSpace(change.Op) {
		case "write":
			if existed {
				if err := writeFile(filepath.Join(beforeDir, clean), orig); err != nil {
					return "", err
				}
			}
			if err := writeFile(filepath.Join(afterDir, clean), change.Content); err != nil {
				return "", err
			}
		case "delete":
			if !existed {
				return "", fmt.Errorf("delete %q: file does not exist in workspace", change.Path)
			}
			if err := writeFile(filepath.Join(beforeDir, clean), orig); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("unsupported operation %q for %q", change.Op, change.Path)
		}
	}

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-color", "--no-ext-diff", "--no-index", "--", beforeDir, afterDir)
	// git diff --no-index exits 1 when files differ, which is the expected
	// success case for a non-empty diff. Only a real failure (exit code > 1)
	// is an error.
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() > 1 {
			return "", fmt.Errorf("render proposal diff: %w", err)
		}
	}

	// Rewrite the staged-directory paths into Git-style a/ and b/ paths.
	rendered := rewriteDiffPaths(out.String(), beforeDir, afterDir)
	return rendered, nil
}

// rewriteDiffPaths converts the temporary staging directory prefixes into the
// conventional a/ and b/ paths of a Git diff. Git renders --no-index paths as
// a/<path> and b/<path>, stripping the leading slash of the absolute staging
// directories, so the staging prefix (without its leading slash) is removed.
func rewriteDiffPaths(diff, beforeDir, afterDir string) string {
	beforePrefix := strings.TrimPrefix(beforeDir, "/") + "/"
	afterPrefix := strings.TrimPrefix(afterDir, "/") + "/"

	lines := strings.Split(diff, "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			line = strings.ReplaceAll(line, beforePrefix, "")
			line = strings.ReplaceAll(line, afterPrefix, "")
		case strings.HasPrefix(line, "--- "):
			line = strings.Replace(line, beforePrefix, "", 1)
		case strings.HasPrefix(line, "+++ "):
			line = strings.Replace(line, afterPrefix, "", 1)
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create directory for %q: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write staging file %q: %w", path, err)
	}
	return nil
}
