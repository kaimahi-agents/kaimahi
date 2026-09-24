package app

import (
	"context"
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

// legacyCombinedItems is the exact shape `kmx status`'s one combined kagent
// read returns: an Agent, a ModelConfig and two Pods in a single list.
const legacyCombinedItems = `{"items":[
{"kind":"Agent","metadata":{"name":"hello-world"},"spec":{"declarative":{"modelConfig":"local"}},"status":{"conditions":[{"type":"Ready","status":"True"},{"type":"Accepted","status":"True"}]}},
{"kind":"ModelConfig","metadata":{"name":"local"},"spec":{"provider":"OpenAI","model":"qwen2.5:3b"},"status":{"conditions":[{"type":"Accepted","status":"True"}]}},
{"kind":"Pod","metadata":{"name":"kagent-controller-0"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"restartCount":3}]}},
{"kind":"Pod","metadata":{"name":"kagent-querydoc-0"},"status":{"phase":"Pending","conditions":[{"type":"Ready","status":"False"}]}}
]}`

// legacyRuntimeFixture answers exactly the reads the legacy runtime's status
// path makes: platform detection (so a test can choose which platform is
// installed), the one combined kagent get, and the ancillary tolerant reads
// `kmx status` adds around it. Every call is recorded, so a test can prove
// the combined snapshot was read once and that a refused command read
// nothing at all.
func legacyRuntimeFixture(t *testing.T) (*App, *strings.Builder, string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_CALLS"
case "$*" in
  *"--api-group=core.orka.ai"*)
    case "$KMX_TEST_ORKA_PLATFORM" in
      present) printf 'agents.core.orka.ai\n' ;;
    esac
    exit 0 ;;
  *"get --raw /apis/kagent.dev/v1alpha3"*)
    case "$KMX_TEST_KAGENTV1_PLATFORM" in
      present) printf '{"kind":"APIResourceList","groupVersion":"kagent.dev/v1alpha3","resources":[{"name":"agenttemplates","kind":"AgentTemplate"}]}\n'; exit 0 ;;
      *) printf 'Error from server (NotFound): the server could not find the requested resource\n' >&2; exit 1 ;;
    esac ;;
  *"get agents.kagent.dev,modelconfigs,pods"*) printf '%s' "$KMX_TEST_COMBINED"; exit 0 ;;
  *"get agents.kagent.dev"*)
    printf '%s' '{"items":[{"metadata":{"name":"hello-world"},"spec":{"declarative":{"modelConfig":"local"}},"status":{"conditions":[{"type":"Ready","status":"True"},{"type":"Accepted","status":"True"}]}}]}'
    exit 0 ;;
  *"get agents.core.orka.ai concierge"*)
    printf '%s' '{"metadata":{"name":"concierge","namespace":"team-a"},"spec":{"providerRef":{"name":"local"}},"status":{"ready":true,"activeTasks":2,"lastUsed":"2025-01-01T00:00:00Z"}}'
    exit 0 ;;
  *"get agents.core.orka.ai"*)
    printf '%s' '{"items":[{"metadata":{"name":"concierge","namespace":"team-a"},"spec":{"providerRef":{"name":"local"},"model":{"name":"qwen2.5:3b"}},"status":{"ready":true}}]}'
    exit 0 ;;
  *"config view"*)
    printf '%s' '{"current-context":"kind-test","contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}],"clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}]}'
    exit 0 ;;
  *"get secrets"*) exit 0 ;;
esac
printf '%s' '{"items":[]}'
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_TEST_CALLS", filepath.Join(dir, "calls"))
	t.Setenv("KMX_TEST_COMBINED", legacyCombinedItems)
	var out strings.Builder
	return &App{
		Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceKubeCtx},
		Run: &run.Runner{Stdout: &out, Stderr: &out},
		Out: &out, Err: &out,
	}, &out, dir
}

func fixtureCalls(t *testing.T, dir string) string {
	t.Helper()
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(calls)
}

func countCalls(calls, want string) int {
	count := 0
	for _, line := range strings.Split(calls, "\n") {
		if strings.Contains(line, want) {
			count++
		}
	}
	return count
}

