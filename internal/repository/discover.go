package repository

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type Repository struct {
	Name     string
	RootPath string
}

func Discover(ctx context.Context, path string) (Repository, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Repository{}, fmt.Errorf("repository path must not be empty")
	}

	command := exec.CommandContext(
		ctx,
		"git",
		"-C",
		path,
		"rev-parse",
		"--show-toplevel",
	)

	output, err := command.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			message := strings.TrimSpace(string(exitErr.Stderr))
			if message != "" {
				return Repository{}, fmt.Errorf(
					"discover Git repository from %q: %s",
					path,
					message,
				)
			}
		}

		return Repository{}, fmt.Errorf(
			"discover Git repository from %q: %w",
			path,
			err,
		)
	}

	rootPath := strings.TrimSpace(string(output))
	if rootPath == "" {
		return Repository{}, fmt.Errorf(
			"git returned an empty repository root for %q",
			path,
		)
	}

	rootPath, err = filepath.Abs(rootPath)
	if err != nil {
		return Repository{}, fmt.Errorf(
			"resolve repository root %q: %w",
			rootPath,
			err,
		)
	}

	rootPath = filepath.Clean(rootPath)

	name := filepath.Base(rootPath)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return Repository{}, fmt.Errorf(
			"cannot derive repository name from %q",
			rootPath,
		)
	}

	return Repository{
		Name:     name,
		RootPath: rootPath,
	}, nil
}
