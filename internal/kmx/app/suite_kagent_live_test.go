package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Opt in only from the existing pinned kind shard. This qualifies private
// runtime helpers, not release lift, evaluation or a persisted reuse contract.
func TestLiveSuiteKagentPlacement(t *testing.T) {
	if os.Getenv("KMX_SUITE_KAGENT_LIVE") != "1" {
		t.Skip("existing kagent kind shard opts into private suite placement")
	}
	if os.Getenv("KMX_LIVE_KIND_CONTEXT") != "kind-kmx-kagent-lift" {
		t.Fatal("suite helper qualification requires its dedicated kind context")
	}
	t.Setenv("KMX_HOME", t.TempDir())
	var output bytes.Buffer
	runner := run.Default()
	runner.Stdout, runner.Stderr, runner.Echo = &output, &output, false
	a := &App{Cfg: &config.Config{KubeContext: "kind-kmx-kagent-lift"}, Run: runner, Out: &output, Err: &output}
	if err := requireLiveLocalKind(t.Context(), a); err != nil {
		t.Fatal("suite helper qualification target is not local kind")
	}
	// Keep all subprocess bytes private; errors never quote raw source/response.
	writes := 0
	a.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
		for _, arg := range cmd.Args {
			if arg == "create" || arg == "apply" || arg == "replace" || arg == "patch" || arg == "delete" {
				writes++
				break
			}
		}
		return cmd.Run()
	}
	counts := func() map[string]any {
		t.Helper()
		raw, err := a.kagentCapture(t.Context(), nil, "get", "--raw", "/api/v1/namespaces/agents/services/http:lift-fixture:9000/proxy/evidence")
		var evidence map[string]any
		if err != nil || json.Unmarshal(raw, &evidence) != nil {
			t.Fatal("cannot observe bounded deterministic fixture counters")
		}
		return evidence
	}
	before := counts()
	root := filepath.Join(t.TempDir(), "workspace")
	input := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(input, []byte("Answer briefly; no tool bindings.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "suite-greeter", Model: "fixture", InstructionsFile: input, Runtime: workspace.RuntimeProfile{ID: "kagent", Version: "v0.10.3", Engine: "go"}}); err != nil {
		t.Fatal("cannot author fixture suite")
	}
	artifact, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal("cannot prepare fixture suite")
	}
	suite, err := workspace.Load(t.Context(), artifact)
	if err != nil {
		t.Fatal("cannot load fixture suite")
	}
	binding := agentruntime.KagentBindings{APIVersion: agentruntime.KagentBindingsAPIVersion, Kind: agentruntime.KagentBindingsKind, Namespace: "agents", ModelConfig: agentruntime.KagentModelConfigBindings{Provider: "openai", BaseURL: "http://lift-fixture.agents.svc.cluster.local:9000/v1", SecretRef: agentruntime.KagentSecretRefBindings{Name: "lift-model-key", Key: "api-key"}}}
	prepared, err := a.prepareSuiteKagent(t.Context(), suite, binding)
	if err != nil || writes != 0 {
		t.Fatal("suite preparation failed or performed admission/mutation")
	}
	placed, err := a.placeSuiteKagent(t.Context(), prepared, nil)
	if err != nil || placed.Observation == nil || len(placed.Outcomes) != 2 {
		t.Fatal("suite placement failed; inspect private shard logs")
	}
	for _, resource := range placed.Outcomes {
		if resource.Outcome != agentruntime.ResourceCreated || resource.UID == "" || resource.Generation < 1 {
			t.Fatal("suite placement lost created identity")
		}
	}
	baselineWrites := writes
	repeated, err := a.placeSuiteKagent(t.Context(), prepared, placed.Observation)
	if err != nil || writes != baselineWrites || !reflect.DeepEqual(repeated.Observation, placed.Observation) {
		t.Fatal("record-bound suite repeat changed identities or wrote resources")
	}
	if _, err = a.placeSuiteKagent(t.Context(), prepared, nil); err == nil || !strings.Contains(err.Error(), "occupied") || writes != baselineWrites {
		t.Fatal("occupied suite target was admitted or adopted without proof")
	}
	changed := *placed.Observation
	changed.Subject.Target.NamespaceUID = "replaced-namespace"
	if _, err = a.placeSuiteKagent(t.Context(), prepared, &changed); err == nil || writes != baselineWrites {
		t.Fatal("changed namespace proof authorized reuse")
	}
	if !reflect.DeepEqual(before, counts()) {
		t.Fatal("placement helpers sent native/model/tool traffic")
	}
	t.Log("private suite kagent prepare/new/reuse/occupied/drift qualified; zero additional model/tool calls")
}
