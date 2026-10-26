package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/state"
)

func TestExecutableMonitorDetectsReplacementByFileIdentity(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux file identity is required")
	}
	dir := t.TempDir()
	installed := filepath.Join(dir, "forgehand")
	running := filepath.Join(dir, "running")
	if err := os.WriteFile(installed, []byte("first"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(installed, running); err != nil {
		t.Fatal(err)
	}
	monitor := newExecutableMonitor(installed, running)

	matching := monitor.status(time.Now().UTC())
	if matching.Comparison != "match" || matching.RestartRecommended {
		t.Fatalf("matching status = %#v", matching)
	}

	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("second"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, installed); err != nil {
		t.Fatal(err)
	}

	mismatch := monitor.status(time.Now().UTC())
	if mismatch.Comparison != "mismatch" || !mismatch.RestartRecommended || mismatch.RunningFileIdentity == mismatch.InstalledFileIdentity {
		t.Fatalf("replacement status = %#v", mismatch)
	}
}

func TestDaemonStatusUsesLiveWorkersAndDegradesWhenDatabaseUnavailable(t *testing.T) {
	db, err := state.OpenAt(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workers := &workerGroup{owners: map[int64]context.CancelFunc{7: func() {}}}
	dispatcher := newDispatcher(db, workers, 3, nil, nil)
	started := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	provider := newDaemonStatusProvider(db, dispatcher, workers, started)
	provider.now = func() time.Time { return started.Add(90 * time.Second) }

	status, err := provider.DaemonStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Identity.UptimeSeconds != 90 || status.Activity.ActiveWorkers == nil || *status.Activity.ActiveWorkers != 1 || status.Activity.RunningSessions == nil || *status.Activity.RunningSessions != 1 {
		t.Fatalf("unexpected live status: %#v", status)
	}
	if status.Activity.WorkerCapacity == nil || *status.Activity.WorkerCapacity != 3 || status.Health.Database != "ok" {
		t.Fatalf("unexpected capacity/health: %#v", status)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	degraded, err := provider.DaemonStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if degraded.Status != "degraded" || degraded.Health.Database != "unavailable" || degraded.Activity.QueuedExecutions != nil {
		t.Fatalf("unavailable database status = %#v", degraded)
	}
}

func TestDaemonStatusReportsUnavailableDispatcherAndWorkers(t *testing.T) {
	provider := newDaemonStatusProvider(nil, nil, nil, time.Now().UTC())
	status, err := provider.DaemonStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "degraded" || status.Health.Dispatcher != "unavailable" || status.Health.Workers != "unavailable" {
		t.Fatalf("status = %#v", status)
	}
	if status.Activity.ActiveWorkers != nil || status.Activity.WorkerCapacity != nil {
		t.Fatalf("unavailable runtime reported numeric activity: %#v", status.Activity)
	}
}
