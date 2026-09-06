package version

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
)

func buildInfo(mainVersion string, settings ...debug.BuildSetting) *debug.BuildInfo {
	info := &debug.BuildInfo{Settings: settings}
	info.Main.Version = mainVersion
	return info
}

func TestResolveReadsTheThreeSourcesInOrder(t *testing.T) {
	sha := "fb456ebfb456ebfb456ebfb456ebfb456ebfb456e"
	cases := []struct {
		name       string
		tag        string
		info       *debug.BuildInfo
		ok         bool
		wantVer    string
		wantSource string
		release    bool
	}{
		{
			name:       "release build: the stamped tag wins over everything else",
			tag:        "v0.1.0",
			info:       buildInfo("(devel)", debug.BuildSetting{Key: "vcs.revision", Value: sha}),
			ok:         true,
			wantVer:    "v0.1.0",
			wantSource: "release build",
			release:    true,
		},
		{
			name:       "go install at a tag",
			info:       buildInfo("v0.1.0"),
			ok:         true,
			wantVer:    "v0.1.0",
			wantSource: "installed with go install",
		},
		{
			name:       "go install at a branch or sha: a pseudo-version",
			info:       buildInfo("v0.0.0-20260903120000-fb456ebfb456"),
			ok:         true,
			wantVer:    "v0.0.0-20260903120000-fb456ebfb456",
			wantSource: "installed with go install",
		},
		{
			// A modern toolchain synthesises Main.Version in a checkout
			// build too. It is a fine string to print; it is NOT a
			// `go install`, and must not claim to be.
			name: "checkout build with a synthesised pseudo-version",
			info: buildInfo("v0.0.0-20260903195837-fb456ebfb456",
				debug.BuildSetting{Key: "vcs.revision", Value: sha}),
			ok:         true,
			wantVer:    "v0.0.0-20260903195837-fb456ebfb456",
			wantSource: "development build from a checkout",
		},
		{
			name:       "checkout build with no synthesised version: never a bare sha",
			info:       buildInfo("(devel)", debug.BuildSetting{Key: "vcs.revision", Value: sha}),
			ok:         true,
			wantVer:    "v0.0.0-dev+fb456ebfb456",
			wantSource: "development build from a checkout",
		},
		{
			name: "modified checkout says so",
			info: buildInfo("(devel)",
				debug.BuildSetting{Key: "vcs.revision", Value: sha},
				debug.BuildSetting{Key: "vcs.modified", Value: "true"}),
			ok:         true,
			wantVer:    "v0.0.0-dev+fb456ebfb456.dirty",
			wantSource: "development build from a MODIFIED checkout",
		},
		{
			name:       "no vcs information at all",
			info:       buildInfo("(devel)"),
			ok:         true,
			wantVer:    Unknown,
			wantSource: "built without version or revision information",
		},
		{
			name:       "no build info at all",
			ok:         false,
			wantVer:    Unknown,
			wantSource: "no build information recorded",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer stub(c.tag)()
			got := Resolve(c.info, c.ok)
			if got.Version != c.wantVer || got.Source != c.wantSource || got.Release != c.release {
				t.Errorf("Resolve() = %+v, want {%s %s %v}", got, c.wantVer, c.wantSource, c.release)
			}
		})
	}
}

// The whole point of stamping a version: a released binary identifies
// itself by its tag. Not "unknown", and not the sha the old install instruction made you
// type.
func TestReleaseBuildNeverReportsUnknownOrABareSha(t *testing.T) {
	bareSha := regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	for _, tag := range []string{"v0.1.0", "v0.1.0-rc.1", "v1.2.3", " v0.2.0 "} {
		defer stub(tag)()
		// The worst case for the fallbacks: a CI checkout build, which is
		// exactly what the release job produces.
		got := Resolve(buildInfo("(devel)", debug.BuildSetting{Key: "vcs.revision", Value: "fb456ebfb456ebfb456ebfb456ebfb456ebfb456e"}), true)
		if got.Version == Unknown {
			t.Fatalf("tag %q: release build reported %q", tag, Unknown)
		}
		if bareSha.MatchString(got.Version) {
			t.Fatalf("tag %q: release build reported a bare sha %q", tag, got.Version)
		}
		if got.Version != strings.TrimSpace(tag) {
			t.Fatalf("tag %q: release build reported %q", tag, got.Version)
		}
		if !got.Release {
			t.Fatalf("tag %q: release build not marked as a release", tag)
		}
	}
}

// The link between the release job and this package is a STRING in a
// workflow file. Renaming the variable, moving the package, or dropping the
// -X flag would produce releases that silently report a dev version — the
// exact failure the stamp exists to end. Pin the two together.
func TestReleaseWorkflowStampsThisVariable(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Skipf("no release workflow to check (%v)", err)
	}
	want := "github.com/kaimahi-agents/kaimahi/internal/kmx/version.Tag="
	if !strings.Contains(string(body), "-X "+want) {
		t.Fatalf("release.yml does not stamp %s — released binaries would report a development version", want)
	}
}

func stub(tag string) func() {
	previous := Tag
	Tag = tag
	return func() { Tag = previous }
}
