package daemon

import (
	"context"
	"fmt"

	"github.com/Hisha/Forgehand/internal/discovery"
	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
)

type projectDiscovery struct {
	Project     state.Project
	Observation state.DiscoveryObservation
}

func discoverRegisteredProject(
	ctx context.Context,
	stateDB *state.Database,
	projectID int64,
) (projectDiscovery, error) {
	project, err := stateDB.GetProject(ctx, projectID)
	if err != nil {
		return projectDiscovery{}, fmt.Errorf("get registered project: %w", err)
	}

	commitHash, hasHead, err := repository.HeadCommit(ctx, project.RootPath)
	if err != nil {
		return projectDiscovery{}, fmt.Errorf("resolve project HEAD: %w", err)
	}
	if !hasHead {
		return projectDiscovery{}, fmt.Errorf("cannot discover project %d: repository has unborn HEAD", project.ID)
	}

	summary, err := discovery.Discover(ctx, project.RootPath, commitHash)
	if err != nil {
		return projectDiscovery{}, fmt.Errorf("discover project at %s: %w", commitHash, err)
	}
	observation, err := stateDB.CreateDiscoveryObservation(ctx, project.ID, summary)
	if err != nil {
		return projectDiscovery{}, fmt.Errorf("persist project discovery: %w", err)
	}

	return projectDiscovery{
		Project:     project,
		Observation: observation,
	}, nil
}
