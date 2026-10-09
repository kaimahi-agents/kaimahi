package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"go.yaml.in/yaml/v3"
)

func reconcileFixture(t *testing.T) (orkaRuntimeAdapter, agentruntime.RenderedBundle, string) {
	t.Helper()
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", kubectlFixture(t, "TestReconcileKubectlHelper"))
	fixtures, e := filepath.Abs("../orkaschema/fixtures")
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_RECONCILE_DIR", dir)
	t.Setenv("KMX_RECONCILE_FIXTURES", fixtures)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	a := &App{Cfg: &config.Config{KubeContext: "kind-test", ContextSource: config.SourceFlag}, Run: &run.Runner{}, Out: io.Discard, Err: io.Discard, waitTiming: fastWaitTiming()}
	opt := goldenNoTaskCreate("")
	opt.Out = filepath.Join(dir, "bundle.yaml")
	opt.BundlePath = filepath.Join(dir, "agents", "sample")
	adapter := orkaRuntimeAdapter{app: a, create: &opt}
	source, e := portableOrkaSource(opt)
	if e != nil {
		t.Fatal(e)
	}
	prepared, e := agentruntime.PreparePortableRender(source, adapter)
	if e != nil {
		t.Fatal(e)
	}
	rendered, e := adapter.Render(context.Background(), prepared, agentruntime.RenderOptions{})
	if e != nil {
		t.Fatal(e)
	}
	return adapter, rendered, dir
}

func reconcileLive(t *testing.T, dir, kind string, rendered agentruntime.RenderedBundle) map[string]any {
	t.Helper()
	var desired map[string]any
	for _, doc := range rendered.DeployDocuments() {
		var d map[string]any
		if e := yaml.Unmarshal(doc, &d); e == nil && d["kind"] == kind {
			desired = d
			break
		}
	}
	if desired == nil {
		t.Fatal("missing rendered kind", kind)
	}
	meta := desired["metadata"].(map[string]any)
	meta["uid"] = strings.ToLower(kind) + "-uid"
	meta["resourceVersion"] = "1"
	meta["generation"] = float64(1)
	return desired
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

func TestReconcileOutcomesAndReceipt(t *testing.T) {
	for _, tc := range []struct{ name, kind, marker, change, want string }{
		{"absent", "", "", "", "created"},
		{"owned identical", "Agent", "own", "", "reused"},
		{"owned edited", "Agent", "own", "drift", "updated"},
		{"owned description edited", "Agent", "own", "description", "updated"},
		{"unmarked identical", "Agent", "", "", "adopted"},
		{"unmarked different", "Agent", "", "drift", "refused"},
		{"foreign marked", "Agent", "other", "", "refused"},
		{"terminating", "Agent", "own", "terminating", "refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, bundle, dir := reconcileFixture(t)
			if tc.kind != "" {
				provider := reconcileLive(t, dir, "Provider", bundle)
				seedReconcile(t, dir, provider)
				agent := reconcileLive(t, dir, "Agent", bundle)
				meta := agent["metadata"].(map[string]any)
				if tc.marker != "" {
					annotations, _ := meta["annotations"].(map[string]any)
					if annotations == nil {
						annotations = map[string]any{}
					}
					annotations["kaimahi.dev/bundle"] = map[string]string{"own": "sample", "other": "foreign"}[tc.marker]
					annotations["kaimahi.dev/portable-digest"] = bundle.PortableDigest()
					annotations["kaimahi.dev/rendered-digest"] = bundle.RenderedDigest()
					meta["annotations"] = annotations
				}
				if tc.change == "drift" {
					agent["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "edited live"}
				}
				if tc.change == "description" {
					meta["annotations"].(map[string]any)["kaimahi.dev/description"] = "edited live"
				}
				if tc.change == "terminating" {
					meta["deletionTimestamp"] = "2026-01-01T00:00:00Z"
				}
				seedReconcile(t, dir, agent)
			}
			result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true})
			if tc.want == "refused" {
				if err == nil || !strings.Contains(err.Error(), "Agent/sample") {
					t.Fatalf("expected named refusal: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Ref.UID != "agent-uid" || result.Receipt.PortableDigest != bundle.PortableDigest() || result.Receipt.RenderedDigest != bundle.RenderedDigest() || result.Receipt.Bundle != "sample" || result.Receipt.Target.Context != "kind-test" {
				t.Fatalf("invalid receipt: %+v", result)
			}
			if len(result.Receipt.Resources) != 2 || string(result.Receipt.Resources[1].Outcome) != tc.want || result.Receipt.Resources[1].UID != "agent-uid" || result.Receipt.Resources[1].Generation < 1 {
				t.Fatalf("wrong outcome: %+v", result.Receipt.Resources)
			}
			calls := orkaCalls(t, dir)
			for _, call := range calls {
				if !slices.Equal(call.Args[:2], []string{"--context", "kind-test"}) {
					t.Fatalf("unpinned: %v", call.Args)
				}
			}
		})
	}
}

// Removing the origin write from either create or adoption must fail this test.
func TestReconcileOriginSetOnCreationAndAdoption(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		seed       bool
	}{
		{name: "create", want: "created"},
		{name: "adopt", want: "adopted", seed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, bundle, dir := reconcileFixture(t)
			if tc.seed {
				for _, kind := range []string{"Provider", "Agent"} {
					seedReconcile(t, dir, reconcileLive(t, dir, kind, bundle))
				}
			}
			result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true})
			if err != nil {
				t.Fatal(err)
			}
			for i, kind := range []string{"Provider", "Agent"} {
				if string(result.Receipt.Resources[i].Outcome) != tc.want {
					t.Fatalf("%s outcome: %+v", kind, result.Receipt.Resources[i])
				}
				if got := storedReconcileOrigin(t, dir, kind); got != tc.want {
					t.Fatalf("%s origin = %v, want %s", kind, got, tc.want)
				}
			}
			receipt, err := json.Marshal(result.Receipt)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(receipt), "kaimahi.dev/origin") || strings.Contains(string(receipt), `"origin"`) {
				t.Fatalf("origin leaked into receipt: %s", receipt)
			}
		})
	}
}

