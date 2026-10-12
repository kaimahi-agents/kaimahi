package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	protocol "github.com/kaimahi-agents/kaimahi/internal/kmx/app/kubectlfake"
)

type protocolStep struct {
	args   []string
	stdin  string
	env    []string
	status int
}
type protocolResult struct {
	stdout, stderr string
	status         int
}

func TestKubectlProtocolExecutableParity(t *testing.T) {
	pinned := func(args ...string) []string {
		return append([]string{"--context", "kind-test", "--request-timeout=10s"}, args...)
	}
	for _, tc := range []struct {
		name, helper, dirKey string
		handler              func(protocol.Request) error
		steps                []protocolStep
	}{
		{"reconcile", "TestReconcileKubectlHelper", "KMX_RECONCILE_DIR", protocol.Reconcile, []protocolStep{
			{args: []string{"version", "--client"}},
			{args: pinned("config", "view", "-o", "json")},
			{args: []string{"--context", "wrong", "config", "view"}, status: 1},
			{args: pinned("create", "-f", "-"), stdin: "{", status: 1},
			{args: pinned("create", "-f", "-", "--dry-run=server"), stdin: `{"kind":"Agent","metadata":{"name":"sample"},"spec":{"description":"original"}}`},
			{args: pinned("create", "-f", "-"), stdin: `{"kind":"Agent","metadata":{"name":"sample","annotations":{"kaimahi.dev/portable-digest":"old","kaimahi.dev/rendered-digest":"old"}},"spec":{"description":"original"}}`},
			{args: pinned("get", "agents.core.orka.ai", "sample", "-o", "json")},
			{args: pinned("patch", "agents.core.orka.ai", "sample", "--type=json", "-p", `[{"op":"test","path":"/metadata/resourceVersion","value":"1"},{"op":"replace","path":"/metadata/annotations/kaimahi.dev~1portable-digest","value":"new"},{"op":"replace","path":"/metadata/annotations/kaimahi.dev~1rendered-digest","value":"new"}]`)},
			{args: pinned("get", "agents.core.orka.ai", "sample", "-o", "json")},
			{args: pinned("get", "tasks.core.orka.ai", "--all-namespaces", "-o=go-template={{len .items}}")},
			{args: pinned("patch", "agents.core.orka.ai", "sample", "--type=json", "-p", "{"), status: 1},
			{args: pinned("delete", "agents.core.orka.ai", "sample", "--resource-version=2")},
			{args: pinned("get", "agents.core.orka.ai", "sample", "--ignore-not-found=true", "-o", "json")},
		}},
		{"kagent", "TestKagentKubectlHelper", "KMX_KAGENT_TEST_DIR", protocol.Kagent, []protocolStep{
			{args: []string{"version", "--client"}},
			{args: pinned("config", "view", "-o", "json")},
			{args: []string{"--context", "wrong", "config", "view"}, status: 1},
			{args: pinned("create", "-f", "-"), stdin: "{", status: 1},
			{args: pinned("create", "-f", "-", "--dry-run=server"), stdin: `{"kind":"ModelConfig","metadata":{"name":"sample"},"spec":{"openAI":{}}}`},
			{args: pinned("create", "-f", "-"), stdin: `{"kind":"ModelConfig","metadata":{"name":"sample"},"spec":{"openAI":{}}}`},
			{args: pinned("create", "-f", "-"), stdin: `{"kind":"Agent","metadata":{"name":"sample"},"spec":{"description":"original"}}`},
			{args: pinned("get", "agents.kagent.dev", "sample", "-o", "json")},
			{args: pinned("get", "services", "--all-namespaces", "-o", "json")},
			{args: pinned("get", "services", "--all-namespaces", "-o", "json"), env: []string{"KMX_KAGENT_TEST_SCENARIO=controller-replaced"}},
			{args: pinned("get", "deployment", "sample", "-o", "json"), env: []string{"KMX_KAGENT_TEST_SCENARIO=workload-malformed"}},
			{args: pinned("get", "deployment", "sample", "-o", "json"), env: []string{"KMX_KAGENT_TEST_SCENARIO=workload-unreadable"}, status: 1},
			{args: pinned("create", "--raw", "/api/a2a/agents/sample"), stdin: `{"method":"message/send","id":"request-1"}`},
			{args: pinned("get", "service", "sample", "-o", "json")},
			{args: pinned("get", "agents.kagent.dev", "sample", "-o", "json"), env: []string{"KMX_KAGENT_TEST_SCENARIO=final-agent-rejected"}},
			{args: pinned("get", "--raw", "/version"), env: []string{"KMX_KAGENT_TEST_VERSION=v0.10.3"}},
		}},
		{"orka-pins", "TestFixtureProtocolHelper", "KMX_RECONCILE_DIR", protocol.Pins, []protocolStep{{}}},
		{"kagent-pins", "TestKagentKubectlPinsHelper", "KMX_KAGENT_TEST_DIR", protocol.KagentPins, []protocolStep{{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := kubectlFixtureBinary(t)
			executableDir, inProcessDir := t.TempDir(), t.TempDir()
			for i, step := range tc.steps {
				env := []string{"GORACE=atexit_sleep_ms=0", tc.dirKey + "=" + executableDir}
				env = append(env, step.env...)
				cmd := exec.CommandContext(t.Context(), binary, append([]string{"-test.run=^" + tc.helper + "$", "--"}, step.args...)...)
				cmd.Env = env
				cmd.Stdin = bytes.NewBufferString(step.stdin)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				executable := protocolResult{stdout: stdout.String(), stderr: stderr.String(), status: protocolStatus(t, err)}
				env[1] = tc.dirKey + "=" + inProcessDir
				stdout.Reset()
				stderr.Reset()
				err = tc.handler(protocol.Request{Context: t.Context(), Args: step.args, Getenv: protocol.Environment(env), Stdin: bytes.NewBufferString(step.stdin), Stdout: &stdout, Stderr: &stderr})
				inProcess := protocolResult{stdout: stdout.String(), stderr: stderr.String(), status: protocolStatus(t, err)}
				if !reflect.DeepEqual(executable, inProcess) || executable.status != step.status {
					t.Fatalf("step %d parity: executable=%+v inProcess=%+v want status=%d", i, executable, inProcess, step.status)
				}
				if got, want := protocolFiles(t, inProcessDir), protocolFiles(t, executableDir); !reflect.DeepEqual(got, want) {
					t.Fatalf("step %d state/call log mismatch:\ngot=%v\nwant=%v", i, got, want)
				}
			}
		})
	}
}

func protocolStatus(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var executable *exec.ExitError
	if errors.As(err, &executable) {
		return executable.ExitCode()
	}
	var inProcess *protocol.ExitError
	if errors.As(err, &inProcess) {
		return inProcess.Code
	}
	t.Fatalf("unexpected boundary error: %v", err)
	return -1
}
func protocolFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = string(raw)
	}
	return files
}

