package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestAgentListOutputValidation(t *testing.T) {
	a := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := a.ListAgents("toml", "team-a"); err == nil {
		t.Fatal("unsupported agent list output was accepted")
	}
}

// An Orka Agent with no model of its own resolves the Provider's default.
// An empty cell there would read as "no model", which is a different claim.
func TestOrkaAgentListRowsNameTheProviderDefault(t *testing.T) {
	var agents objectList[orkaAgentSpec]
	if err := json.Unmarshal([]byte(`{"items":[
  {"metadata":{"name":"zeta"},"spec":{"providerRef":{"name":"p-z"},"model":{"name":"llama"}},"status":{"ready":false}},
  {"metadata":{"name":"alpha"},"spec":{"providerRef":{"name":"p-a"}},"status":{"ready":true}}
]}`), &agents); err != nil {
		t.Fatal(err)
	}
	rows := orkaAgentListRows(agents.Items)
	if rows[0][0] != "alpha" || rows[0][1] != "yes" || rows[0][2] != "p-a" {
		t.Fatalf("unexpected first row: %v", rows[0])
	}
	if !strings.Contains(rows[0][3], "Provider's default") {
		t.Errorf("an absent model reads as %q, which does not say where the model comes from", rows[0][3])
	}
	if rows[1][0] != "zeta" || rows[1][1] != "no" || rows[1][3] != "llama" {
		t.Fatalf("unexpected second row: %v", rows[1])
	}
}

func TestOrkaAgentListRowsAcceptExternalRuntimeObjects(t *testing.T) {
	var agents objectList[orkaAgentSpec]
	if err := json.Unmarshal([]byte(`{"items":[{
  "metadata":{"name":"cli-agent"},
  "spec":{"providerRef":{"name":"provider"},"runtime":{"type":"cli","command":["agent"]}},
  "status":{"ready":true}
}]}`), &agents); err != nil {
		t.Fatal(err)
	}
	rows := orkaAgentListRows(agents.Items)
	if len(rows) != 1 || rows[0][0] != "cli-agent" || rows[0][1] != "yes" {
		t.Fatalf("rows = %#v", rows)
	}
}

// A cluster with no Orka at all is a different answer from an empty namespace,
// and saying "none" about the first would be wrong.
func TestOrkaAgentListSeparatesAbsentKindFromEmptyNamespace(t *testing.T) {
	a, _ := agentListFixture(t)
	t.Setenv("KMX_INSPECT_AGENTS", `ERROR:error: the server doesn't have a resource type "agents": private-list-token`)
	err := a.ListAgents("table", "team-a")
	if err == nil {
		t.Fatal("a cluster with no Orka kind reported an empty list")
	}
	want := "kmx --context kind-test cluster orka install"
	if runtime.GOOS == "windows" {
		want = "supported administration environment"
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not say how to fix it: %v", err)
	}
	if strings.Contains(err.Error(), "private-list-token") {
		t.Fatalf("raw kubectl stderr escaped: %v", err)
	}
}

func TestOrkaAgentListHumanFieldsAreTerminalSafeAndBounded(t *testing.T) {
	a, _ := agentListFixture(t)
	injected := "safe\nforged\t" + "\x1b]52;c;OSC-CANARY\a" + "\x1b[2J" + "\u009b31m" + "\u009dC1-CANARY\u009c" + "\u202e\u200b" + strings.Repeat("x", 600)
	raw, err := json.Marshal(map[string]any{"items": []any{map[string]any{
		"metadata": map[string]any{"name": injected},
		"spec":     map[string]any{"providerRef": map[string]any{"name": injected}, "model": map[string]any{"name": injected}},
		"status":   map[string]any{"ready": true},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_INSPECT_AGENTS", string(raw))
	if err := a.ListAgents("table", "team-a"); err != nil {
		t.Fatal(err)
	}
	out := a.Out.(*bytes.Buffer).String()
	if strings.ContainsAny(out, "\x1b\u009b\u009d\u009c\u202e\u200b") || strings.Contains(out, "OSC-CANARY") || strings.Contains(out, "C1-CANARY") || strings.Contains(out, "\nforged") {
		t.Fatalf("unsafe cluster field reached human output: %q", out)
	}
	if !strings.Contains(out, "safe forged") || len(out) > 1800 {
		t.Fatalf("human output was not compacted and bounded (%d bytes): %q", len(out), out)
	}
}

func TestOrkaAgentListMachineOutputPreservesValuesAndBoundsCapture(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			a, _ := agentListFixture(t)
			value := "raw\n\x1b[31m\u202e"
			t.Setenv("KMX_INSPECT_AGENTS", value)
			if err := a.ListAgents(format, "team-a"); err != nil {
				t.Fatal(err)
			}
			if got := a.Out.(*bytes.Buffer).String(); got != value {
				t.Fatalf("machine %s changed: %q", format, got)
			}
		})
	}
	a, _ := agentListFixture(t)
	t.Setenv("KMX_INSPECT_AGENTS", "REPEAT:4194305:x")
	err := a.ListAgents("json", "team-a")
	if err == nil || !strings.Contains(err.Error(), "safe size limit") || strings.Contains(err.Error(), strings.Repeat("x", 100)) {
		t.Fatalf("oversized machine output was not safely refused: %v", err)
	}
}

func TestOrkaAgentListErrorsAreSafelyClassified(t *testing.T) {
	for _, tc := range []struct {
		name, stderr, want string
	}{
		{"not-found", `Error from server (NotFound): agents.core.orka.ai "private-agent" not found`, "requested Kubernetes resource not found"},
		{"forbidden", `Error from server (Forbidden): private-token`, "access forbidden"},
		{"unauthorized", `error: You must be logged in to the server (Unauthorized): private-token`, "access forbidden"},
		{"timeout", `Unable to connect to the server: dial tcp private-host: i/o timeout`, "cancelled or timed out"},
		{"generic", `exec auth plugin failed at private-host with private-token`, "check permissions, prerequisites and the selected context"},
		{"oversized-stderr", "ERRORREPEAT:8192:x", "check permissions, prerequisites and the selected context"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := agentListFixture(t)
			if strings.HasPrefix(tc.stderr, "ERRORREPEAT:") {
				t.Setenv("KMX_INSPECT_AGENTS", tc.stderr)
			} else {
				t.Setenv("KMX_INSPECT_AGENTS", "ERROR:"+tc.stderr)
			}
			err := a.ListAgents("table", "team-a")
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private") || len(err.Error()) > 512 {
				t.Fatalf("unsafe classification: %v", err)
			}
		})
	}
}

