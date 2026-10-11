// Package components deterministically identifies structural software
// components and the dependencies between them from a pinned committed Git
// tree. Analysis is read-only: it reads committed blobs through Git primitives,
// never the working tree, and never executes repository-provided code or
// resolves dependencies with external tools or the network.
//
// The representation is language-neutral: components carry an optional language
// label and dependencies classify imports into internal, external-module,
// standard-library, and unresolved. Only Go source is parsed in this milestone;
// general repository components are derived from committed build and
// configuration files.
package components

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strings"

	"github.com/Hisha/Forgehand/internal/discovery"
	"github.com/Hisha/Forgehand/internal/inventory"
	"github.com/Hisha/Forgehand/internal/repository"
)

const EngineVersion = 1

const (
	DefaultMaxFiles     = 200000
	DefaultMaxBlobBytes = 64 << 20
)

// Budget bounds resource consumption during a single analysis.
type Budget struct {
	// MaxFiles caps the number of committed Go source and go.mod files that are
	// analyzed. When exceeded, the analysis is marked truncated.
	MaxFiles int
	// MaxBlobBytes caps the size of a single committed blob read for parsing.
	// Larger files are skipped and recorded as skipped sources, marking the
	// analysis partial.
	MaxBlobBytes int64
}

func DefaultBudget() Budget {
	return Budget{MaxFiles: DefaultMaxFiles, MaxBlobBytes: DefaultMaxBlobBytes}
}

// ComponentKind is the structural role of a component.
type ComponentKind string

const (
	KindModule        ComponentKind = "module"
	KindPackage       ComponentKind = "package"
	KindCommand       ComponentKind = "command"
	KindBuild         ComponentKind = "build"
	KindConfiguration ComponentKind = "configuration"
)

// DependencyKind classifies an observed import.
type DependencyKind string

const (
	DependencyInternal       DependencyKind = "internal"
	DependencyExternalModule DependencyKind = "external-module"
	DependencyStdlib         DependencyKind = "stdlib"
	DependencyUnresolved     DependencyKind = "unresolved"
)

// SourceEvidence ties a fact to a specific committed blob and declaration.
type SourceEvidence struct {
	Path        string
	BlobHash    string
	Declaration string
	Line        int
}

// Component is a structural unit with a stable, deterministic identity.
type Component struct {
	Kind     ComponentKind
	Identity string
	Path     string
	Language string
	Internal bool
	Evidence []SourceEvidence
}

// Dependency is an import observed in analyzed source, classified against
// repository-internal, declared-module, and embedded stdlib references.
type Dependency struct {
	Kind         DependencyKind
	Source       string
	Target       string
	TargetModule string
	Language     string
	Evidence     []SourceEvidence
}

// DeclaredModule is a module requirement declared in a committed go.mod. It is
// a declared fact and is reported separately from imports observed in source.
type DeclaredModule struct {
	Path     string
	Version  string
	Indirect bool
}

// SkippedFile records a committed source that was not analyzed and why.
type SkippedFile struct {
	Path     string
	BlobHash string
	Reason   string
}

// DependencyCount aggregates observed dependencies by classification.
type DependencyCount struct {
	Kind  DependencyKind
	Count int
}

// Summary reports analysis completeness and aggregate counts. Freshness of an
// analysis relative to repository HEAD is reported separately by callers.
type Summary struct {
	Complete                bool
	Truncated               bool
	Sources                 int
	Analyzed                int
	Skipped                 int
	Modules                 int
	Packages                int
	Commands                int
	BuildComponents         int
	ConfigurationComponents int
	ModuleRequirements      []DeclaredModule
	SkippedFiles            []SkippedFile
	Dependencies            []DependencyCount
}

// Result is the deterministic output of a single committed-tree analysis.
type Result struct {
	Components   []Component
	Dependencies []Dependency
	Summary      Summary
}

