package discovery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestLanguageClassification(t *testing.T) {
	tests := map[string]string{
		"main.go":          "Go",
		"main.c":           "C",
		"main.cpp":         "C++",
		"main.cs":          "C#",
		"main.py":          "Python",
		"main.rs":          "Rust",
		"Main.java":        "Java",
		"main.js":          "JavaScript",
		"main.ts":          "TypeScript",
		"main.lua":         "Lua",
		"schema.sql":       "SQL",
		"script.sh":        "Shell",
		"document.xml":     "XML",
		"data.json":        "JSON",
		"config.yaml":      "YAML",
		"README.md":        "Markdown",
		"module.cmake":     "CMake",
		"CMakeLists.txt":   "CMake",
		"tools/SConstruct": "Python",
		"home/.bashrc":     "Shell",
	}

	for filePath, want := range tests {
		t.Run(filePath, func(t *testing.T) {
			got, ok := classifyLanguage(filePath)
			if !ok || got != want {
				t.Fatalf("classifyLanguage(%q) = %q, %v; want %q, true", filePath, got, ok, want)
			}
		})
	}
}

func TestAmbiguousHeaderRemainsUnclassified(t *testing.T) {
	if language, ok := classifyLanguage("include/shared.h"); ok {
		t.Fatalf("ambiguous .h classified as %q", language)
	}
	if language, ok := classifyLanguage("include/shared.hpp"); !ok || language != "C++" {
		t.Fatalf("explicit C++ header classified as %q, %v", language, ok)
	}
}

func TestUnknownFileRemainsUnclassified(t *testing.T) {
	if language, ok := classifyLanguage("assets/archive.weird"); ok {
		t.Fatalf("unknown file classified as %q", language)
	}
}

func TestBuildSystemDetectionIncludesNestedEvidence(t *testing.T) {
	tests := map[string]string{
		"go.mod":                    "Go modules",
		"native/CMakeLists.txt":     "CMake",
		"src/App.csproj":            ".NET project",
		"solutions/App.sln":         ".NET solution",
		"solutions/App.slnx":        ".NET solution",
		"rust/Cargo.toml":           "Cargo",
		"python/pyproject.toml":     "Python pyproject",
		"legacy/setup.py":           "Python setuptools",
		"web/package.json":          "Node.js package",
		"java/pom.xml":              "Maven",
		"gradle/build.gradle":       "Gradle",
		"gradle/build.gradle.kts":   "Gradle",
		"vendor/component/Makefile": "Make",
	}

	for filePath, want := range tests {
		got, ok := classifyBuildSystem(filePath)
		if !ok || got != want {
			t.Fatalf("classifyBuildSystem(%q) = %q, %v; want %q, true", filePath, got, ok, want)
		}
	}
}

func TestDiscoverCleanRepository(t *testing.T) {
	root := newDiscoveryRepository(t, map[string]string{
		"go.mod":    "module example.invalid/clean\n",
		"main.go":   "package main\n",
		"README.md": "# Clean\n",
	})
	commit := discoveryGitOutput(t, root, "rev-parse", "HEAD")

	summary, err := Discover(context.Background(), root, commit)
	if err != nil {
		t.Fatalf("discover clean repository: %v", err)
	}
	if summary.CommitHash != commit || summary.TrackedFiles != 3 {
		t.Fatalf("clean discovery summary = %#v", summary)
	}
	assertLanguageCount(t, summary, "Go", 1)
	assertLanguageCount(t, summary, "Markdown", 1)
	if summary.UnclassifiedFiles != 1 {
		t.Fatalf("unclassified files = %d, want 1", summary.UnclassifiedFiles)
	}
	wantBuild := []BuildSystemIndicator{
		{Name: "Go modules", Evidence: EvidenceReference{Path: "go.mod"}},
	}
	if !reflect.DeepEqual(summary.BuildSystems, wantBuild) {
		t.Fatalf("build indicators = %#v, want %#v", summary.BuildSystems, wantBuild)
	}
}

