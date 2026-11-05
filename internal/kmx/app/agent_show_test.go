package app

// `kmx agent show` against a fake cluster.
//
// The chain this view renders is three reads joined by hand today, and every
// test below is about one of the joins being broken in a different way. The
// healthy case is the least interesting one here: what an operator needs from
// this command is the sentence that names the broken hop.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

const healthyAgent = `{"metadata":{"name":"concierge","namespace":"demo"},
"spec":{"providerRef":{"name":"local"},"model":{"name":"qwen2.5:3b"},
        "tools":[{"name":"menu_lookup"},{"name":"refund_issue","enabled":false}]},
"status":{"ready":true,"activeTasks":0}}`

const healthyProvider = `{"metadata":{"name":"local","namespace":"demo"},
"spec":{"type":"openai","baseURL":"http://ollama.ollama:11434/v1",
"defaultModel":"qwen2.5:3b","secretRef":{"name":"local-provider-key","key":"api-key"}},
"status":{"ready":true}}`

type showFixture struct {
	app *App
	out *bytes.Buffer
}

func newShowFixture(t *testing.T) *showFixture {
	t.Helper()
	dir := t.TempDir()
	installDirectKubectlFixture(t, dir, "inspect")
	out := &bytes.Buffer{}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_INSPECT_CALLS", filepath.Join(dir, "args"))
	t.Setenv("KMX_INSPECT_AGENT", healthyAgent)
	t.Setenv("KMX_INSPECT_PROVIDER", healthyProvider)
	t.Setenv("KMX_INSPECT_SECRET", "secret/local-provider-key\n")
	t.Setenv("KMX_INSPECT_TASKS", `{"items":[]}`)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	r := run.Default()
	r.Stdout, r.Stderr = out, out
	return &showFixture{
		app: &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceKubeCtx},
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
	t.Setenv("KMX_INSPECT_PROVIDER", `ERROR:Error from server (NotFound): providers.core.orka.ai "local" not found`)
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
	t.Setenv("KMX_INSPECT_SECRET", `ERROR:Error from server (NotFound): secrets "k" not found`)
	if err := f.app.ShowAgent("concierge", showOpts()); err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(f.out.String(), "MISSING") {
		t.Errorf("a missing Secret is not reported as missing:\n%s", f.out.String())
	}

	g := newShowFixture(t)
	t.Setenv("KMX_INSPECT_SECRET", `ERROR:Error from server (Forbidden): secrets is forbidden`)
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
	t.Setenv("KMX_INSPECT_PROVIDER", `ERROR:Unable to connect to the server: dial tcp: i/o timeout`)
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
	t.Setenv("KMX_INSPECT_AGENT", `{"metadata":{"name":"concierge","namespace":"demo"},
	"spec":{"providerRef":{"name":"local"}},"status":{"ready":true}}`)
	if err := f.app.ShowAgent("concierge", showOpts()); err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(f.out.String(), "the Provider's default") {
		t.Errorf("the view does not say where the model came from:\n%s", f.out.String())
	}
}

func TestAgentShowUsesCrossNamespaceProviderAndSecret(t *testing.T) {
	f := newShowFixture(t)
	t.Setenv("KMX_INSPECT_AGENT", `{"metadata":{"name":"concierge","namespace":"demo"},"spec":{"providerRef":{"name":"local","namespace":"inference"}},"status":{"ready":true}}`)
	t.Setenv("KMX_INSPECT_PROVIDER", `{"metadata":{"name":"local","namespace":"inference"},"spec":{"type":"openai","baseURL":"https://model.invalid/v1","defaultModel":"qualified-model","secretRef":{"name":"model-key"}},"status":{"ready":true}}`)
	if err := f.app.ShowAgent("concierge", showOpts()); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	if !strings.Contains(out, "provider inference/local") || !strings.Contains(out, "secret inference/model-key") || !strings.Contains(out, "qualified-model") {
		t.Fatalf("cross-namespace dependency was not rendered exactly:\n%s", out)
	}
	calls, err := os.ReadFile(os.Getenv("KMX_INSPECT_CALLS"))
	if err != nil {
		t.Fatal(err)
	}
	if text := string(calls); !strings.Contains(text, "-n inference get providers.core.orka.ai local") || !strings.Contains(text, "-n inference get secret model-key") {
		t.Fatalf("cross-namespace dependency was read from the wrong target:\n%s", text)
	}
}

