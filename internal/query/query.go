// Package query provides deterministic, read-only queries over Forgehand's
// persisted repository observations.
//
// The engine reads only records stored in the Forgehand state database. It never
// invokes Git, repository discovery, inventory, component analysis, builds, or
// tests; never executes code from an analyzed repository; never reads the
// working tree; and never mutates persisted observations. All results are
// ordered deterministically and preserve the evidence provenance captured during
// analysis.
//
// Freshness and completeness are distinct properties. The engine reports whether
// a stored analysis was complete or partial, and reports the commit the analysis
// was pinned to. Comparing that commit with the live repository HEAD (to label an
// observation current or stale) is the caller's responsibility, so the engine
// remains free of Git access.
package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Hisha/Forgehand/internal/components"
	"github.com/Hisha/Forgehand/internal/state"
)

// ErrProjectNotFound indicates the requested project is not registered.
var ErrProjectNotFound = errors.New("project not found")

// ErrNoObservation indicates the project has no persisted component analysis.
var ErrNoObservation = errors.New("no stored component analysis")

// ErrObservationNotFound indicates the explicitly requested analysis id does not
// exist.
var ErrObservationNotFound = errors.New("component analysis not found")

// ErrCrossProject indicates a requested analysis belongs to a different project
// than the one the query is scoped to.
var ErrCrossProject = errors.New("component analysis belongs to a different project")

// ErrUnknownComponent indicates the requested component identity is not present
// in the selected observation. It is distinct from a known component that has no
// matching dependencies or dependents.
var ErrUnknownComponent = errors.New("unknown component")

// Component kinds, matching the identities established by component analysis.
const (
	KindModule        = "module"
	KindPackage       = "package"
	KindCommand       = "command"
	KindBuild         = "build"
	KindConfiguration = "configuration"
)

// Dependency kinds, matching the classifications established by analysis.
const (
	DependencyInternal       = "internal"
	DependencyExternalModule = "external-module"
	DependencyStdlib         = "stdlib"
	DependencyUnresolved     = "unresolved"
)

// Engine executes queries over persisted observations.
type Engine struct {
	db *state.Database
}

// New returns an Engine backed by the given state database.
func New(db *state.Database) *Engine {
	return &Engine{db: db}
}

// Observation describes the persisted analysis a query executed against.
type Observation struct {
	AnalysisID    int64
	ProjectID     int64
	Commit        string
	HeadCommit    string
	IsDirty       bool
	EngineVersion int
	ObservedAt    time.Time
	Complete      bool
	Truncated     bool
	Sources       int
	Analyzed      int
	Skipped       int
}

// Evidence ties a component or dependency to a committed Git blob.
type Evidence struct {
	Path        string
	BlobHash    string
	Declaration string
	Line        int
}

// Component is a structural unit identified by analysis.
type Component struct {
	Kind     string
	Identity string
	Path     string
	Language string
	Internal bool
	Evidence []Evidence
}

// Dependency is an observed import relationship between components or modules.
type Dependency struct {
	Kind         string
	Source       string
	Target       string
	TargetModule string
	Language     string
	Evidence     []Evidence
}

// Selection identifies which persisted analysis a query reads. A zero AnalysisID
// selects the project's latest analysis. Explicitly selecting an analysis is
// supported where a stable observation id is known; it never triggers a refresh.
type Selection struct {
	AnalysisID int64
}

// ComponentFilter selects components from an observation. The zero value matches
// every component.
//
// Semantics: Kind, Identity, and Path are exact, case-sensitive matches.
// Contains is a case-sensitive substring match against the component identity, so
// callers can perform prefix or substring lookups when an exact identity is not
// known. Limit bounds the number of returned components after ordering; a value
// of zero or less means unbounded.
type ComponentFilter struct {
	Kind     string
	Identity string
	Path     string
	Contains string
	Limit    int
}

// ComponentsResult is the outcome of a component query.
type ComponentsResult struct {
	Observation Observation
	Components  []Component
	Limited     bool
}

// DependenciesResult is the outcome of an outgoing-dependency query.
type DependenciesResult struct {
	Observation  Observation
	Component    Component
	Dependencies []Dependency
	Warnings     []string
}

