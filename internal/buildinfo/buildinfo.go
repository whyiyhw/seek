// Package buildinfo is the build-identity accessor every non-TUI
// surface can share (card c_ni7mzw: the web page's About row needs the
// same string the banner shows).
//
// It exists because the identity used to live INSIDE internal/tui
// (banner.go's VersionString/formatVersion) — fine while the banner was
// its only consumer, wrong the moment an RPC method or a serve-status
// payload needs it: importing the render stack to print a git hash
// couples every such surface to lipgloss. tui now DELEGATES here, so
// there is exactly one formatting rule (and banner_test keeps testing
// it through the thin tui wrapper).
package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Version returns the build identity for the running binary as a single
// human-readable string, from runtime/debug.BuildInfo (module version,
// short git hash, `+` dirty marker).
//
//	"v0.1.0 · abc1234"   — clean release
//	"dev · abc1234+"     — local build, uncommitted changes
//	"dev"                — local build, no VCS info
//	"unknown"            — buildinfo unavailable (extremely rare)
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return Format(info)
}

// Format is the pure-function core of Version. Split out so callers
// (and tests) can construct synthetic BuildInfo without depending on
// the real build environment.
func Format(info *debug.BuildInfo) string {
	version := info.Main.Version
	// "(devel)" is go-build's marker for a tag-less build. Pseudo-
	// versions like "v0.0.0-YYYYMMDDHHMMSS-<hash>" mean the same
	// thing — collapse both to "dev" so the line stays readable;
	// the short hash from vcs.revision carries the actual identity.
	switch {
	case version == "" || version == "(devel)":
		version = "dev"
	case strings.HasPrefix(version, "v0.0.0-"):
		version = "dev"
	}

	var (
		rev      string
		modified bool
	)
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 7 {
				rev = s.Value[:7]
			}
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}

	if rev == "" {
		return version
	}
	suffix := ""
	if modified {
		suffix = "+"
	}
	return fmt.Sprintf("%s · %s%s", version, rev, suffix)
}
