package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// validateAccountName validates a Linux account/service-account name. Account
// names are used in systemd unit instance names and in command arguments, so
// they must be restricted to a conservative character set.
func validateAccountName(name string) error {
	if name == "" {
		return errors.New("account name is required")
	}
	if strings.IndexByte(name, 0) >= 0 {
		return errors.New("account name contains a NUL byte")
	}
	if len(name) > 32 {
		return errors.New("account name is too long (max 32 characters)")
	}
	if !isValidUnixUsername(name) {
		return fmt.Errorf("invalid account name %q", name)
	}
	return nil
}

func isValidUnixUsername(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '_' {
				return false
			}
			continue
		}
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// validateAbsolutePath rejects relative paths and paths containing NUL bytes.
func validateAbsolutePath(name, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("%s contains a NUL byte", name)
	}
	clean := filepath.Clean(value)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("%s must be an absolute path", name)
	}
	return clean, nil
}