// Replacing a drifted object must preserve an existing origin, and must not
// guess an origin for owned objects created before the marker existed.
func TestReconcileUpdatePreservesOriginIncludingLegacyAbsence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin any
	}{
		{name: "created", origin: "created"},
		{name: "adopted", origin: "adopted"},
		{name: "legacy without origin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, bundle, dir := reconcileFixture(t)
			provider := reconcileLive(t, dir, "Provider", bundle)
			seedReconcile(t, dir, provider)
			agent := reconcileLive(t, dir, "Agent", bundle)
			agent["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "drift"}
			meta := agent["metadata"].(map[string]any)
			ann, _ := meta["annotations"].(map[string]any)
			if ann == nil {
				ann = map[string]any{}
			}
			ann[orkaBundleMarker] = "sample"
			ann[orkaPortableMarker] = bundle.PortableDigest()
			ann[orkaRenderedMarker] = bundle.RenderedDigest()
			if tc.origin != nil {
				ann["kaimahi.dev/origin"] = tc.origin
			}
			meta["annotations"] = ann
			seedReconcile(t, dir, agent)
			result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true})
			if err != nil {
				t.Fatal(err)
			}
			if result.Receipt.Resources[1].Outcome != agentruntime.ResourceUpdated {
				t.Fatalf("expected update, got %+v", result.Receipt.Resources[1])
			}
			if got := storedReconcileOrigin(t, dir, "Agent"); got != tc.origin {
				t.Fatalf("updated origin = %v, want %v", got, tc.origin)
			}
		})
	}
}

func storedReconcileOrigin(t *testing.T, dir, kind string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, strings.ToLower(kind)+"s.core.orka.ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	annotations, _ := obj["metadata"].(map[string]any)["annotations"].(map[string]any)
	return annotations["kaimahi.dev/origin"]
}

