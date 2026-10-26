//go:build !linux

package daemon

import (
	"time"

	"github.com/Hisha/Forgehand/internal/remoteapi"
)

type executableMonitor struct{}

func newExecutableMonitor(_, _ string) executableMonitor { return executableMonitor{} }

func (executableMonitor) status(now time.Time) remoteapi.ExecutableResponse {
	return remoteapi.ExecutableResponse{
		Comparison: "unknown",
		Reason:     "file_identity_unsupported",
		CheckedAt:  now.Format(time.RFC3339),
	}
}