// Analyze identifies components and dependencies in the committed tree at
// commitHash. It pins analysis to that exact commit regardless of later
// working-tree or HEAD changes. Output is fully sorted and deterministic, and
// every dependency is supported by an observed import declaration.
func Analyze(
	ctx context.Context,
	rootPath string,
	commitHash string,
	budget Budget,
) (Result, error) {
	commitHash = strings.TrimSpace(commitHash)
	if commitHash == "" {
		return Result{}, fmt.Errorf("commit hash must not be empty")
	}

	gitFiles, err := repository.ListTree(ctx, rootPath, commitHash)
	if err != nil {
		return Result{}, fmt.Errorf("list committed repository tree: %w", err)
	}
	sort.Slice(gitFiles, func(i, j int) bool {
		return gitFiles[i].Path < gitFiles[j].Path
	})

	sources := sourceRelevantFiles(gitFiles)
	truncated := false
	if budget.MaxFiles > 0 && len(sources) > budget.MaxFiles {
		sources = sources[:budget.MaxFiles]
		truncated = true
	}

	var skipped []SkippedFile

	modules, moreSkipped, err := analyzeGoModules(ctx, rootPath, sources, budget)
	if err != nil {
		return Result{}, err
	}
	skipped = append(skipped, moreSkipped...)

	files, moreSkipped, err := analyzeGoFiles(ctx, rootPath, sources, budget)
	if err != nil {
		return Result{}, err
	}
	skipped = append(skipped, moreSkipped...)

	packages := groupByDirectory(files)
	resolvePackageConflicts(packages)
	skipped = append(skipped, conflictingPackageSkips(packages)...)

	components, cells := buildComponents(packages, modules)
	components = append(buildModuleComponents(modules), components...)

	general := buildGeneralComponents(gitFiles, componentPaths(components))
	components = append(general, components...)
	sortComponents(components)

	dependencies, dependencyCounts := buildDependencies(packages, modules, cells)

	analyzed := 0
	for _, group := range packages {
		for _, record := range group {
			if record.skipReason == "" {
				analyzed++
			}
		}
	}

	sortSkipped(skipped)

	complete := !truncated && len(skipped) == 0

	return Result{
		Components:   components,
		Dependencies: dependencies,
		Summary: Summary{
			Complete:                complete,
			Truncated:               truncated,
			Sources:                 len(sources),
			Analyzed:                analyzed,
			Skipped:                 len(skipped),
			Modules:                 summaryKindCount(components, KindModule),
			Packages:                summaryKindCount(components, KindPackage),
			Commands:                summaryKindCount(components, KindCommand),
			BuildComponents:         summaryKindCount(components, KindBuild),
			ConfigurationComponents: summaryKindCount(components, KindConfiguration),
			ModuleRequirements:      declaredModuleRequirements(modules),
			SkippedFiles:            skipped,
			Dependencies:            dependencyCounts,
		},
	}, nil
}

type goModuleInfo struct {
	dir        string
	modulePath string
	goVersion  string
	requires   []DeclaredModule
	file       repository.GitFile
	moduleLine int
}

type importRef struct {
	importPath string
	line       int
}

type goFileRecord struct {
	file        repository.GitFile
	dir         string
	packageName string
	packageLine int
	hasMain     bool
	mainLine    int
	imports     []importRef
	skipReason  string
}

func sourceRelevantFiles(gitFiles []repository.GitFile) []repository.GitFile {
	sources := make([]repository.GitFile, 0, len(gitFiles))
	for _, file := range gitFiles {
		base := path.Base(file.Path)
		if strings.HasSuffix(base, ".go") || base == "go.mod" {
			sources = append(sources, file)
		}
	}
	return sources
}

func analyzeGoModules(
	ctx context.Context,
	rootPath string,
	sources []repository.GitFile,
	budget Budget,
) ([]goModuleInfo, []SkippedFile, error) {
	modules := make([]goModuleInfo, 0)
	var skipped []SkippedFile
	for _, source := range sources {
		if path.Base(source.Path) != "go.mod" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		parsed, err := readAndParseGoMod(ctx, rootPath, source, budget)
		if err != nil {
			skipped = appendSkipped(skipped, source, "malformed go.mod: "+err.Error())
			continue
		}

		modules = append(modules, goModuleInfo{
			dir:        path.Dir(source.Path),
			modulePath: parsed.modulePath,
			goVersion:  parsed.goVersion,
			requires:   parsed.requires,
			file:       source,
			moduleLine: parsed.moduleLine,
		})
	}
	return modules, skipped, nil
}

func readAndParseGoMod(
	ctx context.Context,
	rootPath string,
	source repository.GitFile,
	budget Budget,
) (parsedGoMod, error) {
	content, err := readCommittedBlob(ctx, rootPath, source, budget)
	if err != nil {
		return parsedGoMod{}, err
	}
	return parseGoMod(source.Path, content)
}