func TestReconcileRefusesReceiptAfterProviderLosesReady(t *testing.T) {
	adapter, bundle, _ := reconcileFixture(t)
	t.Setenv("KMX_RECONCILE_READY_REGRESSION", "1")
	result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true})
	if err == nil || !strings.Contains(err.Error(), "Provider/sample") || result.Receipt.Bundle != "" {
		t.Fatalf("NotReady Provider received a receipt: %+v %v", result, err)
	}
}

func TestCreateOnlyRefusesReceiptAfterProviderLosesReady(t *testing.T) {
	adapter, bundle, _ := reconcileFixture(t)
	t.Setenv("KMX_RECONCILE_READY_REGRESSION", "1")
	result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{})
	if err == nil || !strings.Contains(err.Error(), "Provider/sample") || result.Receipt.Bundle != "" {
		t.Fatalf("NotReady Provider received a create receipt: %+v %v", result, err)
	}
}

func TestReconcileRefusesInvalidOwnedDigests(t *testing.T) {
	for _, tc := range []struct{ name, field, digest string }{
		{"empty portable", "kaimahi.dev/portable-digest", ""},
		{"short portable", "kaimahi.dev/portable-digest", "abcd"},
		{"nonhex portable", "kaimahi.dev/portable-digest", strings.Repeat("z", 64)},
		{"uppercase portable", "kaimahi.dev/portable-digest", strings.Repeat("A", 64)},
		{"empty rendered", "kaimahi.dev/rendered-digest", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, bundle, dir := reconcileFixture(t)
			seedReconcile(t, dir, reconcileLive(t, dir, "Provider", bundle))
			agent := reconcileLive(t, dir, "Agent", bundle)
			meta := agent["metadata"].(map[string]any)
			annotations, _ := meta["annotations"].(map[string]any)
			if annotations == nil {
				annotations = map[string]any{}
			}
			annotations["kaimahi.dev/bundle"] = "sample"
			annotations["kaimahi.dev/portable-digest"] = bundle.PortableDigest()
			annotations["kaimahi.dev/rendered-digest"] = bundle.RenderedDigest()
			annotations[tc.field] = tc.digest
			meta["annotations"] = annotations
			seedReconcile(t, dir, agent)
			result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true})
			if err == nil || !strings.Contains(err.Error(), "Agent/sample") || result.Receipt.Bundle != "" {
				t.Fatalf("invalid ownership digest accepted: %+v %v", result, err)
			}
			for _, call := range orkaCalls(t, dir) {
				if call.Document != nil && (slices.Contains(call.Args, "create") || slices.Contains(call.Args, "replace")) && !slices.Contains(call.Args, "--dry-run=server") {
					t.Fatalf("wrote before refusing invalid marker: %+v", call)
				}
			}
		})
	}
}

// A metadata-only marker change during the final Ready check must not earn a
// receipt: the spec can still match even though the live digest is stale.
func TestReconcileVerificationRefusesStaleMarkers(t *testing.T) {
	adapter, rendered, dir := reconcileFixture(t)
	provider := reconcileLive(t, dir, "Provider", rendered)
	meta := provider["metadata"].(map[string]any)
	annotations, _ := meta["annotations"].(map[string]any)
	if annotations == nil {
		annotations = map[string]any{}
	}
	annotations[orkaBundleMarker] = "sample"
	annotations[orkaPortableMarker] = strings.Repeat("a", 64)
	annotations[orkaRenderedMarker] = rendered.RenderedDigest()
	meta["annotations"] = annotations
	seedReconcile(t, dir, provider)
	bundle, err := orkaBundleFromRendered(rendered)
	if err != nil {
		t.Fatal(err)
	}
	id := orkaIdentity{Kind: "Provider", Name: "sample", UID: "provider-uid", Generation: 1}
	if err := adapter.app.verifyOrkaReconcile(context.Background(), adapter.create.Namespace, bundle.Provider, rendered, id); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale markers earned a receipt: %v", err)
	}
}

