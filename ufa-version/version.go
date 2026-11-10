// Package ufaversion holds the build-time-injected version string shared by
// every UFA sub-application binary, and the small amount of glue each of
// their main()s needs to answer "--version" the same way.
//
// Version defaults to "dev" for a plain `go build`/`go run .` with no
// ldflags (including Docker builds, which don't have a .git directory to
// derive a real version from). Every sub-application's Makefile overrides it
// at link time:
//
//	go build -ldflags "-X ufa-version.Version=$$(../scripts/compute-version.sh)" -o <bin> .
//
// See scripts/compute-version.sh for how that string is derived from git —
// in short, the git tag if HEAD is exactly tagged, else
// "<next-patch-of-most-recent-tag>-<branch-heuristic>-<shortsha>". Versions
// are computed at the repo level, not per sub-application: every binary
// built from the same commit gets the same version.
package ufaversion

import (
	"fmt"
	"os"
)

// Version is overridden at link time via -ldflags "-X ufa-version.Version=...".
var Version = "dev"

// HandleVersionFlag reports whether "--version" appears anywhere in the
// process's own arguments (os.Args[1:]), printing Version and returning true
// if so. It scans os.Args directly rather than relying on any particular
// flag-parsing library, so it works the same way regardless of whether the
// caller uses the stdlib "flag" package, a raw os.Args switch, or something
// else. Callers with argv that isn't entirely their own (ufa-loader, which
// forwards everything after the wrapped binary's name) should check their
// own flags directly instead of calling this.
func HandleVersionFlag() bool {
	for _, a := range os.Args[1:] {
		if a == "--version" {
			fmt.Println(Version)
			return true
		}
	}
	return false
}