func analyzeGoFiles(
	ctx context.Context,
	rootPath string,
	sources []repository.GitFile,
	budget Budget,
) ([]goFileRecord, []SkippedFile, error) {
	records := make([]goFileRecord, 0)
	var skipped []SkippedFile
	for _, source := range sources {
		if !strings.HasSuffix(path.Base(source.Path), ".go") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		content, err := readCommittedBlob(ctx, rootPath, source, budget)
		if err != nil {
			skipped = appendSkipped(skipped, source, err.Error())
			continue
		}

		record, err := parseGoFile(source, content)
		if err != nil {
			skipped = appendSkipped(skipped, source, "malformed source: "+err.Error())
			continue
		}
		records = append(records, record)
	}
	return records, skipped, nil
}

func readCommittedBlob(
	ctx context.Context,
	rootPath string,
	file repository.GitFile,
	budget Budget,
) ([]byte, error) {
	if budget.MaxBlobBytes > 0 && file.Size > budget.MaxBlobBytes {
		return nil, fmt.Errorf(
			"blob %d bytes exceeds analysis size budget %d bytes",
			file.Size,
			budget.MaxBlobBytes,
		)
	}
	return repository.ReadBlob(ctx, rootPath, file.BlobHash)
}

func appendSkipped(skipped []SkippedFile, file repository.GitFile, reason string) []SkippedFile {
	return append(skipped, SkippedFile{
		Path:     file.Path,
		BlobHash: file.BlobHash,
		Reason:   reason,
	})
}

func parseGoFile(file repository.GitFile, content []byte) (goFileRecord, error) {
	record := goFileRecord{file: file, dir: path.Dir(file.Path)}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file.Path, content, parser.SkipObjectResolution)
	if err != nil {
		return record, err
	}

	record.packageName = parsed.Name.Name
	record.packageLine = fset.Position(parsed.Name.Pos()).Line
	record.imports = collectImports(fset, parsed)
	record.hasMain, record.mainLine = collectMainFunction(fset, parsed)
	return record, nil
}

func collectImports(fset *token.FileSet, file *ast.File) []importRef {
	imports := make([]importRef, 0, len(file.Imports))
	for _, spec := range file.Imports {
		imports = append(imports, importRef{
			importPath: strings.Trim(spec.Path.Value, `"`),
			line:       fset.Position(spec.Pos()).Line,
		})
	}
	return imports
}

func collectMainFunction(fset *token.FileSet, file *ast.File) (bool, int) {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name != "main" {
			continue
		}
		if function.Type.Params != nil && len(function.Type.Params.List) > 0 {
			continue
		}
		if function.Type.Results != nil && len(function.Type.Results.List) > 0 {
			continue
		}
		return true, fset.Position(function.Pos()).Line
	}
	return false, 0
}

func groupByDirectory(records []goFileRecord) map[string][]*goFileRecord {
	grouped := make(map[string][]*goFileRecord)
	for index := range records {
		record := &records[index]
		grouped[record.dir] = append(grouped[record.dir], record)
	}
	return grouped
}

// resolvePackageConflicts marks files whose package name differs from the
// lexically-first file in the same directory. Go requires one package name per
// directory; the remaining files form the authoritative package.
func resolvePackageConflicts(packages map[string][]*goFileRecord) {
	for _, records := range packages {
		if len(records) < 2 {
			continue
		}
		primary := records[0].packageName
		for _, record := range records[1:] {
			if record.packageName != primary {
				record.skipReason = "conflicting package name"
			}
		}
	}
}

func conflictingPackageSkips(packages map[string][]*goFileRecord) []SkippedFile {
	var skipped []SkippedFile
	for _, directory := range sortedDirectories(packages) {
		for _, record := range packages[directory] {
			if record.skipReason != "conflicting package name" {
				continue
			}
			skipped = append(skipped, SkippedFile{
				Path:     record.file.Path,
				BlobHash: record.file.BlobHash,
				Reason:   record.skipReason,
			})
		}
	}
	return skipped
}

// componentCell is an identified package or command component. The set of cell
// identities is exactly the set of repository-internal dependencies.
type componentCell struct {
	component Component
}