func TestReconcileRefusesOwnershipChangedWhileWaiting(t *testing.T) {
	adapter, bundle, dir := reconcileFixture(t)
	provider := reconcileLive(t, dir, "Provider", bundle)
	annotations, _ := provider["metadata"].(map[string]any)["annotations"].(map[string]any)
	if annotations == nil {
		annotations = map[string]any{}
	}
	annotations["kaimahi.dev/bundle"] = "sample"
	annotations["kaimahi.dev/portable-digest"] = bundle.PortableDigest()
	annotations["kaimahi.dev/rendered-digest"] = bundle.RenderedDigest()
	provider["metadata"].(map[string]any)["annotations"] = annotations
	seedReconcile(t, dir, provider)
	t.Setenv("KMX_RECONCILE_STEAL", "Provider")
	result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true})
	if err == nil || !strings.Contains(err.Error(), "Provider/sample") || result.Receipt.Bundle != "" {
		t.Fatalf("ownership changed during Ready: %+v %v", result, err)
	}
}

func TestReconcileRemovesStaleRenderedDescription(t *testing.T) {
	adapter, bundle, dir := reconcileFixture(t)
	seedReconcile(t, dir, reconcileLive(t, dir, "Provider", bundle))
	agent := reconcileLive(t, dir, "Agent", bundle)
	meta := agent["metadata"].(map[string]any)
	annotations := meta["annotations"].(map[string]any)
	annotations["kaimahi.dev/bundle"] = "sample"
	annotations["kaimahi.dev/portable-digest"] = bundle.PortableDigest()
	annotations["kaimahi.dev/rendered-digest"] = bundle.RenderedDigest()
	annotations["kaimahi.dev/description"] = "stale description"
	seedReconcile(t, dir, agent)
	var wanted map[string]any
	if err := yaml.Unmarshal(bundle.DeployDocuments()[1], &wanted); err != nil {
		t.Fatal(err)
	}
	delete(wanted["metadata"].(map[string]any), "annotations")
	withoutDescription, err := yaml.Marshal(wanted)
	if err != nil {
		t.Fatal(err)
	}
	documents := []agentruntime.Document{agentruntime.ReviewDocument(bundle.Documents()[0]), agentruntime.ApplyDocument(bundle.DeployDocuments()[0]), agentruntime.ApplyDocument(withoutDescription)}
	changed, err := agentruntime.NewRenderedBundle(agentruntime.Orka, []byte("portable source without description"), documents)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Deploy(context.Background(), changed, agentruntime.DeployOptions{Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Resources[1].Outcome != "updated" {
		t.Fatalf("stale description reused: %+v", result)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agents.core.orka.ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if got := stored["metadata"].(map[string]any)["annotations"].(map[string]any)["kaimahi.dev/description"]; got != nil {
		t.Fatalf("stale description remained: %v", got)
	}
}

func TestReconcileGuardNamesUpdatesToRemoteTarget(t *testing.T) {
	adapter, bundle, _ := reconcileFixture(t)
	t.Setenv("KMX_RECONCILE_REMOTE", "1")
	adapter.app.Cfg.Confirm = "kind-test"
	var diagnostics bytes.Buffer
	adapter.app.Err = &diagnostics
	if _, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostics.String(), "create or update owned Orka Provider and Agent") {
		t.Fatalf("guard omitted update action: %s", diagnostics.String())
	}
}

func TestReconcileProviderConflictPreventsAgentWrite(t *testing.T) {
	adapter, bundle, dir := reconcileFixture(t)
	provider := reconcileLive(t, dir, "Provider", bundle)
	provider["spec"].(map[string]any)["defaultModel"] = "unrelated"
	seedReconcile(t, dir, provider)
	result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true})
	if err == nil || !strings.Contains(err.Error(), "Provider/sample") || result.Receipt.Bundle != "" {
		t.Fatalf("unmarked Provider conflict: %+v %v", result, err)
	}
	for _, c := range orkaCalls(t, dir) {
		if c.Document != nil && (slices.Contains(c.Args, "create") || slices.Contains(c.Args, "replace")) && !slices.Contains(c.Args, "--dry-run=server") {
			t.Fatalf("wrote after conflict: %+v", c)
		}
	}
}

