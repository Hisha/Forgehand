package repository

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type GitFile struct {
	Path     string
	BlobHash string
	Mode     string
	Size     int64
}

func ListTree(
	ctx context.Context,
	rootPath string,
	commit string,
) ([]GitFile, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return nil, fmt.Errorf("repository root path must not be empty")
	}

	commit = strings.TrimSpace(commit)
	if commit == "" {
		return nil, fmt.Errorf("commit must not be empty")
	}

	cmd := exec.CommandContext(
		ctx,
		"git",
		"-C",
		rootPath,
		"ls-tree",
		"-r",
		"-l",
		"-z",
		commit,
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list Git tree at %s: %w", commit, err)
	}

	return parseTree(output)
}

func parseTree(data []byte) ([]GitFile, error) {
	records := strings.Split(string(data), "\x00")
	files := make([]GitFile, 0, len(records))

	for _, record := range records {
		if record == "" {
			continue
		}

		header, path, found := strings.Cut(record, "\t")
		if !found {
			return nil, fmt.Errorf("invalid Git tree record: missing path")
		}

		fields := strings.Fields(header)
		if len(fields) != 4 {
			return nil, fmt.Errorf(
				"invalid Git tree record header %q",
				header,
			)
		}

		mode := fields[0]
		objectType := fields[1]
		blobHash := fields[2]
		sizeText := fields[3]

		if objectType != "blob" {
			continue
		}

		size, err := strconv.ParseInt(sizeText, 10, 64)
		if err != nil {
			return nil, fmt.Errorf(
				"parse Git blob size %q for %q: %w",
				sizeText,
				path,
				err,
			)
		}

		files = append(files, GitFile{
			Path:     path,
			BlobHash: blobHash,
			Mode:     mode,
			Size:     size,
		})
	}

	return files, nil
}