func TestDiscoverIsDeterministicAndOrdered(t *testing.T) {
	root := newDiscoveryRepository(t, map[string]string{
		"z/main.py":             "print('z')\n",
		"a/main.go":             "package main\n",
		"z/package.json":        "{}\n",
		"a/package.json":        "{}\n",
		"native/CMakeLists.txt": "cmake_minimum_required(VERSION 3.20)\n",
		"include/ambiguous.h":   "/* ambiguous */\n",
	})
	commit := discoveryGitOutput(t, root, "rev-parse", "HEAD")

	first, err := Discover(context.Background(), root, commit)
	if err != nil {
		t.Fatalf("first discovery: %v", err)
	}
	second, err := Discover(context.Background(), root, commit)
	if err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated discovery differs:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if !sort.SliceIsSorted(first.Languages, func(i, j int) bool {
		return first.Languages[i].Language < first.Languages[j].Language
	}) {
		t.Fatalf("languages are not sorted: %#v", first.Languages)
	}
	if !sort.SliceIsSorted(first.BuildSystems, func(i, j int) bool {
		if first.BuildSystems[i].Name != first.BuildSystems[j].Name {
			return first.BuildSystems[i].Name < first.BuildSystems[j].Name
		}
		return first.BuildSystems[i].Evidence.Path < first.BuildSystems[j].Evidence.Path
	}) {
		t.Fatalf("build indicators are not sorted: %#v", first.BuildSystems)
	}
	if first.UnclassifiedFiles != 1 {
		t.Fatalf("unclassified files = %d, want 1", first.UnclassifiedFiles)
	}
	wantIndicators := []BuildSystemIndicator{
		{Name: "CMake", Evidence: EvidenceReference{Path: "native/CMakeLists.txt"}},
		{Name: "Node.js package", Evidence: EvidenceReference{Path: "a/package.json"}},
		{Name: "Node.js package", Evidence: EvidenceReference{Path: "z/package.json"}},
	}
	if !reflect.DeepEqual(first.BuildSystems, wantIndicators) {
		t.Fatalf("build indicators = %#v, want %#v", first.BuildSystems, wantIndicators)
	}
}

func TestDiscoverUsesExactHistoricalCommit(t *testing.T) {
	root := newDiscoveryRepository(t, map[string]string{
		"old.go": "package old\n",
	})
	oldCommit := discoveryGitOutput(t, root, "rev-parse", "HEAD")

	if err := os.Remove(filepath.Join(root, "old.go")); err != nil {
		t.Fatalf("remove old source: %v", err)
	}
	writeDiscoveryFile(t, root, "new.py", "print('new')\n")
	runDiscoveryGit(t, root, "add", "--all")
	runDiscoveryGit(t, root, "commit", "-m", "replace language")
	newCommit := discoveryGitOutput(t, root, "rev-parse", "HEAD")

	oldSummary, err := Discover(context.Background(), root, oldCommit)
	if err != nil {
		t.Fatalf("discover historical commit: %v", err)
	}
	newSummary, err := Discover(context.Background(), root, newCommit)
	if err != nil {
		t.Fatalf("discover current commit: %v", err)
	}

	assertLanguageCount(t, oldSummary, "Go", 1)
	assertLanguageCount(t, newSummary, "Python", 1)
	if oldSummary.CommitHash != oldCommit || newSummary.CommitHash != newCommit {
		t.Fatalf("discovery did not retain exact commit hashes")
	}
}

func TestDirtyWorkingTreeDoesNotContaminateDiscoveryOrMutateGit(t *testing.T) {
	root := newDiscoveryRepository(t, map[string]string{
		"main.go": "package main\n",
	})
	commit := discoveryGitOutput(t, root, "rev-parse", "HEAD")
	writeDiscoveryFile(t, root, "main.go", "dirty working content\n")
	writeDiscoveryFile(t, root, "untracked.py", "print('untracked')\n")
	statusBefore := discoveryGitOutput(t, root, "status", "--porcelain=v2", "--untracked-files=all")

	summary, err := Discover(context.Background(), root, commit)
	if err != nil {
		t.Fatalf("discover dirty repository: %v", err)
	}
	statusAfter := discoveryGitOutput(t, root, "status", "--porcelain=v2", "--untracked-files=all")
	headAfter := discoveryGitOutput(t, root, "rev-parse", "HEAD")

	if summary.TrackedFiles != 1 || summary.UnclassifiedFiles != 0 {
		t.Fatalf("dirty discovery summary = %#v", summary)
	}
	assertLanguageCount(t, summary, "Go", 1)
	if statusAfter != statusBefore {
		t.Fatalf("discovery changed Git status:\nbefore=%q\nafter=%q", statusBefore, statusAfter)
	}
	if headAfter != commit {
		t.Fatalf("discovery changed HEAD from %s to %s", commit, headAfter)
	}
}

func assertLanguageCount(t *testing.T, summary Summary, language string, want int) {
	t.Helper()
	for _, count := range summary.Languages {
		if count.Language == language {
			if count.Count != want {
				t.Fatalf("%s count = %d, want %d", language, count.Count, want)
			}
			return
		}
	}
	t.Fatalf("language %q missing from %#v", language, summary.Languages)
}

func newDiscoveryRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create repository: %v", err)
	}
	runDiscoveryGit(t, root, "init")
	runDiscoveryGit(t, root, "config", "user.name", "Forgehand Test")
	runDiscoveryGit(t, root, "config", "user.email", "forgehand@example.invalid")
	for filePath, content := range files {
		writeDiscoveryFile(t, root, filePath, content)
	}
	runDiscoveryGit(t, root, "add", "--all")
	runDiscoveryGit(t, root, "commit", "-m", "initial")
	return root
}

func writeDiscoveryFile(t *testing.T, root, filePath, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filePath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", filePath, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filePath, err)
	}
}

func runDiscoveryGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func discoveryGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}
