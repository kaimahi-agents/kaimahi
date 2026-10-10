// Package version answers one question an adopter has to be able to ask:
// which build of kmx is this?
//
// Until the first tag, the only answer available was a commit sha — the
// same sha the install instruction made you type. A binary that can only
// identify itself by the thing you had to know already is not a version.
//
// Three sources, in the order they are trusted:
//
//  1. Tag, stamped at link time by the release job from the git tag it is
//     building. This is the only source that can name a RELEASE, because a
//     CI build from a checkout looks exactly like any other checkout build
//     to the toolchain: Main.Version is "(devel)" and the tag is nowhere in
//     the build info.
//  2. Main.Version, which the module proxy fills in for `go install
//     …/cmd/kmx@v0.1.0` (the tag) and for `@main` or `@<sha>` (a
//     pseudo-version). This is the path the README's one-line install takes.
//  3. vcs.revision, which a `go build` from a checkout records. A developer
//     build, and it says so rather than pretending to be a version.
//
// The rule the release depends on: a binary built from a tag must never
// report "unknown", and must never report a bare sha as if it were a
// version. TestReleaseBuildNeverReportsUnknownOrABareSha holds that line.
package version

import (
	"runtime/debug"
	"strings"
)

// Tag is set at link time:
//
//	go build -ldflags "-X github.com/kaimahi-agents/kaimahi/internal/kmx/version.Tag=v0.1.0"
//
// Empty in every build that is not a release build, which is why the
// fallbacks below are not decoration.
var Tag string

// Unknown is what a binary with no identifying information at all reports.
// It is reachable only when the toolchain recorded no build info — never
// from a release build.
const Unknown = "unknown"

// Build is a resolved answer plus its provenance, because "v0.1.0" from a
// release and "v0.1.0" from a local experiment mean different things, and
// an operator debugging a cluster deserves to see which one they have.
type Build struct {
	// Version is what to print: a tag, a pseudo-version, or a dev string.
	Version string
	// Source names where Version came from, in words.
	Source string
	// Release reports whether this is a build produced from a tag by the
	// release job.
	Release bool
}

// Resolve reads the three sources in order. info/ok are what
// debug.ReadBuildInfo returns, passed in so the decision is testable.
func Resolve(info *debug.BuildInfo, ok bool) Build {
	if tag := strings.TrimSpace(Tag); tag != "" {
		return Build{Version: tag, Source: "release build", Release: true}
	}
	if !ok || info == nil {
		return Build{Version: Unknown, Source: "no build information recorded"}
	}
	// vcs.revision is what separates the two remaining cases, and it is
	// present in exactly one of them: a build from a checkout records it,
	// a build from the module cache cannot. Order matters, because a modern
	// toolchain ALSO synthesises Main.Version from the VCS state in a checkout
	// build — reading Main.Version first would label every developer's
	// own binary "installed with go install".
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	main := info.Main.Version
	usable := main != "" && main != "(devel)"
	if rev != "" {
		source := "development build from a checkout"
		if dirty {
			source = "development build from a MODIFIED checkout"
		}
		if usable {
			// The toolchain's own synthesised pseudo-version, which
			// already carries its own dirty marker.
			return Build{Version: main, Source: source}
		}
		// Never present a bare sha as a version: prefix it so it reads as
		// what it is — a build standing in front of no release at all.
		v := "v0.0.0-dev+" + short(rev)
		if dirty {
			v += ".dirty"
		}
		return Build{Version: v, Source: source}
	}
	if usable {
		return Build{Version: main, Source: "installed with go install"}
	}
	return Build{Version: Unknown, Source: "built without version or revision information"}
}

// String is "<version> (<source>)".
func (b Build) String() string { return b.Version + " (" + b.Source + ")" }

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
