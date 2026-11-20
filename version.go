package main

import (
	"cmp"
	"runtime/debug"
	"strings"
)

// Set through -ldflags by the Makefile and GoReleaser; see buildInfo for the
// fallbacks.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// buildInfo returns the version, commit and build date. Values from -ldflags
// win; otherwise they come from what the Go toolchain recorded, which covers
// "go install ...@v0.1.0" and plain "go build" in a checkout.
func buildInfo() (v, c, d string) {
	v, c, d = version, commit, date
	if bi, ok := debug.ReadBuildInfo(); ok {
		// A tagged release ("v0.1.0"), not a pseudo-version of an untagged build.
		if mv := bi.Main.Version; v == "dev" && mv != "(devel)" && mv != "" && !strings.Contains(mv, "-") {
			v = strings.TrimPrefix(mv, "v")
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				c = cmp.Or(c, s.Value[:min(7, len(s.Value))])
			case "vcs.time":
				d = cmp.Or(d, s.Value[:min(10, len(s.Value))])
			}
		}
	}
	return v, cmp.Or(c, "none"), cmp.Or(d, "unknown")
}
