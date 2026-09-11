package app

// `kmx agent show` against a fake cluster.
//
// The chain this view renders is three reads joined by hand today, and every
// test below is about one of the joins being broken in a different way. The
// healthy case is the least interesting one here: what an operator needs from
// this command is the sentence that names the broken hop.

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

// The fake answers the four reads this command makes, switched by env so a
// test can break exactly one hop and leave the rest healthy.
const fakeShowKubectl = `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_ARGS"
case "$*" in
  *"get agents.core.orka.ai"*)
    case "$KMX_TEST_AGENT" in
      notfound) printf 'Error from server (NotFound): agents.core.orka.ai "concierge" not found\n' >&2; exit 1 ;;
      *) printf '%s' "$KMX_TEST_AGENT" ;;
    esac ;;
  *"get providers.core.orka.ai"*)
    case "$KMX_TEST_PROVIDER" in
      notfound) printf 'Error from server (NotFound): providers.core.orka.ai "local" not found\n' >&2; exit 1 ;;
      unreachable) printf 'Unable to connect to the server: dial tcp: i/o timeout\n' >&2; exit 1 ;;
      *) printf '%s' "$KMX_TEST_PROVIDER" ;;
    esac ;;
  *"get secret"*)
    case "$KMX_TEST_SECRET" in
      notfound) printf 'Error from server (NotFound): secrets "k" not found\n' >&2; exit 1 ;;
      denied) printf 'Error from server (Forbidden): secrets is forbidden\n' >&2; exit 1 ;;
      *) printf 'secret/local-provider-key\n' ;;
    esac ;;
  *"get tasks.core.orka.ai"*) printf '%s' "$KMX_TEST_TASKS" ;;
esac
exit 0
`

const healthyAgent = `{"metadata":{"name":"concierge","namespace":"demo"},
"spec":{"providerRef":{"name":"local"},"model":{"name":"qwen2.5:3b"},
        "tools":[{"name":"menu_lookup"},{"name":"refund_issue","enabled":false}]},
"status":{"ready":true,"activeTasks":0}}`

const healthyProvider = `{"spec":{"type":"openai","baseURL":"http://ollama.ollama:11434/v1",
"defaultModel":"qwen2.5:3b","secretRef":{"name":"local-provider-key","key":"api-key"}},
"status":{"ready":true}}`

type showFixture struct {
	app *App
	out *bytes.Buffer
}

func newShowFixture(t *testing.T) *showFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake kubectl is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakeShowKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TEST_ARGS", filepath.Join(dir, "args"))
	t.Setenv("KMX_TEST_AGENT", healthyAgent)
	t.Setenv("KMX_TEST_PROVIDER", healthyProvider)
	t.Setenv("KMX_TEST_TASKS", `{"items":[]}`)
	r := run.Default()
	r.Stdout, r.Stderr = out, out
	return &showFixture{
		app: &App{Cfg: &config.Config{KubeContext: "kind-x", ContextSource: config.SourceKubeCtx},
			Run: r, Out: out, Err: out},
		out: out,
	}
}

func showOpts() ShowOptions { return ShowOptions{Namespace: "demo"} }

// Orka watches namespaces explicitly. A default that guessed wrong would
// report "not found" about a namespace the operator never meant.
func TestAgentShowRequiresANamespace(t *testing.T) {
	f := newShowFixture(t)
	err := f.app.ShowAgent("concierge", ShowOptions{})
	if err == nil || !strings.Contains(err.Error(), "--namespace is required") {
		t.Fatalf("a missing namespace was accepted: %v", err)
	}
}

// The healthy case still has to render every hop: a chain that only appears
// when it is broken is a chain nobody trusts when it says nothing.
func TestAgentShowRendersTheWholeChainWhenHealthy(t *testing.T) {
	f := newShowFixture(t)
	if err := f.app.ShowAgent("concierge", showOpts()); err != nil {
		t.Fatalf("show: %v", err)
	}
	for _, want := range []string{"concierge", "provider local", "secret local-provider-key", "present", "qwen2.5:3b"} {
		if !strings.Contains(f.out.String(), want) {
			t.Errorf("the view omits %q:\n%s", want, f.out.String())
		}
	}
}

