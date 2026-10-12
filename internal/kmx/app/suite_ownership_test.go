package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Literal keys keep the reservation tests independent of the implementation.
var suiteOwnershipTestKeys = []string{
	"kaimahi.dev/suite", "kaimahi.dev/suite-digest", "kaimahi.dev/suite-member",
	"kaimahi.dev/suite-renderer", "kaimahi.dev/suite-binding-digest", "kaimahi.dev/suite-rendered-digest",
}

func suiteOwnershipTestMarkers() map[string]any {
	return map[string]any{
		"kaimahi.dev/suite": "test-suite", "kaimahi.dev/suite-digest": "sha256:" + strings.Repeat("a", 64),
		"kaimahi.dev/suite-member": "sample", "kaimahi.dev/suite-renderer": "kmx.native-orka/v1",
		"kaimahi.dev/suite-binding-digest": strings.Repeat("b", 64), "kaimahi.dev/suite-rendered-digest": strings.Repeat("c", 64),
	}
}

func assertNoOwnershipWrites(t *testing.T, dir string, before int, noNormalization bool) {
	t.Helper()
	for _, call := range orkaCalls(t, dir)[before:] {
		mutation := slices.Contains(call.Args, "create") || slices.Contains(call.Args, "replace") || slices.Contains(call.Args, "patch") || slices.Contains(call.Args, "delete")
		if mutation && (noNormalization || !slices.Contains(call.Args, "--dry-run=server")) {
			t.Fatalf("ownership refusal reached write/admission: %+v", call)
		}
	}
}

// Missing a reservation check must fail before normalization can mask ownership.
func TestLegacyOwnershipReservesEverySuiteKey(t *testing.T) {
	for _, kind := range []string{"Provider", "Agent"} {
		for _, mode := range []string{"inspect", "matching"} {
			for _, key := range suiteOwnershipTestKeys {
				for _, value := range []any{nil, "", 17, "malformed"} {
					t.Run(kind+"/"+mode+"/"+key+"/"+fmtOwnershipValue(value), func(t *testing.T) {
						adapter, rendered, dir := reconcileFixtureMode(t, true)
						bundle, err := orkaBundleFromRendered(rendered)
						if err != nil {
							t.Fatal(err)
						}
						live := reconcileLive(t, dir, kind, rendered)
						addOrkaMarker(live, map[string]any{key: value})
						seedReconcile(t, dir, live)
						desired := bundle.Provider
						if kind == "Agent" {
							desired = bundle.Agent
						}
						if mode == "inspect" {
							_, err = adapter.app.inspectOrkaReconcile(t.Context(), adapter.create.Namespace, desired, rendered)
						} else {
							_, err = adapter.app.matchingOrkaResource(t.Context(), adapter.create.Namespace, desired)
						}
						if err == nil || !strings.Contains(err.Error(), "suite") {
							t.Fatalf("reserved suite marker accepted: %v", err)
						}
						assertNoOwnershipWrites(t, dir, 0, true)
					})
				}
			}
		}
	}
}

