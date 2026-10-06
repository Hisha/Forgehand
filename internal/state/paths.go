package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func Dir() (string, error) {
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "forgehand"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	if home == "" {
		return "", errors.New("home directory is empty")
	}

	return filepath.Join(home, ".local", "state", "forgehand"), nil
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

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	return nil
}
