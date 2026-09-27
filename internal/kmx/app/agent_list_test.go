package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	t.Setenv("KMX_TEST_ORKA", "missing")
	err := a.ListAgents("table", "team-a")
	if err == nil {
		t.Fatal("a cluster with no Orka kind reported an empty list")
	}
	if !strings.Contains(err.Error(), "kmx --context kind-test orka install") {
		t.Errorf("the refusal does not say how to fix it: %v", err)
	}
}

func agentListFixture(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_CALLS"
case "$*" in
  *"agents.core.orka.ai"*)
    if [ "$KMX_TEST_ORKA" = missing ]; then
      printf 'error: the server doesn'"'"'t have a resource type "agents"\n' >&2; exit 1
    fi
    printf '{"items":[]}'; exit 0 ;;
esac
printf '{"items":[]}'
exit 0
`
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_TEST_CALLS", filepath.Join(dir, "calls"))
	var out bytes.Buffer
	return &App{
		Cfg: &config.Config{KubeContext: "kind-test"},
		Run: &run.Runner{Stdout: &out, Stderr: &out},
		Out: &out, Err: &out,
	}, dir
}
