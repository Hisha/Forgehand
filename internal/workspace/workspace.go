// Package workspace manages an isolated Git worktree and the sandboxed file
// operations a controlled coding task may perform inside it.
//
// The worktree is a detached checkout of the project's base commit created from
// the primary repository. It is the only place a task may read or write: every
// path is confined to the worktree root and reserved paths (the Git metadata
// directory) are rejected. The primary working tree is never touched.
package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ErrPathEscape indicates a requested path would resolve outside the worktree.
var ErrPathEscape = errors.New("path escapes the workspace")

// ErrReservedPath indicates a requested path names reserved Git metadata.
var ErrReservedPath = errors.New("path names reserved workspace metadata")

// ErrEmptyPath indicates a requested path was empty.
var ErrEmptyPath = errors.New("path must not be empty")

// Change is a single file mutation in a proposal or an applied change.
type Change struct {
	// Op is "write" (create or overwrite) or "delete".
	Op string
	// Path is a Git-style path relative to the workspace root.
	Path string
	// Content is the full new file content for a "write".
	Content string
}

// Workspace is an opened, sandboxed worktree.
type Workspace struct {
	root         string
	resolvedRoot string
}

// Exists reports whether a path already exists.
func Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// Ensure creates a detached worktree at worktreePath checked out at baseCommit,
// or reuses an existing clean worktree already at that commit. It never removes
// an existing directory: a pre-existing path that is not a clean worktree at
// baseCommit is refused so applied or partially applied work is never silently
// destroyed.
func Ensure(ctx context.Context, repoRoot, baseCommit, worktreePath string) error {
	repoRoot = strings.TrimSpace(repoRoot)
	baseCommit = strings.TrimSpace(baseCommit)
	worktreePath = strings.TrimSpace(worktreePath)

	if repoRoot == "" {
		return fmt.Errorf("repository root must not be empty")
	}
	if baseCommit == "" {
		return fmt.Errorf("base commit must not be empty")
	}
	if worktreePath == "" {
		return fmt.Errorf("worktree path must not be empty")
	}

	if Exists(worktreePath) {
		ok, err := isCleanWorktreeAt(ctx, worktreePath, baseCommit)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf(
				"workspace %q already exists but is not a clean worktree at %s; "+
					"remove it with task cleanup before retrying",
				worktreePath,
				baseCommit,
			)
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o700); err != nil {
		return fmt.Errorf("create workspace parent directory: %w", err)
	}

	if err := runGit(ctx, repoRoot, "worktree", "add", "--detach", worktreePath, baseCommit); err != nil {
		return fmt.Errorf("create worktree at %s: %w", baseCommit, err)
	}

	return nil
}

// Remove deletes a worktree from its primary repository. It is only called on
// explicit user request; applied work is otherwise retained for inspection.
func Remove(ctx context.Context, repoRoot, worktreePath string) error {
	repoRoot = strings.TrimSpace(repoRoot)
	worktreePath = strings.TrimSpace(worktreePath)
	if repoRoot == "" || worktreePath == "" {
		return fmt.Errorf("repository root and worktree path are required")
	}

	if err := runGit(ctx, repoRoot, "worktree", "remove", "--force", worktreePath); err != nil {
		// The worktree may already be gone or unregistered; prune stale
		// administrative entries before reporting failure.
		_ = runGit(ctx, repoRoot, "worktree", "prune")
		if Exists(worktreePath) {
			return fmt.Errorf("remove worktree %q: %w", worktreePath, err)
		}
	}
	return nil
}

func isCleanWorktreeAt(ctx context.Context, path, baseCommit string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("inspect workspace %q: %w", path, err)
	}
	if !info.IsDir() {
		return false, nil
	}

	gitFile := filepath.Join(path, ".git")
	gitInfo, err := os.Stat(gitFile)
	if err != nil || gitInfo.IsDir() {
		return false, nil
	}

	head, err := gitOutput(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return false, nil
	}
	if strings.TrimSpace(head) != baseCommit {
		return false, nil
	}

	status, err := gitOutput(ctx, path, "status", "--porcelain")
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(status) == "", nil
}

// Open opens an existing worktree directory and resolves its real root for
// containment checks.
func Open(path string) (*Workspace, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("workspace path must not be empty")
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace path: %w", err)
	}
	abs = filepath.Clean(abs)

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root symlinks: %w", err)
	}

	return &Workspace{root: abs, resolvedRoot: resolved}, nil
}

