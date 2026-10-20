package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type stateOwnership struct {
	file *os.File
}

func acquireStateOwnership(stateDir string) (*stateOwnership, error) {
	path := filepath.Join(stateDir, "daemon.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon state lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("another Forgehand daemon owns state directory %q", stateDir)
		}
		return nil, fmt.Errorf("lock daemon state directory: %w", err)
	}
	// The lock itself is authoritative; this text is diagnostic only. Rewrite it
	// after acquiring ownership so a stale PID can never be mistaken for a lock.
	if err := file.Truncate(0); err != nil {
		file.Close()
		return nil, fmt.Errorf("initialize daemon state lock: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		file.Close()
		return nil, fmt.Errorf("initialize daemon state lock: %w", err)
	}
	executable, _ := os.Executable()
	metadata := "pid=" + strconv.Itoa(os.Getpid()) + "\nexecutable=" + strings.TrimSpace(executable) + "\n"
	if _, err := file.WriteString(metadata); err != nil {
		file.Close()
		return nil, fmt.Errorf("record daemon state owner: %w", err)
	}
	return &stateOwnership{file: file}, nil
}

func (o *stateOwnership) Close() error {
	if o == nil || o.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(o.file.Fd()), unix.LOCK_UN)
	closeErr := o.file.Close()
	o.file = nil
	if unlockErr != nil {
		return fmt.Errorf("unlock daemon state directory: %w", unlockErr)
	}
	return closeErr
}
