// Package query provides deterministic, read-only queries over Forgehand's
// persisted repository observations.
package query

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrUnknownProject indicates the requested project is not registered.
var ErrUnknownProject = errors.New("unknown project")

// SuggestedFileReason records why a suggested file was selected for
// investigation.
type SuggestedFileReason string

const (
	// ReasonComponentDeclaration indicates the file contains the selected
	// component's declaration evidence.
	ReasonComponentDeclaration SuggestedFileReason = "component-declaration"
	// ReasonOutgoingDependencyDeclaration indicates the file contains an
	// outgoing dependency declaration for the selected component.
	ReasonOutgoingDependencyDeclaration SuggestedFileReason = "outgoing-dependency-declaration"
	// ReasonIncomingDependencyDeclaration indicates the file contains an
	// incoming dependency declaration (from another component) that references
	// the selected component.
	ReasonIncomingDependencyDeclaration SuggestedFileReason = "incoming-dependency-declaration"
)

// InvestigationRequest specifies the parameters for an investigation.
type InvestigationRequest struct {
	ProjectID   int64
	AnalysisID  int64
	ComponentID string
	Format      string
	Limit       int
}

// InvestigationResult is the outcome of an investigation.
type InvestigationResult struct {
	SchemaVersion  int
	ProjectID      int64
	AnalysisID     int64
	Commit         string
	Status         string
	Complete       bool
	Component      Component
	Dependencies   []Dependency
	Dependents     []Dependency
	Evidence       []Evidence
	SuggestedFiles []SuggestedFile
	Warnings       []string
	Truncated      bool
	TruncatedBy    string
	TotalEvidence  int
	TotalFiles     int
}

// SuggestedFile describes a repository-relative file recommended for
// investigation, together with the deterministic reasons it was selected.
type SuggestedFile struct {
	Path    string
	Reasons []SuggestedFileReason
}

