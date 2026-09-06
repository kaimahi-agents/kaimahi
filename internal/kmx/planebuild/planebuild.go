// Package planebuild produces the governance plane's container image on the
// machine kmx is running on, without a clone and without a registry.
//
// The problem it solves is a consequence of the repository's own shape. The
// plane is a SEPARATE Go module under plane/, so `go:embed` cannot carry its
// source into the kmx binary — the toolchain refuses outright ("cannot embed
// directory: in different module"). The same nested module is what makes the
// answer possible: `go install
// github.com/kaimahi-agents/kaimahi/plane/cmd/kaimahi-proxy@<revision>`
// resolves through the public Go proxy at any revision on main, checksummed
// by the sum database, with no clone and nothing published by this project.
//
// Two sources, and a checkout always wins:
//
//   - FETCH — kmx reads its own revision out of its build info and installs
//     the plane at exactly that revision, so the binary and the manifests it
//     carries are one revision of one repository. This is the path a user
//     who ran `go install …/cmd/kmx@<sha>` takes.
//   - SOURCE — `kmx plane --source <path>`, and the auto-detection that
//     picks up a checkout kmx is being run from, build the working tree
//     instead. This is what the Makefile passes, and it is why CI still
//     proves the code a PR changes: a PR touching plane/ would otherwise be
//     tested against whatever the proxy last published.
package planebuild

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
)

// RuntimeBase is the image the proxy binary is packaged onto when kmx builds
// from a FETCHED module and therefore has no plane/Dockerfile on disk.
//
// It must stay identical to the runtime stage of plane/Dockerfile — the same
// distroless static image, nonroot by default, carrying CA certificates for
// the keyed hosted upstreams. TestRuntimeBaseMatchesThePlaneDockerfile pins
// the two together from a checkout; nothing else can, since this is the one
// path where that file does not exist.
const RuntimeBase = "gcr.io/distroless/static-debian12:nonroot"

// Binary is the name of the proxy command, in the module, in the image, and
// as the entrypoint.
const Binary = "kaimahi-proxy"

// ModulePath is the package `go install` is asked for.
const ModulePath = "github.com/kaimahi-agents/kaimahi/plane/cmd/" + Binary

// Dockerfile is the image recipe for the FETCHED path: the already-built
// static binary copied onto the same runtime base plane/Dockerfile uses.
//
// The source path does NOT use this — it builds plane/Dockerfile itself, so
// that a checkout has exactly one image recipe and a PR that changes how the
// image is built is the thing CI exercises.
func Dockerfile() string {
	return "FROM " + RuntimeBase + "\n" +
		"COPY " + Binary + " /" + Binary + "\n" +
		`ENTRYPOINT ["/` + Binary + `"]` + "\n"
}

