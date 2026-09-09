package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/planebuild"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The exact stderr from the failure this retry exists for: main's post-merge
// job on f9914d4, twice, before the same command succeeded unchanged.
const proxyRaceErr = `go: github.com/kaimahi-agents/kaimahi/plane/cmd/kaimahi-proxy@v0.0.0-20260903053920-f9914d4ce40c: ` +
	`module github.com/kaimahi-agents/kaimahi@v0.0.0-20260903053920-f9914d4ce40c found, ` +
	`but does not contain package github.com/kaimahi-agents/kaimahi/plane/cmd/kaimahi-proxy`

func TestProxyRaceIsRecognised(t *testing.T) {
	if !planeNotOnProxyYet.MatchString(proxyRaceErr) {
		t.Error("this failure must trigger the explicit module resolve")
	}
}

func TestPlaneRefusesDisagreeingKindTargetBeforeBuildOrLoad(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		for _, entry := range []string{"plane", "plane image", "planeImage", "loadImage"} {
			t.Run(engine+"/"+entry, func(t *testing.T) {
				dir := t.TempDir()
				log := filepath.Join(dir, "commands")
				t.Setenv("KMX_IDENTITY_LOG", log)
				t.Setenv("KMX_TOOLCHAIN", "off")
				for _, tool := range []string{"kind", "kubectl", "go", "git", engine} {
					fakeTool(t, dir, tool, `printf '%s\n' "$0 $*" >> "$KMX_IDENTITY_LOG"; exit 99`)
				}
				t.Setenv("PATH", dir)
				a := &App{Cfg: &config.Config{KindCluster: "other", KubeContext: "kind-reviewed", ContainerEngine: engine},
					Run: &run.Runner{}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
				var err error
				switch entry {
				case "plane":
					err = a.Plane(PlaneOptions{})
				case "plane image":
					err = a.Plane(PlaneOptions{Step: "image"})
				case "planeImage":
					err = a.planeImage(PlaneOptions{})
				case "loadImage":
					err = a.loadImage()
				}
				if err == nil || !strings.Contains(err.Error(), "does not identify kind cluster") || !strings.Contains(err.Error(), "kind-other") {
					t.Fatalf("identity disagreement was not refused: %v", err)
				}
				if commands, err := os.ReadFile(log); !os.IsNotExist(err) {
					t.Fatalf("ran commands before refusing identity: %s (%v)", commands, err)
				}
			})
		}
	}
}

func TestLoadImageUsesMatchingKindIdentity(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "commands")
	t.Setenv("KMX_IDENTITY_LOG", log)
	fakeTool(t, dir, "kind", `printf '%s\n' "$*" >> "$KMX_IDENTITY_LOG"`)
	t.Setenv("PATH", dir)
	a := &App{Cfg: &config.Config{KindCluster: "reviewed", KubeContext: "kind-reviewed", ContainerEngine: "docker"}, Run: &run.Runner{}}
	if err := a.loadImage(); err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(log)
	if err != nil || string(commands) != "load docker-image "+PlaneImage+" --name reviewed\n" {
		t.Fatalf("loaded the wrong kind identity: %s (%v)", commands, err)
	}
}

func TestGoInstallPlaneChecksBothExitStatuses(t *testing.T) {
	for _, tc := range []struct {
		name, first, retry, resolve string
		wantErr, wantResolve        bool
	}{
		{"success", "0", "0", "0", false, false},
		{"compile failure", "2", "0", "0", true, false},
		{"proxy recovered", "1", "0", "0", false, true},
		{"retry failed", "1", "3", "0", true, true},
		{"resolve failed", "1", "0", "4", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			t.Setenv("KMX_INSTALL_LOG", log)
			t.Setenv("KMX_INSTALL_FIRST", tc.first)
			t.Setenv("KMX_INSTALL_RETRY", tc.retry)
			t.Setenv("KMX_INSTALL_RESOLVE", tc.resolve)
			t.Setenv("KMX_PROXY_RACE", proxyRaceErr)
			fakeTool(t, dir, "go", `
if [ "$1" = list ]; then
  printf 'resolve\n' >> "$KMX_INSTALL_LOG"
  exit "$KMX_INSTALL_RESOLVE"
fi
if [ -f "$KMX_INSTALL_LOG" ]; then
  printf 'retry\n' >> "$KMX_INSTALL_LOG"
  printf 'retry diagnostic\n'
  exit "$KMX_INSTALL_RETRY"
fi
printf 'install\n' >> "$KMX_INSTALL_LOG"
if [ "$KMX_INSTALL_FIRST" = 1 ]; then printf '%s\n' "$KMX_PROXY_RACE"; fi
exit "$KMX_INSTALL_FIRST"`)
			t.Setenv("PATH", dir)
			a := &App{Err: &bytes.Buffer{}}
			err := a.goInstallPlane(&run.Runner{}, planebuild.Install{Args: []string{"install", "example@rev"}}, "rev")
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v, want error=%v", err, tc.wantErr)
			}
			calls, _ := os.ReadFile(log)
			if strings.Contains(string(calls), "resolve") != tc.wantResolve {
				t.Fatalf("unexpected resolve behavior: %s", calls)
			}
			if tc.name == "retry failed" && (!strings.Contains(err.Error(), "exited 3") || !strings.Contains(err.Error(), "retry diagnostic")) {
				t.Fatalf("retry failure lost its diagnostic: %v", err)
			}
		})
	}
}

// The priming step must name the module that actually provides the binary,
// not the package and not the repo root — asking for either is what fails.
func TestNestedModuleIsThePlanesOwnModule(t *testing.T) {
	if planebuild.NestedModule != "github.com/kaimahi-agents/kaimahi/plane" {
		t.Errorf("nested module is %q", planebuild.NestedModule)
	}
	if !strings.HasPrefix(planebuild.ModulePath, planebuild.NestedModule+"/") {
		t.Errorf("%q must live inside %q", planebuild.ModulePath, planebuild.NestedModule)
	}
	if planebuild.NestedModule == "github.com/kaimahi-agents/kaimahi" {
		t.Error("the root module is exactly what Go already falls back to")
	}
}

func TestRealBuildFailuresAreNotRetried(t *testing.T) {
	// Waiting a minute to repeat a compile error helps nobody, and hiding a
	// genuine missing package behind five retries would be worse.
	for name, out := range map[string]string{
		"compile error":       "plane/internal/meter/meter.go:12:2: undefined: foo",
		"another module":      `module example.com/other@v1.2.3 found, but does not contain package example.com/other/cmd/thing`,
		"unknown revision":    "go: github.com/kaimahi-agents/kaimahi/plane@abc123: unknown revision abc123",
		"network unreachable": "go: module lookup disabled by GOFLAGS=-mod=vendor",
		"empty":               "",
	} {
		if planeNotOnProxyYet.MatchString(out) {
			t.Errorf("%s must not trigger the resolve path: %q", name, out)
		}
	}
}