func TestOrkaAgentListPrerequisiteFailureIsRedacted(t *testing.T) {
	a, _ := agentListFixture(t)
	t.Setenv("KMX_INSPECT_VERSION_FAIL", "1")
	t.Setenv("KMX_INSPECT_VERSION_STDERR", "exec-auth private-token at private-host")
	err := a.ListAgents("table", "team-a")
	if err == nil || !strings.Contains(err.Error(), "kubectl is unavailable or unusable") || strings.Contains(err.Error(), "private") || len(err.Error()) > 256 {
		t.Fatalf("unsafe prerequisite error: %v", err)
	}
}

func TestOrkaInspectionDisablesKubercPreferences(t *testing.T) {
	a, _ := agentListFixture(t)
	t.Setenv("KUBERC", "/private/kuberc")
	t.Setenv("KUBECTL_KUBERC", "true")
	a.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
		env := cmd.Environ()
		if !slices.Contains(env, "KUBERC=off") || !slices.Contains(env, "KUBECTL_KUBERC=false") {
			t.Fatalf("kuberc preferences not frozen: %v", env)
		}
		_, err := io.WriteString(cmd.Stdout, `{"items":[]}`)
		return err
	}
	if err := a.ListAgents("table", "team-a"); err != nil {
		t.Fatal(err)
	}
}

func TestOrkaAgentListUsesBoundedCaptureAndCancellation(t *testing.T) {
	a, _ := agentListFixture(t)
	var got []string
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		got = append([]string(nil), cmd.Args[1:]...)
		_, err := io.WriteString(cmd.Stdout, `{"items":[]}`)
		return err
	}
	if err := a.ListAgents("table", "team-a"); err != nil {
		t.Fatal(err)
	}
	want := []string{"--context", "kind-test", "--request-timeout=10s", "-n", "team-a", "get", "agents.core.orka.ai", "-o", "json"}
	if !slices.Equal(got, want) {
		t.Fatalf("capture args = %v, want %v", got, want)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	a.Run.Context = cancelled
	a.captureCommand = nil
	if _, err := a.orkaCapture(cancelled, nil, "get", "agents.core.orka.ai"); err == nil || !strings.Contains(err.Error(), "cancelled or timed out") {
		t.Fatalf("cancelled inspection = %v", err)
	}
}

func TestOrkaAgentListCancelsActiveCompiledKubectl(t *testing.T) {
	a, dir := agentListFixture(t)
	t.Setenv("KMX_INSPECT_DELAY", "1h")
	ctx, cancel := context.WithCancel(context.Background())
	a.Run.Context = ctx
	done := make(chan error, 1)
	go func() { done <- a.ListAgents("table", "team-a") }()

	calls := filepath.Join(dir, "calls")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if raw, err := os.ReadFile(calls); err == nil && strings.Contains(string(raw), "get agents.core.orka.ai") {
			break
		} else if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("compiled kubectl did not reach the active request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancelled or timed out") || strings.Contains(err.Error(), "private") {
			t.Fatalf("active cancellation was not safely classified: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("active kubectl did not stop on cancellation")
	}
}

func agentListFixture(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	installDirectKubectlFixture(t, dir, "inspect")
	t.Setenv("PATH", dir)
	t.Setenv("KMX_INSPECT_CALLS", filepath.Join(dir, "calls"))
	t.Setenv("KMX_INSPECT_AGENTS", `{"items":[]}`)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	var out bytes.Buffer
	return &App{
		Cfg: &config.Config{KubeContext: "kind-test"},
		Run: &run.Runner{Stdout: &out, Stderr: io.Discard},
		Out: &out, Err: &out,
	}, dir
}
