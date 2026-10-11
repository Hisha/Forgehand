package components

//go:generate go run ./stdlibgen

import (
	"bufio"
	_ "embed"
	"strings"
)

//go:embed stdlib.txt
var stdlibSource string

// standardLibraryPackages is the authoritative, version-aware set of standard
// library import paths for the Go version that compiled Forgehand. See
// stdlibReferenceVersion and the stdlibgen generator.
var standardLibraryPackages map[string]struct{}

func init() {
	standardLibraryPackages = make(map[string]struct{})
	scanner := bufio.NewScanner(strings.NewReader(stdlibSource))
	for scanner.Scan() {
		importPath := strings.TrimSpace(scanner.Text())
		if importPath != "" {
			standardLibraryPackages[importPath] = struct{}{}
		}
	}
}

// isStandardLibrary reports whether importPath belongs to the standard library
// per the embedded, version-aware reference. It never falls back to heuristics
// such as whether the first path segment contains a dot.
func isStandardLibrary(importPath string) bool {
	_, ok := standardLibraryPackages[importPath]
	return ok
}