func fmtOwnershipValue(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func suiteOwnershipTestOwner() orkaSuiteOwnership {
	return orkaSuiteOwnership{Suite: "test-suite", Digest: "sha256:" + strings.Repeat("a", 64), Member: "sample", Renderer: "kmx.native-orka/v1", BindingDigest: strings.Repeat("b", 64), RenderedDigest: strings.Repeat("c", 64)}
}

func TestSuiteOwnershipRefusesExistingOtherDomains(t *testing.T) {
	type ownerCase struct {
		name        string
		annotations map[string]any
	}
	for _, kind := range []string{"Provider", "Agent"} {
		cases := []ownerCase{
			{"unmarked", nil}, {"bundle", map[string]any{orkaBundleMarker: "sample", orkaPortableMarker: strings.Repeat("d", 64), orkaRenderedMarker: strings.Repeat("e", 64)}},
			{"mixed", func() map[string]any { m := suiteOwnershipTestMarkers(); m[orkaBundleMarker] = "sample"; return m }()},
		}
		for _, key := range []string{orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker} {
			cases = append(cases, ownerCase{"partial-bundle/" + key, map[string]any{key: nil}})
			m := suiteOwnershipTestMarkers()
			m[key] = nil
			cases = append(cases, ownerCase{"mixed-null/" + key, m})
		}
		for _, key := range suiteOwnershipTestKeys {
			for _, v := range []any{nil, "", 17, "malformed"} {
				m := suiteOwnershipTestMarkers()
				m[key] = v
				cases = append(cases, ownerCase{"invalid/" + key + "/" + fmtOwnershipValue(v), m})
			}
			m := suiteOwnershipTestMarkers()
			delete(m, key)
			cases = append(cases, ownerCase{"missing/" + key, m})
		}
		for key, foreign := range map[string]any{"kaimahi.dev/suite": "other-suite", "kaimahi.dev/suite-member": "other-member", "kaimahi.dev/suite-renderer": "kmx.native-orka/v2"} {
			m := suiteOwnershipTestMarkers()
			m[key] = foreign
			cases = append(cases, ownerCase{"foreign/" + key, m})
		}
		for key, malformed := range map[string]any{"kaimahi.dev/suite-digest": strings.Repeat("a", 64), "kaimahi.dev/suite-binding-digest": strings.Repeat("A", 64), "kaimahi.dev/suite-rendered-digest": strings.Repeat("z", 64)} {
			m := suiteOwnershipTestMarkers()
			m[key] = malformed
			cases = append(cases, ownerCase{"malformed-digest/" + key, m})
		}
		for _, tc := range cases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				adapter, rendered, dir := reconcileFixtureMode(t, true)
				bundle, err := orkaBundleFromRendered(rendered)
				if err != nil {
					t.Fatal(err)
				}
				live := reconcileLive(t, dir, kind, rendered)
				if tc.annotations != nil {
					addOrkaMarker(live, tc.annotations)
				}
				seedReconcile(t, dir, live)
				desired := bundle.Provider
				if kind == "Agent" {
					desired = bundle.Agent
				}
				if _, err := adapter.app.inspectSuiteOrkaReconcile(t.Context(), "orka-system", desired, suiteOwnershipTestOwner()); err == nil || !strings.Contains(err.Error(), kind+"/sample") {
					t.Fatalf("suite accepted wrong owner: %v", err)
				}
				assertNoOwnershipWrites(t, dir, 0, true)
			})
		}
	}
}

