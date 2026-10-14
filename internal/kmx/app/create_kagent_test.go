package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

type kagentCall struct {
	Args []string       `json:"args"`
	Body map[string]any `json:"body,omitempty"`
}

func kagentCreateFixtureMode(t *testing.T, scenario string, inProcess bool) (*App, CreateOptions, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	if !inProcess {
		fakeTool(t, dir, "kubectl", kubectlFixture(t, "TestKagentKubectlHelper"))
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_TOOLCHAIN", "off")
	t.Setenv("KMX_KAGENT_TEST_DIR", dir)
	t.Setenv("KMX_KAGENT_TEST_SCENARIO", scenario)
	t.Setenv("KMX_KAGENT_TEST_RUNTIME", "go")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	profile := kagentLiftProfile()
	profile.AgentSchema = fmt.Sprintf("%x", sha256.Sum256([]byte(`{"type":"object"}`)))
	profile.ModelSchema = profile.AgentSchema
	out, diagnostics := &bytes.Buffer{}, &bytes.Buffer{}
	runner := &run.Runner{Stdout: out, Stderr: diagnostics, Echo: true}
	a := &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Run: runner, Out: out, Err: diagnostics, waitTiming: fastWaitTiming(), kagentProfile: &profile}
	if inProcess {
		a.captureCommand = kagentCaptureFixture
		forbidKubectlLaunch(t, dir)
	}
	opt := CreateOptions{
		Runtime: "kagent", KagentRuntime: "go", Name: "sample", Namespace: "agents",
		Description: "Sample Kagent agent", ProviderType: "openai", Model: "gpt-4o-mini",
		Secret: "model-key", SecretKey: "api-key", InstructionText: "Answer briefly.",
		Out: filepath.Join(dir, "sample.yaml"), BundlePath: filepath.Join(dir, "agents", "sample"),
	}
	return a, opt, out, diagnostics, dir
}

func kagentCalls(t *testing.T, dir string) []kagentCall {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var calls []kagentCall
	decoder := json.NewDecoder(f)
	for {
		var call kagentCall
		if err := decoder.Decode(&call); err == io.EOF {
			return calls
		} else if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
}
