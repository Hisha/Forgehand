package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const BaselineCommitMessage = "Establish Forgehand baseline"

// WorkingTreeFingerprint returns an opaque digest of the repository state that
// matters to project intake: HEAD, the index, tracked worktree changes, and
// untracked paths and content. Ignored files are intentionally excluded.
//
// This is an optimistic concurrency guard. It cannot prevent another process
// from changing the repository after the caller verifies the digest.
func WorkingTreeFingerprint(ctx context.Context, rootPath string) (string, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return "", fmt.Errorf("repository root path must not be empty")
	}

	digest := sha256.New()

	head, hasHead, err := HeadCommit(ctx, rootPath)
	if err != nil {
		return "", err
	}
	writeFingerprintField(digest, "head", head)
	if !hasHead {
		writeFingerprintField(digest, "head-state", "unborn")
	}

	index, err := gitOutputBytes(ctx, rootPath, "ls-files", "--stage", "-z")
	if err != nil {
		return "", fmt.Errorf("inspect Git index: %w", err)
	}
	writeFingerprintBytes(digest, "index", index)

	trackedDiff, err := gitOutputBytes(
		ctx,
		rootPath,
		"diff",
		"--no-ext-diff",
		"--binary",
		"--full-index",
		"--no-renames",
	)
	if err != nil {
		return "", fmt.Errorf("inspect tracked working tree content: %w", err)
	}
	writeFingerprintBytes(digest, "tracked-worktree", trackedDiff)

	untracked, err := gitOutputBytes(
		ctx,
		rootPath,
		"ls-files",
		"--others",
		"--exclude-standard",
		"-z",
	)
	if err != nil {
		return "", fmt.Errorf("list untracked Git paths: %w", err)
	}

	paths := splitNULPaths(untracked)
	sort.Strings(paths)
	for _, path := range paths {
		if err := fingerprintUntrackedPath(digest, rootPath, path); err != nil {
			return "", fmt.Errorf("fingerprint untracked path %q: %w", path, err)
		}
	}

	return hex.EncodeToString(digest.Sum(nil)), nil
}

func HeadCommit(ctx context.Context, rootPath string) (string, bool, error) {
	cmd := exec.CommandContext(
		ctx,
		"git",
		"-C",
		rootPath,
		"rev-parse",
		"--verify",
		"--quiet",
		"HEAD",
	)

	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect Git HEAD: %w", err)
	}

	return strings.TrimSpace(string(output)), true, nil
}

func CommitBaseline(ctx context.Context, rootPath string) error {
	// Validate Git's existing identity configuration before staging anything.
	// Forgehand never supplies or changes author/committer identity.
	if _, err := gitOutputBytes(ctx, rootPath, "var", "GIT_AUTHOR_IDENT"); err != nil {
		return fmt.Errorf("read Git author identity: %w", err)
	}
	if _, err := gitOutputBytes(ctx, rootPath, "var", "GIT_COMMITTER_IDENT"); err != nil {
		return fmt.Errorf("read Git committer identity: %w", err)
	}

	if err := runIntakeGit(ctx, rootPath, "add", "--all"); err != nil {
		return fmt.Errorf("stage baseline content: %w", err)
	}

	if err := runIntakeGit(
		ctx,
		rootPath,
		"commit",
		"-m",
		BaselineCommitMessage,
	); err != nil {
		return fmt.Errorf("commit baseline content: %w", err)
	}

	return nil
}

func DiscardWorkingTree(ctx context.Context, rootPath string) error {
	if _, hasHead, err := HeadCommit(ctx, rootPath); err != nil {
		return err
	} else if !hasHead {
		return fmt.Errorf("cannot discard changes because the repository has no HEAD commit")
	}

	if err := runIntakeGit(ctx, rootPath, "reset", "--hard", "HEAD"); err != nil {
		return fmt.Errorf("restore tracked files and index: %w", err)
	}

	// git clean excludes ignored files unless -x/-X is supplied. A single -f
	// deliberately preserves untracked nested Git repositories.
	if err := runIntakeGit(ctx, rootPath, "clean", "-fd"); err != nil {
		return fmt.Errorf("remove untracked files and directories: %w", err)
	}

	return nil
}

func gitOutputBytes(
	ctx context.Context,
	rootPath string,
	args ...string,
) ([]byte, error) {
	commandArgs := append([]string{"-C", rootPath}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message := strings.TrimSpace(string(exitErr.Stderr))
			if message != "" {
				return nil, fmt.Errorf("%w: %s", err, message)
			}
		}
		return nil, err
	}
	return output, nil
}

func runIntakeGit(ctx context.Context, rootPath string, args ...string) error {
	commandArgs := append([]string{"-C", rootPath}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return fmt.Errorf("git %s: %w: %s", args[0], err, message)
		}
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	return nil
}

func splitNULPaths(data []byte) []string {
	records := strings.Split(string(data), "\x00")
	paths := make([]string, 0, len(records))
	for _, record := range records {
		if record != "" {
			paths = append(paths, record)
		}
	}
	return paths
}

func fingerprintUntrackedPath(digest hash.Hash, rootPath, gitPath string) error {
	path := filepath.Join(rootPath, filepath.FromSlash(gitPath))
	relative, err := filepath.Rel(rootPath, path)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes repository")
	}

	info, err := os.Lstat(path)
	if err != nil {
		return err
	}

	writeFingerprintField(digest, "untracked-path", gitPath)
	writeFingerprintField(digest, "untracked-mode", info.Mode().String())

	switch {
	case info.Mode().IsRegular():
		return fingerprintRegularFile(digest, path)
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		writeFingerprintField(digest, "symlink-target", target)
		return nil
	default:
		// Do not open devices, sockets, or FIFOs. Their type and path still
		// participate in the state token.
		return nil
	}
}

func fingerprintRegularFile(digest hash.Hash, path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("open file")
	}
	defer file.Close()

	fileDigest := sha256.New()
	if _, err := io.Copy(fileDigest, file); err != nil {
		return err
	}
	writeFingerprintBytes(digest, "untracked-content", fileDigest.Sum(nil))
	return nil
}

func writeFingerprintField(digest hash.Hash, name, value string) {
	writeFingerprintBytes(digest, name, []byte(value))
}

func writeFingerprintBytes(digest hash.Hash, name string, value []byte) {
	_, _ = io.WriteString(digest, fmt.Sprintf("%d:%s:%d:", len(name), name, len(value)))
	_, _ = digest.Write(value)
}
