package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// statusFixture answers exactly the one read orkaRuntimeAdapter.Status makes
// (get agents.core.orka.ai), switched by env so a test can choose the
// healthy shape or a NotFound failure.
func statusFixture(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_CALLS"
case "$*" in
  *"get agents.core.orka.ai"*)
    case "$KMX_TEST_AGENT" in
      notfound) printf 'Error from server (NotFound): agents.core.orka.ai "concierge" not found\n' >&2; exit 1 ;;
      *) printf '%s' "$KMX_TEST_AGENT" ;;
    esac ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_TEST_CALLS", filepath.Join(dir, "calls"))
	t.Setenv("KMX_TEST_AGENT", `{"metadata":{"name":"concierge","namespace":"demo"},
"spec":{"providerRef":{"name":"local"}},
"status":{"ready":true,"activeTasks":2,"lastUsed":"2025-01-01T00:00:00Z"}}`)
	var out strings.Builder
	return &App{
		Cfg: &config.Config{KubeContext: "kind-x"},
		Run: &run.Runner{Stdout: &out, Stderr: &out},
		Out: &out, Err: &out,
	}, dir
}

func statusRef(namespace, name string) agentruntime.AgentRef {
	return agentruntime.AgentRef{Runtime: agentruntime.Orka, Namespace: namespace, Kind: "agents.core.orka.ai", Name: name}
}

// DESIGN.md §3: Orka's lifecycle Status wraps its own workload state — the
// exact read `kmx agent show` already exercises — never the aggregate
// `kmx status` sections, which remain entirely app-owned.
func TestOrkaAdapterStatusReadsWorkloadState(t *testing.T) {
	a, dir := statusFixture(t)
	adapter := orkaRuntimeAdapter{app: a}
	got, err := adapter.Status(context.Background(), statusRef("demo", "concierge"), agentruntime.StatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Instance != nil {
		t.Fatalf("Orka has no template/instance split; Instance = %+v, want nil", got.Instance)
	}
	fields := map[string]string{}
	for _, field := range got.Pair.Fields {
		fields[field.Label] = field.Value
	}
	if fields["ready"] != "yes" {
		t.Errorf("fields = %+v, want ready=yes", fields)
	}
	if fields["active tasks"] != "2" {
		t.Errorf("fields = %+v, want active tasks=2", fields)
	}
	if fields["last used"] != "2025-01-01T00:00:00Z" {
		t.Errorf("fields = %+v, want last used=2025-01-01T00:00:00Z", fields)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if !strings.Contains(string(calls), "get agents.core.orka.ai concierge") {
		t.Errorf("Status did not read the exact Agent:\n%s", calls)
	}
}

// A not-ready Agent must be reported, not hidden by a merged boolean:
// DESIGN.md §1 is explicit that Status "exposes no merged readiness
// boolean" and PairStatus only ever carries the raw Fields for Orka.
func TestOrkaAdapterStatusReportsNotReady(t *testing.T) {
	a, _ := statusFixture(t)
	t.Setenv("KMX_TEST_AGENT", `{"metadata":{"name":"concierge","namespace":"demo"},
"spec":{},"status":{"ready":false,"activeTasks":0}}`)
	adapter := orkaRuntimeAdapter{app: a}
	got, err := adapter.Status(context.Background(), statusRef("demo", "concierge"), agentruntime.StatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range got.Pair.Fields {
		if field.Label == "ready" && field.Value != "no" {
			t.Fatalf("fields = %+v, want ready=no", got.Pair.Fields)
		}
	}
}

// A NotFound Agent must surface as an error, not an empty status: silently
// reporting a missing Agent as "no fields" would read as a live check that
// never ran.
func TestOrkaAdapterStatusSurfacesNotFound(t *testing.T) {
	a, _ := statusFixture(t)
	t.Setenv("KMX_TEST_AGENT", "notfound")
	adapter := orkaRuntimeAdapter{app: a}
	_, err := adapter.Status(context.Background(), statusRef("demo", "concierge"), agentruntime.StatusOptions{})
	if err == nil || !strings.Contains(err.Error(), "no Orka Agent") {
		t.Fatalf("err = %v, want the same not-found message `kmx agent show` gives", err)
	}
}

// An empty AgentRef names nothing to read; Status must fail closed rather
// than guess a namespace or list every Agent.
func TestOrkaAdapterStatusRequiresNamespaceAndName(t *testing.T) {
	a, dir := statusFixture(t)
	adapter := orkaRuntimeAdapter{app: a}
	if _, err := adapter.Status(context.Background(), agentruntime.AgentRef{}, agentruntime.StatusOptions{}); err == nil {
		t.Fatal("an empty AgentRef was accepted")
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if len(calls) != 0 {
		t.Fatalf("a refused status still contacted the cluster:\n%s", calls)
	}
}

// Explicit adapter dispatch: looking Orka up in the shared registry and
// calling Status through the LifecycleAdapter interface must behave exactly
// like calling the concrete adapter directly — the registry adds lookup, not
// a second behavior.
func TestLifecycleRuntimeRegistryDispatchesOrkaStatusExplicitly(t *testing.T) {
	a, _ := statusFixture(t)
	registry, err := a.lifecycleRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := registry.Lookup(agentruntime.Orka)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, ok := adapter.(agentruntime.LifecycleAdapter)
	if !ok {
		t.Fatalf("registered Orka adapter %T does not implement LifecycleAdapter", adapter)
	}
	if !lifecycle.Capabilities().Status {
		t.Fatal("registered Orka adapter does not declare Status supported")
	}
	got, err := lifecycle.Status(context.Background(), statusRef("demo", "concierge"), agentruntime.StatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pair.Fields) == 0 {
		t.Fatal("dispatched Status returned no fields")
	}
}

// Legacy kagent's lifecycle Status remains an unchanged skeleton: Task 6
// wraps only Orka's workload state. Explicit dispatch through the shared
// registry must still return the one shared typed error, never fall back to
// Orka's implementation.
func TestLifecycleRuntimeRegistryKeepsKagentStatusUnsupported(t *testing.T) {
	a := &App{}
	registry, err := a.lifecycleRuntimeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := registry.Lookup(agentruntime.Kagent)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, ok := adapter.(agentruntime.LifecycleAdapter)
	if !ok {
		t.Fatalf("registered kagent adapter %T does not implement LifecycleAdapter", adapter)
	}
	_, err = lifecycle.Status(context.Background(), agentruntime.AgentRef{}, agentruntime.StatusOptions{})
	var unsupported *agentruntime.UnsupportedVerbError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, not *UnsupportedVerbError", err)
	}
	if unsupported.Runtime != agentruntime.Kagent || unsupported.Verb != agentruntime.VerbStatus {
		t.Fatalf("unsupported = %+v", unsupported)
	}
}