// The failure this command exists for: the Agent is fine and its Provider is
// not, so every model call is refused and the Agent's own object says nothing.
func TestAgentShowNamesAProviderThatDoesNotExist(t *testing.T) {
	f := newShowFixture(t)
	t.Setenv("KMX_TEST_PROVIDER", "notfound")
	if err := f.app.ShowAgent("concierge", showOpts()); err != nil {
		t.Fatalf("show: %v", err)
	}
	out := f.out.String()
	if !strings.Contains(out, "every model call is refused") {
		t.Errorf("the view does not say what a missing Provider costs:\n%s", out)
	}
}

// A missing Secret and an unreadable one have opposite fixes — create it, or
// go fix your permissions — so they must not print the same word.
func TestAgentShowSeparatesAMissingSecretFromAnUnreadableOne(t *testing.T) {
	f := newShowFixture(t)
	t.Setenv("KMX_TEST_SECRET", "notfound")
	if err := f.app.ShowAgent("concierge", showOpts()); err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(f.out.String(), "MISSING") {
		t.Errorf("a missing Secret is not reported as missing:\n%s", f.out.String())
	}

	g := newShowFixture(t)
	t.Setenv("KMX_TEST_SECRET", "denied")
	if err := g.app.ShowAgent("concierge", showOpts()); err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(g.out.String(), unknownHop) {
		t.Errorf("an unreadable Secret is not reported as unknown:\n%s", g.out.String())
	}
	if strings.Contains(g.out.String(), "MISSING") {
		t.Error("an unreadable Secret was reported as missing, which sends an operator to create one that may exist")
	}
}

// An unreachable API server is not a Provider that is absent, and the view
// must not turn one into the other.
func TestAgentShowDoesNotReportAnUnreadProviderAsAbsent(t *testing.T) {
	f := newShowFixture(t)
	t.Setenv("KMX_TEST_PROVIDER", "unreachable")
	if err := f.app.ShowAgent("concierge", showOpts()); err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.Contains(f.out.String(), "does not exist") {
		t.Errorf("an unreadable Provider was reported as absent:\n%s", f.out.String())
	}
}

// An Agent that names no model falls back to the Provider's default, so an
// empty cell would hide the answer rather than report it.
func TestAgentShowResolvesTheModelThroughTheProviderDefault(t *testing.T) {
	f := newShowFixture(t)
	t.Setenv("KMX_TEST_AGENT", `{"metadata":{"name":"concierge","namespace":"demo"},
	"spec":{"providerRef":{"name":"local"}},"status":{"ready":true}}`)
	if err := f.app.ShowAgent("concierge", showOpts()); err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(f.out.String(), "the Provider's default") {
		t.Errorf("the view does not say where the model came from:\n%s", f.out.String())
	}
}

// A listed-but-disabled tool is a different state from one that was never
// wired, and only one of them is somebody's mistake.
func TestAgentShowKeepsADisabledToolVisible(t *testing.T) {
	f := newShowFixture(t)
	if err := f.app.ShowAgent("concierge", showOpts()); err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(f.out.String(), "refund_issue (disabled)") {
		t.Errorf("a disabled tool was dropped rather than shown as disabled:\n%s", f.out.String())
	}
}

// The structured view has to carry the same verdicts, or a script and a
// person reading the same cluster would disagree.
func TestAgentShowJSONCarriesTheSameVerdicts(t *testing.T) {
	f := newShowFixture(t)
	t.Setenv("KMX_TEST_PROVIDER", "notfound")
	opt := showOpts()
	opt.Output = "json"
	if err := f.app.ShowAgent("concierge", opt); err != nil {
		t.Fatalf("show: %v", err)
	}
	out := f.out.String()
	if !strings.Contains(out, `"ready": "unknown"`) {
		t.Errorf("json does not report the unread Provider as unknown:\n%s", out)
	}
	if !strings.Contains(out, "not reached") {
		t.Error("json does not say the Secret hop was never reached")
	}
}

func TestAgentShowRejectsAnUnsupportedOutput(t *testing.T) {
	f := newShowFixture(t)
	opt := showOpts()
	opt.Output = "yaml"
	if err := f.app.ShowAgent("concierge", opt); err == nil {
		t.Fatal("an unsupported output was accepted")
	}
}