func TestSuiteOwnershipCreatesReusesUpdatesAndRefreshes(t *testing.T) {
	for _, kind := range []string{"Provider", "Agent"} {
		for _, mode := range []string{"create", "reuse", "update", "refresh"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				adapter, rendered, dir := reconcileFixtureMode(t, true)
				bundle, err := orkaBundleFromRendered(rendered)
				if err != nil {
					t.Fatal(err)
				}
				desired := bundle.Provider
				if kind == "Agent" {
					desired = bundle.Agent
				}
				owner := suiteOwnershipTestOwner()
				if mode != "create" {
					live := reconcileLive(t, dir, kind, rendered)
					m := suiteOwnershipTestMarkers()
					if mode == "refresh" {
						m["kaimahi.dev/suite-digest"] = "sha256:" + strings.Repeat("d", 64)
						m["kaimahi.dev/suite-binding-digest"] = strings.Repeat("e", 64)
						m["kaimahi.dev/suite-rendered-digest"] = strings.Repeat("f", 64)
					}
					addOrkaMarker(live, m)
					addOrkaMarker(live, map[string]any{"independent.dev/note": "preserve", "kaimahi.dev/origin": "created"})
					if mode == "update" {
						if kind == "Provider" {
							live["spec"].(map[string]any)["defaultModel"] = "drift"
						} else {
							live["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "drift"}
						}
					}
					seedReconcile(t, dir, live)
				}
				before, _ := json.Marshal(desired)
				check, err := adapter.app.inspectSuiteOrkaReconcile(t.Context(), "orka-system", desired, owner)
				if err != nil {
					t.Fatal(err)
				}
				want := map[string]agentruntime.ResourceOutcome{"create": agentruntime.ResourceCreated, "reuse": agentruntime.ResourceReused, "update": agentruntime.ResourceUpdated, "refresh": agentruntime.ResourceReused}[mode]
				if check.outcome != want || check.markerRefresh != (mode == "refresh") {
					t.Fatalf("decision: %+v", check)
				}
				id, err := adapter.app.applyOrkaReconcile(t.Context(), "orka-system", check)
				if err != nil {
					t.Fatal(err)
				}
				wantGeneration := int64(1)
				if mode == "update" {
					wantGeneration = 2
				}
				if id.UID != strings.ToLower(kind)+"-uid" || id.Generation != wantGeneration {
					t.Fatalf("identity: %+v", id)
				}
				if err := adapter.app.verifySuiteOrkaReconcile(t.Context(), "orka-system", desired, owner, id); err != nil {
					t.Fatal(err)
				}
				after, _ := json.Marshal(desired)
				if !bytes.Equal(before, after) {
					t.Fatal("ownership altered hashed desired document")
				}
				raw, err := os.ReadFile(filepath.Join(dir, orkaPlural(kind)+".json"))
				if err != nil {
					t.Fatal(err)
				}
				var stored map[string]any
				if err := json.Unmarshal(raw, &stored); err != nil {
					t.Fatal(err)
				}
				meta := stored["metadata"].(map[string]any)
				ann := meta["annotations"].(map[string]any)
				for k, v := range suiteOwnershipTestMarkers() {
					if ann[k] != v {
						t.Fatalf("marker %s = %v want %v", k, ann[k], v)
					}
				}
				for _, k := range []string{orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker} {
					if _, present := ann[k]; present {
						t.Fatalf("suite wrote legacy marker %s", k)
					}
				}
				if ann[orkaOriginMarker] != "created" {
					t.Fatalf("origin: %v", ann)
				}
				if mode != "create" && ann["independent.dev/note"] != "preserve" {
					t.Fatal("independent metadata stripped")
				}
				writes := orkaClusterWrites(t, dir, 0)
				if mode == "reuse" {
					if len(writes) != 0 {
						t.Fatalf("reuse wrote: %+v", writes)
					}
					return
				}
				if len(writes) != 1 {
					t.Fatalf("expected one write: %+v", writes)
				}
				call := writes[0]
				if mode == "update" {
					m := call.Document["metadata"].(map[string]any)
					if m["uid"] != id.UID || m["resourceVersion"] != "1" {
						t.Fatalf("replacement lost UID/RV: %+v", call)
					}
				}
				if mode == "refresh" {
					if len(call.Patch) != 5 || !reflect.DeepEqual(call.Patch[:2], []map[string]any{{"op": "test", "path": "/metadata/resourceVersion", "value": "1"}, {"op": "test", "path": "/metadata/uid", "value": id.UID}}) {
						t.Fatalf("suite refresh lost UID/RV: %+v", call)
					}
					wantPaths := []string{"/metadata/annotations/kaimahi.dev~1suite-digest", "/metadata/annotations/kaimahi.dev~1suite-binding-digest", "/metadata/annotations/kaimahi.dev~1suite-rendered-digest"}
					for i, op := range call.Patch[2:] {
						if op["op"] != "replace" || op["path"] != wantPaths[i] {
							t.Fatalf("unexpected refresh: %+v", call)
						}
					}
					if meta["resourceVersion"] != "2" || meta["generation"] != float64(1) {
						t.Fatalf("refresh changed generation or missed version: %v", meta)
					}
				}
			})
		}
	}
}

func TestSuiteOwnershipPlanIsReadOnlyAndRefusesTask(t *testing.T) {
	adapter, rendered, dir := reconcileFixtureMode(t, true)
	bundle, err := orkaBundleFromRendered(rendered)
	if err != nil {
		t.Fatal(err)
	}
	checks, err := adapter.app.planSuiteOrkaReconcile(t.Context(), bundle, "orka-system", suiteOwnershipTestOwner())
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 || checks[0].id.Kind != "Provider" || checks[1].id.Kind != "Agent" {
		t.Fatalf("plan: %+v", checks)
	}
	assertNoOwnershipWrites(t, dir, 0, false)
	for _, c := range orkaCalls(t, dir) {
		if c.Document != nil && c.Document["kind"] != "Provider" && c.Document["kind"] != "Agent" {
			t.Fatalf("planned Secret/Task write: %+v", c)
		}
	}
	bundle.Task = map[string]any{"kind": "Task", "metadata": map[string]any{"name": "one-shot"}}
	before := len(orkaCalls(t, dir))
	if _, err := adapter.app.planSuiteOrkaReconcile(t.Context(), bundle, "orka-system", suiteOwnershipTestOwner()); err == nil || !strings.Contains(err.Error(), "Task") {
		t.Fatalf("Task accepted: %v", err)
	}
	if len(orkaCalls(t, dir)) != before {
		t.Fatal("Task plan contacted cluster")
	}
}