// Root returns the workspace root path.
func (w *Workspace) Root() string {
	return w.root
}

// Stat returns file information for a workspace-relative path after validating
// that it is contained in the workspace and not reserved metadata. It refuses
// paths that would resolve outside the worktree, so callers can safely gate
// operations (such as formatting) on real, contained files.
func (w *Workspace) Stat(rel string) (os.FileInfo, error) {
	target, err := w.resolve(rel)
	if err != nil {
		return nil, err
	}
	if err := w.ensureResolvedWithin(target); err != nil {
		return nil, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", rel, err)
	}
	return info, nil
}

func (w *Workspace) resolve(rel string) (string, error) {
	return w.resolveInternal(rel, false)
}

func (w *Workspace) resolveInternal(rel string, allowRoot bool) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		if allowRoot {
			return w.root, nil
		}
		return "", ErrEmptyPath
	}
	if strings.IndexByte(rel, 0) >= 0 {
		return "", fmt.Errorf("path contains a NUL byte")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: absolute path %q", ErrPathEscape, rel)
	}

	cleaned := filepath.Clean(filepath.FromSlash(rel))

	if cleaned == "." {
		if allowRoot {
			return w.root, nil
		}
		return "", ErrEmptyPath
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, rel)
	}
	if cleaned == ".git" || strings.HasPrefix(cleaned, ".git"+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrReservedPath, rel)
	}

	joined := filepath.Join(w.root, cleaned)
	if joined != w.root && !strings.HasPrefix(joined, w.root+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, rel)
	}
	return joined, nil
}

// ensureResolvedWithin verifies that target, after resolving symlinks, stays
// inside the workspace root. For a not-yet-existing target the nearest existing
// ancestor is resolved instead.
func (w *Workspace) ensureResolvedWithin(target string) error {
	probe := target
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			if resolved == w.resolvedRoot ||
				strings.HasPrefix(resolved, w.resolvedRoot+string(filepath.Separator)) {
				return nil
			}
			return fmt.Errorf("%w: %q", ErrPathEscape, target)
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("resolve path %q: %w", target, err)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return fmt.Errorf("%w: %q", ErrPathEscape, target)
		}
		probe = parent
	}
}

// Read returns the content of a file, up to maxBytes. It refuses non-regular
// files and paths that escape the workspace.
func (w *Workspace) Read(rel string, maxBytes int64) ([]byte, error) {
	target, err := w.resolve(rel)
	if err != nil {
		return nil, err
	}
	if err := w.ensureResolvedWithin(target); err != nil {
		return nil, err
	}

	info, err := os.Lstat(target)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("read %q: not a regular file", rel)
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return nil, fmt.Errorf(
			"read %q: file is %d bytes, over the %d byte limit",
			rel,
			info.Size(),
			maxBytes,
		)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", rel, err)
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		data = data[:maxBytes]
	}
	return data, nil
}

// List returns the entries of a directory, sorted, excluding the Git metadata
// directory, bounded by maxEntries.
func (w *Workspace) List(rel string, maxEntries int) ([]string, error) {
	target, err := w.resolveInternal(rel, true)
	if err != nil {
		return nil, err
	}
	if err := w.ensureResolvedWithin(target); err != nil {
		return nil, err
	}

	info, err := os.Lstat(target)
	if err != nil {
		return nil, fmt.Errorf("list %q: %w", rel, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("list %q: not a directory", rel)
	}

	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, fmt.Errorf("list %q: %w", rel, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)

	if maxEntries > 0 && len(names) > maxEntries {
		names = names[:maxEntries]
	}
	return names, nil
}

// SearchResult is one literal-match hit.
type SearchResult struct {
	Path string
	Line int
	Text string
}

// Search scans tracked files for a literal needle, returning up to maxResults
// hits. Files larger than maxFileBytes are skipped.
func (w *Workspace) Search(
	ctx context.Context,
	needle string,
	maxResults int,
	maxFileBytes int64,
) ([]SearchResult, error) {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return nil, fmt.Errorf("search pattern must not be empty")
	}

	files, err := w.TrackedFiles(ctx)
	if err != nil {
		return nil, err
	}

	var results []SearchResult

	for _, path := range files {
		if maxResults > 0 && len(results) >= maxResults {
			break
		}

		target, err := w.resolve(path)
		if err != nil {
			continue
		}
		if err := w.ensureResolvedWithin(target); err != nil {
			continue
		}

		info, err := os.Lstat(target)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if maxFileBytes > 0 && info.Size() > maxFileBytes {
			continue
		}

		data, err := os.ReadFile(target)
		if err != nil {
			continue
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}

		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if maxResults > 0 && len(results) >= maxResults {
				break
			}
			if strings.Contains(line, needle) {
				text := strings.TrimSpace(line)
				if len(text) > 200 {
					text = text[:200]
				}
				results = append(results, SearchResult{
					Path: path,
					Line: i + 1,
					Text: text,
				})
			}
		}
	}

	return results, nil
}