// DependentsResult is the outcome of a direct reverse-dependency query.
type DependentsResult struct {
	Observation Observation
	Component   Component
	Dependents  []Dependency
	Warnings    []string
}

// EvidenceResult is the outcome of an evidence query.
type EvidenceResult struct {
	Observation Observation
	Component   Component
	Evidence    []Evidence
	Warnings    []string
}

// Reclassification records a dependency whose classification changed between two
// observations while the underlying relationship stayed the same. It is reported
// separately so such a dependency is never reported as both added and removed.
type Reclassification struct {
	Source     string
	Target     string
	FromKind   string
	ToKind     string
	FromModule string
	ToModule   string
}

// Comparison is the deterministic difference between two observations.
type Comparison struct {
	ProjectID           int64
	From                Observation
	To                  Observation
	AddedComponents     []Component
	RemovedComponents   []Component
	AddedDependencies   []Dependency
	RemovedDependencies []Dependency
	Reclassified        []Reclassification
}

// Components returns the components of a project's selected analysis, optionally
// filtered, in deterministic order.
func (e *Engine) Components(
	ctx context.Context,
	projectID int64,
	selection Selection,
	filter ComponentFilter,
) (ComponentsResult, error) {
	if _, err := e.project(ctx, projectID); err != nil {
		return ComponentsResult{}, err
	}

	record, err := e.observation(ctx, projectID, selection)
	if err != nil {
		return ComponentsResult{}, err
	}

	components := make([]Component, 0, len(record.Components))
	for _, stored := range record.Components {
		component := fromComponentRecord(stored)
		if !filter.matches(component) {
			continue
		}
		components = append(components, component)
	}
	sortComponents(components)

	limited := false
	if filter.Limit > 0 && len(components) > filter.Limit {
		components = components[:filter.Limit]
		limited = true
	}

	return ComponentsResult{
		Observation: observationMeta(record),
		Components:  components,
		Limited:     limited,
	}, nil
}

// Dependencies returns the outgoing dependencies of a component identity. An
// unknown identity is reported as ErrUnknownComponent; a known identity with no
// outgoing dependencies returns an empty list.
func (e *Engine) Dependencies(
	ctx context.Context,
	projectID int64,
	identity string,
	selection Selection,
) (DependenciesResult, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return DependenciesResult{}, fmt.Errorf("component identity must not be empty")
	}
	if _, err := e.project(ctx, projectID); err != nil {
		return DependenciesResult{}, err
	}

	record, err := e.observation(ctx, projectID, selection)
	if err != nil {
		return DependenciesResult{}, err
	}

	component, matches, err := resolveComponent(record, identity)
	if err != nil {
		return DependenciesResult{}, err
	}

	dependencies := outgoingDependencies(record, identity)

	return DependenciesResult{
		Observation:  observationMeta(record),
		Component:    component,
		Dependencies: dependencies,
		Warnings:     ambiguityWarnings(identity, matches),
	}, nil
}

// Dependents returns the direct dependents of a specified internal component
// identity. It returns only direct relationships; it does not traverse
// transitively. Module requirements are never treated as dependents.
func (e *Engine) Dependents(
	ctx context.Context,
	projectID int64,
	identity string,
	selection Selection,
) (DependentsResult, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return DependentsResult{}, fmt.Errorf("component identity must not be empty")
	}
	if _, err := e.project(ctx, projectID); err != nil {
		return DependentsResult{}, err
	}

	record, err := e.observation(ctx, projectID, selection)
	if err != nil {
		return DependentsResult{}, err
	}

	component, matches, err := resolveComponent(record, identity)
	if err != nil {
		return DependentsResult{}, err
	}

	targets := incomingDependencies(record, identity)

	return DependentsResult{
		Observation: observationMeta(record),
		Component:   component,
		Dependents:  targets,
		Warnings:    ambiguityWarnings(identity, matches),
	}, nil
}

