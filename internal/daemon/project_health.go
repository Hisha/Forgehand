package daemon

import (
	"context"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
)

// Project readiness statuses reported by the project-health command. They
// describe repository maintenance only and never describe session activity.
const (
	// ProjectHealthCurrent means every stored observation matches the live
	// repository revision.
	ProjectHealthCurrent = "current"
	// ProjectHealthStale means at least one stored observation describes an
	// older revision than the live repository.
	ProjectHealthStale = "stale"
	// ProjectHealthUnobserved means the project has no stored inventory or
	// component analysis yet.
	ProjectHealthUnobserved = "unobserved"
	// ProjectHealthUnavailable means the live repository revision could not be
	// determined, so the stored observations cannot be compared with it.
	ProjectHealthUnavailable = "unavailable"
)

// inspectProjectHealth compares a project's stored observations with the live
// repository revision. It never builds an inventory, never runs analysis, and
// never mutates stored state: it only reads what is already persisted and
// compares it with the current revision.
//
// A repository that cannot be read is reported as "unavailable" rather than
// failing the whole request, because one broken checkout must not hide the
// readiness of every other registered project.
func inspectProjectHealth(
	ctx context.Context,
	stateDB *state.Database,
	project state.Project,
) ipc.ProjectHealth {
	health := ipc.ProjectHealth{
		ProjectID: project.ID,
		Name:      project.Name,
		RootPath:  project.RootPath,
		Status:    ProjectHealthUnobserved,
	}

	inventory, err := stateDB.LatestProjectInventory(ctx, project.ID, false)
	if err != nil {
		health.Status = ProjectHealthUnavailable
		return health
	}
	analysis, err := stateDB.LatestComponentObservation(ctx, project.ID, false)
	if err != nil {
		health.Status = ProjectHealthUnavailable
		return health
	}

	head, hasHead, err := repository.HeadCommit(ctx, project.RootPath)
	if err != nil || !hasHead {
		health.Status = ProjectHealthUnavailable
		return health
	}
	health.Head = head

	if dirty, dirtyErr := repository.IsDirty(ctx, project.RootPath); dirtyErr == nil {
		health.Dirty = dirty
	}

	observed := false
	stale := false

	if inventory != nil {
		observed = true
		health.InventoryCommit = inventory.CommitHash
		health.ObservedAt = inventory.ObservedAt.Format(time.RFC3339Nano)
		if inventory.CommitHash != head {
			stale = true
		}
	}
	if analysis != nil {
		observed = true
		health.AnalysisCommit = analysis.CommitHash
		if health.ObservedAt == "" {
			health.ObservedAt = analysis.ObservedAt.Format(time.RFC3339Nano)
		}
		if analysis.CommitHash != head {
			stale = true
		}
	}

	switch {
	case !observed:
		health.Status = ProjectHealthUnobserved
	case stale:
		health.Status = ProjectHealthStale
	default:
		health.Status = ProjectHealthCurrent
	}

	return health
}

// inspectProjectHealthList reports readiness for every registered project.
func inspectProjectHealthList(
	ctx context.Context,
	stateDB *state.Database,
) ([]ipc.ProjectHealth, error) {
	projects, err := stateDB.ListProjects(ctx)
	if err != nil {
		return nil, err
	}

	health := make([]ipc.ProjectHealth, 0, len(projects))
	for _, project := range projects {
		health = append(health, inspectProjectHealth(ctx, stateDB, project))
	}
	return health, nil
}
