package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
