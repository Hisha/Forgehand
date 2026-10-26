//go:build linux

package daemon

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/Hisha/Forgehand/internal/remoteapi"
)

type executableFileIdentity struct {
	device uint64
	inode  uint64
	size   int64
	mtime  int64
}

func (i executableFileIdentity) label() string { return fmt.Sprintf("%d:%d", i.device, i.inode) }

type executableMonitor struct {
	installedPath string
	runningPath   string
	baseline      executableFileIdentity
	baselineOK    bool
}

func newExecutableMonitor(installedPath, runningPath string) executableMonitor {
	identity, ok := readExecutableFileIdentity(runningPath)
	return executableMonitor{installedPath: installedPath, runningPath: runningPath, baseline: identity, baselineOK: ok}
}

func (m executableMonitor) status(now time.Time) remoteapi.ExecutableResponse {
	result := remoteapi.ExecutableResponse{Comparison: "unknown", Reason: "file_identity_unavailable", CheckedAt: now.Format(time.RFC3339)}
	running, runningOK := readExecutableFileIdentity(m.runningPath)
	installed, installedOK := readExecutableFileIdentity(m.installedPath)
	if runningOK {
		result.RunningFileIdentity = running.label()
	}
	if installedOK {
		result.InstalledFileIdentity = installed.label()
	}
	if !m.baselineOK || !runningOK || !installedOK {
		return result
	}
	if running.device != m.baseline.device || running.inode != m.baseline.inode {
		result.Reason = "running_identity_changed"
		return result
	}
	if installed.device != m.baseline.device || installed.inode != m.baseline.inode || installed.size != m.baseline.size || installed.mtime != m.baseline.mtime {
		result.Comparison = "mismatch"
		result.Reason = "installed_executable_changed"
		result.RestartRecommended = true
		return result
	}
	result.Comparison = "match"
	result.Reason = "same_file_identity"
	return result
}

func readExecutableFileIdentity(path string) (executableFileIdentity, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return executableFileIdentity{}, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return executableFileIdentity{}, false
	}
	return executableFileIdentity{device: uint64(stat.Dev), inode: stat.Ino, size: info.Size(), mtime: info.ModTime().UnixNano()}, true
}
