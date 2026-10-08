package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	StateDirEnvironment   = "FORGEHAND_STATE_DIR"
	SocketPathEnvironment = "FORGEHAND_SOCKET_PATH"
	SocketModeEnvironment = "FORGEHAND_SOCKET_MODE"
	defaultSocketMode     = 0o600
)

func StateDir() (string, error) {
	if configured := strings.TrimSpace(os.Getenv(StateDirEnvironment)); configured != "" {
		return validateDirectoryPath("state directory", configured)
	}

	if stateHome := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); stateHome != "" {
		return validateDirectoryPath("XDG state directory", filepath.Join(stateHome, "forgehand"))
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	if home == "" {
		return "", errors.New("home directory is empty")
	}
	return validateDirectoryPath("state directory", filepath.Join(home, ".local", "state", "forgehand"))
}

func SocketPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv(SocketPathEnvironment)); configured != "" {
		return validateSocketPath(configured)
	}

	runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	if runtimeDir == "" {
		return "", errors.New("XDG_RUNTIME_DIR is not set and FORGEHAND_SOCKET_PATH is not configured")
	}
	return validateSocketPath(filepath.Join(runtimeDir, "forgehand", "forgehand.sock"))
}

func SocketMode() (fs.FileMode, error) {
	configured := strings.TrimSpace(os.Getenv(SocketModeEnvironment))
	if configured == "" {
		return defaultSocketMode, nil
	}

	value, err := strconv.ParseUint(configured, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("parse %s as an octal mode: %w", SocketModeEnvironment, err)
	}
	mode := fs.FileMode(value)
	if mode != 0o600 && mode != 0o660 {
		return 0, fmt.Errorf("%s must be 0600 or 0660", SocketModeEnvironment)
	}
	return mode, nil
}

func validateDirectoryPath(name, value string) (string, error) {
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("%s contains a NUL byte", name)
	}
	clean := filepath.Clean(value)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("%s must be absolute", name)
	}
	if clean == string(filepath.Separator) {
		return "", fmt.Errorf("%s must not be the filesystem root", name)
	}
	return clean, nil
}

func validateSocketPath(value string) (string, error) {
	if strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("socket path contains a NUL byte")
	}
	clean := filepath.Clean(value)
	if !filepath.IsAbs(clean) {
		return "", errors.New("socket path must be absolute")
	}
	if filepath.Dir(clean) == string(filepath.Separator) {
		return "", errors.New("socket must be inside a dedicated runtime directory")
	}
	if len([]byte(clean)) >= 108 {
		return "", errors.New("socket path is too long for a Linux Unix-domain socket")
	}
	if filepath.Base(clean) == "." || filepath.Base(clean) == string(filepath.Separator) {
		return "", errors.New("socket path must name a file")
	}
	return clean, nil
}
