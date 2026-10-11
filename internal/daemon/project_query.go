package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/query"
	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
)

// handleQuery serves read-only queries over persisted observations. It never
// runs analysis, inventory, discovery, builds, or tests; it only reads stored
// records and, for freshness labelling, the live repository HEAD.
func handleQuery(
	ctx context.Context,
	stateDB *state.Database,
	request ipc.Request,
) (ipc.QueryResponse, error) {
	engine := query.New(stateDB)
	selection := query.Selection{AnalysisID: request.AnalysisID}
	includeEvidence := request.IncludeEvidence || request.Command == "query-evidence"

	switch request.Command {
	case "query-components":
		result, err := engine.Components(ctx, request.ProjectID, selection, query.ComponentFilter{
			Kind:     request.Kind,
			Identity: request.Identity,
			Path:     request.ComponentPath,
			Contains: request.Contains,
			Limit:    request.Limit,
		})
		if err != nil {
			return ipc.QueryResponse{}, err
		}

		head, hasHead := queryHead(ctx, stateDB, request.ProjectID)
		observation := queryObservationToIPC(head, hasHead, result.Observation)
		components := make([]ipc.AnalysisComponent, 0, len(result.Components))
		for _, component := range result.Components {
			components = append(components, queryComponentToIPC(component, includeEvidence))
		}

		return ipc.QueryResponse{
			ProjectID:   request.ProjectID,
			Query:       "components",
			Observation: &observation,
			Components:  components,
			Total:       len(components),
			Empty:       len(components) == 0,
			Limited:     result.Limited,
			Warnings:    queryWarnings(observation, result.Observation, result.Limited),
		}, nil

	case "query-dependencies":
		result, err := engine.Dependencies(ctx, request.ProjectID, request.Identity, selection)
		if err != nil {
			return ipc.QueryResponse{}, err
		}

		head, hasHead := queryHead(ctx, stateDB, request.ProjectID)
		observation := queryObservationToIPC(head, hasHead, result.Observation)
		dependencies := make([]ipc.AnalysisDependency, 0, len(result.Dependencies))
		for _, dependency := range result.Dependencies {
			dependencies = append(dependencies, queryDependencyToIPC(dependency, includeEvidence))
		}

		return ipc.QueryResponse{
			ProjectID:    request.ProjectID,
			Query:        "dependencies",
			Observation:  &observation,
			Components:   []ipc.AnalysisComponent{queryComponentToIPC(result.Component, includeEvidence)},
			Dependencies: dependencies,
			Total:        len(dependencies),
			Empty:        len(dependencies) == 0,
			Warnings:     queryWarnings(observation, result.Observation, false, result.Warnings...),
		}, nil

	case "query-dependents":
		result, err := engine.Dependents(ctx, request.ProjectID, request.Identity, selection)
		if err != nil {
			return ipc.QueryResponse{}, err
		}

		head, hasHead := queryHead(ctx, stateDB, request.ProjectID)
		observation := queryObservationToIPC(head, hasHead, result.Observation)
		dependents := make([]ipc.QueryDependent, 0, len(result.Dependents))
		for _, dependent := range result.Dependents {
			dependents = append(dependents, queryDependentToIPC(dependent, includeEvidence))
		}

		return ipc.QueryResponse{
			ProjectID:   request.ProjectID,
			Query:       "dependents",
			Observation: &observation,
			Components:  []ipc.AnalysisComponent{queryComponentToIPC(result.Component, includeEvidence)},
			Dependents:  dependents,
			Total:       len(dependents),
			Empty:       len(dependents) == 0,
			Warnings:    queryWarnings(observation, result.Observation, false, result.Warnings...),
		}, nil

	case "query-evidence":
		result, err := engine.Evidence(ctx, request.ProjectID, request.Identity, selection)
		if err != nil {
			return ipc.QueryResponse{}, err
		}

		head, hasHead := queryHead(ctx, stateDB, request.ProjectID)
		observation := queryObservationToIPC(head, hasHead, result.Observation)
		evidence := queryEvidenceToIPC(result.Evidence)

		return ipc.QueryResponse{
			ProjectID:   request.ProjectID,
			Query:       "evidence",
			Observation: &observation,
			Components:  []ipc.AnalysisComponent{queryComponentToIPC(result.Component, false)},
			Evidence:    evidence,
			Total:       len(evidence),
			Empty:       len(evidence) == 0,
			Warnings:    queryWarnings(observation, result.Observation, false, result.Warnings...),
		}, nil

	case "query-changes":
		comparison, err := engine.Compare(
			ctx,
			request.ProjectID,
			request.FromAnalysisID,
			request.ToAnalysisID,
		)
		if err != nil {
			return ipc.QueryResponse{}, err
		}

		head, hasHead := queryHead(ctx, stateDB, request.ProjectID)
		from := queryObservationToIPC(head, hasHead, comparison.From)
		to := queryObservationToIPC(head, hasHead, comparison.To)

		changes := ipc.QueryChanges{
			From:                from,
			To:                  to,
			AddedComponents:     queryComponentsToIPC(comparison.AddedComponents, false),
			RemovedComponents:   queryComponentsToIPC(comparison.RemovedComponents, false),
			AddedDependencies:   queryDependenciesToIPC(comparison.AddedDependencies, false),
			RemovedDependencies: queryDependenciesToIPC(comparison.RemovedDependencies, false),
			Reclassified:        queryReclassificationsToIPC(comparison.Reclassified),
		}

		total := len(changes.AddedComponents) + len(changes.RemovedComponents) +
			len(changes.AddedDependencies) + len(changes.RemovedDependencies) +
			len(changes.Reclassified)

		warnings := queryWarnings(from, comparison.From, false)
		warnings = append(warnings, queryWarnings(to, comparison.To, false)...)

		return ipc.QueryResponse{
			ProjectID: request.ProjectID,
			Query:     "changes",
			Changes:   &changes,
			Total:     total,
			Empty:     total == 0,
			Warnings:  warnings,
		}, nil

	default:
		return ipc.QueryResponse{}, fmt.Errorf("unknown query command: %s", request.Command)
	}
}