func buildComponents(
	packages map[string][]*goFileRecord,
	modules []goModuleInfo,
) ([]Component, map[string]componentCell) {
	components := make([]Component, 0)
	cells := make(map[string]componentCell)

	for _, dir := range sortedDirectories(packages) {
		records := packages[dir]
		if len(records) == 0 {
			continue
		}

		primary := records[0]
		hasMain := false
		evidence := make([]SourceEvidence, 0, len(records))
		for _, record := range records {
			if record.skipReason != "" {
				continue
			}
			evidence = append(evidence, SourceEvidence{
				Path:        record.file.Path,
				BlobHash:    record.file.BlobHash,
				Declaration: "package " + record.packageName,
				Line:        record.packageLine,
			})
			if record.hasMain {
				evidence = append(evidence, SourceEvidence{
					Path:        record.file.Path,
					BlobHash:    record.file.BlobHash,
					Declaration: "func main()",
					Line:        record.mainLine,
				})
			}
			hasMain = hasMain || record.hasMain
		}
		if len(evidence) == 0 {
			continue
		}

		identity, internal := packageIdentity(dir, modules)
		kind := KindPackage
		if primary.packageName == "main" && hasMain {
			kind = KindCommand
		}

		component := Component{
			Kind:     kind,
			Identity: identity,
			Path:     dir,
			Language: "Go",
			Internal: internal,
			Evidence: sortEvidence(evidence),
		}
		components = append(components, component)
		cells[identity] = componentCell{component: component}
	}
	return components, cells
}

func buildModuleComponents(modules []goModuleInfo) []Component {
	components := make([]Component, 0, len(modules))
	for _, module := range modules {
		components = append(components, Component{
			Kind:     KindModule,
			Identity: module.modulePath,
			Path:     module.dir,
			Language: "Go",
			Internal: false,
			Evidence: []SourceEvidence{
				{
					Path:        module.file.Path,
					BlobHash:    module.file.BlobHash,
					Declaration: "module " + module.modulePath,
					Line:        module.moduleLine,
				},
			},
		})
	}
	return components
}

func packageIdentity(dir string, modules []goModuleInfo) (string, bool) {
	module, relDir := nearestModule(dir, modules)
	if module == nil {
		identity := relRepositoryPath(dir)
		return identity, hasInternalElement(identity)
	}
	identity := module.modulePath
	if relDir != "" {
		identity += "/" + relDir
	}
	return identity, hasInternalElement(identity)
}

func nearestModule(dir string, modules []goModuleInfo) (*goModuleInfo, string) {
	var best *goModuleInfo
	bestLen := -1
	for index := range modules {
		module := &modules[index]
		if !moduleContainsDir(module.dir, dir) {
			continue
		}
		if len(module.dir) > bestLen {
			best = module
			bestLen = len(module.dir)
		}
	}
	if best == nil {
		return nil, ""
	}
	if dir == best.dir {
		return best, ""
	}
	if best.dir == "." {
		return best, dir
	}
	return best, strings.TrimPrefix(dir, best.dir+"/")
}

// moduleContainsDir reports whether dir lies inside the module rooted at
// moduleDir. The repository-root module (".") contains every directory.
func moduleContainsDir(moduleDir, dir string) bool {
	if moduleDir == "." {
		return true
	}
	return dir == moduleDir || strings.HasPrefix(dir, moduleDir+"/")
}

func relRepositoryPath(dir string) string {
	if dir == "." {
		return "."
	}
	return dir
}

func hasInternalElement(value string) bool {
	for _, element := range strings.Split(value, "/") {
		if element == "internal" {
			return true
		}
	}
	return false
}

func buildGeneralComponents(
	gitFiles []repository.GitFile,
	occupied map[string]bool,
) []Component {
	buildDirs := make(map[string][]repository.GitFile)
	configDirs := make(map[string][]repository.GitFile)

	for _, file := range gitFiles {
		language, _ := discovery.ClassifyLanguage(file.Path)
		dir := path.Dir(file.Path)
		if hasBuildIndicator(file.Path) {
			buildDirs[dir] = append(buildDirs[dir], file)
		} else if inventory.ClassifyCategory(file.Path, language) == inventory.CategoryConfiguration {
			configDirs[dir] = append(configDirs[dir], file)
		}
	}

	components := make([]Component, 0)
	for _, dir := range sortedFileDirs(buildDirs) {
		if occupied[dir] {
			continue
		}
		components = append(components, generalComponent(KindBuild, dir, buildDirs[dir]))
		occupied[dir] = true
	}

	for _, dir := range sortedFileDirs(configDirs) {
		if occupied[dir] {
			continue
		}
		components = append(components, generalComponent(KindConfiguration, dir, configDirs[dir]))
		occupied[dir] = true
	}

	return components
}

