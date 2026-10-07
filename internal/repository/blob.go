package repository

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func ReadBlob(
	ctx context.Context,
	rootPath string,
	blobHash string,
) ([]byte, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return nil, fmt.Errorf("repository root path must not be empty")
	}

	blobHash = strings.TrimSpace(blobHash)
	if blobHash == "" {
		return nil, fmt.Errorf("blob hash must not be empty")
	}

	cmd := exec.CommandContext(
		ctx,
		"git",
		"-C",
		rootPath,
		"cat-file",
		"blob",
		blobHash,
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read Git blob %s: %w", blobHash, err)
	}

	return output, nil
}