func TestReconcileCreatedMarkersSupportNoWriteRerun(t *testing.T) {
	adapter, bundle, dir := reconcileFixture(t)
	first, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"providers", "agents"} {
		raw, e := os.ReadFile(filepath.Join(dir, kind+".core.orka.ai.json"))
		if e != nil {
			t.Fatal(e)
		}
		var obj map[string]any
		if e = json.Unmarshal(raw, &obj); e != nil {
			t.Fatal(e)
		}
		ann := obj["metadata"].(map[string]any)["annotations"].(map[string]any)
		if ann["kaimahi.dev/bundle"] != "sample" || ann["kaimahi.dev/portable-digest"] != bundle.PortableDigest() || ann["kaimahi.dev/rendered-digest"] != bundle.RenderedDigest() {
			t.Fatalf("missing marker on %s: %v", kind, ann)
		}
	}
	second, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.Ref.UID != second.Ref.UID || second.Receipt.Resources[0].Outcome != "reused" || second.Receipt.Resources[1].Outcome != "reused" {
		t.Fatalf("rerun: %+v", second)
	}
	for _, c := range orkaCalls(t, dir) {
		if slices.Contains(c.Args, "replace") && !slices.Contains(c.Args, "--dry-run=server") {
			t.Fatalf("rerun wrote existing object: %+v", c)
		}
	}
}

