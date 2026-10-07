package repository

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type Snapshot struct {
	HeadCommit     string
	Branch         string
	Detached       bool
	Dirty          bool
	TrackedFiles   int
	UntrackedFiles int
}

func Observe(ctx context.Context, rootPath string) (Snapshot, error) {
	headCommit, err := gitOutput(
		ctx,
		rootPath,
		"rev-parse",
		"--verify",
		"HEAD",
	)
	if err != nil {
		headCommit = ""
	}

	branch, err := gitOutput(
		ctx,
		rootPath,
		"symbolic-ref",
		"--quiet",
		"--short",
		"HEAD",
	)

	detached := false
	if err != nil {
		if headCommit != "" {
			detached = true
		}
		branch = ""
	}

	trackedOutput, err := gitOutput(
		ctx,
		rootPath,
		"ls-files",
	)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list tracked files: %w", err)
	}

	trackedFiles := countNonEmptyLines(trackedOutput)

	untrackedOutput, err := gitOutput(
		ctx,
		rootPath,
		"ls-files",
		"--others",
		"--exclude-standard",
	)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list untracked files: %w", err)
	}

	untrackedFiles := countNonEmptyLines(untrackedOutput)

	statusOutput, err := gitOutput(
		ctx,
		rootPath,
		"status",
		"--porcelain",
		"--untracked-files=normal",
	)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read repository status: %w", err)
	}

	return Snapshot{
		HeadCommit:     headCommit,
		Branch:         branch,
		Detached:       detached,
		Dirty:          strings.TrimSpace(statusOutput) != "",
		TrackedFiles:   trackedFiles,
		UntrackedFiles: untrackedFiles,
	}, nil
}

func gitOutput(
	ctx context.Context,
	rootPath string,
	args ...string,
) (string, error) {
	commandArgs := append([]string{"-C", rootPath}, args...)

	command := exec.CommandContext(
		ctx,
		"git",
		commandArgs...,
	)

	output, err := command.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			message := strings.TrimSpace(string(exitErr.Stderr))
			if message != "" {
				return "", fmt.Errorf(
					"git %s: %s",
					strings.Join(args, " "),
					message,
				)
			}
		}

		return "", fmt.Errorf(
			"git %s: %w",
			strings.Join(args, " "),
			err,
		)
	}

	return strings.TrimSpace(string(output)), nil
}

func countNonEmptyLines(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}

	count := 0

	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}

	return count
}

func boolToInt(value bool) int {
	if value {
		return 1
	}

	return 0
}
