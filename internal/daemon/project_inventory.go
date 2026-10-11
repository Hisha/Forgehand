package daemon

import (
	"context"
	"fmt"

	"github.com/Hisha/Forgehand/internal/inventory"
	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
)

// Inventory currency relative to the live repository HEAD.
const (
	InspectionStatusCurrent     = "current"
	InspectionStatusStale       = "stale"
	InspectionStatusUnavailable = "unavailable"
)

type projectInventory struct {
	Project   state.Project
	Inventory state.ProjectInventory
}

// inspectionOptions selects which stored details an inspection loads. It never
// causes analysis to run: it only controls which already-stored observations
// and details are read back.
type inspectionOptions struct {
	IncludeFiles        bool
	IncludeComponents   bool
	IncludeDependencies bool
}

func (o inspectionOptions) includeDetails() bool {
	return o.IncludeComponents || o.IncludeDependencies
}

// projectInspection is a read-only view of a project's latest stored
// observations. Inventory and Analysis currency are evaluated independently:
// Status refers to the inventory, and Analysis.Status (if present) refers to the
// component analysis.
type projectInspection struct {
	Project        state.Project
	Inventory      *state.ProjectInventory
	Analysis       *state.ComponentObservation
	Status         string
	AnalysisStatus string
	Head           string
	Dirty          bool
}

// inventoryRegisteredProject builds and persists a deterministic inventory of
// a registered project's committed tree. Analysis is pinned to the project's
// current HEAD; the working tree is observed separately for disclosure.
func inventoryRegisteredProject(
	ctx context.Context,
	stateDB *state.Database,
	projectID int64,
) (projectInventory, error) {
	project, err := stateDB.GetProject(ctx, projectID)
	if err != nil {
		return projectInventory{}, fmt.Errorf("get registered project: %w", err)
	}

	commitHash, hasHead, err := repository.HeadCommit(ctx, project.RootPath)
	if err != nil {
		return projectInventory{}, fmt.Errorf("resolve project HEAD: %w", err)
	}
	if !hasHead {
		return projectInventory{}, fmt.Errorf(
			"cannot inventory project %d: repository has unborn HEAD",
			project.ID,
		)
	}

	result, err := inventory.Build(
		ctx,
		project.RootPath,
		commitHash,
		inventory.DefaultBudget(),
	)
	if err != nil {
		return projectInventory{}, fmt.Errorf(
			"inventory project at %s: %w",
			commitHash,
			err,
		)
	}

	isDirty, err := repository.IsDirty(ctx, project.RootPath)
	if err != nil {
		return projectInventory{}, fmt.Errorf(
			"observe project working tree: %w",
			err,
		)
	}

	record, err := stateDB.CreateProjectInventory(
		ctx,
		project.ID,
		commitHash,
		commitHash,
		isDirty,
		inventory.EngineVersion,
		result,
	)
	if err != nil {
		return projectInventory{}, fmt.Errorf("persist project inventory: %w", err)
	}

	return projectInventory{
		Project:   project,
		Inventory: record,
	}, nil
}

