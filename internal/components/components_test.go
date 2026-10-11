package components

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzeIdentifiesComponentsAndDependencies(t *testing.T) {
	root := newComponentsRepository(t, standardFixture())
	commit := componentsGitOutput(t, root, "rev-parse", "HEAD")

	result, err := Analyze(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	if !result.Summary.Complete {
		t.Fatalf("analysis not complete: %#v", result.Summary)
	}
	if result.Summary.Truncated {
		t.Fatal("analysis unexpectedly truncated")
	}
	if result.Summary.Sources != 5 {
		t.Fatalf("sources = %d, want 5", result.Summary.Sources)
	}
	if result.Summary.Analyzed != 4 {
		t.Fatalf("analyzed = %d, want 4", result.Summary.Analyzed)
	}
	if result.Summary.Skipped != 0 {
		t.Fatalf("skipped = %d, want 0", result.Summary.Skipped)
	}

	assertComponent(t, result, KindModule, "example.com/app", ".", false)
	assertComponent(t, result, KindPackage, "example.com/app", ".", false)
	assertComponent(t, result, KindPackage, "example.com/app/internal/greeting", "internal/greeting", true)
	assertComponent(t, result, KindPackage, "example.com/app/internal/word", "internal/word", true)
	assertComponent(t, result, KindCommand, "example.com/app/cmd/tool", "cmd/tool", false)
	assertComponent(t, result, KindBuild, "scripts", "scripts", false)
	assertComponent(t, result, KindConfiguration, "config", "config", false)

	if result.Summary.Modules != 1 {
		t.Fatalf("modules = %d, want 1", result.Summary.Modules)
	}
	if result.Summary.Packages != 3 {
		t.Fatalf("packages = %d, want 3", result.Summary.Packages)
	}
	if result.Summary.Commands != 1 {
		t.Fatalf("commands = %d, want 1", result.Summary.Commands)
	}
	if result.Summary.BuildComponents != 1 {
		t.Fatalf("build components = %d, want 1", result.Summary.BuildComponents)
	}
	if result.Summary.ConfigurationComponents != 1 {
		t.Fatalf("configuration components = %d, want 1", result.Summary.ConfigurationComponents)
	}

	assertDependency(t, result, DependencyInternal, "example.com/app", "example.com/app/internal/greeting", "")
	assertDependency(t, result, DependencyInternal, "example.com/app", "example.com/app/internal/word", "")
	assertDependency(t, result, DependencyStdlib, "example.com/app", "fmt", "")
	assertDependency(t, result, DependencyExternalModule, "example.com/app", "github.com/example/dep/pkg", "github.com/example/dep")
	assertDependency(t, result, DependencyUnresolved, "example.com/app", "github.com/unknown/missing", "")
	assertDependency(t, result, DependencyInternal, "example.com/app/internal/greeting", "example.com/app/internal/word", "")
	assertDependency(t, result, DependencyExternalModule, "example.com/app/internal/greeting", "github.com/example/dep/sub", "github.com/example/dep")
	assertDependency(t, result, DependencyInternal, "example.com/app/cmd/tool", "example.com/app/internal/greeting", "")

	counts := dependencyCount(result)
	if counts[DependencyInternal] != 4 {
		t.Fatalf("internal dependencies = %d, want 4", counts[DependencyInternal])
	}
	if counts[DependencyExternalModule] != 2 {
		t.Fatalf("external-module dependencies = %d, want 2", counts[DependencyExternalModule])
	}
	if counts[DependencyStdlib] != 1 {
		t.Fatalf("stdlib dependencies = %d, want 1", counts[DependencyStdlib])
	}
	if counts[DependencyUnresolved] != 1 {
		t.Fatalf("unresolved dependencies = %d, want 1", counts[DependencyUnresolved])
	}
}

func TestAnalyzeReportsDeclaredRequirementsSeparately(t *testing.T) {
	root := newComponentsRepository(t, standardFixture())
	commit := componentsGitOutput(t, root, "rev-parse", "HEAD")

	result, err := Analyze(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	requirements := result.Summary.ModuleRequirements
	if len(requirements) != 2 {
		t.Fatalf("requirements = %#v, want 2", requirements)
	}
	if requirements[0].Path != "github.com/example/dep" || requirements[0].Version != "v1.2.3" || requirements[0].Indirect {
		t.Fatalf("requirement 0 = %#v", requirements[0])
	}
	if requirements[1].Path != "github.com/example/indirect" || !requirements[1].Indirect {
		t.Fatalf("requirement 1 = %#v", requirements[1])
	}
}

func TestAnalyzePreventsDuplicateComponents(t *testing.T) {
	root := newComponentsRepository(t, standardFixture())
	commit := componentsGitOutput(t, root, "rev-parse", "HEAD")

	result, err := Analyze(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	modules := 0
	buildAtRoot := 0
	for _, component := range result.Components {
		if component.Kind == KindModule {
			modules++
		}
		if component.Kind == KindBuild && component.Path == "." {
			buildAtRoot++
		}
	}
	if modules != 1 {
		t.Fatalf("module components = %d, want 1", modules)
	}
	if buildAtRoot != 0 {
		t.Fatalf("redundant build component at module root: %#v", result.Components)
	}
}

func TestAnalyzeIsDeterministic(t *testing.T) {
	root := newComponentsRepository(t, standardFixture())
	commit := componentsGitOutput(t, root, "rev-parse", "HEAD")

	first, err := Analyze(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("first analyze: %v", err)
	}
	second, err := Analyze(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("second analyze: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated analyses differ:\nfirst=%#v\nsecond=%#v", first, second)
	}
}

func TestAnalyzePinsToCommit(t *testing.T) {
	root := newComponentsRepository(t, standardFixture())
	commit := componentsGitOutput(t, root, "rev-parse", "HEAD")
	before, err := Analyze(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("analyze before: %v", err)
	}

	writeComponentsFile(t, root, "extra/extra.go", "package extra\n")
	runComponentsGit(t, root, "add", "--all")
	runComponentsGit(t, root, "commit", "-m", "add extra package")

	after, err := Analyze(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("analyze after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("analysis of pinned commit changed after new commit")
	}
}

func TestAnalyzeSkipsMalformedSourceAndMarksPartial(t *testing.T) {
	files := standardFixture()
	files["internal/broken/broken.go"] = "package broken\nfunc {\n"
	root := newComponentsRepository(t, files)
	commit := componentsGitOutput(t, root, "rev-parse", "HEAD")

	result, err := Analyze(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if result.Summary.Complete {
		t.Fatal("analysis with malformed source reported complete")
	}
	if result.Summary.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1", result.Summary.Skipped)
	}
	if len(result.Summary.SkippedFiles) != 1 || !strings.Contains(result.Summary.SkippedFiles[0].Reason, "malformed source") {
		t.Fatalf("skipped files = %#v", result.Summary.SkippedFiles)
	}
	for _, component := range result.Components {
		if component.Path == "internal/broken" {
			t.Fatalf("malformed package produced a component: %#v", component)
		}
	}
}

func TestAnalyzeSkipsOversizeBlob(t *testing.T) {
	root := newComponentsRepository(t, standardFixture())
	commit := componentsGitOutput(t, root, "rev-parse", "HEAD")

	budget := DefaultBudget()
	budget.MaxBlobBytes = 16
	result, err := Analyze(context.Background(), root, commit, budget)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if result.Summary.Complete {
		t.Fatal("analysis with skipped oversize blobs reported complete")
	}
	if result.Summary.Skipped == 0 {
		t.Fatal("oversize blobs were not recorded as skipped")
	}
}

func TestAnalyzeTruncationMarksPartial(t *testing.T) {
	root := newComponentsRepository(t, standardFixture())
	commit := componentsGitOutput(t, root, "rev-parse", "HEAD")

	budget := DefaultBudget()
	budget.MaxFiles = 1
	result, err := Analyze(context.Background(), root, commit, budget)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if result.Summary.Complete {
		t.Fatal("truncated analysis reported complete")
	}
	if !result.Summary.Truncated {
		t.Fatal("analysis did not record truncation")
	}
}

func TestAnalyzeConflictingPackageNames(t *testing.T) {
	files := standardFixture()
	files["internal/greeting/other.go"] = "package other\n"
	root := newComponentsRepository(t, files)
	commit := componentsGitOutput(t, root, "rev-parse", "HEAD")

	result, err := Analyze(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if result.Summary.Complete {
		t.Fatal("conflicting package names reported complete")
	}
	found := false
	for _, skipped := range result.Summary.SkippedFiles {
		if skipped.Path == "internal/greeting/other.go" && strings.Contains(skipped.Reason, "conflicting") {
			found = true
		}
	}
	if !found {
		t.Fatalf("conflicting file not recorded as skipped: %#v", result.Summary.SkippedFiles)
	}
	assertComponent(t, result, KindPackage, "example.com/app/internal/greeting", "internal/greeting", true)
}

func TestAnalyzeReportsUnimportedRequirementsSeparately(t *testing.T) {
	root := newComponentsRepository(t, standardFixture())
	commit := componentsGitOutput(t, root, "rev-parse", "HEAD")

	result, err := Analyze(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	// github.com/example/indirect is declared but never imported.
	declared := false
	for _, requirement := range result.Summary.ModuleRequirements {
		if requirement.Path == "github.com/example/indirect" {
			declared = true
			if !requirement.Indirect {
				t.Fatalf("indirect requirement not marked indirect: %#v", requirement)
			}
		}
	}
	if !declared {
		t.Fatalf("declared requirement missing: %#v", result.Summary.ModuleRequirements)
	}
	for _, dependency := range result.Dependencies {
		if dependency.TargetModule == "github.com/example/indirect" {
			t.Fatalf("unimported requirement appeared as a dependency: %#v", dependency)
		}
	}
}

func TestClassifyImport(t *testing.T) {
	cells := map[string]componentCell{
		"example.com/app/internal/greeting": {},
	}
	requirements := []DeclaredModule{
		{Path: "github.com/example/dep"},
	}

	tests := []struct {
		name       string
		importPath string
		wantKind   DependencyKind
		wantModule string
	}{
		{name: "internal", importPath: "example.com/app/internal/greeting", wantKind: DependencyInternal},
		{name: "external module", importPath: "github.com/example/dep/pkg", wantKind: DependencyExternalModule, wantModule: "github.com/example/dep"},
		{name: "external module boundary", importPath: "github.com/example/dependency", wantKind: DependencyUnresolved},
		{name: "stdlib", importPath: "fmt", wantKind: DependencyStdlib},
		{name: "stdlib subpackage", importPath: "net/http", wantKind: DependencyStdlib},
		{name: "unresolved", importPath: "github.com/unknown/missing", wantKind: DependencyUnresolved},
		{name: "empty", importPath: "", wantKind: DependencyUnresolved},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kind, module := classifyImport(test.importPath, cells, requirements)
			if kind != test.wantKind {
				t.Fatalf("kind = %q, want %q", kind, test.wantKind)
			}
			if module != test.wantModule {
				t.Fatalf("module = %q, want %q", module, test.wantModule)
			}
		})
	}
}

func TestAnalyzeUsesModuleQualifiedPackageIdentities(t *testing.T) {
	files := map[string]string{
		"go.mod":         "module example.com/root\n\ngo 1.22\n",
		"root.go":        "package root\n",
		"sub/go.mod":     "module example.com/sub\n\ngo 1.22\n",
		"sub/sub.go":     "package sub\n",
		"sub/pkg/pkg.go": "package pkg\n",
	}
	root := newComponentsRepository(t, files)
	commit := componentsGitOutput(t, root, "rev-parse", "HEAD")

	result, err := Analyze(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	assertComponent(t, result, KindModule, "example.com/root", ".", false)
	assertComponent(t, result, KindModule, "example.com/sub", "sub", false)
	assertComponent(t, result, KindPackage, "example.com/root", ".", false)
	assertComponent(t, result, KindPackage, "example.com/sub", "sub", false)
	assertComponent(t, result, KindPackage, "example.com/sub/pkg", "sub/pkg", false)
}

func TestAnalyzeIdentitiesAreStableAcrossRuns(t *testing.T) {
	root := newComponentsRepository(t, standardFixture())
	commit := componentsGitOutput(t, root, "rev-parse", "HEAD")

	first, err := Analyze(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("first analyze: %v", err)
	}
	second, err := Analyze(context.Background(), root, commit, DefaultBudget())
	if err != nil {
		t.Fatalf("second analyze: %v", err)
	}

	firstIdentities := componentIdentities(first)
	secondIdentities := componentIdentities(second)
	if !reflect.DeepEqual(firstIdentities, secondIdentities) {
		t.Fatalf("identities changed between runs:\n%#v\nvs\n%#v", firstIdentities, secondIdentities)
	}
}

func componentIdentities(result Result) []string {
	identities := make([]string, 0, len(result.Components))
	for _, component := range result.Components {
		identities = append(identities, string(component.Kind)+":"+component.Identity)
	}
	return identities
}

func TestAnalyzeRejectsEmptyCommit(t *testing.T) {
	root := newComponentsRepository(t, standardFixture())
	if _, err := Analyze(context.Background(), root, " ", DefaultBudget()); err == nil {
		t.Fatal("empty commit was accepted")
	}
}

func TestAnalyzeHonorsCancellation(t *testing.T) {
	root := newComponentsRepository(t, standardFixture())
	commit := componentsGitOutput(t, root, "rev-parse", "HEAD")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Analyze(ctx, root, commit, DefaultBudget()); err == nil {
		t.Fatal("canceled analysis succeeded")
	}
}

func TestParseGoMod(t *testing.T) {
	parsed, err := parseGoMod("go.mod", []byte(`module example.com/thing

go 1.22

require (
	github.com/one/dep v1.0.0
	github.com/two/dep v2.0.0 // indirect
)

require github.com/three/dep v3.0.0
`))
	if err != nil {
		t.Fatalf("parseGoMod: %v", err)
	}
	if parsed.modulePath != "example.com/thing" {
		t.Fatalf("module path = %q", parsed.modulePath)
	}
	if parsed.goVersion != "1.22" {
		t.Fatalf("go version = %q", parsed.goVersion)
	}
	if parsed.moduleLine != 1 {
		t.Fatalf("module line = %d, want 1", parsed.moduleLine)
	}
	if len(parsed.requires) != 3 {
		t.Fatalf("requires = %#v", parsed.requires)
	}
	if parsed.requires[1].Path != "github.com/two/dep" || !parsed.requires[1].Indirect {
		t.Fatalf("indirect requirement = %#v", parsed.requires[1])
	}
	if parsed.requires[2].Path != "github.com/three/dep" || parsed.requires[2].Indirect {
		t.Fatalf("single requirement = %#v", parsed.requires[2])
	}
}

func TestParseGoModRejectsMissingModule(t *testing.T) {
	if _, err := parseGoMod("go.mod", []byte("go 1.22\n")); err == nil {
		t.Fatal("go.mod without module directive was accepted")
	}
}

func standardFixture() map[string]string {
	return map[string]string{
		"go.mod": `module example.com/app

go 1.22

require (
	github.com/example/dep v1.2.3
	github.com/example/indirect v0.1.0 // indirect
)
`,
		"app.go": `package app

import (
	"fmt"

	"example.com/app/internal/greeting"
	"example.com/app/internal/word"
	"github.com/example/dep/pkg"
	_ "github.com/unknown/missing"
)

func Use() {
	_ = greeting.Hello
	_ = word.Name
	fmt.Println(pkg.Value)
}
`,
		"internal/greeting/greeting.go": `package greeting

import (
	"example.com/app/internal/word"
	"github.com/example/dep/sub"
)

var Hello = word.Name
var Sub = sub.Value
`,
		"internal/word/word.go": "package word\n\nvar Name = \"word\"\n",
		"cmd/tool/main.go": `package main

import "example.com/app/internal/greeting"

func main() {
	_ = greeting.Hello
}
`,
		"Makefile":         "all:\n\ttrue\n",
		"scripts/Makefile": "all:\n\ttrue\n",
		"config/app.yaml":  "key: value\n",
		"README.md":        "# App\n",
	}
}

func assertComponent(
	t *testing.T,
	result Result,
	kind ComponentKind,
	identity string,
	componentPath string,
	internal bool,
) {
	t.Helper()
	for _, component := range result.Components {
		if component.Kind == kind && component.Identity == identity {
			if component.Path != componentPath {
				t.Fatalf("component %s/%s path = %q, want %q", kind, identity, component.Path, componentPath)
			}
			if component.Internal != internal {
				t.Fatalf("component %s/%s internal = %v, want %v", kind, identity, component.Internal, internal)
			}
			if len(component.Evidence) == 0 {
				t.Fatalf("component %s/%s lacks evidence", kind, identity)
			}
			return
		}
	}
	t.Fatalf("component %s/%s missing from %#v", kind, identity, result.Components)
}

func assertDependency(
	t *testing.T,
	result Result,
	kind DependencyKind,
	source string,
	target string,
	targetModule string,
) {
	t.Helper()
	for _, dependency := range result.Dependencies {
		if dependency.Kind == kind && dependency.Source == source && dependency.Target == target {
			if dependency.TargetModule != targetModule {
				t.Fatalf("dependency %s %s->%s module = %q, want %q", kind, source, target, dependency.TargetModule, targetModule)
			}
			if len(dependency.Evidence) == 0 {
				t.Fatalf("dependency %s %s->%s lacks evidence", kind, source, target)
			}
			return
		}
	}
	t.Fatalf("dependency %s %s->%s missing from %#v", kind, source, target, result.Dependencies)
}

func dependencyCount(result Result) map[DependencyKind]int {
	counts := make(map[DependencyKind]int)
	for _, count := range result.Summary.Dependencies {
		counts[count.Kind] = count.Count
	}
	return counts
}

func newComponentsRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("create repository: %v", err)
	}
	runComponentsGit(t, root, "init")
	runComponentsGit(t, root, "config", "user.email", "components@example.invalid")
	runComponentsGit(t, root, "config", "user.name", "Components Test")
	for filePath, content := range files {
		writeComponentsFile(t, root, filePath, content)
	}
	runComponentsGit(t, root, "add", "--all")
	runComponentsGit(t, root, "commit", "-m", "initial")
	return root
}

func writeComponentsFile(t *testing.T, root, filePath, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filePath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", filePath, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filePath, err)
	}
}

func runComponentsGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func componentsGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}