// outgoingDependencies returns the direct outgoing dependencies of the given
// source identity, ordered deterministically. A known identity with no outgoing
// dependencies yields an empty, non-nil slice.
func outgoingDependencies(record state.ComponentObservation, identity string) []Dependency {
	dependencies := make([]Dependency, 0)
	for _, stored := range record.Dependencies {
		if stored.Source != identity {
			continue
		}
		dependencies = append(dependencies, fromDependencyRecord(stored))
	}
	sortDependencies(dependencies)
	return dependencies
}

// incomingDependencies returns the direct internal dependents of the given
// target identity, ordered deterministically. Module requirements are never
// treated as dependents; only internal relationships are considered.
func incomingDependencies(record state.ComponentObservation, identity string) []Dependency {
	dependents := make([]Dependency, 0)
	for _, stored := range record.Dependencies {
		if stored.Kind != DependencyInternal || stored.Target != identity {
			continue
		}
		dependents = append(dependents, fromDependencyRecord(stored))
	}
	sortDependencies(dependents)
	return dependents
}

// componentEvidence returns the committed source evidence for every component
// sharing the given identity, ordered deterministically. Evidence from all
// matches is merged so an ambiguous identity does not silently drop provenance.
func componentEvidence(record state.ComponentObservation, identity string) []Evidence {
	evidence := make([]Evidence, 0)
	for _, stored := range record.Components {
		if stored.Identity != identity {
			continue
		}
		for _, item := range stored.Evidence {
			evidence = append(evidence, fromComponentEvidence(item))
		}
	}
	sortEvidence(evidence)
	return evidence
}

// Evidence returns the source evidence associated with a component identity.
// Missing line numbers and declarations are preserved as absent rather than
// invented. Evidence always references committed Git blobs.
func (e *Engine) Evidence(
	ctx context.Context,
	projectID int64,
	identity string,
	selection Selection,
) (EvidenceResult, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return EvidenceResult{}, fmt.Errorf("component identity must not be empty")
	}
	if _, err := e.project(ctx, projectID); err != nil {
		return EvidenceResult{}, err
	}

	record, err := e.observation(ctx, projectID, selection)
	if err != nil {
		return EvidenceResult{}, err
	}

	component, matches, err := resolveComponent(record, identity)
	if err != nil {
		return EvidenceResult{}, err
	}

	evidence := componentEvidence(record, identity)

	return EvidenceResult{
		Observation: observationMeta(record),
		Component:   component,
		Evidence:    evidence,
		Warnings:    ambiguityWarnings(identity, matches),
	}, nil
}

// Compare deterministically compares two explicitly selected persisted
// observations that must both belong to the requested project. Component
// identity and dependency relationship are compared canonically: a dependency
// whose classification changed is reported as a reclassification rather than as
// an addition and a removal, and evidence-only differences (blob hashes or line
// numbers) are ignored. Comparison never persists a new observation.
func (e *Engine) Compare(
	ctx context.Context,
	projectID int64,
	fromID int64,
	toID int64,
) (Comparison, error) {
	if fromID <= 0 || toID <= 0 {
		return Comparison{}, fmt.Errorf("analysis ids must be positive")
	}
	if _, err := e.project(ctx, projectID); err != nil {
		return Comparison{}, err
	}

	from, err := e.observation(ctx, projectID, Selection{AnalysisID: fromID})
	if err != nil {
		return Comparison{}, err
	}
	to, err := e.observation(ctx, projectID, Selection{AnalysisID: toID})
	if err != nil {
		return Comparison{}, err
	}

	fromComponents := componentIndex(from)
	toComponents := componentIndex(to)
	fromDependencies := dependencyIndex(from)
	toDependencies := dependencyIndex(to)

	comparison := Comparison{
		ProjectID: projectID,
		From:      observationMeta(from),
		To:        observationMeta(to),
	}

	comparison.AddedComponents = missingComponents(toComponents, fromComponents)
	comparison.RemovedComponents = missingComponents(fromComponents, toComponents)
	comparison.AddedDependencies = missingDependencies(toDependencies, fromDependencies)
	comparison.RemovedDependencies = missingDependencies(fromDependencies, toDependencies)

	for key, fromDependency := range fromDependencies {
		toDependency, ok := toDependencies[key]
		if !ok {
			continue
		}
		if fromDependency.Kind == toDependency.Kind &&
			fromDependency.TargetModule == toDependency.TargetModule {
			continue
		}
		comparison.Reclassified = append(comparison.Reclassified, Reclassification{
			Source:     fromDependency.Source,
			Target:     fromDependency.Target,
			FromKind:   fromDependency.Kind,
			ToKind:     toDependency.Kind,
			FromModule: fromDependency.TargetModule,
			ToModule:   toDependency.TargetModule,
		})
	}
	sortReclassifications(comparison.Reclassified)

	return comparison, nil
}

