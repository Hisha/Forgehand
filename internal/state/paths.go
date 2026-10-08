package state

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Hisha/Forgehand/internal/config"
)

func Dir() (string, error) {
	return config.StateDir()
}

func DatabasePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "forgehand.db"), nil
}

func EnsureDir() error {
	dir, err := Dir()
	if err != nil {
		return err
	}

	return EnsureDirAt(dir)
}

func EnsureDirAt(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) == string(filepath.Separator) {
		return fmt.Errorf("state directory must be an absolute path other than root")
	}
	if err := os.MkdirAll(filepath.Clean(dir), 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	return nil
}
