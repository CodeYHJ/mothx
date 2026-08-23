// Package version contains the product version shared by every entry point.
// Release builds replace Version through -ldflags; source builds use embedded
// VCS information when available and always expose a non-empty value.
package version

import (
	"runtime/debug"
	"strings"
)

// Version is replaced in release builds with -ldflags. Empty is intentional:
// source builds use the VCS revision from Go's embedded build information.
var Version string

func Current() string {
	if value := strings.TrimSpace(Version); value != "" {
		return value
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if value := strings.TrimSpace(info.Main.Version); value != "" && value != "(devel)" {
			return value
		}
		var revision, modified string
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = strings.TrimSpace(setting.Value)
			case "vcs.modified":
				modified = strings.TrimSpace(setting.Value)
			}
		}
		if revision != "" {
			if modified == "true" {
				return revision + "-dirty"
			}
			return revision
		}
	}
	return "unknown"
}
