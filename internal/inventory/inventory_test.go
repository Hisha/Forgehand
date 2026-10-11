package inventory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Hisha/Forgehand/internal/discovery"
)

func TestClassifyCategory(t *testing.T) {
	tests := map[string]Category{
		"main.go":           CategorySource,
		"src/util.hpp":      CategorySource,
		"tools/build.cmake": CategoryBuild,
		"CMakeLists.txt":    CategoryBuild,
		"go.mod":            CategoryBuild,
		"package.json":      CategoryBuild,
		"README.md":         CategoryDocumentation,
		"docs/guide.rst":    CategoryDocumentation,
		"LICENSE":           CategoryDocumentation,
		"config.yaml":       CategoryConfiguration,
		"settings.json":     CategoryConfiguration,
		".gitignore":        CategoryConfiguration,
		"Dockerfile":        CategoryConfiguration,
		"db/schema.sql":     CategoryData,
		"assets/logo.bin":   CategoryOther,
		"unknown.xyz":       CategoryOther,
	}

	for filePath, want := range tests {
		language, _ := discovery.ClassifyLanguage(filePath)
		got := ClassifyCategory(filePath, language)
		if got != want {
			t.Fatalf("ClassifyCategory(%q) = %q, want %q", filePath, got, want)
		}
	}
}

func TestBuildIsDeterministicAndOrdered(t *testing.T) {
	root := newInventoryRepository(t, map[string]string{
		"CMakeLists.txt":    "cmake_minimum_required(VERSION 3.20)\n",
		"README.md":         "# Readme\n",
		"config.yaml":       "key: value\n",
		"docs/guide.md":     "# Guide\n",
		"go.mod":            "module example.invalid/inventory\n",
		"main.go":           "package main\n",
		"assets/logo.bin":   "not text content\n",
		"schema.sql":        "CREATE TABLE t (id INTEGER);\n",
		"src/inner/deep.go": "package inner\n",
		"src/util.hpp":      "#pragma once\n",
	})
	commit := inventoryGitOutput(t, root, "rev-parse", "HEAD")

	first, err := Build(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	second, err := Build(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated builds differ:\nfirst=%#v\nsecond=%#v", first, second)
	}

	if first.Summary.TrackedFiles != 10 {
		t.Fatalf("tracked files = %d, want 10", first.Summary.TrackedFiles)
	}
	if first.Summary.UnclassifiedFiles != 2 {
		t.Fatalf("unclassified files = %d, want 2", first.Summary.UnclassifiedFiles)
	}
	if !sort.SliceIsSorted(first.Files, func(i, j int) bool {
		return first.Files[i].Path < first.Files[j].Path
	}) {
		t.Fatalf("files are not sorted: %#v", first.Files)
	}
	if !sort.SliceIsSorted(first.Summary.Languages, func(i, j int) bool {
		return first.Summary.Languages[i].Language < first.Summary.Languages[j].Language
	}) {
		t.Fatalf("languages are not sorted: %#v", first.Summary.Languages)
	}

	assertLanguageCount(t, first, "C++", 1)
	assertLanguageCount(t, first, "CMake", 1)
	assertLanguageCount(t, first, "Go", 2)
	assertLanguageCount(t, first, "Markdown", 2)
	assertLanguageCount(t, first, "SQL", 1)
	assertLanguageCount(t, first, "YAML", 1)

	wantBuild := []string{"CMake", "Go modules"}
	if len(first.Summary.BuildSystems) != 2 {
		t.Fatalf("build systems = %#v, want 2", first.Summary.BuildSystems)
	}
	for i, want := range wantBuild {
		if first.Summary.BuildSystems[i].Name != want {
			t.Fatalf("build system %d = %q, want %q", i, first.Summary.BuildSystems[i].Name, want)
		}
		if first.Summary.BuildSystems[i].Evidence.Path == "" {
			t.Fatalf("build system %q lacks evidence", want)
		}
	}

	categories := map[Category]int{}
	for _, count := range first.Summary.Categories {
		categories[count.Category] = count.Count
	}
	wantCategories := map[Category]int{
		CategoryBuild:         2,
		CategoryConfiguration: 1,
		CategoryData:          1,
		CategoryDocumentation: 2,
		CategoryOther:         1,
		CategorySource:        3,
	}
	if !reflect.DeepEqual(categories, wantCategories) {
		t.Fatalf("categories = %#v, want %#v", categories, wantCategories)
	}

	directories := map[string]int{}
	for _, directory := range first.Summary.Directories {
		directories[directory.Path] = directory.Files
	}
	wantDirectories := map[string]int{
		"assets":    1,
		"docs":      1,
		"src":       2,
		"src/inner": 1,
	}
	if !reflect.DeepEqual(directories, wantDirectories) {
		t.Fatalf("directories = %#v, want %#v", directories, wantDirectories)
	}

	for _, file := range first.Files {
		if file.Path == "main.go" && (file.Language != "Go" || file.Category != CategorySource) {
			t.Fatalf("main.go classified as %q/%q", file.Language, file.Category)
		}
		if file.Path == "main.go" && (file.BlobHash == "" || file.Size == 0) {
			t.Fatalf("main.go lacks blob evidence: %#v", file)
		}
	}
}

func TestBuildTruncatesAtBudget(t *testing.T) {
	root := newInventoryRepository(t, map[string]string{
		"z.go": "package z\n",
		"m.go": "package m\n",
		"a.go": "package a\n",
		"b.go": "package b\n",
	})
	commit := inventoryGitOutput(t, root, "rev-parse", "HEAD")

	result, err := Build(context.Background(), root, commit, Budget{MaxFiles: 3})
	if err != nil {
		t.Fatalf("bounded build: %v", err)
	}
	if !result.Summary.Truncated {
		t.Fatal("bounded build was not marked truncated")
	}
	if len(result.Files) != 3 {
		t.Fatalf("truncated files = %d, want 3", len(result.Files))
	}
	if result.Summary.TrackedFiles != 3 {
		t.Fatalf("truncated tracked files = %d, want 3", result.Summary.TrackedFiles)
	}
	want := []string{"a.go", "b.go", "m.go"}
	for i, path := range want {
		if result.Files[i].Path != path {
			t.Fatalf("truncated file %d = %q, want %q", i, result.Files[i].Path, path)
		}
	}

	full, err := Build(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("full build: %v", err)
	}
	if full.Summary.Truncated || len(full.Files) != 4 {
		t.Fatalf("full build marked truncated or incomplete: %#v", full.Summary)
	}
}

func TestBuildUsesExactHistoricalCommit(t *testing.T) {
	root := newInventoryRepository(t, map[string]string{
		"old.go": "package old\n",
	})
	oldCommit := inventoryGitOutput(t, root, "rev-parse", "HEAD")

	if err := os.Remove(filepath.Join(root, "old.go")); err != nil {
		t.Fatalf("remove old source: %v", err)
	}
	writeInventoryFile(t, root, "new.py", "print('new')\n")
	runInventoryGit(t, root, "add", "--all")
	runInventoryGit(t, root, "commit", "-m", "replace language")
	newCommit := inventoryGitOutput(t, root, "rev-parse", "HEAD")

	oldResult, err := Build(context.Background(), root, oldCommit, DefaultBudget())
	if err != nil {
		t.Fatalf("build historical commit: %v", err)
	}
	newResult, err := Build(context.Background(), root, newCommit, DefaultBudget())
	if err != nil {
		t.Fatalf("build current commit: %v", err)
	}

	assertLanguageCount(t, oldResult, "Go", 1)
	assertLanguageCount(t, newResult, "Python", 1)
	if oldResult.Files[0].Path != "old.go" || newResult.Files[0].Path != "new.py" {
		t.Fatalf("historical build contaminated by working tree or newer commits")
	}
}

func TestBuildDoesNotExecuteRepositoryCode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create repository: %v", err)
	}
	runInventoryGit(t, root, "init")
	runInventoryGit(t, root, "config", "user.email", "inventory@example.invalid")
	runInventoryGit(t, root, "config", "user.name", "Inventory Test")

	marker := filepath.Join(root, "executed")
	script := "#!/bin/sh\ntouch " + shellQuote(marker) + "\n"
	scriptPath := filepath.Join(root, "danger.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write executable script: %v", err)
	}
	writeInventoryFile(t, root, "main.go", "package main\n")
	runInventoryGit(t, root, "add", "--all")
	runInventoryGit(t, root, "commit", "-m", "initial")
	commit := inventoryGitOutput(t, root, "rev-parse", "HEAD")

	if _, err := Build(context.Background(), root, commit, DefaultBudget()); err != nil {
		t.Fatalf("build repository with executable: %v", err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("inventory executed a repository-provided file")
	}
}

