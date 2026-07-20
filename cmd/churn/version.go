package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// version is the release version, overridable at build time with
//
//	go build -ldflags "-X main.version=v1.2.3"
//
// It defaults to "dev" for a plain `go build`; the VCS revision below still
// pins exactly which commit a binary was built from.
var version = "dev"

// versionString renders one line: the release version, the VCS revision
// (short, with a +dirty marker when the tree had uncommitted changes at build
// time), the Go toolchain, and the target platform. The VCS fields come from
// the build info the Go toolchain stamps into every module build.
func versionString() string {
	rev, dirty, gover := "unknown", "", runtime.Version()
	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.GoVersion != "" {
			gover = bi.GoVersion
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					dirty = "+dirty"
				}
			}
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return fmt.Sprintf("churn %s (%s%s) %s %s/%s",
		version, rev, dirty, gover, runtime.GOOS, runtime.GOARCH)
}
