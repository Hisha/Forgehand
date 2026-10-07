package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
)

func addProject(
	ctx context.Context,
	stateDB *state.Database,
	path string,
) (state.Project, error) {
	discovered, err := repository.Discover(ctx, path)
	if err != nil {
		return state.Project{}, fmt.Errorf(
			"discover project: %w",
			err,
		)
	}

	changes, err := repository.WorkingTreeDelta(
		ctx,
		discovered.RootPath,
	)
	if err != nil {
		return state.Project{}, fmt.Errorf(
			"inspect project working tree: %w",
			err,
		)
	}

	if len(changes) != 0 {
		return state.Project{}, dirtyProjectError(changes)
	}

	project, err := stateDB.CreateProject(
		ctx,
		discovered.Name,
		discovered.RootPath,
	)
	if err != nil {
		return state.Project{}, fmt.Errorf(
			"create project: %w",
			err,
		)
	}

	return project, nil
}

func dirtyProjectError(
	changes []repository.WorkingTreeChange,
) error {
	var message strings.Builder

	fmt.Fprintf(
		&message,
		"project has %d uncommitted change",
		len(changes),
	)

	if len(changes) != 1 {
		message.WriteString("s")
	}

	message.WriteString("; establish a clean Git baseline before adding it")

	for _, change := range changes {
		fmt.Fprintf(
			&message,
			"\n  %s",
			change.Path,
		)
	}

	return fmt.Errorf("%s", message.String())
}
