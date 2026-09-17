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

func TestAgentListRowsAreSortedAndShowWiring(t *testing.T) {
	var agents objectList[agentStatus]
	if err := json.Unmarshal([]byte(`{"items":[
  {"metadata":{"name":"zeta"},"spec":{"declarative":{"modelConfig":"model-z","tools":[]}},"status":{"conditions":[{"Type":"Ready","Status":"False"}]}},
  {"metadata":{"name":"alpha"},"spec":{"declarative":{"modelConfig":"model-a","tools":[{"mcpServer":{"name":"tools"}}]}},"status":{"conditions":[{"Type":"Ready","Status":"True"},{"Type":"Accepted","Status":"True"}]}}
]}`), &agents); err != nil {
		t.Fatal(err)
	}
	rows := agentListRows(agents.Items)
	if rows[0][0] != "alpha" || rows[0][1] != "yes" || rows[0][2] != "yes" || rows[0][3] != "model-a" || rows[0][4] != "tools" {
		t.Fatalf("unexpected first row: %v", rows[0])
	}
	if rows[1][0] != "zeta" || rows[1][4] != "none" {
		t.Fatalf("unexpected second row: %v", rows[1])
	}
}

func TestAgentListOutputValidation(t *testing.T) {
	a := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := a.ListAgents("toml", ""); err == nil {
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

// The namespace selects the runtime. Without one this reports the legacy
// kagent runtime, as it always has; with one it reports Orka. Merging them
// into a single table would imply two different kinds are interchangeable.
func TestAgentListNamespaceSelectsTheRuntime(t *testing.T) {
	for _, tc := range []struct {
		name, namespace, wantKind string
	}{
		{"no namespace reads the legacy runtime", "", "agents.kagent.dev"},
		{"a namespace reads Orka", "team-a", "agents.core.orka.ai"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, dir := agentListFixture(t)
			if err := a.ListAgents("table", tc.namespace); err != nil {
				t.Fatal(err)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if !strings.Contains(string(calls), tc.wantKind) {
				t.Errorf("did not read %s:\n%s", tc.wantKind, calls)
			}
			if tc.namespace != "" && !strings.Contains(string(calls), "-n "+tc.namespace) {
				t.Errorf("did not use the named namespace:\n%s", calls)
			}
			if tc.namespace == "" && strings.Contains(string(calls), "core.orka.ai") {
				t.Errorf("a bare list reached for Orka:\n%s", calls)
			}
		})
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
