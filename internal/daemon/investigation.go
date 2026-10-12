package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/Hisha/Forgehand/internal/ipc"
	"github.com/Hisha/Forgehand/internal/query"
	"github.com/Hisha/Forgehand/internal/state"
)

// handleInvestigation serves read-only investigation requests.
func handleInvestigation(
	ctx context.Context,
	stateDB *state.Database,
	request ipc.Request,
) (ipc.InvestigationResponse, error) {
	engine := query.New(stateDB)
	analysisID := request.AnalysisID
	if analysisID == 0 && request.Analysis > 0 {
		analysisID = request.Analysis
	}

	componentID := request.Identity
	if componentID == "" {
		componentID = request.ComponentPath
	}
	if componentID == "" {
		return ipc.InvestigationResponse{}, fmt.Errorf("component identity required")
	}

	result, err := engine.Investigate(ctx, request.ProjectID, query.InvestigationRequest{
		ProjectID:   request.ProjectID,
		AnalysisID:  analysisID,
		ComponentID: componentID,
		Format:      request.Format,
		Limit:       request.Limit,
	})
	if err != nil {
		// normalize unknown project error
		if strings.Contains(err.Error(), "project not found") {
			return ipc.InvestigationResponse{}, fmt.Errorf("unknown project: %d", request.ProjectID)
		}
		if strings.Contains(err.Error(), "unknown component") {
			// keep as is or reformat; preserve error
		}
		if strings.Contains(err.Error(), "analysis not found") || strings.Contains(err.Error(), "component analysis not found") {
			// will be caught; pass through
		}
		return ipc.InvestigationResponse{}, err
	}

	// compute freshness
	head, hasHead := queryHead(ctx, stateDB, request.ProjectID)
	status := "current"
	if hasHead {
		if head != result.Commit {
			status = "stale"
		} else {
			status = "current"
		}
	} else {
		status = "unavailable"
	}
	result.Status = status

	warnings := append([]string(nil), result.Warnings...)
	switch status {
	case InspectionStatusStale:
		warnings = append(warnings, fmt.Sprintf(
			"analysis %d describes commit %s but HEAD is %s",
			result.AnalysisID,
			result.Commit,
			head,
		))
	case InspectionStatusUnavailable:
		warnings = append(warnings, "unable to verify current repository HEAD; freshness is unavailable")
	}
	if !result.Complete {
		// include completeness warning if present in query warnings style
		warnings = append(warnings, fmt.Sprintf(
			"analysis %d is partial",
			result.AnalysisID,
		))
	}
	if result.Truncated {
		warnings = append(warnings, "output was truncated by the requested limit")
	}

	suggested := make([]ipc.InvestigationFile, 0, len(result.SuggestedFiles))
	for _, sf := range result.SuggestedFiles {
		reasons := make([]string, 0, len(sf.Reasons))
		for _, r := range sf.Reasons {
			reasons = append(reasons, string(r))
		}
		suggested = append(suggested, ipc.InvestigationFile{Path: sf.Path, Reasons: reasons})
	}

	resp := ipc.InvestigationResponse{
		SchemaVersion:  result.SchemaVersion,
		ProjectID:      result.ProjectID,
		AnalysisID:     result.AnalysisID,
		Commit:         result.Commit,
		Status:         status,
		Complete:       result.Complete,
		Component:      queryComponentToIPC(result.Component, true),
		Dependencies:   queryDependenciesToIPC(result.Dependencies, true),
		Dependents:     dependentsToInvestigation(result.Dependents, true),
		Evidence:       queryEvidenceToIPC(result.Evidence),
		SuggestedFiles: suggested,
		Warnings:       warnings,
		Truncated:      result.Truncated,
		TruncatedBy:    result.TruncatedBy,
		TotalEvidence:  result.TotalEvidence,
		TotalFiles:     result.TotalFiles,
	}
	return resp, nil
}

func dependentsToInvestigation(deps []query.Dependency, includeEvidence bool) []ipc.QueryDependent {
	if len(deps) == 0 {
		return nil
	}
	out := make([]ipc.QueryDependent, 0, len(deps))
	for _, dep := range deps {
		out = append(out, queryDependentToIPC(dep, includeEvidence))
	}
	return out
}
