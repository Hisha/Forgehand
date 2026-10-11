// Package inventory builds deterministic, evidence-backed inventories of a
// committed Git tree. It classifies every committed blob, derives a directory
// structure, and reuses the discovery classifiers for programming language and
// build-system detection. Inventory output is read-only: it never reads the
// working tree and never executes repository-provided code.
package inventory

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/Hisha/Forgehand/internal/discovery"
	"github.com/Hisha/Forgehand/internal/repository"
)

const EngineVersion = 1

const DefaultMaxFiles = 200000

// Category partitions committed files into coarse, orthogonal roles. Each file
// maps to exactly one category.
type Category string

const (
	CategorySource        Category = "source"
	CategoryConfiguration Category = "configuration"
	CategoryDocumentation Category = "documentation"
	CategoryBuild         Category = "build"
	CategoryData          Category = "data"
	CategoryOther         Category = "other"
)

// Budget bounds resource consumption during a single inventory.
type Budget struct {
	// MaxFiles caps the number of committed files that are inventoried.
	// When exceeded, the inventory is marked truncated instead of stopping.
	MaxFiles int
}

func DefaultBudget() Budget {
	return Budget{MaxFiles: DefaultMaxFiles}
}

// File is one committed blob's inventory record.
type File struct {
	Path     string
	BlobHash string
	Mode     string
	Size     int64
	Language string
	Category Category
}

// Directory counts the committed files contained recursively underneath a
// repository directory. The root directory is omitted.
type Directory struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
}

type CategoryCount struct {
	Category Category `json:"category"`
	Count    int      `json:"count"`
}

// Summary aggregates an inventory. Languages and build systems reuse the
// discovery types so the same classification logic stays authoritative.
type Summary struct {
	TrackedFiles      int                              `json:"tracked_files"`
	UnclassifiedFiles int                              `json:"unclassified_files"`
	Languages         []discovery.LanguageCount        `json:"languages"`
	BuildSystems      []discovery.BuildSystemIndicator `json:"build_systems"`
	Directories       []Directory                      `json:"directories"`
	Categories        []CategoryCount                  `json:"categories"`
	Truncated         bool                             `json:"truncated"`
}

// Result is the deterministic output of a single committed-tree inventory.
type Result struct {
	Files   []File
	Summary Summary
}

// Build analyzes the committed tree at commitHash. It pins analysis to that
// exact commit regardless of later working-tree or HEAD changes. build order
// is deterministic, cancellation is honored between batches, and no
// repository-provided code is executed.
func Build(
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

	truncated := false
	if budget.MaxFiles > 0 && len(gitFiles) > budget.MaxFiles {
		gitFiles = gitFiles[:budget.MaxFiles]
		truncated = true
	}

	sort.Slice(gitFiles, func(i, j int) bool {
		return gitFiles[i].Path < gitFiles[j].Path
	})

	files := make([]File, 0, len(gitFiles))
	languageTotals := make(map[string]int)
	categoryTotals := make(map[Category]int)
	indicators := make([]discovery.BuildSystemIndicator, 0)
	directoryTotals := make(map[string]int)
	unclassified := 0

	for i, gitFile := range gitFiles {
		if i%512 == 0 {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
		}

		language, hasLanguage := discovery.ClassifyLanguage(gitFile.Path)
		category := ClassifyCategory(gitFile.Path, language)

		if hasLanguage {
			languageTotals[language]++
		} else {
			unclassified++
		}
		categoryTotals[category]++

		if name, ok := discovery.ClassifyBuildSystem(gitFile.Path); ok {
			indicators = append(indicators, discovery.BuildSystemIndicator{
				Name: name,
				Evidence: discovery.EvidenceReference{
					Path: gitFile.Path,
				},
			})
		}

		for _, directory := range parentDirectories(gitFile.Path) {
			directoryTotals[directory]++
		}

		files = append(files, File{
			Path:     gitFile.Path,
			BlobHash: gitFile.BlobHash,
			Mode:     gitFile.Mode,
			Size:     gitFile.Size,
			Language: language,
			Category: category,
		})
	}

	languages := make([]discovery.LanguageCount, 0, len(languageTotals))
	for language, count := range languageTotals {
		languages = append(languages, discovery.LanguageCount{
			Language: language,
			Count:    count,
		})
	}
	sort.Slice(languages, func(i, j int) bool {
		return languages[i].Language < languages[j].Language
	})

	categories := make([]CategoryCount, 0, len(categoryTotals))
	for category, count := range categoryTotals {
		categories = append(categories, CategoryCount{
			Category: category,
			Count:    count,
		})
	}
	sort.Slice(categories, func(i, j int) bool {
		return categories[i].Category < categories[j].Category
	})

	directories := make([]Directory, 0, len(directoryTotals))
	for directory, count := range directoryTotals {
		directories = append(directories, Directory{
			Path:  directory,
			Files: count,
		})
	}
	sort.Slice(directories, func(i, j int) bool {
		return directories[i].Path < directories[j].Path
	})

	sort.Slice(indicators, func(i, j int) bool {
		if indicators[i].Name != indicators[j].Name {
			return indicators[i].Name < indicators[j].Name
		}
		return indicators[i].Evidence.Path < indicators[j].Evidence.Path
	})

	return Result{
		Files: files,
		Summary: Summary{
			TrackedFiles:      len(files),
			UnclassifiedFiles: unclassified,
			Languages:         languages,
			BuildSystems:      indicators,
			Directories:       directories,
			Categories:        categories,
			Truncated:         truncated,
		},
	}, nil
}

// parentDirectories returns every non-root ancestor directory of a committed
// path, in deterministic order.
func parentDirectories(filePath string) []string {
	directories := make([]string, 0)
	current := path.Dir(filePath)
	for current != "." && current != "/" && current != "" {
		directories = append([]string{current}, directories...)
		current = path.Dir(current)
	}
	return directories
}

// ClassifyCategory is a deterministic, non-content heuristic that partitions a
// committed file path into a coarse category. Build-system detection takes
// priority because it identifies the project's build files. The classifier is
// exported so component analysis can share one authoritative classification.
func ClassifyCategory(filePath string, language string) Category {
	base := strings.ToLower(path.Base(filePath))
	extension := strings.ToLower(path.Ext(base))

	if extension == ".cmake" {
		return CategoryBuild
	}
	if _, ok := discovery.ClassifyBuildSystem(filePath); ok {
		return CategoryBuild
	}
	if isDocumentationPath(base, extension) {
		return CategoryDocumentation
	}
	if isConfigurationPath(base, extension) {
		return CategoryConfiguration
	}
	if extension == ".sql" {
		return CategoryData
	}
	if language != "" {
		return CategorySource
	}
	return CategoryOther
}

func isDocumentationPath(base, extension string) bool {
	switch extension {
	case ".md", ".markdown", ".mdown", ".adoc", ".rst":
		return true
	}

	stem := strings.TrimSuffix(base, extension)
	switch stem {
	case "readme", "contributing", "changelog", "changes", "authors", "copying", "license", "licence", "notice":
		return true
	}
	return strings.HasPrefix(stem, "license-") || strings.HasPrefix(stem, "notice-")
}

func isConfigurationPath(base, extension string) bool {
	switch extension {
	case ".conf", ".toml", ".ini", ".cfg", ".properties", ".env", ".yaml", ".yml", ".json":
		return true
	}

	switch base {
	case ".gitignore", ".gitattributes", ".gitmodules", ".dockerignore", ".editorconfig":
		return true
	case "dockerfile":
		return true
	}
	return strings.HasPrefix(base, ".env.")
}