func generalComponent(kind ComponentKind, dir string, files []repository.GitFile) Component {
	evidence := make([]SourceEvidence, 0, len(files))
	for _, file := range files {
		evidence = append(evidence, SourceEvidence{
			Path:     file.Path,
			BlobHash: file.BlobHash,
			Line:     1,
		})
	}
	return Component{
		Kind:     kind,
		Identity: dir,
		Path:     dir,
		Language: "",
		Internal: false,
		Evidence: sortEvidence(evidence),
	}
}

func hasBuildIndicator(filePath string) bool {
	_, ok := discovery.ClassifyBuildSystem(filePath)
	return ok
}

func buildDependencies(
	packages map[string][]*goFileRecord,
	modules []goModuleInfo,
	cells map[string]componentCell,
) ([]Dependency, []DependencyCount) {
	requirements := declaredModuleRequirements(modules)

	type key struct {
		kind         DependencyKind
		source       string
		target       string
		targetModule string
	}
	aggregate := make(map[key]*Dependency)

	for _, dir := range sortedDirectories(packages) {
		identity, _ := packageIdentity(dir, modules)
		if _, ok := cells[identity]; !ok {
			continue
		}

		for _, record := range packages[dir] {
			if record.skipReason != "" {
				continue
			}
			for _, ref := range record.imports {
				importPath := strings.TrimSpace(ref.importPath)
				kind, targetModule := classifyImport(importPath, cells, requirements)
				candidate := key{kind: kind, source: identity, target: importPath, targetModule: targetModule}
				entry, ok := aggregate[candidate]
				if !ok {
					entry = &Dependency{
						Kind:         kind,
						Source:       identity,
						Target:       importPath,
						TargetModule: targetModule,
						Language:     "Go",
					}
					aggregate[candidate] = entry
				}
				entry.Evidence = append(entry.Evidence, SourceEvidence{
					Path:        record.file.Path,
					BlobHash:    record.file.BlobHash,
					Declaration: importPath,
					Line:        ref.line,
				})
			}
		}
	}

	dependencies := make([]Dependency, 0, len(aggregate))
	for _, entry := range aggregate {
		entry.Evidence = sortEvidence(entry.Evidence)
		dependencies = append(dependencies, *entry)
	}
	sort.Slice(dependencies, func(i, j int) bool {
		a, b := dependencies[i], dependencies[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.TargetModule < b.TargetModule
	})

	counts := make(map[DependencyKind]int)
	for _, dependency := range dependencies {
		counts[dependency.Kind]++
	}
	dependencyCounts := make([]DependencyCount, 0, len(counts))
	for kind, count := range counts {
		dependencyCounts = append(dependencyCounts, DependencyCount{Kind: kind, Count: count})
	}
	sort.Slice(dependencyCounts, func(i, j int) bool {
		return dependencyCounts[i].Kind < dependencyCounts[j].Kind
	})

	return dependencies, dependencyCounts
}

// classifyImport resolves an observed import path against the authoritative
// references available in the committed repository and the embedded stdlib
// reference. Classification is: internal when the path resolves to an
// identified repository package; external-module when a declared module
// requirement covers the path; stdlib when the embedded version-aware stdlib
// reference contains the path; otherwise unresolved. It never falls back to
// path-shape heuristics.
func classifyImport(
	importPath string,
	cells map[string]componentCell,
	requirements []DeclaredModule,
) (DependencyKind, string) {
	if importPath == "" {
		return DependencyUnresolved, ""
	}

	if _, ok := cells[importPath]; ok {
		return DependencyInternal, ""
	}

	if modulePath, ok := longestRequiredModule(importPath, requirements); ok {
		return DependencyExternalModule, modulePath
	}

	if isStandardLibrary(importPath) {
		return DependencyStdlib, ""
	}

	return DependencyUnresolved, ""
}

// longestRequiredModule returns the declared module whose path is the longest
// prefix of importPath (respecting package boundaries).
func longestRequiredModule(
	importPath string,
	requirements []DeclaredModule,
) (string, bool) {
	var best string
	bestLength := -1
	for _, requirement := range requirements {
		if importPath == requirement.Path ||
			strings.HasPrefix(importPath, requirement.Path+"/") {
			if len(requirement.Path) > bestLength {
				best = requirement.Path
				bestLength = len(requirement.Path)
			}
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}