func TestBuildRejectsEmptyCommit(t *testing.T) {
	root := newInventoryRepository(t, map[string]string{"a.go": "package a\n"})
	if _, err := Build(context.Background(), root, " ", DefaultBudget()); err == nil {
		t.Fatal("empty commit was accepted")
	}
}

func TestBuildHonorsCancellation(t *testing.T) {
	root := newInventoryRepository(t, map[string]string{"a.go": "package a\n"})
	commit := inventoryGitOutput(t, root, "rev-parse", "HEAD")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Build(ctx, root, commit, DefaultBudget()); err == nil {
		t.Fatal("canceled build succeeded")
	}
}

func assertLanguageCount(t *testing.T, result Result, language string, want int) {
	t.Helper()
	for _, count := range result.Summary.Languages {
		if count.Language == language {
			if count.Count != want {
				t.Fatalf("%s count = %d, want %d", language, count.Count, want)
			}
			return
		}
	}
	t.Fatalf("language %q missing from %#v", language, result.Summary.Languages)
}

func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}

func newInventoryRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create repository: %v", err)
	}
	runInventoryGit(t, root, "init")
	runInventoryGit(t, root, "config", "user.email", "inventory@example.invalid")
	runInventoryGit(t, root, "config", "user.name", "Inventory Test")
	for filePath, content := range files {
		writeInventoryFile(t, root, filePath, content)
	}
	runInventoryGit(t, root, "add", "--all")
	runInventoryGit(t, root, "commit", "-m", "initial")
	return root
}

func writeInventoryFile(t *testing.T, root, filePath, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filePath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", filePath, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filePath, err)
	}
}

func runInventoryGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func inventoryGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}
