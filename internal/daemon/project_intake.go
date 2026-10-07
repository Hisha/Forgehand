package daemon

import (
	"context"
	"crypto/subtle"
	"fmt"

	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
)

type projectIntake struct {
	Repository  repository.Repository
	Changes     []repository.WorkingTreeChange
	Fingerprint string
}

const (
	projectIntakeCommit  = "commit"
	projectIntakeDiscard = "discard"
	projectIntakeCancel  = "cancel"
)

func inspectProjectIntake(
	ctx context.Context,
	path string,
) (projectIntake, error) {
	discovered, err := repository.Discover(ctx, path)
	if err != nil {
		return projectIntake{}, fmt.Errorf(
			"discover project: %w",
			err,
		)
	}

	var changes []repository.WorkingTreeChange
	var fingerprint string
	for attempt := 0; attempt < 3; attempt++ {
		before, err := repository.WorkingTreeFingerprint(ctx, discovered.RootPath)
		if err != nil {
			return projectIntake{}, fmt.Errorf("fingerprint project working tree: %w", err)
		}

		changes, err = repository.WorkingTreeDelta(ctx, discovered.RootPath)
		if err != nil {
			return projectIntake{}, fmt.Errorf("inspect project working tree: %w", err)
		}

		after, err := repository.WorkingTreeFingerprint(ctx, discovered.RootPath)
		if err != nil {
			return projectIntake{}, fmt.Errorf("fingerprint project working tree: %w", err)
		}
		if before == after {
			fingerprint = after
			break
		}
	}
	if fingerprint == "" {
		return projectIntake{}, fmt.Errorf("project changed repeatedly while it was being inspected")
	}

	return projectIntake{
		Repository:  discovered,
		Changes:     changes,
		Fingerprint: fingerprint,
	}, nil
}

func resolveProjectIntake(
	ctx context.Context,
	stateDB *state.Database,
	path string,
	action string,
	expectedFingerprint string,
) (state.Project, error) {
	if action == projectIntakeCancel {
		return state.Project{}, nil
	}
	if action != projectIntakeCommit && action != projectIntakeDiscard {
		return state.Project{}, fmt.Errorf("unknown project intake action %q", action)
	}
	if expectedFingerprint == "" {
		return state.Project{}, fmt.Errorf("expected project state fingerprint must not be empty")
	}

	discovered, err := repository.Discover(ctx, path)
	if err != nil {
		return state.Project{}, fmt.Errorf("discover project: %w", err)
	}
	actualFingerprint, err := repository.WorkingTreeFingerprint(ctx, discovered.RootPath)
	if err != nil {
		return state.Project{}, fmt.Errorf("fingerprint project working tree: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(actualFingerprint), []byte(expectedFingerprint)) != 1 {
		return state.Project{}, fmt.Errorf("project Git state changed after inspection; inspect it again before choosing an action")
	}

	switch action {
	case projectIntakeCommit:
		if err := repository.CommitBaseline(ctx, discovered.RootPath); err != nil {
			return state.Project{}, err
		}
	case projectIntakeDiscard:
		if err := repository.DiscardWorkingTree(ctx, discovered.RootPath); err != nil {
			return state.Project{}, err
		}
	}

	changes, err := repository.WorkingTreeDelta(ctx, discovered.RootPath)
	if err != nil {
		return state.Project{}, fmt.Errorf("verify project working tree: %w", err)
	}
	if len(changes) != 0 {
		return state.Project{}, fmt.Errorf("project working tree is not clean after %s", action)
	}

	return admitProject(ctx, stateDB, projectIntake{Repository: discovered})
}

func admitProject(
	ctx context.Context,
	stateDB *state.Database,
	intake projectIntake,
) (state.Project, error) {
	if len(intake.Changes) != 0 {
		return state.Project{}, fmt.Errorf(
			"cannot admit project with uncommitted changes",
		)
	}

	project, err := stateDB.CreateProject(
		ctx,
		intake.Repository.Name,
		intake.Repository.RootPath,
	)
	if err != nil {
		return state.Project{}, fmt.Errorf(
			"create project: %w",
			err,
		)
	}

	return project, nil
}
