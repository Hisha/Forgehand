package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Hisha/Forgehand/internal/components"
	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
)

type projectAnalysis struct {
	Project  state.Project
	Analysis state.ComponentObservation
	Status   string
	Head     string
	Dirty    bool
}

func analyzeRegisteredProject(
	ctx context.Context,
	stateDB *state.Database,
	projectID int64,
) (projectAnalysis, error) {
	project, err := stateDB.GetProject(ctx, projectID)
	if err != nil {
		return projectAnalysis{}, fmt.Errorf("get registered project: %w", err)
	}

	commitHash, hasHead, err := repository.HeadCommit(ctx, project.RootPath)
	if err != nil {
		return projectAnalysis{}, fmt.Errorf("resolve project HEAD: %w", err)
	}
	if !hasHead {
		return projectAnalysis{}, fmt.Errorf(
			"cannot analyze project %d: repository has unborn HEAD",
			project.ID,
		)
	}

	result, err := components.Analyze(
		ctx,
		project.RootPath,
		commitHash,
		components.DefaultBudget(),
	)
	if err != nil {
		return projectAnalysis{}, fmt.Errorf(
			"analyze project at %s: %w",
			commitHash,
			err,
		)
	}

	isDirty, err := repository.IsDirty(ctx, project.RootPath)
	if err != nil {
		return projectAnalysis{}, fmt.Errorf(
			"observe project working tree: %w",
			err,
		)
	}

	record, err := stateDB.CreateComponentObservation(
		ctx,
		project.ID,
		commitHash,
		commitHash,
		isDirty,
		components.EngineVersion,
		result,
	)
	if err != nil {
		return projectAnalysis{}, fmt.Errorf("persist component analysis: %w", err)
	}

	return projectAnalysis{
		Project:  project,
		Analysis: *record,
		Status:   analysisStatus(commitHash, commitHash, hasHead),
		Head:     commitHash,
		Dirty:    isDirty,
	}, nil
}

func analysisStatus(
	analysisCommit string,
	head string,
	hasHead bool,
) string {
	if !hasHead {
		return InspectionStatusUnavailable
	}
	if strings.EqualFold(head, analysisCommit) || head == analysisCommit {
		return InspectionStatusCurrent
	}
	return InspectionStatusStale
}

func analysisToIPC(
	record state.ComponentObservation,
	status string,
	head string,
	dirty bool,
	includeComponents bool,
	includeDependencies bool,
) ipc.ComponentAnalysis {
	var components []ipc.AnalysisComponent
	if includeComponents {
		components = make([]ipc.AnalysisComponent, 0, len(record.Components))
		for _, comp := range record.Components {
			components = append(components, ipc.AnalysisComponent{
				Kind:     comp.Kind,
				Identity: comp.Identity,
				Path:     comp.Path,
				Language: comp.Language,
				Internal: comp.Internal,
				Evidence: evidenceToIPC(comp.Evidence),
			})
		}
	}

	var deps []ipc.AnalysisDependency
	if includeDependencies {
		deps = make([]ipc.AnalysisDependency, 0, len(record.Dependencies))
		for _, dep := range record.Dependencies {
			deps = append(deps, ipc.AnalysisDependency{
				Kind:         dep.Kind,
				Source:       dep.Source,
				Target:       dep.Target,
				TargetModule: dep.TargetModule,
				Language:     dep.Language,
				Evidence:     evidenceToIPC(dep.Evidence),
			})
		}
	}

	summary := ipc.AnalysisSummary{
		Complete:                record.Summary.Complete,
		Truncated:               record.Summary.Truncated,
		Sources:                 record.Summary.Sources,
		Analyzed:                record.Summary.Analyzed,
		Skipped:                 record.Summary.Skipped,
		Modules:                 record.Summary.Modules,
		Packages:                record.Summary.Packages,
		Commands:                record.Summary.Commands,
		BuildComponents:         record.Summary.BuildComponents,
		ConfigurationComponents: record.Summary.ConfigurationComponents,
	}

	for _, c := range record.Summary.Dependencies {
		summary.Dependencies = append(summary.Dependencies, ipc.AnalysisDependencyCount{
			Kind:  string(c.Kind),
			Count: c.Count,
		})
	}

	reqs := make([]ipc.ModuleRequirement, 0, len(record.Summary.ModuleRequirements))
	for _, r := range record.Summary.ModuleRequirements {
		reqs = append(reqs, ipc.ModuleRequirement{
			Path:     r.Path,
			Version:  r.Version,
			Indirect: r.Indirect,
		})
	}

	skipped := make([]ipc.AnalysisSkippedFile, 0, len(record.Summary.SkippedFiles))
	for _, s := range record.Summary.SkippedFiles {
		skipped = append(skipped, ipc.AnalysisSkippedFile{
			Path:     s.Path,
			BlobHash: s.BlobHash,
			Reason:   s.Reason,
		})
	}

	return ipc.ComponentAnalysis{
		ID:                 record.ID,
		ProjectID:          record.ProjectID,
		CommitHash:         record.CommitHash,
		HeadCommit:         record.HeadCommit,
		IsDirty:            record.IsDirty,
		EngineVersion:      record.EngineVersion,
		ObservedAt:         record.ObservedAt.Format(time.RFC3339Nano),
		Status:             status,
		Head:               head,
		Dirty:              dirty,
		Summary:            summary,
		Components:         components,
		Dependencies:       deps,
		SkippedFiles:       skipped,
		ModuleRequirements: reqs,
	}
}

func evidenceToIPC(evidence []components.SourceEvidence) []ipc.SourceEvidence {
	result := make([]ipc.SourceEvidence, 0, len(evidence))
	for _, e := range evidence {
		result = append(result, ipc.SourceEvidence{
			Path:        e.Path,
			BlobHash:    e.BlobHash,
			Declaration: e.Declaration,
			Line:        e.Line,
		})
	}
	return result
}
