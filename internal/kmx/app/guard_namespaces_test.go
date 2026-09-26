package app

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The banner is output, and the supported first-answer path is Orka-only, so
// a banner naming the legacy runtime is the path advertising something it
// does not install. CI greps the whole quickstart transcript for "kagent"
// for exactly that reason; these tests are the same assertion without a
// cluster. "kaimahi" is checked on the namespace line alone, because the
// confirmation hint names KAIMAHI_CONFIRM and that is the product, not a
// namespace.
func assertOrkaOnlyBanner(t *testing.T, what, banner string) {
	t.Helper()
	if !strings.Contains(banner, OrkaPathNamespaces) {
		t.Errorf("%s does not name %q:\n%s", what, OrkaPathNamespaces, banner)
	}
	if strings.Contains(strings.ToLower(banner), "kagent") {
		t.Errorf("%s still names the legacy runtime:\n%s", what, banner)
	}
	for _, line := range strings.Split(banner, "\n") {
		if strings.Contains(line, "namespace(s):") && strings.Contains(strings.ToLower(line), "kaimahi") {
			t.Errorf("%s still names the kaimahi namespace: %q", what, line)
		}
	}
}

// The scoped list is the two namespaces the Orka path actually writes to,
// and it is derived from the constant the installer uses rather than spelled
// again here.
func TestOrkaPathNamespacesAreTheTwoTheOrkaPathWritesTo(t *testing.T) {
	if want := "ollama, " + OrkaNamespace; OrkaPathNamespaces != want {
		t.Fatalf("OrkaPathNamespaces is %q, want %q", OrkaPathNamespaces, want)
	}
}

// GuardCreateIn is the whole new surface: an exact namespace list for the
// callers that know theirs. Everything else keeps config.GuardNamespaces,
// which this asserts in the same place so the distinction is one test away
// from either change.
func TestGuardCreateInScopesTheBannerAndOrdinaryGuardsDoNot(t *testing.T) {
	scoped := guardBanner(t, func(a *App) error {
		return a.GuardCreateIn("create a local cluster and a first agent", "kmx quickstart", OrkaPathNamespaces)
	})
	assertOrkaOnlyBanner(t, "GuardCreateIn's banner", scoped)

	for _, tc := range []struct {
		name string
		call func(*App) error
	}{
		{"Guard", func(a *App) error { return a.Guard("act", "kmx status") }},
		{"GuardCreate", func(a *App) error { return a.GuardCreate("act", "kmx up --step cluster") }},
		{"GuardKnown", func(a *App) error { return a.GuardKnown("act", "kmx down") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			banner := guardBanner(t, tc.call)
			if !strings.Contains(banner, config.GuardNamespaces) {
				t.Errorf("%s no longer names %q:\n%s", tc.name, config.GuardNamespaces, banner)
			}
		})
	}
}

// The supported first answer. The guard runs before any step, so a fixture
// whose first step fails still proves what the banner said.
func TestQuickstartGuardBannerNamesOnlyTheOrkaNamespaces(t *testing.T) {
	a, errOut := guardFixture(t)
	if err := a.Quickstart(QuickstartOptions{}); err == nil {
		t.Fatalf("the fixture's first step must fail; got a complete run:\n%s", errOut)
	}
	assertOrkaOnlyBanner(t, "the quickstart banner", errOut.String())
}

// A bare `kmx up` is the Orka runtime and nothing else — CI greps its
// transcript for the legacy runtime too.
func TestBareUpGuardBannerNamesOnlyTheOrkaNamespaces(t *testing.T) {
	a, errOut, _ := upFixture(t)
	a.guarded = false
	if err := a.Up(""); err != nil {
		t.Fatalf("a bare Orka-only `kmx up` failed: %v\n%s", err, errOut)
	}
	assertOrkaOnlyBanner(t, "the bare `kmx up` banner", errOut.String())
}

// Every addressable step is on the Orka path now, so every one of them gets
// the scoped banner. This is the half that stops the wider legacy list
// surviving behind a step name: naming a namespace nothing is written to is
// its own untruth, and `kagent` is no longer written to by anything.
func TestEveryUpStepBannerNamesOnlyTheOrkaNamespaces(t *testing.T) {
	for _, step := range []string{"ollama", "model", "orka"} {
		t.Run(step, func(t *testing.T) {
			a, errOut, _ := upFixture(t)
			a.guarded = false
			if err := a.Up(step); err != nil {
				t.Fatalf("the %s step failed: %v\n%s", step, err, errOut)
			}
			assertOrkaOnlyBanner(t, "the `kmx up --step "+step+"` banner", errOut.String())
		})
	}
}

// The wizard shows its target twice: inside the TUI on the already-safe
// local path, and through the guard on any other. Both are the same list.
func TestQuickstartWizardTargetIsScopedOnBothPaths(t *testing.T) {
	a, _ := guardFixture(t)
	target, err := a.quickstartWizardTarget()
	if err != nil {
		t.Fatalf("local target: %v", err)
	}
	if target.Namespaces != OrkaPathNamespaces {
		t.Errorf("the wizard's local target names %q, want %q", target.Namespaces, OrkaPathNamespaces)
	}

	remote, errOut := guardFixture(t)
	remote.Cfg.KubeContext = "remote-cluster"
	remote.Cfg.Confirm = "remote-cluster"
	if _, err := remote.quickstartWizardTarget(); err != nil {
		t.Fatalf("a confirmed remote target must proceed: %v\n%s", err, errOut)
	}
	assertOrkaOnlyBanner(t, "the wizard's guard banner", errOut.String())
}

// guardFixture is a cluster boundary that answers `kubectl config view` and
// fails every other command, so a command runs as far as its guard and no
// further. kind-test is loopback (proceeds without asking) and
// remote-cluster is not (the confirmation path).
func guardFixture(t *testing.T) (*App, *bytes.Buffer) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake cluster boundary is a shell script")
	}
	dir := t.TempDir()
	kubectl := `#!/bin/sh
case "$*" in
  *version*) exit 0 ;;
  *"config view -o json"*)
    printf '%s' '{"contexts":[{"name":"kind-test","context":{"cluster":"local"}},{"name":"remote-cluster","context":{"cluster":"remote"}}],"clusters":[{"name":"local","cluster":{"server":"https://127.0.0.1:6443"}},{"name":"remote","cluster":{"server":"https://example.invalid:443"}}],"current-context":"kind-test"}'
    exit 0 ;;
esac
exit 99
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(kubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	// Runnable enough to pass preflight, and unable to do any cluster work:
	// a command reaches its guard and stops at its first real step.
	fakeTool(t, dir, "kind", `case "$*" in *version*) exit 0 ;; esac; exit 99`)
	fakeTool(t, dir, "docker", "exit 0")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TOOLCHAIN", "off")
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a := &App{
		Cfg: &config.Config{KindCluster: "test", KubeContext: "kind-test",
			ContextSource: config.SourceKubeCtx, ContainerEngine: "docker", Model: "qwen2.5:3b"},
		Run: &run.Runner{Stdout: out, Stderr: errOut}, Out: out, Err: errOut,
	}
	return a, errOut
}

// guardBanner runs one guard call against guardFixture and returns what the
// operator was shown.
func guardBanner(t *testing.T, call func(*App) error) string {
	t.Helper()
	a, errOut := guardFixture(t)
	if err := call(a); err != nil {
		t.Fatalf("local kind must proceed: %v\n%s", err, errOut)
	}
	return errOut.String()
}
