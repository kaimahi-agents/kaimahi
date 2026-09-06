package planebuild

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
)

func settings(pairs ...string) []debug.BuildSetting {
	var s []debug.BuildSetting
	for i := 0; i < len(pairs); i += 2 {
		s = append(s, debug.BuildSetting{Key: pairs[i], Value: pairs[i+1]})
	}
	return s
}

// A RELEASED binary fetches the plane at its TAG, and must do so even when
// the build info is unhelpful. This is not hypothetical: the first release
// candidate shipped with vcs.modified=true (the job wrote its notes into its
// own checkout before building), so every binary in it refused to name a
// revision and `kmx plane` was dead on arrival. The tag is the source of
// truth precisely so that a build-info accident cannot take the plane down
// with it — the release job proves plane/<tag> exists before publishing.
func TestAReleasedBinaryFetchesThePlaneAtItsTag(t *testing.T) {
	previous := version.Tag
	version.Tag = "v0.1.0"
	defer func() { version.Tag = previous }()

	for _, tc := range []struct {
		name string
		info *debug.BuildInfo
		ok   bool
	}{
		{
			name: "a clean release build",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "(devel)"},
				Settings: settings("vcs.revision", "ffed1ee20737abcdef0123456789abcdef012345", "vcs.modified", "false"),
			},
			ok: true,
		},
		{
			name: "a release build the toolchain called dirty",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "(devel)"},
				Settings: settings("vcs.revision", "ffed1ee20737abcdef0123456789abcdef012345", "vcs.modified", "true"),
			},
			ok: true,
		},
		{
			name: "a release build with no build information at all",
			ok:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Revision(tc.info, tc.ok)
			if err != nil {
				t.Fatalf("Revision() = %v, want the tag", err)
			}
			if got != "v0.1.0" {
				t.Fatalf("Revision() = %q, want the stamped tag", got)
			}
		})
	}
}