// Revision returns the module version to fetch the plane at: kmx's own.
//
// A RELEASE binary carries its tag; a binary installed from the proxy
// carries a pseudo-version in Main.Version; one built from a checkout carries
// vcs.revision instead. All three are accepted by `go install …@<version>` —
// the tag because the release pushes plane/vX.Y.Z alongside it.
//
// A DIRTY checkout build is refused rather than silently downgraded to its
// last commit: the whole contract of this path is that the plane is built at
// the revision kmx itself is, and a build whose tree had uncommitted changes
// cannot honour it. That case has an answer — `--source`, which is what a
// checkout gets automatically anyway.
func Revision(info *debug.BuildInfo, ok bool) (string, error) {
	const advice = "\n  Build the plane from a checkout instead: kmx plane --source <path to the repo>"
	// A RELEASE binary knows its version from the tag stamped into it, and
	// the release job refuses to publish unless the plane module's matching
	// tag (plane/vX.Y.Z) exists at the same commit. Prefer it over the
	// build info: the tag is the source of truth, it is what a `go install
	// …/cmd/kmx@vX.Y.Z` binary resolves to anyway (Main.Version IS the
	// tag), and it does not depend on VCS stamping surviving the build.
	// The release job found out why that last clause matters: it wrote one
	// file into its own checkout before building, the toolchain recorded
	// vcs.modified=true, and every released binary refused to name a
	// revision at all.
	if tag := strings.TrimSpace(version.Tag); tag != "" {
		return tag, nil
	}
	if !ok || info == nil {
		return "", fmt.Errorf("kmx has no build information, so it cannot tell which revision to fetch the plane at." + advice)
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev != "" {
		if dirty {
			return "", fmt.Errorf("kmx was built from a MODIFIED checkout (revision %s), so fetching the plane at that revision would deploy different code than this binary was built from."+advice, short(rev))
		}
		return rev, nil
	}
	// Not `version`: that is the package this function's first check reads.
	moduleVersion := info.Main.Version
	if moduleVersion == "" || moduleVersion == "(devel)" {
		return "", fmt.Errorf("kmx was built without a recorded version (%q), so it cannot tell which revision to fetch the plane at."+advice, moduleVersion)
	}
	return moduleVersion, nil
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// DetectSource walks up from dir looking for the checkout kmx is being run
// from, and returns its root.
//
// The test is structural, not nominal: the directory must be the root of
// THIS module (a go.mod declaring github.com/kaimahi-agents/kaimahi) and
// must carry the nested plane module. A directory that merely has a plane/
// subdirectory is not this repository, and building whatever is in it would
// be worse than fetching.
func DetectSource(dir string, exists func(string) bool, readFile func(string) ([]byte, error)) (string, bool) {
	for {
		mod := filepath.Join(dir, "go.mod")
		if b, err := readFile(mod); err == nil && declaresRootModule(b) {
			if exists(filepath.Join(dir, "plane", "go.mod")) {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func declaresRootModule(gomod []byte) bool {
	for _, line := range strings.Split(string(gomod), "\n") {
		if strings.TrimSpace(line) == "module github.com/kaimahi-agents/kaimahi" {
			return true
		}
	}
	return false
}

// DetectSourceFS is DetectSource against the real filesystem.
func DetectSourceFS(dir string) (string, bool) {
	return DetectSource(dir, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}, os.ReadFile)
}

// TargetPlatform is the platform the proxy binary has to run on: the kind
// node's, which is Linux on the host's architecture (the container engine
// runs a Linux VM of the host's own arch on macOS and Windows).
func TargetPlatform() (string, string) { return "linux", runtime.GOARCH }

// Install describes the `go install` kmx is about to run: the arguments, the
// environment changes, and where the binary will land afterwards.
//
// It is a value rather than a side effect because the interesting part is
// entirely in the decision. `go install` REFUSES to cross-compile while GOBIN
// is set —
//
//	go: cannot install cross-compiled binaries when GOBIN is set
//
// — and GOBIN is set for anyone using mise, asdf or a plain `go env -w`. On
// Linux/amd64 the plane's target (linux/amd64) is not a cross-compile and
// the refusal never appears; on a Mac it appears every single time. So the
// cross case unsets GOBIN and reads the binary out of GOPATH's
// per-platform bin directory, which is where the toolchain puts it.
type Install struct {
	// Args is the full `go` argument list.
	Args []string
	// Env are the variables to set for the child.
	Env []string
	// Unset are the variables to REMOVE from the child's environment.
	Unset []string
	// Output is where the installed binary will be.
	Output string
	// Cross reports whether this is a cross-compile (why GOBIN is unset).
	Cross bool
}

// PlanInstall decides how to run `go install …@rev` for the target platform.
//
// gobin is a directory kmx owns; it is used only when the build is NOT a
// cross-compile, so an installed proxy binary never lands on top of whatever
// the operator already has in their own GOBIN.
func PlanInstall(rev, hostOS, hostArch, targetOS, targetArch, gopath, gobin string) Install {
	cross := hostOS != targetOS || hostArch != targetArch
	in := Install{
		Args: []string{"install", ModulePath + "@" + rev},
		Env: []string{
			"GOOS=" + targetOS,
			"GOARCH=" + targetArch,
			// The image is distroless static: a cgo-linked binary would not
			// run on it. plane/Dockerfile builds with CGO_ENABLED=0 for the
			// same reason.
			"CGO_ENABLED=0",
		},
		Cross: cross,
	}
	if cross {
		in.Unset = []string{"GOBIN"}
		in.Output = filepath.Join(gopath, "bin", targetOS+"_"+targetArch, Binary)
		return in
	}
	in.Env = append(in.Env, "GOBIN="+gobin)
	in.Output = filepath.Join(gobin, Binary)
	return in
}

// GOBINStillSet is the message for the one case PlanInstall cannot fix: a
// GOBIN set in the toolchain's own environment file (`go env -w GOBIN=…`),
// which unsetting the variable does not clear.
func GOBINStillSet(gobin string) error {
	return fmt.Errorf("go install cannot cross-compile the plane while GOBIN is set, and GOBIN=%s comes from Go's environment file rather than the environment kmx controls.\n"+
		"  Clear it (go env -u GOBIN), or build the plane from a checkout: kmx plane --source <path to the repo>", gobin)
}