func TestAgentShowRefusesInvalidReferenceNamespaceBeforeFollowup(t *testing.T) {
	f := newShowFixture(t)
	t.Setenv("KMX_INSPECT_AGENT", `{"metadata":{"name":"concierge","namespace":"demo"},"spec":{"providerRef":{"name":"local","namespace":"--server=attacker"}}}`)
	err := f.app.ShowAgent("concierge", showOpts())
	if err == nil || !strings.Contains(err.Error(), "invalid identity or Provider reference") {
		t.Fatalf("invalid Provider namespace was accepted: %v", err)
	}
	calls, readErr := os.ReadFile(os.Getenv("KMX_INSPECT_CALLS"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(calls), "get providers.core.orka.ai") || strings.Contains(string(calls), "get secret") {
		t.Fatalf("invalid namespace reached follow-up kubectl:\n%s", calls)
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
	t.Setenv("KMX_INSPECT_PROVIDER", `ERROR:Error from server (NotFound): providers.core.orka.ai "local" not found`)
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

func TestAgentShowJSONReturnsWriteError(t *testing.T) {
	f := newShowFixture(t)
	reader, writer := io.Pipe()
	defer writer.Close()
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	f.app.Out = writer
	opt := showOpts()
	opt.Output = "json"
	if err := f.app.ShowAgent("concierge", opt); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("JSON output error = %v, want %v", err, io.ErrClosedPipe)
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

func TestAgentShowHumanFieldsAreTerminalSafeAndBounded(t *testing.T) {
	f := newShowFixture(t)
	injected := "safe\nforged\t" + "\x1b]52;c;OSC-CANARY\a" + "\x1b[2J" + "\u009b31m" + "\u009dC1-CANARY\u009c" + "\u202e\u200b" + strings.Repeat("x", 600)
	agent := map[string]any{
		"metadata": map[string]any{"name": "concierge", "namespace": "demo"},
		"spec": map[string]any{
			"providerRef": map[string]any{"name": "local"},
			"model":       map[string]any{"name": injected},
			"tools":       []any{map[string]any{"name": injected}},
			"skills":      []any{map[string]any{"name": injected}},
		},
		"status": map[string]any{"ready": true, "lastUsed": injected},
	}
	provider := map[string]any{
		"metadata": map[string]any{"name": "local", "namespace": "demo"},
		"spec":     map[string]any{"type": injected, "baseURL": injected, "secretRef": map[string]any{"name": "model-key"}},
		"status":   map[string]any{"ready": false, "message": injected},
	}
	tasks := map[string]any{"items": []any{map[string]any{
		"metadata": map[string]any{"name": injected, "namespace": "demo", "creationTimestamp": "2026-01-02T03:04:05Z"},
		"spec":     map[string]any{"agentRef": map[string]any{"name": "concierge"}},
		"status":   map[string]any{"phase": injected, "message": injected},
	}}}
	for key, value := range map[string]any{"KMX_INSPECT_AGENT": agent, "KMX_INSPECT_PROVIDER": provider, "KMX_INSPECT_TASKS": tasks} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, string(raw))
	}
	if err := f.app.ShowAgent("concierge", showOpts()); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	if strings.ContainsAny(out, "\x1b\u009b\u009d\u009c\u202e\u200b") || strings.Contains(out, "OSC-CANARY") || strings.Contains(out, "C1-CANARY") || strings.Contains(out, "\nforged") {
		t.Fatalf("unsafe cluster field reached show output: %q", out)
	}
	if !strings.Contains(out, "safe forged") || len(out) > 4000 {
		t.Fatalf("show output was not compacted and bounded (%d bytes): %q", len(out), out)
	}
}

func TestAgentShowJSONPreservesClusterValues(t *testing.T) {
	f := newShowFixture(t)
	value := "raw\n\x1b[31m\u202e"
	raw, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"name": "concierge", "namespace": "demo"},
		"spec":     map[string]any{"providerRef": map[string]any{"name": "local"}, "model": map[string]any{"name": value}},
		"status":   map[string]any{"ready": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_INSPECT_AGENT", string(raw))
	opt := showOpts()
	opt.Output = "json"
	if err := f.app.ShowAgent("concierge", opt); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(f.out.Bytes(), &got); err != nil || got.Model != value {
		t.Fatalf("machine JSON changed cluster value: %+v, %v", got, err)
	}
}

func TestAgentShowWithholdsCredentialBearingProviderDiagnostics(t *testing.T) {
	canary := "kmh_" + "OBVIOUSLYFAKE000000"
	for _, tc := range []struct {
		name, endpoint, message string
	}{
		{"userinfo", "https://user:" + canary + "@model.invalid/v1", ""},
		{"query", "https://model.invalid/v1?api_key=" + canary, ""},
		{"path", "https://model.invalid/" + canary, ""},
		{"message", "https://model.invalid/v1", "authentication failed for " + canary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, format := range []string{"table", "json"} {
				f := newShowFixture(t)
				provider := map[string]any{
					"metadata": map[string]any{"name": "local", "namespace": "demo"},
					"spec": map[string]any{"type": "openai", "baseURL": tc.endpoint,
						"secretRef": map[string]any{"name": "model-key"}},
					"status": map[string]any{"ready": tc.message == "", "message": tc.message},
				}
				raw, err := json.Marshal(provider)
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("KMX_INSPECT_PROVIDER", string(raw))
				opt := showOpts()
				opt.Output = format
				if err := f.app.ShowAgent("concierge", opt); err != nil {
					t.Fatal(err)
				}
				out := f.out.String()
				if strings.Contains(out, canary) || tc.message != "" && strings.Contains(out, tc.message) {
					t.Fatalf("%s output disclosed Provider diagnostics: %q", format, out)
				}
				if tc.endpoint != "https://model.invalid/v1" && !strings.Contains(out, "endpoint withheld") {
					t.Fatalf("%s output did not disclose endpoint withholding: %q", format, out)
				}
			}
		})
	}
}

func TestAgentShowTaskAttributionIncludesReferenceNamespace(t *testing.T) {
	f := newShowFixture(t)
	t.Setenv("KMX_INSPECT_TASKS", `{"items":[
{"metadata":{"name":"right","namespace":"demo","creationTimestamp":"2026-01-02T00:00:00Z"},"spec":{"agentRef":{"name":"concierge"}}},
{"metadata":{"name":"wrong","namespace":"demo","creationTimestamp":"2026-01-03T00:00:00Z"},"spec":{"agentRef":{"name":"concierge","namespace":"other"}}},
{"metadata":{"name":"foreign","namespace":"other","creationTimestamp":"2026-01-04T00:00:00Z"},"spec":{"agentRef":{"name":"concierge","namespace":"demo"}}}
]}`)
	if err := f.app.ShowAgent("concierge", showOpts()); err != nil {
		t.Fatal(err)
	}
	if out := f.out.String(); !strings.Contains(out, "right") || strings.Contains(out, "wrong") || strings.Contains(out, "foreign") {
		t.Fatalf("Task attribution ignored namespace identity:\n%s", out)
	}
}

func TestAgentShowReadsBoundedTaskPages(t *testing.T) {
	f := newShowFixture(t)
	t.Setenv("KMX_INSPECT_TASKS", `{"metadata":{"continue":"next"},"items":[{"metadata":{"name":"one","namespace":"demo","creationTimestamp":"2026-01-01T00:00:00Z"},"spec":{"agentRef":{"name":"concierge"}},"status":{"phase":"Succeeded"}}]}`)
	t.Setenv("KMX_INSPECT_TASKS_CONTINUED", `{"items":[{"metadata":{"name":"two","namespace":"demo","creationTimestamp":"2026-01-02T00:00:00Z"},"spec":{"agentRef":{"name":"concierge"}},"status":{"phase":"Running"}}]}`)
	if err := f.app.ShowAgent("concierge", showOpts()); err != nil {
		t.Fatal(err)
	}
	if text := f.out.String(); !strings.Contains(text, "one") || !strings.Contains(text, "two") {
		t.Fatalf("paged Task history was not preserved:\n%s", text)
	}
	calls, err := os.ReadFile(filepath.Join(os.Getenv("KMX_INSPECT_CALLS")))
	if err != nil {
		t.Fatal(err)
	}
	text := string(calls)
	if !strings.Contains(text, "get --raw /apis/core.orka.ai/v1alpha1/namespaces/demo/tasks?limit=128") || !strings.Contains(text, "continue=next") || strings.Contains(text, "get tasks.core.orka.ai -o json") {
		t.Fatalf("Task history was not paged and bounded:\n%s", text)
	}
}

func TestAgentShowOversizedReadsAndErrorsStaySafe(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
	}{
		{"stdout", "REPEAT:4194305:x", "safe size limit"},
		{"stderr", "ERRORREPEAT:8192:x", "check permissions, prerequisites and the selected context"},
		{"forbidden", "ERROR:Error from server (Forbidden): private-token", "access forbidden"},
		{"unauthorized", "ERROR:error: You must be logged in to the server (Unauthorized): private-token", "access forbidden"},
		{"timeout", "ERROR:Unable to connect to private-host: i/o timeout", "cancelled or timed out"},
		{"generic", "ERROR:exec auth private-token at private-host", "check permissions, prerequisites and the selected context"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newShowFixture(t)
			t.Setenv("KMX_INSPECT_AGENT", tc.value)
			err := f.app.ShowAgent("concierge", showOpts())
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private") || len(err.Error()) > 1024 {
				t.Fatalf("unsafe show error: %v", err)
			}
		})
	}
}