// Which revision the plane is fetched at is the whole safety property of the
// clone-free path: the plane must be built at the revision kmx ITSELF is, or
// the binary is deploying code it was never tested with.
func TestRevisionIsKmxsOwn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		info    *debug.BuildInfo
		ok      bool
		want    string
		wantErr string
	}{
		{
			name: "installed from the module proxy: the pseudo-version",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20260903013736-ffed1ee20737"}},
			ok:   true,
			want: "v0.0.0-20260903013736-ffed1ee20737",
		},
		{
			name: "built from a clean checkout: the revision",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "(devel)"},
				Settings: settings("vcs.revision", "ffed1ee20737abcdef0123456789abcdef012345", "vcs.modified", "false"),
			},
			ok:   true,
			want: "ffed1ee20737abcdef0123456789abcdef012345",
		},
		{
			// The failure this prevents: kmx built from a tree with local
			// changes, run outside that tree, fetching the plane at the last
			// commit — a plane that is NOT the code this kmx was built from,
			// deployed silently.
			name: "built from a dirty checkout: refused, with the fix named",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "(devel)"},
				Settings: settings("vcs.revision", "ffed1ee20737", "vcs.modified", "true"),
			},
			ok:      true,
			wantErr: "MODIFIED checkout",
		},
		{
			name:    "built with no stamping at all",
			info:    &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			ok:      true,
			wantErr: "without a recorded version",
		},
		{
			name:    "no build information",
			ok:      false,
			wantErr: "no build information",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Revision(tc.info, tc.ok)
			switch {
			case tc.wantErr != "":
				if err == nil {
					t.Fatalf("Revision() = %q, want an error containing %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tc.wantErr)
				}
				// Every refusal has to name the way forward, because the
				// operator cannot rebuild kmx to fix it.
				if !strings.Contains(err.Error(), "--source") {
					t.Errorf("refusal does not name --source: %v", err)
				}
			case err != nil:
				t.Fatalf("Revision() failed: %v", err)
			case got != tc.want:
				t.Errorf("Revision() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A checkout wins over the proxy, and only a real one counts. Getting this
// wrong would mean a PR that changes plane/ was "proved" against whatever
// the proxy last published, rather than against the code it changed.
func TestDetectSourceFindsThisRepositoryAndNothingElse(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "work", "kaimahi")
	deep := filepath.Join(repo, "internal", "kmx", "app")
	if err := os.MkdirAll(filepath.Join(repo, "plane"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(repo, "go.mod"), "module github.com/kaimahi-agents/kaimahi\n\ngo 1.26\n")
	write(filepath.Join(repo, "plane", "go.mod"), "module github.com/kaimahi-agents/kaimahi/plane\n")

	if got, ok := DetectSourceFS(deep); !ok || got != repo {
		t.Errorf("from a subdirectory: got %q, %v; want %q, true", got, ok, repo)
	}
	if got, ok := DetectSourceFS(repo); !ok || got != repo {
		t.Errorf("from the root: got %q, %v; want %q, true", got, ok, repo)
	}
	if _, ok := DetectSourceFS(root); ok {
		t.Errorf("outside the checkout, a source was detected")
	}

	// Some other project that happens to have a plane/ directory is not
	// this repository, and building it would be worse than fetching.
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(filepath.Join(other, "plane"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(other, "go.mod"), "module example.com/other\n")
	write(filepath.Join(other, "plane", "go.mod"), "module example.com/other/plane\n")
	if _, ok := DetectSourceFS(other); ok {
		t.Errorf("an unrelated module with a plane/ directory was taken for this repository")
	}

	// This module without its nested plane module cannot build a plane.
	noPlane := filepath.Join(root, "noplane")
	if err := os.MkdirAll(noPlane, 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(noPlane, "go.mod"), "module github.com/kaimahi-agents/kaimahi\n")
	if _, ok := DetectSourceFS(noPlane); ok {
		t.Errorf("a checkout with no plane/ module was accepted as a source")
	}
}

// `go install` refuses to cross-compile while GOBIN is set, and GOBIN is set
// for every mise/asdf user. On Linux/amd64 (CI) the plane's target is not a
// cross-compile and the refusal never appears — which is exactly why it has
// to be tested here rather than discovered by the first contributor on a Mac.
func TestPlanInstallHandlesTheGOBINCrossCompileRefusal(t *testing.T) {
	const rev = "v0.0.0-20260903013736-ffed1ee20737"

	same := PlanInstall(rev, "linux", "amd64", "linux", "amd64", "/gopath", "/cache/plane-bin")
	if same.Cross {
		t.Errorf("linux/amd64 -> linux/amd64 reported as a cross-compile")
	}
	if len(same.Unset) != 0 {
		t.Errorf("GOBIN unset on a native build: %v", same.Unset)
	}
	if !contains(same.Env, "GOBIN=/cache/plane-bin") {
		t.Errorf("native build does not put the binary in kmx's own directory: %v", same.Env)
	}
	if want := filepath.Join("/cache/plane-bin", Binary); same.Output != want {
		t.Errorf("native output %q, want %q", same.Output, want)
	}

	cross := PlanInstall(rev, "darwin", "arm64", "linux", "arm64", "/gopath", "/cache/plane-bin")
	if !cross.Cross {
		t.Errorf("darwin/arm64 -> linux/arm64 not reported as a cross-compile")
	}
	if !contains(cross.Unset, "GOBIN") {
		t.Errorf("cross build does not unset GOBIN: %v", cross.Unset)
	}
	for _, e := range cross.Env {
		if strings.HasPrefix(e, "GOBIN=") {
			t.Errorf("cross build sets GOBIN anyway (%q) — go install refuses that", e)
		}
	}
	// With GOBIN unset the toolchain puts a cross-compiled binary under
	// GOPATH/bin/<goos>_<goarch>/, not GOPATH/bin/.
	if want := filepath.Join("/gopath", "bin", "linux_arm64", Binary); cross.Output != want {
		t.Errorf("cross output %q, want %q", cross.Output, want)
	}

	for _, in := range []Install{same, cross} {
		if !contains(in.Env, "CGO_ENABLED=0") {
			t.Errorf("build does not disable cgo (%v) — the runtime image is distroless static", in.Env)
		}
		if want := "install"; in.Args[0] != want {
			t.Errorf("args %v do not start with %q", in.Args, want)
		}
		if got := in.Args[1]; got != ModulePath+"@"+rev {
			t.Errorf("args install %q, want the plane at kmx's own revision", got)
		}
	}
}

// The fetched path has no plane/Dockerfile to build, so it carries the
// runtime stage as a constant. The two must not drift: a base image changed
// in plane/Dockerfile for a reason (CA certificates, nonroot, a CVE) would
// otherwise stay unchanged on the one path with no checkout to notice.
func TestRuntimeBaseMatchesThePlaneDockerfile(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "plane", "Dockerfile"))
	if err != nil {
		t.Skipf("no checkout to compare against: %v", err)
	}
	last := ""
	for _, line := range strings.Split(string(body), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "FROM" {
			last = fields[1]
		}
	}
	if last != RuntimeBase {
		t.Errorf("plane/Dockerfile's runtime stage is %q but kmx packages onto %q", last, RuntimeBase)
	}
	if df := Dockerfile(); !strings.Contains(df, "FROM "+RuntimeBase) ||
		!strings.Contains(df, `ENTRYPOINT ["/`+Binary+`"]`) {
		t.Errorf("generated Dockerfile does not package the binary onto the runtime base:\n%s", df)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