func (e *Engine) project(ctx context.Context, projectID int64) (state.Project, error) {
	if projectID <= 0 {
		return state.Project{}, fmt.Errorf("project id must be positive")
	}
	project, err := e.db.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return state.Project{}, fmt.Errorf("%w: %d", ErrProjectNotFound, projectID)
		}
		return state.Project{}, fmt.Errorf("load project %d: %w", projectID, err)
	}
	return project, nil
}

// observation loads the selected analysis and enforces project boundaries.
func (e *Engine) observation(
	ctx context.Context,
	projectID int64,
	selection Selection,
) (state.ComponentObservation, error) {
	if selection.AnalysisID > 0 {
		record, err := e.db.GetComponentObservation(ctx, selection.AnalysisID, true)
		if err != nil {
			return state.ComponentObservation{}, fmt.Errorf("load analysis %d: %w", selection.AnalysisID, err)
		}
		if record == nil {
			return state.ComponentObservation{}, fmt.Errorf("%w: %d", ErrObservationNotFound, selection.AnalysisID)
		}
		if record.ProjectID != projectID {
			return state.ComponentObservation{}, fmt.Errorf(
				"%w: analysis %d belongs to project %d, not %d",
				ErrCrossProject,
				selection.AnalysisID,
				record.ProjectID,
				projectID,
			)
		}
		return *record, nil
	}

	record, err := e.db.LatestComponentObservation(ctx, projectID, true)
	if err != nil {
		return state.ComponentObservation{}, fmt.Errorf("load latest analysis: %w", err)
	}
	if record == nil {
		return state.ComponentObservation{}, fmt.Errorf("%w for project %d", ErrNoObservation, projectID)
	}
	return *record, nil
}

func observationMeta(record state.ComponentObservation) Observation {
	return Observation{
		AnalysisID:    record.ID,
		ProjectID:     record.ProjectID,
		Commit:        record.CommitHash,
		HeadCommit:    record.HeadCommit,
		IsDirty:       record.IsDirty,
		EngineVersion: record.EngineVersion,
		ObservedAt:    record.ObservedAt,
		Complete:      record.Summary.Complete,
		Truncated:     record.Summary.Truncated,
		Sources:       record.Summary.Sources,
		Analyzed:      record.Summary.Analyzed,
		Skipped:       record.Summary.Skipped,
	}
}

func (f ComponentFilter) matches(component Component) bool {
	if f.Kind != "" && component.Kind != f.Kind {
		return false
	}
	if f.Identity != "" && component.Identity != f.Identity {
		return false
	}
	if f.Path != "" && component.Path != f.Path {
		return false
	}
	if f.Contains != "" && !strings.Contains(component.Identity, f.Contains) {
		return false
	}
	return true
}

// resolveComponent finds the component(s) with the given canonical identity and
// chooses a deterministic representative. It reports how many components shared
// the identity so callers can warn about ambiguity.
func resolveComponent(record state.ComponentObservation, identity string) (Component, int, error) {
	matches := make([]Component, 0)
	for _, stored := range record.Components {
		if stored.Identity == identity {
			matches = append(matches, fromComponentRecord(stored))
		}
	}
	if len(matches) == 0 {
		return Component{}, 0, fmt.Errorf("%w: %q", ErrUnknownComponent, identity)
	}
	sort.Slice(matches, func(i, j int) bool {
		if kindRank(matches[i].Kind) != kindRank(matches[j].Kind) {
			return kindRank(matches[i].Kind) < kindRank(matches[j].Kind)
		}
		if matches[i].Identity != matches[j].Identity {
			return matches[i].Identity < matches[j].Identity
		}
		return matches[i].Path < matches[j].Path
	})
	return matches[0], len(matches), nil
}

