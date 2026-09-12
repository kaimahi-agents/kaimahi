package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The quickstart checks used to share the workflow driver's agent fixture.
// Only agent invocation survives: no admin/tool API is part of quickstart.
const fakeQuickstartKubectl = `#!/bin/sh
case "$*" in
  *"svc/kagent-controller"*)
    printf 'Forwarding from 127.0.0.1:%s -> 8083\n' "$KMX_TEST_CHAT_PORT"
    exec sleep 30 ;;
  *"get agents.kagent.dev "*) printf 'agent.kagent.dev/hello-world\n'; exit 0 ;;
  *"get --raw"*) printf '{"name":"hello-world"}\n'; exit 0 ;;
esac
exit 0
`

type quickstartFixture struct {
	app         *App
	out, errOut *bytes.Buffer
	replyFile   string
}

func newQuickstartFixture(t *testing.T) *quickstartFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are shell scripts")
	}
	dir := t.TempDir()
	f := &quickstartFixture{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, replyFile: filepath.Join(dir, "reply")}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(fakeQuickstartKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	kagent := filepath.Join(bin, "kagent")
	if err := os.WriteFile(kagent, []byte("#!/bin/sh\ncat \""+f.replyFile+"\"\nexit \"${KMX_TEST_KAGENT_EXIT:-0}\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TEST_CHAT_PORT", "18083")
	cfg := &config.Config{KindCluster: "no-such-cluster-kmx-test", KubeContext: "kind-no-such-cluster-kmx-test", ContextSource: config.SourceKubeCtx, ChatPort: "18083", Credential: "release-agent", KagentBin: kagent, KagentVersion: config.DefaultKagentVersion}
	r := run.Default()
	r.Echo = false
	r.Stdout, r.Stderr = f.out, f.errOut
	f.app = &App{Cfg: cfg, Run: r, Out: f.out, Err: f.errOut}
	return f
}

func (f *quickstartFixture) reply(t *testing.T, state, text string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"status": map[string]any{"state": state}, "artifacts": []any{map[string]any{"parts": []any{map[string]any{"kind": "text", "text": text}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.replyFile, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
