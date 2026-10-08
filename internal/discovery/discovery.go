package discovery

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/Hisha/Forgehand/internal/repository"
)

type Summary struct {
	CommitHash        string                 `json:"commit_hash"`
	TrackedFiles      int                    `json:"tracked_files"`
	UnclassifiedFiles int                    `json:"unclassified_files"`
	Languages         []LanguageCount        `json:"languages"`
	BuildSystems      []BuildSystemIndicator `json:"build_systems"`
}

type LanguageCount struct {
	Language string `json:"language"`
	Count    int    `json:"count"`
}

type EvidenceReference struct {
	Path string `json:"path"`
}

type BuildSystemIndicator struct {
	Name     string            `json:"name"`
	Evidence EvidenceReference `json:"evidence"`
}

var extensionLanguages = map[string]string{
	".bash":     "Shell",
	".c":        "C",
	".cc":       "C++",
	".cmake":    "CMake",
	".cpp":      "C++",
	".cs":       "C#",
	".cts":      "TypeScript",
	".cxx":      "C++",
	".go":       "Go",
	".hh":       "C++",
	".hpp":      "C++",
	".hxx":      "C++",
	".java":     "Java",
	".js":       "JavaScript",
	".json":     "JSON",
	".jsx":      "JavaScript",
	".lua":      "Lua",
	".markdown": "Markdown",
	".md":       "Markdown",
	".mjs":      "JavaScript",
	".mts":      "TypeScript",
	".py":       "Python",
	".pyw":      "Python",
	".rs":       "Rust",
	".sh":       "Shell",
	".sql":      "SQL",
	".ts":       "TypeScript",
	".tsx":      "TypeScript",
	".xml":      "XML",
	".xsd":      "XML",
	".xsl":      "XML",
	".xslt":     "XML",
	".yaml":     "YAML",
	".yml":      "YAML",
	".zsh":      "Shell",
}

var filenameLanguages = map[string]string{
	".bash_profile":  "Shell",
	".bashrc":        "Shell",
	".profile":       "Shell",
	".zshrc":         "Shell",
	"cmakelists.txt": "CMake",
	"sconscript":     "Python",
	"sconstruct":     "Python",
}

func Discover(
	ctx context.Context,
	rootPath string,
	commitHash string,
) (Summary, error) {
	commitHash = strings.TrimSpace(commitHash)
	if commitHash == "" {
		return Summary{}, fmt.Errorf("commit hash must not be empty")
	}

	files, err := repository.ListTree(ctx, rootPath, commitHash)
	if err != nil {
		return Summary{}, fmt.Errorf("list committed repository tree: %w", err)
	}

	languageTotals := make(map[string]int)
	indicators := make([]BuildSystemIndicator, 0)
	unclassified := 0

	for _, file := range files {
		if language, ok := classifyLanguage(file.Path); ok {
			languageTotals[language]++
		} else {
			unclassified++
		}

		if name, ok := classifyBuildSystem(file.Path); ok {
			indicators = append(indicators, BuildSystemIndicator{
				Name:     name,
				Evidence: EvidenceReference{Path: file.Path},
			})
		}
	}

	languages := make([]LanguageCount, 0, len(languageTotals))
	for language, count := range languageTotals {
		languages = append(languages, LanguageCount{
			Language: language,
			Count:    count,
		})
	}
	sort.Slice(languages, func(i, j int) bool {
		return languages[i].Language < languages[j].Language
	})
	sort.Slice(indicators, func(i, j int) bool {
		if indicators[i].Name != indicators[j].Name {
			return indicators[i].Name < indicators[j].Name
		}
		return indicators[i].Evidence.Path < indicators[j].Evidence.Path
	})

	return Summary{
		CommitHash:        commitHash,
		TrackedFiles:      len(files),
		UnclassifiedFiles: unclassified,
		Languages:         languages,
		BuildSystems:      indicators,
	}, nil
}

func classifyLanguage(filePath string) (string, bool) {
	base := strings.ToLower(path.Base(filePath))
	if language, ok := filenameLanguages[base]; ok {
		return language, true
	}

	extension := strings.ToLower(path.Ext(base))
	if extension == ".h" {
		return "", false
	}
	language, ok := extensionLanguages[extension]
	return language, ok
}

func classifyBuildSystem(filePath string) (string, bool) {
	base := path.Base(filePath)
	lower := strings.ToLower(base)

	switch {
	case base == "CMakeLists.txt":
		return "CMake", true
	case base == "Makefile":
		return "Make", true
	case lower == "cargo.toml":
		return "Cargo", true
	case lower == "go.mod":
		return "Go modules", true
	case lower == "package.json":
		return "Node.js package", true
	case lower == "pom.xml":
		return "Maven", true
	case lower == "pyproject.toml":
		return "Python pyproject", true
	case lower == "setup.py":
		return "Python setuptools", true
	case lower == "build.gradle" || lower == "build.gradle.kts":
		return "Gradle", true
	case strings.HasSuffix(lower, ".csproj"):
		return ".NET project", true
	case strings.HasSuffix(lower, ".sln") || strings.HasSuffix(lower, ".slnx"):
		return ".NET solution", true
	default:
		return "", false
	}
}
