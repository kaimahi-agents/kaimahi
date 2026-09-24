package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
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
	if err := a.ListAgents(ListOptions{Output: "toml"}); err == nil {
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

// DESIGN.md §4: explicit legacy `kagent` retains its fixed namespace and the
// exact bare-list rows it has always printed — and asks no platform
// detection question on the way there.
func TestAgentListExplicitKagentPreservesTheBareList(t *testing.T) {
	a, out, dir := legacyRuntimeFixture(t)
	if err := a.ListAgents(ListOptions{Runtime: string(agentruntime.Kagent)}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hello-world") {
		t.Fatalf("the legacy rows were lost:\n%s", out.String())
	}
	calls := fixtureCalls(t, dir)
	if !strings.Contains(calls, "-n kagent get agents.kagent.dev") {
		t.Errorf("did not read the legacy runtime in its fixed namespace:\n%s", calls)
	}
	for _, unwanted := range []string{"core.orka.ai", "/apis/kagent.dev/v1alpha3"} {
		if strings.Contains(calls, unwanted) {
			t.Errorf("an explicit runtime still detected platforms (%s):\n%s", unwanted, calls)
		}
	}
}

// DESIGN.md §4 intentionally changes the old bare-list default: with no
// --runtime, shared platform detection selects Orka first, and Orka still
// requires the namespace it watches.
func TestAgentListOmittedRuntimeDetectsOrkaAndKeepsItsNamespace(t *testing.T) {
	a, out, dir := legacyRuntimeFixture(t)
	t.Setenv("KMX_TEST_ORKA_PLATFORM", "present")
	if err := a.ListAgents(ListOptions{Namespace: "team-a"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "concierge") {
		t.Fatalf("detection did not list Orka Agents:\n%s", out.String())
	}
	calls := fixtureCalls(t, dir)
	if !strings.Contains(calls, "-n team-a get agents.core.orka.ai") {
		t.Errorf("did not read Orka in the named namespace:\n%s", calls)
	}
	if strings.Contains(calls, "agents.kagent.dev") {
		t.Errorf("a detected Orka list read the legacy runtime:\n%s", calls)
	}
}

// After detection selects a platform, kmx reports that platform and the
// missing flag rather than guessing a watched namespace.
func TestAgentListDetectedOrkaWithoutNamespaceNamesBoth(t *testing.T) {
	a, out, dir := legacyRuntimeFixture(t)
	t.Setenv("KMX_TEST_ORKA_PLATFORM", "present")
	err := a.ListAgents(ListOptions{})
	if err == nil {
		t.Fatal("a detected Orka list guessed a namespace")
	}
	for _, want := range []string{string(agentruntime.Orka), "--namespace"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s: %v", want, err)
		}
	}
	if out.String() != "" {
		t.Fatalf("a refused list printed:\n%s", out.String())
	}
	if calls := fixtureCalls(t, dir); strings.Contains(calls, "get agents") {
		t.Fatalf("a refused list still listed Agents:\n%s", calls)
	}
}

// The list detector considers only Orka and kagent-v1, so a legacy-only
// cluster gets the error naming both installations (DESIGN.md §1).
func TestAgentListOmittedRuntimeRefusesALegacyOnlyCluster(t *testing.T) {
	a, out, _ := legacyRuntimeFixture(t)
	err := a.ListAgents(ListOptions{})
	var absent *agentruntime.NoPlatformInstalledError
	if !errors.As(err, &absent) {
		t.Fatalf("err = %v, not *NoPlatformInstalledError", err)
	}
	if out.String() != "" {
		t.Fatalf("a refused list printed:\n%s", out.String())
	}
}

// Conflicts fail rather than being ignored: legacy kagent lists its own
// fixed namespace, so a different --namespace is refused before any read.
func TestAgentListExplicitKagentRefusesANamespaceConflict(t *testing.T) {
	a, out, dir := legacyRuntimeFixture(t)
	err := a.ListAgents(ListOptions{Runtime: string(agentruntime.Kagent), Namespace: "team-a"})
	if err == nil || !strings.Contains(err.Error(), "kagent") {
		t.Fatalf("err = %v", err)
	}
	if out.String() != "" || fixtureCalls(t, dir) != "" {
		t.Fatalf("a refused list printed %q and called:\n%s", out.String(), fixtureCalls(t, dir))
	}
}

// kagent-v1 is detected by shared platform detection but not implemented in
// this build, so it is the registry's own typed unknown-runtime error, never
// another runtime's list.
func TestAgentListKagentV1IsNotYetRegistered(t *testing.T) {
	a, out, _ := legacyRuntimeFixture(t)
	err := a.ListAgents(ListOptions{Runtime: string(agentruntime.KagentV1), Namespace: "kagent"})
	var unknown *agentruntime.UnknownRuntimeError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, not *UnknownRuntimeError", err)
	}
	if out.String() != "" {
		t.Fatalf("an unresolved runtime printed:\n%s", out.String())
	}
}

// A cluster with no Orka at all is a different answer from an empty namespace,
// and saying "none" about the first would be wrong.
func TestOrkaAgentListSeparatesAbsentKindFromEmptyNamespace(t *testing.T) {
	a, _ := agentListFixture(t)
	t.Setenv("KMX_TEST_ORKA", "missing")
	err := a.ListAgents(ListOptions{Output: "table", Namespace: "team-a", Runtime: string(agentruntime.Orka)})
	if err == nil {
		t.Fatal("a cluster with no Orka kind reported an empty list")
	}
	if !strings.Contains(err.Error(), "kmx --context kind-test orka install") {
		t.Errorf("the refusal does not say how to fix it: %v", err)
	}
}

// DESIGN.md §1: list/show are presentation operations kept app-owned and
// registered by the same shared runtime ID as chat and lifecycle — not a
// second string/API switch. Looking Orka up by ID must dispatch to exactly
// the existing --namespace-scoped Orka listing, byte for byte.
func TestListPresentationHandlerDispatchesOrkaByID(t *testing.T) {
	a, dir := agentListFixture(t)
	handler, err := a.listPresentationHandler(agentruntime.Orka)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler("table", "team-a"); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if !strings.Contains(string(calls), "agents.core.orka.ai") || !strings.Contains(string(calls), "-n team-a") {
		t.Errorf("the registered handler did not read Orka Agents in the named namespace:\n%s", calls)
	}
}

// Legacy kagent's own bare list is registered by the same shared runtime ID
// rather than reached through a --namespace branch.
func TestListPresentationHandlerDispatchesKagentByID(t *testing.T) {
	a, _, dir := legacyRuntimeFixture(t)
	handler, err := a.listPresentationHandler(agentruntime.Kagent)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler("table", ""); err != nil {
		t.Fatal(err)
	}
	if calls := fixtureCalls(t, dir); !strings.Contains(calls, "-n kagent get agents.kagent.dev") {
		t.Errorf("the registered handler did not read the legacy runtime:\n%s", calls)
	}
}

// A runtime ID with no registered list handler returns the one shared typed
// error naming that runtime and the "list" verb — never a silent fallback
// to Orka's handler and never an ad hoc string error.
func TestListPresentationHandlerIsUnsupportedForAnUnregisteredRuntime(t *testing.T) {
	a := &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	_, err := a.listPresentationHandler(agentruntime.KagentV1)
	var unsupported *agentruntime.UnsupportedVerbError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, not *UnsupportedVerbError", err)
	}
	if unsupported.Runtime != agentruntime.KagentV1 || unsupported.Verb != agentruntime.VerbList {
		t.Fatalf("unsupported = %+v", unsupported)
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