func TestSuiteOwnershipVerificationRefusesDrift(t *testing.T) {
	for _, mode := range []string{"uid", "generation", "markers", "not-ready", "stale-ready", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			adapter, rendered, dir := reconcileFixtureMode(t, true)
			bundle, err := orkaBundleFromRendered(rendered)
			if err != nil {
				t.Fatal(err)
			}
			live := reconcileLive(t, dir, "Provider", rendered)
			addOrkaMarker(live, suiteOwnershipTestMarkers())
			meta := live["metadata"].(map[string]any)
			switch mode {
			case "uid":
				meta["uid"] = "replacement"
			case "generation":
				meta["generation"] = float64(2)
			case "markers":
				addOrkaMarker(live, map[string]any{"kaimahi.dev/suite-binding-digest": strings.Repeat("e", 64)})
			case "mixed":
				addOrkaMarker(live, map[string]any{orkaPortableMarker: nil})
			}
			seedReconcile(t, dir, live)
			if mode == "not-ready" {
				if err := os.WriteFile(filepath.Join(dir, "provider-not-ready"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "stale-ready" {
				adapter.app.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					if slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "providers.core.orka.ai") && !slices.Contains(cmd.Args, "--ignore-not-found=true") {
						live["status"] = map[string]any{"ready": true, "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": 0}}}
						return json.NewEncoder(cmd.Stdout).Encode(live)
					}
					return reconcileCaptureFixture(ctx, cmd)
				}
			}
			id := orkaIdentity{Kind: "Provider", Name: "sample", UID: "provider-uid", Generation: 1}
			err = adapter.app.verifySuiteOrkaReconcile(t.Context(), "orka-system", bundle.Provider, suiteOwnershipTestOwner(), id)
			if err == nil {
				t.Fatal("drift received verification")
			}
			if mode != "mixed" && !strings.Contains(err.Error(), "changed") && !strings.Contains(err.Error(), "Ready") {
				t.Fatalf("wrong failure: %v", err)
			}
			assertNoOwnershipWrites(t, dir, 0, false)
		})
	}
}

func TestSuiteOwnershipRefusesStaleWritesAndAmbiguousResponses(t *testing.T) {
	for _, mode := range []string{"update-race", "refresh-race", "refresh-replaced-uid", "create-ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			adapter, rendered, dir := reconcileFixtureMode(t, true)
			bundle, err := orkaBundleFromRendered(rendered)
			if err != nil {
				t.Fatal(err)
			}
			live := reconcileLive(t, dir, "Agent", rendered)
			addOrkaMarker(live, suiteOwnershipTestMarkers())
			if mode == "update-race" {
				live["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": "drift"}
			}
			if strings.HasPrefix(mode, "refresh") {
				addOrkaMarker(live, map[string]any{"kaimahi.dev/suite-digest": "sha256:" + strings.Repeat("d", 64)})
			}
			if mode != "create-ambiguous" {
				seedReconcile(t, dir, live)
			}
			check, err := adapter.app.inspectSuiteOrkaReconcile(t.Context(), "orka-system", bundle.Agent, suiteOwnershipTestOwner())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "update-race" || mode == "refresh-race" {
				live["metadata"].(map[string]any)["resourceVersion"] = "concurrent"
				seedReconcile(t, dir, live)
			}
			if mode == "refresh-replaced-uid" {
				live["metadata"].(map[string]any)["uid"] = "replacement"
				seedReconcile(t, dir, live)
			}
			if mode == "create-ambiguous" {
				adapter.app.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
					if err := reconcileCaptureFixture(ctx, cmd); err != nil {
						return err
					}
					if slices.Contains(cmd.Args, "create") && !slices.Contains(cmd.Args, "--dry-run=server") {
						return errors.New("lost response")
					}
					return nil
				}
			}
			before := len(orkaCalls(t, dir))
			if _, err := adapter.app.applyOrkaReconcile(t.Context(), "orka-system", check); err == nil {
				t.Fatal("stale/ambiguous write accepted")
			}
			writes := orkaClusterWrites(t, dir, before)
			if len(writes) != 1 {
				t.Fatalf("write retried or not attempted: %+v", writes)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "agents.core.orka.ai.json"))
			if err != nil {
				t.Fatal(err)
			}
			var stored map[string]any
			if err := json.Unmarshal(raw, &stored); err != nil {
				t.Fatal(err)
			}
			if mode == "update-race" && stored["spec"].(map[string]any)["systemPrompt"].(map[string]any)["inline"] != "drift" {
				t.Fatal("stale replacement persisted")
			}
			if strings.HasPrefix(mode, "refresh") && stored["metadata"].(map[string]any)["annotations"].(map[string]any)["kaimahi.dev/suite-digest"] != "sha256:"+strings.Repeat("d", 64) {
				t.Fatal("stale patch persisted")
			}
		})
	}
}

