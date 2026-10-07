package repository

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type GitChange struct {
	Status     string
	Similarity int
	OldPath    string
	NewPath    string
}

func DiffTrees(
	ctx context.Context,
	rootPath string,
	oldCommit string,
	newCommit string,
) ([]GitChange, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return nil, fmt.Errorf("repository root path must not be empty")
	}

	oldCommit = strings.TrimSpace(oldCommit)
	if oldCommit == "" {
		return nil, fmt.Errorf("old commit must not be empty")
	}

	newCommit = strings.TrimSpace(newCommit)
	if newCommit == "" {
		return nil, fmt.Errorf("new commit must not be empty")
	}

	cmd := exec.CommandContext(
		ctx,
		"git",
		"-C",
		rootPath,
		"diff-tree",
		"--no-commit-id",
		"--name-status",
		"-r",
		"-M",
		"-C",
		"-z",
		oldCommit,
		newCommit,
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf(
			"diff Git trees %s..%s: %w",
			oldCommit,
			newCommit,
			err,
		)
	}

	return parseTreeDiff(output)
}

func parseTreeDiff(data []byte) ([]GitChange, error) {
	fields := strings.Split(string(data), "\x00")

	changes := make([]GitChange, 0)

	for i := 0; i < len(fields); {
		if fields[i] == "" {
			i++
			continue
		}

		rawStatus := fields[i]
		i++

		status := rawStatus[:1]

		switch status {
		case "A":
			if i >= len(fields) || fields[i] == "" {
				return nil, fmt.Errorf("added file missing path")
			}

			changes = append(changes, GitChange{
				Status:  status,
				NewPath: fields[i],
			})
			i++

		case "D":
			if i >= len(fields) || fields[i] == "" {
				return nil, fmt.Errorf("deleted file missing path")
			}

			changes = append(changes, GitChange{
				Status:  status,
				OldPath: fields[i],
			})
			i++

		case "M", "T":
			if i >= len(fields) || fields[i] == "" {
				return nil, fmt.Errorf(
					"%s file missing path",
					status,
				)
			}

			path := fields[i]
			i++

			changes = append(changes, GitChange{
				Status:  status,
				OldPath: path,
				NewPath: path,
			})

		case "R", "C":
			similarity, err := parseSimilarity(rawStatus)
			if err != nil {
				return nil, err
			}

			if i+1 >= len(fields) ||
				fields[i] == "" ||
				fields[i+1] == "" {
				return nil, fmt.Errorf(
					"%s change missing paths",
					status,
				)
			}

			changes = append(changes, GitChange{
				Status:     status,
				Similarity: similarity,
				OldPath:    fields[i],
				NewPath:    fields[i+1],
			})
			i += 2

		default:
			return nil, fmt.Errorf(
				"unsupported Git change status %q",
				rawStatus,
			)
		}
	}

	return changes, nil
}

func parseSimilarity(rawStatus string) (int, error) {
	if len(rawStatus) < 2 {
		return 0, fmt.Errorf(
			"Git change %q missing similarity",
			rawStatus,
		)
	}

	similarity, err := strconv.Atoi(rawStatus[1:])
	if err != nil {
		return 0, fmt.Errorf(
			"parse Git similarity %q: %w",
			rawStatus,
			err,
		)
	}

	if similarity < 0 || similarity > 100 {
		return 0, fmt.Errorf(
			"Git similarity out of range: %d",
			similarity,
		)
	}

	return similarity, nil
}