// legacyStatusData assembles the aggregate `kmx status` facts the way the
// command itself now does: one combined read through the legacy
// LifecycleAdapter, then app-owned aggregation around that same snapshot.
func legacyStatusData(t *testing.T, a *App) *statusData {
	t.Helper()
	snapshot := &kagentStatusSnapshot{}
	slice, err := kagentRuntimeAdapter{app: a, snapshot: snapshot}.Status(context.Background(),
		agentruntime.AgentRef{Runtime: agentruntime.Kagent}, agentruntime.StatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := a.collectStatus(snapshot, slice)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// DESIGN.md §3: legacy kagent declares Render/Deploy/Evaluate permanently
// unsupported and Status supported for the runtime slice this task wraps.
func TestKagentAdapterDeclaresOnlyStatusSupported(t *testing.T) {
	capabilities := kagentRuntimeAdapter{}.Capabilities()
	if !capabilities.Status {
		t.Error("legacy kagent does not declare Status supported")
	}
	if capabilities.Render || capabilities.Deploy || capabilities.Evaluate {
		t.Errorf("legacy kagent declares a lifecycle verb it cannot perform: %+v", capabilities)
	}
}

// Render, Deploy and Evaluate return the one shared typed error naming this
// runtime and that verb — never an ad hoc message and never an attempt.
func TestKagentAdapterRefusesUnsupportedLifecycleVerbs(t *testing.T) {
	a, _, dir := legacyRuntimeFixture(t)
	adapter := kagentRuntimeAdapter{app: a}
	ctx := context.Background()
	for verb, call := range map[string]func() error{
		agentruntime.VerbRender: func() error {
			_, err := adapter.Render(ctx, agentruntime.PortableAgent{}, agentruntime.RenderOptions{})
			return err
		},
		agentruntime.VerbDeploy: func() error {
			_, err := adapter.Deploy(ctx, agentruntime.RenderedBundle{}, agentruntime.DeployOptions{})
			return err
		},
		agentruntime.VerbEvaluate: func() error {
			_, err := adapter.Evaluate(ctx, agentruntime.AgentRef{}, agentruntime.EvaluationRequest{})
			return err
		},
	} {
		err := call()
		var unsupported *agentruntime.UnsupportedVerbError
		if !errors.As(err, &unsupported) {
			t.Fatalf("%s: err = %v, not *UnsupportedVerbError", verb, err)
		}
		if unsupported.Runtime != agentruntime.Kagent || unsupported.Verb != verb {
			t.Fatalf("%s: unsupported = %+v", verb, unsupported)
		}
	}
	if calls := fixtureCalls(t, dir); calls != "" {
		t.Fatalf("an unsupported verb still contacted the cluster:\n%s", calls)
	}
}

// The legacy runtime slice comes from exactly ONE combined read — the same
// snapshot `kmx status`'s app-owned aggregation then reports — so a consumer
// can never see a runtime line and a table that disagree.
func TestKagentAdapterStatusReadsTheCombinedSnapshotOnce(t *testing.T) {
	a, _, dir := legacyRuntimeFixture(t)
	snapshot := &kagentStatusSnapshot{}
	adapter := kagentRuntimeAdapter{app: a, snapshot: snapshot}
	status, err := adapter.Status(context.Background(), agentruntime.AgentRef{Runtime: agentruntime.Kagent, Namespace: config_kagentNamespace}, agentruntime.StatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if status.Instance != nil {
		t.Fatalf("legacy kagent has no template/instance split; Instance = %+v", status.Instance)
	}
	if len(status.Pair.Fields) != 1 || status.Pair.Fields[0].Label != "kagent" {
		t.Fatalf("Pair.Fields = %+v, want exactly the kagent runtime slice", status.Pair.Fields)
	}
	if status.Pair.Fields[0].Value != "1/2 pods ready, 3 restarts" {
		t.Errorf("runtime slice = %q", status.Pair.Fields[0].Value)
	}
	calls := fixtureCalls(t, dir)
	if got := countCalls(calls, "get agents.kagent.dev,modelconfigs,pods"); got != 1 {
		t.Fatalf("combined read happened %d times, want exactly one snapshot:\n%s", got, calls)
	}
	if len(snapshot.items) != 4 || len(snapshot.agents.Items) != 1 || len(snapshot.models.Items) != 1 || len(snapshot.pods.Items) != 2 {
		t.Fatalf("retained snapshot = %d items, %d agents, %d models, %d pods",
			len(snapshot.items), len(snapshot.agents.Items), len(snapshot.models.Items), len(snapshot.pods.Items))
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(snapshot.items[0], &kind); err != nil || kind.Kind != "Agent" {
		t.Fatalf("retained items are not the verbatim kubectl objects: %v, %q", err, kind.Kind)
	}
}

// An empty cluster publishes `[]`, not `null`: a consumer iterates items, and
// null makes them vanish with a zero exit code.
func TestKagentAdapterStatusRetainsEmptyItemsAsAList(t *testing.T) {
	a, _, _ := legacyRuntimeFixture(t)
	t.Setenv("KMX_TEST_COMBINED", `{"items":null}`)
	snapshot := &kagentStatusSnapshot{}
	adapter := kagentRuntimeAdapter{app: a, snapshot: snapshot}
	if _, err := adapter.Status(context.Background(), agentruntime.AgentRef{Runtime: agentruntime.Kagent}, agentruntime.StatusOptions{}); err != nil {
		t.Fatal(err)
	}
	if snapshot.items == nil {
		t.Fatal("an empty cluster retained null items")
	}
}

// Legacy kagent is fixed to its own namespace. A different one is a
// conflict, not something to silently ignore.
func TestKagentAdapterStatusRefusesAForeignNamespace(t *testing.T) {
	a, _, dir := legacyRuntimeFixture(t)
	adapter := kagentRuntimeAdapter{app: a}
	_, err := adapter.Status(context.Background(), agentruntime.AgentRef{Runtime: agentruntime.Kagent, Namespace: "team-a"}, agentruntime.StatusOptions{})
	if err == nil || !strings.Contains(err.Error(), config_kagentNamespace) {
		t.Fatalf("err = %v, want a refusal naming the fixed namespace", err)
	}
	if calls := fixtureCalls(t, dir); calls != "" {
		t.Fatalf("a refused status still contacted the cluster:\n%s", calls)
	}
}

// Explicit dispatch through the shared registry must reach the same legacy
// implementation — the registry adds lookup, not a second behavior.
func TestLifecycleRuntimeRegistryDispatchesKagentStatus(t *testing.T) {
	a, _, _ := legacyRuntimeFixture(t)
	snapshot := &kagentStatusSnapshot{}
	registry, err := a.lifecycleRuntimeRegistry(snapshot)
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
	status, err := lifecycle.Status(context.Background(), agentruntime.AgentRef{Runtime: agentruntime.Kagent}, agentruntime.StatusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Pair.Fields) == 0 || len(snapshot.items) == 0 {
		t.Fatalf("dispatched Status returned %+v and retained %d items", status.Pair.Fields, len(snapshot.items))
	}
}