// queryHead resolves the live repository HEAD for freshness labelling only. It
// never analyzes the repository; a missing or unreadable repository simply
// yields an unavailable freshness status.
func queryHead(ctx context.Context, stateDB *state.Database, projectID int64) (string, bool) {
	project, err := stateDB.GetProject(ctx, projectID)
	if err != nil {
		return "", false
	}
	head, hasHead, err := repository.HeadCommit(ctx, project.RootPath)
	if err != nil {
		return "", false
	}
	return head, hasHead
}

func queryObservationToIPC(head string, hasHead bool, meta query.Observation) ipc.QueryObservation {
	return ipc.QueryObservation{
		AnalysisID:    meta.AnalysisID,
		ProjectID:     meta.ProjectID,
		Commit:        meta.Commit,
		HeadCommit:    meta.HeadCommit,
		IsDirty:       meta.IsDirty,
		EngineVersion: meta.EngineVersion,
		ObservedAt:    meta.ObservedAt.Format(time.RFC3339Nano),
		Status:        analysisStatus(meta.Commit, head, hasHead),
		Head:          head,
		Complete:      meta.Complete,
		Truncated:     meta.Truncated,
		Sources:       meta.Sources,
		Analyzed:      meta.Analyzed,
		Skipped:       meta.Skipped,
	}
}

func queryWarnings(
	observation ipc.QueryObservation,
	meta query.Observation,
	limited bool,
	extra ...string,
) []string {
	warnings := append([]string{}, extra...)

	switch observation.Status {
	case InspectionStatusStale:
		warnings = append(warnings, fmt.Sprintf(
			"analysis %d describes commit %s but HEAD is %s; run `forgehand project analyze %d` to refresh",
			meta.AnalysisID,
			meta.Commit,
			observation.Head,
			meta.ProjectID,
		))
	case InspectionStatusUnavailable:
		warnings = append(warnings, "unable to verify current repository HEAD; freshness is unavailable")
	}

	if !meta.Complete {
		warnings = append(warnings, fmt.Sprintf(
			"analysis %d is partial: %d of %d source(s) were skipped",
			meta.AnalysisID,
			meta.Skipped,
			meta.Sources,
		))
	}
	if limited {
		warnings = append(warnings, "output was truncated by the requested limit")
	}

	return warnings
}

func queryComponentToIPC(component query.Component, includeEvidence bool) ipc.AnalysisComponent {
	out := ipc.AnalysisComponent{
		Kind:     component.Kind,
		Identity: component.Identity,
		Path:     component.Path,
		Language: component.Language,
		Internal: component.Internal,
	}
	if includeEvidence {
		out.Evidence = queryEvidenceToIPC(component.Evidence)
	}
	return out
}

func queryComponentsToIPC(components []query.Component, includeEvidence bool) []ipc.AnalysisComponent {
	if len(components) == 0 {
		return nil
	}
	out := make([]ipc.AnalysisComponent, 0, len(components))
	for _, component := range components {
		out = append(out, queryComponentToIPC(component, includeEvidence))
	}
	return out
}

func queryDependencyToIPC(dependency query.Dependency, includeEvidence bool) ipc.AnalysisDependency {
	out := ipc.AnalysisDependency{
		Kind:         dependency.Kind,
		Source:       dependency.Source,
		Target:       dependency.Target,
		TargetModule: dependency.TargetModule,
		Language:     dependency.Language,
	}
	if includeEvidence {
		out.Evidence = queryEvidenceToIPC(dependency.Evidence)
	}
	return out
}

func queryDependenciesToIPC(dependencies []query.Dependency, includeEvidence bool) []ipc.AnalysisDependency {
	if len(dependencies) == 0 {
		return nil
	}
	out := make([]ipc.AnalysisDependency, 0, len(dependencies))
	for _, dependency := range dependencies {
		out = append(out, queryDependencyToIPC(dependency, includeEvidence))
	}
	return out
}

func queryDependentToIPC(dependent query.Dependency, includeEvidence bool) ipc.QueryDependent {
	out := ipc.QueryDependent{
		Kind:     dependent.Kind,
		Source:   dependent.Source,
		Target:   dependent.Target,
		Language: dependent.Language,
	}
	if includeEvidence {
		out.Evidence = queryEvidenceToIPC(dependent.Evidence)
	}
	return out
}

func queryEvidenceToIPC(evidence []query.Evidence) []ipc.SourceEvidence {
	if len(evidence) == 0 {
		return nil
	}
	out := make([]ipc.SourceEvidence, 0, len(evidence))
	for _, item := range evidence {
		out = append(out, ipc.SourceEvidence{
			Path:        item.Path,
			BlobHash:    item.BlobHash,
			Declaration: item.Declaration,
			Line:        item.Line,
		})
	}
	return out
}

func queryReclassificationsToIPC(items []query.Reclassification) []ipc.QueryReclassification {
	if len(items) == 0 {
		return nil
	}
	out := make([]ipc.QueryReclassification, 0, len(items))
	for _, item := range items {
		out = append(out, ipc.QueryReclassification{
			Source:     item.Source,
			Target:     item.Target,
			FromKind:   item.FromKind,
			ToKind:     item.ToKind,
			FromModule: item.FromModule,
			ToModule:   item.ToModule,
		})
	}
	return out
}