func TestLegacyOwnershipRefusesSuiteAndMixedOwners(t *testing.T) {
	for _, kind := range []string{"Provider", "Agent"} {
		for _, mixed := range []bool{false, true} {
			t.Run(kind+"/mixed="+fmtOwnershipValue(mixed), func(t *testing.T) {
				adapter, rendered, dir := reconcileFixtureMode(t, true)
				bundle, err := orkaBundleFromRendered(rendered)
				if err != nil {
					t.Fatal(err)
				}
				live := reconcileLive(t, dir, kind, rendered)
				addOrkaMarker(live, suiteOwnershipTestMarkers())
				if mixed {
					markBundleOwned(live, "sample", rendered)
				}
				seedReconcile(t, dir, live)
				desired := bundle.Provider
				if kind == "Agent" {
					desired = bundle.Agent
				}
				if _, err := adapter.app.inspectOrkaReconcile(t.Context(), adapter.create.Namespace, desired, rendered); err == nil || !strings.Contains(err.Error(), "suite") {
					t.Fatalf("inspect accepted suite owner: %v", err)
				}
				if _, err := adapter.app.matchingOrkaResource(t.Context(), adapter.create.Namespace, desired); err == nil || !strings.Contains(err.Error(), "suite") {
					t.Fatalf("matching accepted suite owner: %v", err)
				}
				assertNoOwnershipWrites(t, dir, 0, true)
			})
		}
	}
}

func TestLiveCopyOwnershipRefusesBeforeCleaning(t *testing.T) {
	for _, kind := range []string{"Provider", "Agent"} {
		for _, mixed := range []bool{false, true} {
			for _, key := range suiteOwnershipTestKeys {
				t.Run(kind+"/"+key+"/mixed="+fmtOwnershipValue(mixed), func(t *testing.T) {
					_, rendered, _ := reconcileFixtureMode(t, true)
					bundle, err := orkaBundleFromRendered(rendered)
					if err != nil {
						t.Fatal(err)
					}
					marked := bundle.Provider
					if kind == "Agent" {
						marked = bundle.Agent
					}
					addOrkaMarker(marked, map[string]any{key: nil})
					if mixed {
						markBundleOwned(marked, "sample", rendered)
					}
					before, _ := json.Marshal(bundle)
					if _, err := portableLiftBundle(bundle.Agent, bundle.Provider, "sample", "orka-system"); err == nil || !strings.Contains(err.Error(), "suite") {
						t.Fatalf("live-copy stripped owner: %v", err)
					}
					after, _ := json.Marshal(bundle)
					if !bytes.Equal(before, after) {
						t.Fatal("source mutated")
					}
				})
			}
		}
	}
}