func TestAgentShowRefusesClusterControlledKubectlArgumentsBeforeFollowup(t *testing.T) {
	for _, tc := range []struct {
		name, agent, provider string
	}{
		{"provider flag", strings.Replace(healthyAgent, `"name":"local"`, `"name":"--server=https://attacker.invalid"`, 1), healthyProvider},
		{"secret flag", healthyAgent, strings.Replace(healthyProvider, `"name":"local-provider-key"`, `"name":"--token=private"`, 1)},
		{"wrong Agent name", strings.Replace(healthyAgent, `"name":"concierge"`, `"name":"other"`, 1), healthyProvider},
		{"wrong namespace", strings.Replace(healthyAgent, `"namespace":"demo"`, `"namespace":"other"`, 1), healthyProvider},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newShowFixture(t)
			t.Setenv("KMX_INSPECT_AGENT", tc.agent)
			t.Setenv("KMX_INSPECT_PROVIDER", tc.provider)
			err := f.app.ShowAgent("concierge", showOpts())
			if err == nil || !strings.Contains(err.Error(), "invalid") {
				t.Fatalf("unsafe reference was accepted: %v", err)
			}
			calls, readErr := os.ReadFile(os.Getenv("KMX_INSPECT_CALLS"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			text := string(calls)
			if tc.name == "secret flag" {
				if !strings.Contains(text, "get providers.core.orka.ai local") || strings.Contains(text, "get secret") {
					t.Fatalf("unsafe Secret reference reached kubectl:\n%s", text)
				}
			} else if strings.Contains(text, "get providers.core.orka.ai") || strings.Contains(text, "get secret") {
				t.Fatalf("unsafe Agent reference reached follow-up kubectl:\n%s", text)
			}
		})
	}
}

func TestAgentShowBoundsProviderSecretAndTaskReads(t *testing.T) {
	for _, tc := range []struct {
		name, key, want string
	}{
		{"provider", "KMX_INSPECT_PROVIDER", "UNREADY"},
		{"secret", "KMX_INSPECT_SECRET", unknownHop},
		{"tasks", "KMX_INSPECT_TASKS", "none read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newShowFixture(t)
			t.Setenv(tc.key, "REPEAT:4194305:x")
			if err := f.app.ShowAgent("concierge", showOpts()); err != nil {
				t.Fatal(err)
			}
			out := f.out.String()
			if !strings.Contains(out, tc.want) || strings.Contains(out, strings.Repeat("x", 100)) || len(out) > 4096 {
				t.Fatalf("%s read was unsafe or unbounded (%d bytes): %q", tc.name, len(out), out)
			}
		})
	}
}

func TestOrkaHumanTextHandlesUTF8Boundary(t *testing.T) {
	got := orkaHumanText(strings.Repeat("é", 200), 33)
	if !utf8.ValidString(got) || len(got) > 33 || !strings.HasSuffix(got, "…") {
		t.Fatalf("invalid bounded UTF-8: %q (%d bytes)", got, len(got))
	}
}
