package version

import (
	"fmt"
	"runtime/debug"
	"strings"
)

const Name = "Forgehand"

// These variables may be replaced with -ldflags at release build time. They
// remain variables (rather than constants) so packaged builds can carry an
// identity without changing source files.
var (
	Version   = "0.0.1-dev"
	Commit    = ""
	BuildDate = ""
)

// BuildIdentity returns a stable, human-readable identity for the executable.
// VCS metadata embedded by the Go tool is used when release ldflags were not
// supplied, which makes ordinary development builds distinguishable too.
func BuildIdentity() string {
	commit, buildDate, modified := Commit, BuildDate, false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if commit == "" {
					commit = setting.Value
				}
			case "vcs.time":
				if buildDate == "" {
					buildDate = setting.Value
				}
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
	}
	parts := []string{Version}
	if commit != "" {
		if len(commit) > 12 {
			commit = commit[:12]
		}
		if modified {
			commit += "+modified"
		}
		parts = append(parts, "commit "+commit)
	}
	if buildDate != "" {
		parts = append(parts, "built "+buildDate)
	}
	return strings.Join(parts, ", ")
}

func String() string {
	return fmt.Sprintf("%s %s", Name, BuildIdentity())
}