// inspectRegisteredProject loads the latest stored observations for a project
// and evaluates each one's currency relative to the live repository HEAD. It is
// strictly read-only: it never builds inventory, runs analysis, or mutates
// stored records.
func inspectRegisteredProject(
	ctx context.Context,
	stateDB *state.Database,
	projectID int64,
	options inspectionOptions,
) (projectInspection, error) {
	project, err := stateDB.GetProject(ctx, projectID)
	if err != nil {
		return projectInspection{}, fmt.Errorf("get registered project: %w", err)
	}

	inventory, err := stateDB.LatestProjectInventory(ctx, project.ID, options.IncludeFiles)
	if err != nil {
		return projectInspection{}, fmt.Errorf("load latest project inventory: %w", err)
	}

	var analysis *state.ComponentObservation
	if options.includeDetails() {
		analysis, err = stateDB.LatestComponentObservation(ctx, project.ID, true)
		if err != nil {
			return projectInspection{}, fmt.Errorf("load latest component analysis: %w", err)
		}
	}

	if inventory == nil && analysis == nil {
		return projectInspection{}, fmt.Errorf(
			"project %d has no stored observations; run forgehand project inventory %d or forgehand project analyze %d",
			project.ID,
			project.ID,
			project.ID,
		)
	}

	head, hasHead, err := repository.HeadCommit(ctx, project.RootPath)
	if err != nil {
		return projectInspection{}, fmt.Errorf("resolve project HEAD: %w", err)
	}

	dirty := false
	if hasHead {
		dirty, err = repository.IsDirty(ctx, project.RootPath)
		if err != nil {
			return projectInspection{}, fmt.Errorf(
				"observe project working tree: %w",
				err,
			)
		}
	}

	inspection := projectInspection{
		Project: project,
		Head:    head,
		Dirty:   dirty,
	}
	if inventory != nil {
		inspection.Inventory = inventory
		inspection.Status = inspectionStatus(inventory.CommitHash, head, hasHead)
	}
	if analysis != nil {
		inspection.Analysis = analysis
		inspection.AnalysisStatus = analysisStatus(analysis.CommitHash, head, hasHead)
	}

	return inspection, nil
}

// inspectionStatus compares a stored inventory commit with the live HEAD.
func inspectionStatus(
	inventoryCommit string,
	head string,
	hasHead bool,
) string {
	if !hasHead {
		return InspectionStatusUnavailable
	}
	if head == inventoryCommit {
		return InspectionStatusCurrent
	}
	return InspectionStatusStale
}

// inventoryToIPC projects a stored inventory into the IPC wire format.
func inventoryToIPC(record state.ProjectInventory) ipc.Inventory {
	languages := make([]ipc.InventoryLanguage, 0, len(record.Summary.Languages))
	for _, language := range record.Summary.Languages {
		languages = append(languages, ipc.InventoryLanguage{
			Language: language.Language,
			Count:    language.Count,
		})
	}

	buildSystems := make([]ipc.InventoryBuildSystem, 0, len(record.Summary.BuildSystems))
	for _, indicator := range record.Summary.BuildSystems {
		buildSystems = append(buildSystems, ipc.InventoryBuildSystem{
			Name: indicator.Name,
			Evidence: ipc.EvidenceReference{
				Path: indicator.Evidence.Path,
			},
		})
	}

	directories := make([]ipc.InventoryDirectory, 0, len(record.Summary.Directories))
	for _, directory := range record.Summary.Directories {
		directories = append(directories, ipc.InventoryDirectory{
			Path:  directory.Path,
			Files: directory.Files,
		})
	}

	categories := make([]ipc.InventoryCategory, 0, len(record.Summary.Categories))
	for _, category := range record.Summary.Categories {
		categories = append(categories, ipc.InventoryCategory{
			Category: string(category.Category),
			Count:    category.Count,
		})
	}

	files := make([]ipc.InventoryFile, 0, len(record.Files))
	for _, file := range record.Files {
		files = append(files, ipc.InventoryFile{
			Path:     file.Path,
			BlobHash: file.BlobHash,
			Mode:     file.Mode,
			Size:     file.Size,
			Language: file.Language,
			Category: file.Category,
		})
	}

	return ipc.Inventory{
		ID:            record.ID,
		ProjectID:     record.ProjectID,
		CommitHash:    record.CommitHash,
		HeadCommit:    record.HeadCommit,
		IsDirty:       record.IsDirty,
		EngineVersion: record.EngineVersion,
		TrackedFiles:  record.TrackedFiles,
		Truncated:     record.Truncated,
		ObservedAt:    record.ObservedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
		Summary: ipc.InventorySummary{
			TrackedFiles:      record.Summary.TrackedFiles,
			UnclassifiedFiles: record.Summary.UnclassifiedFiles,
			Languages:         languages,
			BuildSystems:      buildSystems,
			Directories:       directories,
			Categories:        categories,
			Truncated:         record.Summary.Truncated,
		},
		Files: files,
	}
}
