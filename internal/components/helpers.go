package components

import (
	"sort"

	"github.com/Hisha/Forgehand/internal/repository"
)

func sortComponents(components []Component) {
	sort.Slice(components, func(i, j int) bool {
		a, b := components[i], components[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Identity < b.Identity
	})
}

func sortSkipped(skipped []SkippedFile) {
	sort.Slice(skipped, func(i, j int) bool {
		return skipped[i].Path < skipped[j].Path
	})
}

func sortEvidence(evidence []SourceEvidence) []SourceEvidence {
	sort.SliceStable(evidence, func(i, j int) bool {
		a, b := evidence[i], evidence[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Declaration < b.Declaration
	})
	return evidence
}

func sortedDirectories(packages map[string][]*goFileRecord) []string {
	directories := make([]string, 0, len(packages))
	for directory := range packages {
		directories = append(directories, directory)
	}
	sort.Strings(directories)
	return directories
}

func sortedFileDirs(groups map[string][]repository.GitFile) []string {
	directories := make([]string, 0, len(groups))
	for directory := range groups {
		directories = append(directories, directory)
	}
	sort.Strings(directories)
	return directories
}

func declaredModuleRequirements(modules []goModuleInfo) []DeclaredModule {
	type key struct {
		path     string
		version  string
		indirect bool
	}
	seen := make(map[key]bool)
	requirements := make([]DeclaredModule, 0)
	for _, module := range modules {
		for _, requirement := range module.requires {
			k := key{path: requirement.Path, version: requirement.Version, indirect: requirement.Indirect}
			if seen[k] {
				continue
			}
			seen[k] = true
			requirements = append(requirements, requirement)
		}
	}
	sort.Slice(requirements, func(i, j int) bool {
		a, b := requirements[i], requirements[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return !a.Indirect && b.Indirect
	})
	return requirements
}

func summaryKindCount(components []Component, kind ComponentKind) int {
	count := 0
	for _, component := range components {
		if component.Kind == kind {
			count++
		}
	}
	return count
}

func componentPaths(components []Component) map[string]bool {
	occupied := make(map[string]bool)
	for _, component := range components {
		switch component.Kind {
		case KindModule, KindPackage, KindCommand:
			occupied[component.Path] = true
		}
	}
	return occupied
}