func TestLegacyCreateOwnershipRefusesDefaultAndPreviewApply(t *testing.T) {
	for _, preview := range []bool{false, true} {
		for _, kind := range []string{"Provider", "Agent"} {
			t.Run(kind+"/preview="+fmtOwnershipValue(preview), func(t *testing.T) {
				a, opt, _, _, dir := orkaCreateFixture(t, "v0.2.0")
				if preview {
					opt.NoApply = true
					if err := a.CreateAgent(opt); err != nil {
						t.Fatal(err)
					}
					opt.NoApply = false
				}
				bundle, err := createOrkaBundle(opt)
				if err != nil {
					t.Fatal(err)
				}
				live := bundle.Provider
				if kind == "Agent" {
					live = bundle.Agent
				}
				meta := live["metadata"].(map[string]any)
				meta["uid"] = "owned-uid"
				meta["generation"] = 1
				meta["resourceVersion"] = "1"
				addOrkaMarker(live, suiteOwnershipTestMarkers())
				seedOrkaCreateObject(t, dir, live)
				err = a.CreateAgent(opt)
				if err == nil || !strings.Contains(err.Error(), "suite") || !strings.Contains(err.Error(), kind+"/sample") {
					t.Fatalf("actual create accepted suite owner: %v", err)
				}
				assertNoOwnershipWrites(t, dir, 0, false)
			})
		}
	}
}

func TestLegacyOwnershipReadersRefuseMixedMarkers(t *testing.T) {
	for _, key := range suiteOwnershipTestKeys {
		t.Run(key, func(t *testing.T) {
			adapter, rendered, dir := reconcileFixtureMode(t, true)
			agent := reconcileLive(t, dir, "Agent", rendered)
			markBundleOwned(agent, "sample", rendered)
			addOrkaMarker(agent, map[string]any{key: nil})
			seedReconcile(t, dir, agent)
			raw, err := adapter.app.orkaCapture(t.Context(), nil, "-n", "orka-system", "get", orkaPlural("Agent"), "sample", "-o", "json")
			if err != nil {
				t.Fatal(err)
			}
			status := describeOrkaResource(raw)
			if status.Marked || status.PortableDigest != "" || status.RenderedDigest != "" {
				t.Fatalf("mixed ownership accepted by descriptor: %+v", status)
			}
			if _, err := adapter.app.bundleEvaluationRef(t.Context(), "sample", "orka-system", rendered.PortableDigest()); err == nil {
				t.Fatal("mixed owner accepted by bundle evaluation")
			}
			ref := agentruntime.AgentRef{Namespace: "orka-system", Name: "sample", UID: "agent-uid"}
			if err := adapter.app.orkaEvaluationRevisionError(t.Context(), ref, rendered.PortableDigest()); err == nil {
				t.Fatal("mixed owner accepted by revision verification")
			}
			if _, err := adapter.app.inspectRetireObject(t.Context(), "orka-system", "Agent", "sample", false); err == nil || !strings.Contains(err.Error(), "suite") {
				t.Fatalf("mixed owner accepted by retirement: %v", err)
			}
			assertNoOwnershipWrites(t, dir, 0, true)
		})
	}
}

// Independent annotations sharing a namespace are not ownership; legacy adoption stays supported.
func TestLegacyOwnershipUnrelatedAnnotationsStillAdopt(t *testing.T) {
	adapter, rendered, dir := reconcileFixtureMode(t, true)
	bundle, err := orkaBundleFromRendered(rendered)
	if err != nil {
		t.Fatal(err)
	}
	live := reconcileLive(t, dir, "Agent", rendered)
	addOrkaMarker(live, map[string]any{"kaimahi.dev/suite-notes": "independent", "other.dev/suite": nil})
	seedReconcile(t, dir, live)
	check, err := adapter.app.inspectOrkaReconcile(context.Background(), "orka-system", bundle.Agent, rendered)
	if err != nil {
		t.Fatal(err)
	}
	if check.outcome != agentruntime.ResourceAdopted {
		t.Fatalf("unmarked adoption changed: %+v", check)
	}
	if _, err := adapter.app.applyOrkaReconcile(t.Context(), "orka-system", check); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agents.core.orka.ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	annotations := stored["metadata"].(map[string]any)["annotations"].(map[string]any)
	if !reflect.DeepEqual(annotations["kaimahi.dev/suite-notes"], "independent") {
		t.Fatal("independent annotation stripped")
	}
}
