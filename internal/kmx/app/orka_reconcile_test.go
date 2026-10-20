package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func reconcileFixtureMode(t *testing.T, inProcess bool) (orkaRuntimeAdapter, string) {
	t.Helper()
	dir := t.TempDir()
	if !inProcess {
		fakeTool(t, dir, "kubectl", kubectlFixture(t, "TestReconcileKubectlHelper"))
	}
	fixtures, e := filepath.Abs("../orkaschema/fixtures")
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_RECONCILE_DIR", dir)
	t.Setenv("KMX_RECONCILE_FIXTURES", fixtures)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	a := &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Run: &run.Runner{}, Out: io.Discard, Err: io.Discard, waitTiming: fastWaitTiming()}
	a.compileOrkaSchema = fixtureOrkaSchemaCompiler(t)
	if inProcess {
		a.captureCommand = reconcileCaptureFixture
		forbidKubectlLaunch(t, dir)
	}
	adapter := orkaRuntimeAdapter{app: a}
	return adapter, dir
}

func seedReconcile(t *testing.T, dir string, doc map[string]any) {
	t.Helper()
	raw, e := json.Marshal(doc)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, strings.ToLower(doc["kind"].(string))+"s.core.orka.ai.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
}