func kindRank(kind string) int {
	switch kind {
	case KindPackage:
		return 0
	case KindCommand:
		return 1
	case KindBuild:
		return 2
	case KindConfiguration:
		return 3
	case KindModule:
		return 4
	default:
		return 5
	}
}

func ambiguityWarnings(identity string, matches int) []string {
	if matches <= 1 {
		return nil
	}
	return []string{fmt.Sprintf(
		"identity %q matches %d components; results merge all matches",
		identity,
		matches,
	)}
}

func fromComponentRecord(record state.ComponentRecord) Component {
	evidence := make([]Evidence, 0, len(record.Evidence))
	for _, item := range record.Evidence {
		evidence = append(evidence, fromComponentEvidence(item))
	}
	sortEvidence(evidence)
	return Component{
		Kind:     record.Kind,
		Identity: record.Identity,
		Path:     record.Path,
		Language: record.Language,
		Internal: record.Internal,
		Evidence: evidence,
	}
}

func fromDependencyRecord(record state.DependencyRecord) Dependency {
	evidence := make([]Evidence, 0, len(record.Evidence))
	for _, item := range record.Evidence {
		evidence = append(evidence, fromComponentEvidence(item))
	}
	sortEvidence(evidence)
	return Dependency{
		Kind:         record.Kind,
		Source:       record.Source,
		Target:       record.Target,
		TargetModule: record.TargetModule,
		Language:     record.Language,
		Evidence:     evidence,
	}
}

func fromComponentEvidence(item components.SourceEvidence) Evidence {
	return Evidence{
		Path:        item.Path,
		BlobHash:    item.BlobHash,
		Declaration: item.Declaration,
		Line:        item.Line,
	}
}

func componentIndex(record state.ComponentObservation) map[string]Component {
	index := make(map[string]Component, len(record.Components))
	for _, stored := range record.Components {
		component := fromComponentRecord(stored)
		index[componentKey(component.Kind, component.Identity)] = component
	}
	return index
}

func dependencyIndex(record state.ComponentObservation) map[string]Dependency {
	index := make(map[string]Dependency, len(record.Dependencies))
	for _, stored := range record.Dependencies {
		dependency := fromDependencyRecord(stored)
		index[dependencyKey(dependency.Source, dependency.Target)] = dependency
	}
	return index
}

func missingComponents(present, absent map[string]Component) []Component {
	result := make([]Component, 0)
	for key, component := range present {
		if _, ok := absent[key]; !ok {
			result = append(result, component)
		}
	}
	sortComponents(result)
	return result
}

func missingDependencies(present, absent map[string]Dependency) []Dependency {
	result := make([]Dependency, 0)
	for key, dependency := range present {
		if _, ok := absent[key]; !ok {
			result = append(result, dependency)
		}
	}
	sortDependencies(result)
	return result
}

func componentKey(kind, identity string) string {
	return kind + "\x00" + identity
}

func dependencyKey(source, target string) string {
	return source + "\x00" + target
}

func sortComponents(components []Component) {
	sort.Slice(components, func(i, j int) bool {
		a, b := components[i], components[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Identity != b.Identity {
			return a.Identity < b.Identity
		}
		return a.Path < b.Path
	})
}

func sortDependencies(dependencies []Dependency) {
	sort.Slice(dependencies, func(i, j int) bool {
		a, b := dependencies[i], dependencies[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.TargetModule < b.TargetModule
	})
}

func sortEvidence(evidence []Evidence) {
	sort.Slice(evidence, func(i, j int) bool {
		a, b := evidence[i], evidence[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Declaration != b.Declaration {
			return a.Declaration < b.Declaration
		}
		return a.BlobHash < b.BlobHash
	})
}

func sortReclassifications(items []Reclassification) {
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Target < b.Target
	})
}
