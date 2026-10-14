package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

type orkaCall struct {
	Args     []string
	Document map[string]any
	Patch    []map[string]any
}

func orkaTestToken() string { return "private-" + "session-token-never-print" }

func orkaCreateFixture(t *testing.T, scenario string) (*App, CreateOptions, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", kubectlFixture(t, "TestOrkaKubectlHelper"))
	fixtures, err := filepath.Abs("../orkaschema/fixtures")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_ORKA_TEST_DIR", dir)
	t.Setenv("KMX_ORKA_TEST_FIXTURES", fixtures)
	t.Setenv("KMX_ORKA_TEST_SCENARIO", scenario)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	out, diagnostics := &bytes.Buffer{}, &bytes.Buffer{}
	a := &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Out: out, Err: diagnostics, Run: &run.Runner{Stdout: out, Stderr: diagnostics, Echo: true}, waitTiming: fastWaitTiming()}
	opt := CreateOptions{Name: "sample", Namespace: "orka-system", ProviderType: "openai", Model: "local", Secret: "model-key", Out: filepath.Join(dir, "bundle.yaml"), BundlePath: filepath.Join(dir, "agents", "sample")}
	return a, opt, out, diagnostics, dir
}

const orkaTestWaitTimeout = 250 * time.Millisecond

// orkaTestDeadline starts a short deadline only after the intended boundary.
// Every other phase keeps its production cap, including preflight and token
// lifetime validation. The returned context proves expiry at that boundary.
func orkaTestDeadline(t *testing.T, a *App, phase string) func() context.Context {
	t.Helper()
	timing := fastWaitTiming()
	var reached context.Context
	timing.timeout = func(parent context.Context, current string, original time.Duration) (context.Context, context.CancelFunc) {
		if current == phase {
			var cancel context.CancelFunc
			reached, cancel = context.WithTimeout(parent, orkaTestWaitTimeout)
			return reached, cancel
		}
		return (&App{}).waitContext(parent, current, original)
	}
	a.waitTiming = timing
	check := func() context.Context {
		t.Helper()
		if reached == nil {
			t.Fatalf("never reached deadline phase %q", phase)
		}
		return reached
	}
	t.Cleanup(func() { check() })
	return check
}

func orkaCalls(t *testing.T, dir string) []orkaCall {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var calls []orkaCall
	dec := json.NewDecoder(f)
	for {
		var c orkaCall
		err := dec.Decode(&c)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}