// A stale portable (or rendered) digest with otherwise identical rendered
// fields is a reused outcome that still refreshes the ownership markers, so a
// rerun and the receipt observe the digests this deploy actually rendered.
// The refresh writes under the live resourceVersion precondition and must
// never bump generation, because the spec did not change.
func TestReconcileSameFieldsStaleMarkersRefreshWithoutGenerationBump(t *testing.T) {
	adapter, bundle, dir := reconcileFixture(t)
	if _, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true}); err != nil {
		t.Fatal(err)
	}
	docs := []agentruntime.Document{agentruntime.ReviewDocument(bundle.Documents()[0])}
	for _, doc := range bundle.DeployDocuments() {
		docs = append(docs, agentruntime.ApplyDocument(doc))
	}
	revised, err := agentruntime.NewRenderedBundle(agentruntime.Orka, []byte("same authored spec with a comment"), docs)
	if err != nil {
		t.Fatal(err)
	}
	if revised.PortableDigest() == bundle.PortableDigest() {
		t.Fatal("fixture did not change the portable digest")
	}
	result, err := adapter.Deploy(context.Background(), revised, agentruntime.DeployOptions{Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.PortableDigest != revised.PortableDigest() || result.Receipt.Resources[0].Outcome != "reused" || result.Receipt.Resources[1].Outcome != "reused" {
		t.Fatalf("same spec should reuse: %+v", result)
	}
	if result.Receipt.Resources[0].Generation != 1 || result.Receipt.Resources[1].Generation != 1 {
		t.Fatalf("marker refresh bumped generation: %+v", result.Receipt.Resources)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agents.core.orka.ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err = json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	meta := obj["metadata"].(map[string]any)
	annotations := meta["annotations"].(map[string]any)
	if annotations["kaimahi.dev/portable-digest"] != revised.PortableDigest() || annotations["kaimahi.dev/rendered-digest"] != revised.RenderedDigest() {
		t.Fatalf("stale markers were not refreshed: %v", annotations)
	}
	if annotations["kaimahi.dev/origin"] != "created" {
		t.Fatalf("digest refresh changed origin: %v", annotations["kaimahi.dev/origin"])
	}
	if meta["resourceVersion"] != "2" {
		t.Fatalf("marker refresh did not perform a versioned write: %v", meta["resourceVersion"])
	}
	if meta["generation"] != float64(1) {
		t.Fatalf("marker refresh bumped stored generation: %v", meta["generation"])
	}
	var sawMarkerRefreshWrite bool
	for _, call := range orkaCalls(t, dir) {
		if !slices.Contains(call.Args, "patch") || !slices.Contains(call.Args, "agents.core.orka.ai") {
			continue
		}
		if len(call.Patch) != 3 || call.Patch[0]["op"] != "test" || call.Patch[0]["path"] != "/metadata/resourceVersion" || call.Patch[0]["value"] != "1" {
			t.Fatalf("marker refresh patch missing resourceVersion precondition: %+v", call)
		}
		for _, op := range call.Patch[1:] {
			path, _ := op["path"].(string)
			if op["op"] != "replace" || !strings.HasPrefix(path, "/metadata/annotations/kaimahi.dev~1") {
				t.Fatalf("marker refresh patch touched unexpected field: %+v", call)
			}
		}
		sawMarkerRefreshWrite = true
	}
	if !sawMarkerRefreshWrite {
		t.Fatal("expected exactly one patch call refreshing the Agent markers")
	}
}

func TestReconcileServerNormalizedEqualityAllowsAdoption(t *testing.T) {
	adapter, bundle, dir := reconcileFixture(t)
	provider := reconcileLive(t, dir, "Provider", bundle)
	provider["spec"].(map[string]any)["rateLimit"] = map[string]any{"requestsPerMinute": float64(12)}
	seedReconcile(t, dir, provider)
	result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Resources[0].Outcome != "adopted" {
		t.Fatalf("normalized spec not adopted: %+v", result)
	}
}

func TestReconcileRefusesConcurrentUpdateAndRerunsAfterPartialFailure(t *testing.T) {
	adapter, bundle, dir := reconcileFixture(t)
	provider := reconcileLive(t, dir, "Provider", bundle)
	seedReconcile(t, dir, provider)
	agent := reconcileLive(t, dir, "Agent", bundle)
	agent["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "drift"}
	meta := agent["metadata"].(map[string]any)
	meta["annotations"] = map[string]any{"kaimahi.dev/bundle": "sample", "kaimahi.dev/portable-digest": bundle.PortableDigest(), "kaimahi.dev/rendered-digest": bundle.RenderedDigest()}
	seedReconcile(t, dir, agent)
	t.Setenv("KMX_RECONCILE_RACE", "Agent")
	if result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true}); err == nil || !strings.Contains(err.Error(), "Agent/sample") || result.Receipt.Bundle != "" {
		t.Fatalf("race not refused: %+v %v", result, err)
	}
	var unchanged map[string]any
	body, _ := os.ReadFile(filepath.Join(dir, "agents.core.orka.ai.json"))
	_ = json.Unmarshal(body, &unchanged)
	if unchanged["spec"].(map[string]any)["systemPrompt"].(map[string]any)["inline"] != "drift" {
		t.Fatal("concurrent edit overwritten")
	}
	t.Setenv("KMX_RECONCILE_RACE", "")
	result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Resources[1].Outcome != "updated" {
		t.Fatalf("retry: %+v", result)
	}
}

func TestReconcileRerunsAfterPartialCreation(t *testing.T) {
	adapter, bundle, dir := reconcileFixture(t)
	t.Setenv("KMX_RECONCILE_FAIL_ONCE", "Agent")
	if result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true}); err == nil || len(result.Receipt.Resources) != 0 {
		t.Fatalf("partial failure issued receipt: %+v %v", result, err)
	}
	result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Resources[0].Outcome != "reused" || result.Receipt.Resources[1].Outcome != "created" {
		t.Fatalf("rerun outcomes: %+v", result.Receipt.Resources)
	}
	for _, call := range orkaCalls(t, dir) {
		if call.Document != nil && call.Document["kind"] == "Provider" && slices.Contains(call.Args, "replace") && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatal("rerun modified owned Provider")
		}
	}
}

