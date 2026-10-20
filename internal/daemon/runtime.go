package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
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

// prepareSocketPath removes only a demonstrably stale Unix socket owned by the
// current user. It never follows symlinks or removes an object after ambiguous
// connection failures such as timeout or permission denial.
func prepareSocketPath(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect socket path: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("socket path %q exists and is not a Unix socket; refusing to remove it", socketPath)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("socket path %q is not owned by the daemon user; refusing to remove it", socketPath)
	}

	conn, dialErr := net.DialTimeout("unix", socketPath, 250*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		return errors.New("Forgehand daemon is already running")
	}
	var opErr *net.OpError
	if !errors.As(dialErr, &opErr) || !errors.Is(opErr.Err, syscall.ECONNREFUSED) {
		return fmt.Errorf("socket path %q may still be active (%v); refusing to remove it", socketPath, dialErr)
	}
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}
