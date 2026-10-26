package daemon

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/Hisha/Forgehand/internal/remoteapi"
	"github.com/Hisha/Forgehand/internal/state"
	"github.com/Hisha/Forgehand/internal/version"
)

type daemonStatusProvider struct {
	db         *state.Database
	dispatcher *dispatcher
	workers    *workerGroup
	startedAt  time.Time
	now        func() time.Time
	executable executableMonitor
}

func newDaemonStatusProvider(db *state.Database, dispatcher *dispatcher, workers *workerGroup, startedAt time.Time) *daemonStatusProvider {
	executable, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return &daemonStatusProvider{
		db: db, dispatcher: dispatcher, workers: workers, startedAt: startedAt.UTC(),
		now: time.Now, executable: newExecutableMonitor(executable, "/proc/self/exe"),
	}
}

func (p *daemonStatusProvider) DaemonStatus(ctx context.Context) (remoteapi.DaemonStatusResponse, error) {
	now := p.now().UTC()
	response := remoteapi.DaemonStatusResponse{
		Status: "ok", ObservedAt: now.Format(time.RFC3339),
		Identity: remoteapi.DaemonIdentityResponse{
			Version: version.Version, BuildIdentity: version.BuildIdentity(), BuildTimestamp: version.BuildDate,
			APIVersion: remoteapi.APIVersion, StartedAt: p.startedAt.Format(time.RFC3339),
			UptimeSeconds: max(0, int64(now.Sub(p.startedAt).Seconds())),
		},
		Executable: p.executable.status(now),
		Health:     remoteapi.DaemonHealthResponse{Database: "unavailable", Dispatcher: "unavailable", Workers: "unavailable"},
	}

	if p.dispatcher != nil {
		capacity := p.dispatcher.capacity()
		response.Activity.WorkerCapacity = &capacity
		response.Health.Dispatcher = "ok"
	}
	if p.workers != nil {
		active := p.workers.ownerCount()
		response.Activity.ActiveWorkers = &active
		response.Activity.RunningSessions = &active
		response.Health.Workers = "ok"
	}
	if p.db != nil {
		queued, counts, err := p.db.ReadOperationalCounts(ctx)
		if err == nil {
			response.Activity.QueuedExecutions = intPointer(queued)
			response.Activity.PendingSessions = intPointer(counts.Pending)
			response.Activity.CompletedSessions = intPointer(counts.Completed)
			response.Activity.FailedSessions = intPointer(counts.Failed)
			response.Health.Database = "ok"
		}
	}
	if response.Health.Database != "ok" || response.Health.Dispatcher != "ok" || response.Health.Workers != "ok" || response.Executable.Comparison == "unknown" {
		response.Status = "degraded"
	}
	return response, nil
}

func intPointer(value int) *int { return &value }