// Investigate builds a deterministic, evidence-backed investigation context for
// the selected component. It reuses the existing query infrastructure and never
// reads the current working tree or mutates persisted state.
func (e *Engine) Investigate(
	ctx context.Context,
	projectID int64,
	request InvestigationRequest,
) (InvestigationResult, error) {
	componentID := strings.TrimSpace(request.ComponentID)
	if componentID == "" {
		return InvestigationResult{}, fmt.Errorf("component identity must not be empty")
	}
	if request.Limit < 0 {
		return InvestigationResult{}, fmt.Errorf("limit must be >= 0")
	}
	if _, err := e.project(ctx, projectID); err != nil {
		// normalize error message to match requirement
		if errors.Is(err, ErrProjectNotFound) {
			return InvestigationResult{}, fmt.Errorf("%w: %d", ErrUnknownProject, projectID)
		}
		return InvestigationResult{}, err
	}

	selection := Selection{AnalysisID: request.AnalysisID}
	record, err := e.observation(ctx, projectID, selection)
	if err != nil {
		return InvestigationResult{}, err
	}

	component, matches, err := resolveComponent(record, componentID)
	if err != nil {
		return InvestigationResult{}, err
	}

	outgoing := outgoingDependencies(record, componentID)
	incoming := incomingDependencies(record, componentID)
	evidence := componentEvidence(record, componentID)
	for _, dep := range outgoing {
		evidence = append(evidence, dep.Evidence...)
	}
	for _, dep := range incoming {
		evidence = append(evidence, dep.Evidence...)
	}
	sortEvidence(evidence)

	// Build suggested files from evidence only, deduplicated with preserved reasons.
	type fileKey struct {
		path string
	}
	fileInfo := make(map[fileKey]*suggestedFileBuilder)
	for _, ev := range evidence {
		key := fileKey{path: ev.Path}
		if _, ok := fileInfo[key]; !ok {
			fileInfo[key] = &suggestedFileBuilder{path: ev.Path}
		}
	}

	// Assign reasons deterministically: component declarations first, then
	// outgoing dependency declarations, then incoming.
	// Component declaration evidence is that from components matching identity.
	compEv := componentEvidence(record, componentID)
	for _, ev := range compEv {
		key := fileKey{path: ev.Path}
		if b, ok := fileInfo[key]; ok {
			b.addReason(ReasonComponentDeclaration)
		}
	}
	// Outgoing dependency declarations
	for _, dep := range outgoing {
		for _, ev := range dep.Evidence {
			key := fileKey{path: ev.Path}
			if b, ok := fileInfo[key]; ok {
				b.addReason(ReasonOutgoingDependencyDeclaration)
			}
		}
	}
	// Incoming dependency declarations
	for _, dep := range incoming {
		for _, ev := range dep.Evidence {
			key := fileKey{path: ev.Path}
			if b, ok := fileInfo[key]; ok {
				b.addReason(ReasonIncomingDependencyDeclaration)
			}
		}
	}

	suggested := make([]SuggestedFile, 0, len(fileInfo))
	for _, b := range fileInfo {
		suggested = append(suggested, b.toSuggestedFile())
	}
	sortSuggestedFiles(suggested)

	result := InvestigationResult{
		SchemaVersion:  1,
		ProjectID:      record.ProjectID,
		AnalysisID:     record.ID,
		Commit:         record.CommitHash,
		Status:         "current",
		Complete:       record.Summary.Complete,
		Component:      component,
		Dependencies:   outgoing,
		Dependents:     incoming,
		Evidence:       append([]Evidence(nil), evidence...),
		SuggestedFiles: suggested,
		Warnings:       append([]string(nil), ambiguityWarnings(componentID, matches)...),
		Truncated:      false,
	}

	if request.Limit > 0 {
		totalEvidence := len(result.Evidence)
		totalFiles := len(result.SuggestedFiles)
		if len(result.Evidence) > request.Limit {
			result.Evidence = result.Evidence[:request.Limit]
			result.Truncated = true
			result.TruncatedBy = "evidence"
		}
		if len(result.SuggestedFiles) > request.Limit {
			result.SuggestedFiles = result.SuggestedFiles[:request.Limit]
			result.Truncated = true
			if result.TruncatedBy == "" {
				result.TruncatedBy = "files"
			} else {
				result.TruncatedBy = "both"
			}
		}
		result.TotalEvidence = totalEvidence
		result.TotalFiles = totalFiles
	}

	// Current/stale status and warnings from observation - determine status relative to head
	// (reusing logic conceptually; daemon will set actual freshness based on HEAD)
	result.Status = "current" // placeholder; daemon layer may adjust

	return result, nil
}

type suggestedFileBuilder struct {
	path    string
	reasons map[SuggestedFileReason]struct{}
}

func (b *suggestedFileBuilder) addReason(r SuggestedFileReason) {
	if b.reasons == nil {
		b.reasons = make(map[SuggestedFileReason]struct{})
	}
	b.reasons[r] = struct{}{}
}

func (b *suggestedFileBuilder) toSuggestedFile() SuggestedFile {
	reasons := make([]SuggestedFileReason, 0, len(b.reasons))
	for r := range b.reasons {
		reasons = append(reasons, r)
	}
	// stable ordering within reasons
	sort.Slice(reasons, func(i, j int) bool {
		return reasonRank(reasons[i]) < reasonRank(reasons[j])
	})
	return SuggestedFile{Path: b.path, Reasons: reasons}
}

func reasonRank(r SuggestedFileReason) int {
	switch r {
	case ReasonComponentDeclaration:
		return 0
	case ReasonOutgoingDependencyDeclaration:
		return 1
	case ReasonIncomingDependencyDeclaration:
		return 2
	default:
		return 3
	}
}

func sortSuggestedFiles(files []SuggestedFile) {
	sort.Slice(files, func(i, j int) bool {
		if files[i].Path != files[j].Path {
			return files[i].Path < files[j].Path
		}
		if len(files[i].Reasons) != len(files[j].Reasons) {
			return len(files[i].Reasons) < len(files[j].Reasons)
		}
		for k := range files[i].Reasons {
			if k < len(files[j].Reasons) && files[i].Reasons[k] != files[j].Reasons[k] {
				return reasonRank(files[i].Reasons[k]) < reasonRank(files[j].Reasons[k])
			}
		}
		return false
	})
}
