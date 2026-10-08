package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func ensureRuntimeDir(socketPath string, socketMode os.FileMode) error {
	dir := filepath.Dir(socketPath)
	desiredMode := os.FileMode(0o700)
	if socketMode == 0o660 {
		desiredMode = 0o770
	}

	created := false
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, desiredMode); err != nil {
			return fmt.Errorf("create runtime directory: %w", err)
		}
		created = true
		info, err = os.Stat(dir)
	}
	if err != nil {
		return fmt.Errorf("inspect runtime directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime path %q is not a directory", dir)
	}
	if created {
		if err := os.Chmod(dir, desiredMode); err != nil {
			return fmt.Errorf("secure runtime directory: %w", err)
		}
		info, err = os.Stat(dir)
		if err != nil {
			return fmt.Errorf("inspect secured runtime directory: %w", err)
		}
	}

	permissions := info.Mode().Perm()
	if permissions&0o007 != 0 {
		return fmt.Errorf("runtime directory %q must deny world access", dir)
	}
	if socketMode == 0o660 && permissions&0o050 != 0o050 {
		return fmt.Errorf("runtime directory %q must permit group traversal for a 0660 socket", dir)
	}
	return nil
}