// TrackedFiles lists the tracked files in the worktree, bounded by maxEntries
// (0 means unbounded). It is used to seed the task prompt.
func (w *Workspace) TrackedFiles(ctx context.Context) ([]string, error) {
	out, err := gitOutputBytes(ctx, w.root, "ls-files", "-z")
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}

	records := strings.Split(string(out), "\x00")
	files := make([]string, 0, len(records))
	for _, record := range records {
		if record != "" {
			files = append(files, record)
		}
	}
	sort.Strings(files)
	return files, nil
}

// Apply writes the given changes into the workspace. Every path is validated
// for containment and reserved names before any mutation, so an invalid change
// aborts the whole apply.
func (w *Workspace) Apply(changes []Change) error {
	targets := make([]string, len(changes))
	for i, change := range changes {
		op := strings.TrimSpace(change.Op)
		if op != "write" && op != "delete" {
			return fmt.Errorf("unsupported change operation %q", change.Op)
		}

		target, err := w.resolve(change.Path)
		if err != nil {
			return err
		}
		if err := w.ensureResolvedWithin(filepath.Dir(target)); err != nil {
			return err
		}
		if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %q is a symlink", ErrReservedPath, change.Path)
		}
		targets[i] = target
	}

	for i, change := range changes {
		target := targets[i]
		switch strings.TrimSpace(change.Op) {
		case "write":
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create directory for %q: %w", change.Path, err)
			}
			if err := os.WriteFile(target, []byte(change.Content), 0o644); err != nil {
				return fmt.Errorf("write %q: %w", change.Path, err)
			}
		case "delete":
			if _, err := os.Lstat(target); err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("delete %q: file does not exist", change.Path)
				}
				return fmt.Errorf("delete %q: %w", change.Path, err)
			}
			if err := os.Remove(target); err != nil {
				return fmt.Errorf("delete %q: %w", change.Path, err)
			}
		default:
			return fmt.Errorf("unsupported operation %q for %q", change.Op, change.Path)
		}
	}

	return nil
}

// Diff returns the working-tree diff of the workspace against its base commit,
// including untracked new files. It stages intent-to-add for new files so they
// appear in the diff.
func (w *Workspace) Diff(ctx context.Context) (string, error) {
	if err := runGit(ctx, w.root, "add", "--intent-to-add", "--all"); err != nil {
		return "", fmt.Errorf("prepare workspace diff: %w", err)
	}

	out, err := gitOutputBytes(ctx, w.root, "diff", "--no-color", "--no-ext-diff", "HEAD")
	if err != nil {
		return "", fmt.Errorf("render workspace diff: %w", err)
	}
	return string(out), nil
}

// IsClean reports whether the workspace has no changes relative to its base
// commit.
func (w *Workspace) IsClean(ctx context.Context) (bool, error) {
	status, err := gitOutput(ctx, w.root, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("inspect workspace status: %w", err)
	}
	return strings.TrimSpace(status) == "", nil
}

func runGit(ctx context.Context, root string, args ...string) error {
	commandArgs := append([]string{"-C", root}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return fmt.Errorf("git %s: %w: %s", args[0], err, message)
		}
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	return nil
}

func gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	data, err := gitOutputBytes(ctx, root, args...)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func gitOutputBytes(ctx context.Context, root string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", root}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	out, readErr := io.ReadAll(stdout)
	waitErr := cmd.Wait()
	if waitErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], waitErr, message)
		}
		return nil, fmt.Errorf("git %s: %w", args[0], waitErr)
	}
	if readErr != nil {
		return nil, readErr
	}
	return out, nil
}