func TestKubectlInProcessProtocolCancellationDuringDelay(t *testing.T) {
	for _, kind := range []string{"reconcile", "kagent"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			env := []string{"KMX_RECONCILE_DIR=" + dir, "KMX_LIFT_TEST=1", "KMX_LIFT_PROVIDER_READ_DELAY=1h", "KMX_KAGENT_TEST_DIR=" + dir, "KMX_KAGENT_TEST_SCENARIO=slow-preflight"}
			ctx, cancel := context.WithCancel(t.Context())
			// Cancel at the first delay environment read, after handler dispatch.
			getenv := protocol.Environment(env)
			r := protocol.Request{Context: ctx, Getenv: func(key string) string {
				if key == "KMX_LIFT_PROVIDER_READ_DELAY" || key == "KMX_KAGENT_TEST_SCENARIO" {
					cancel()
				}
				return getenv(key)
			}, Stdin: bytes.NewReader(nil), Stdout: io.Discard, Stderr: io.Discard}
			var err error
			if kind == "reconcile" {
				r.Args = []string{"--context", "kind-test", "get", "providers.core.orka.ai", "inference"}
				err = protocol.Reconcile(r)
			} else {
				r.Args = []string{"--context", "kind-test", "get", "services", "--all-namespaces"}
				err = protocol.Kagent(r)
			}
			cancel()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("delay did not return cancellation: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "provider-read-delayed")); !os.IsNotExist(err) {
				t.Fatalf("cancelled delay continued: %v", err)
			}
		})
	}
}