func TestReconcileRefusesTaskBeforeClusterWrites(t *testing.T) {
	adapter, _, dir := reconcileFixture(t)
	opt := *adapter.create
	opt.Task = "one shot"
	adapter.create = &opt
	source, e := portableOrkaSource(opt)
	if e != nil {
		t.Fatal(e)
	}
	prepared, e := agentruntime.PreparePortableRender(source, adapter)
	if e != nil {
		t.Fatal(e)
	}
	bundle, e := adapter.Render(context.Background(), prepared, agentruntime.RenderOptions{})
	if e != nil {
		t.Fatal(e)
	}
	_, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{Reconcile: true})
	if err == nil || !strings.Contains(err.Error(), "Task") {
		t.Fatalf("Task not refused: %v", err)
	}
	for _, c := range orkaCalls(t, dir) {
		if slices.Contains(c.Args, "create") || slices.Contains(c.Args, "replace") {
			t.Fatalf("mutated for Task: %+v", c)
		}
	}
}

func TestCreateOnlyDeployReturnsReadyReceipt(t *testing.T) {
	adapter, bundle, _ := reconcileFixture(t)
	result, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ref.UID != "agent-uid" || result.Receipt.Bundle != "sample" || len(result.Receipt.Resources) != 2 || result.Receipt.Resources[0].Outcome != "created" || result.Receipt.Resources[1].Generation != 1 {
		t.Fatalf("create-only receipt: %+v", result)
	}
}

func TestDefaultDeployStillRefusesIdenticalExistingName(t *testing.T) {
	adapter, bundle, dir := reconcileFixture(t)
	seedReconcile(t, dir, reconcileLive(t, dir, "Provider", bundle))
	_, err := adapter.Deploy(context.Background(), bundle, agentruntime.DeployOptions{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("create-only collided without refusing: %v", err)
	}
}

// A later rendered annotation cannot rewrite origin or backfill legacy owned
// objects, even when the lift must replace their drifted spec.
func TestReconcileIgnoresRenderedOriginOnOwnedUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, origin string
	}{
		{name: "marked", origin: "created"},
		{name: "legacy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, rendered, dir := reconcileFixture(t)
			bundle, err := orkaBundleFromRendered(rendered)
			if err != nil {
				t.Fatal(err)
			}
			live := reconcileLive(t, dir, "Agent", rendered)
			live["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "drift"}
			meta := live["metadata"].(map[string]any)
			annotations, _ := meta["annotations"].(map[string]any)
			if annotations == nil {
				annotations = map[string]any{}
			}
			annotations[orkaBundleMarker] = "sample"
			annotations[orkaPortableMarker] = rendered.PortableDigest()
			annotations[orkaRenderedMarker] = rendered.RenderedDigest()
			if tc.origin != "" {
				annotations["kaimahi.dev/origin"] = tc.origin
			}
			meta["annotations"] = annotations
			seedReconcile(t, dir, live)
			wanted := bundle.Agent["metadata"].(map[string]any)
			wantedAnnotations, _ := wanted["annotations"].(map[string]any)
			if wantedAnnotations == nil {
				wantedAnnotations = map[string]any{}
				wanted["annotations"] = wantedAnnotations
			}
			wantedAnnotations["kaimahi.dev/origin"] = "adopted"
			check, err := adapter.app.inspectOrkaReconcile(context.Background(), adapter.create.Namespace, bundle.Agent, rendered)
			if err != nil {
				t.Fatal(err)
			}
			if check.outcome != agentruntime.ResourceUpdated {
				t.Fatalf("drift did not prompt update: %+v", check)
			}
			if _, err := adapter.app.applyOrkaReconcile(context.Background(), adapter.create.Namespace, check); err != nil {
				t.Fatal(err)
			}
			var want any
			if tc.origin != "" {
				want = tc.origin
			}
			if got := storedReconcileOrigin(t, dir, "Agent"); got != want {
				t.Fatalf("owned origin overwritten: got %v, want %v", got, want)
			}
		})
	}
}
