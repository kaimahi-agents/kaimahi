package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"go.yaml.in/yaml/v3"
)

var suiteReservedKeys = []string{"kaimahi.dev/suite", "kaimahi.dev/suite-digest", "kaimahi.dev/suite-member", "kaimahi.dev/suite-renderer", "kaimahi.dev/suite-binding-digest", "kaimahi.dev/suite-rendered-digest"}

func reserveSuiteDoc(doc map[string]any, key string, value any) {
	meta := doc["metadata"].(map[string]any)
	ann, _ := meta["annotations"].(map[string]any)
	if ann == nil {
		ann = map[string]any{}
		meta["annotations"] = ann
	}
	ann[key] = value
}

func persistentOrkaCalls(t *testing.T, dir string) []orkaCall {
	t.Helper()
	var writes []orkaCall
	for _, c := range orkaCalls(t, dir) {
		if slices.Contains(c.Args, "--dry-run=server") {
			continue
		}
		for _, verb := range []string{"create", "replace", "patch", "delete"} {
			if slices.Contains(c.Args, verb) {
				writes = append(writes, c)
				break
			}
		}
	}
	return writes
}

// Presence, not valid marker content, reserves the object against adoption.
func TestSuiteReservationPresenceBlocksLegacyReconcile(t *testing.T) {
	for i, key := range suiteReservedKeys {
		t.Run(key, func(t *testing.T) {
			adapter, rendered, dir := reconcileFixtureMode(t, true)
			doc := reconcileLive(t, dir, "Agent", rendered)
			reserveSuiteDoc(doc, key, []any{nil, "", 17, "malformed", nil, ""}[i])
			seedReconcile(t, dir, doc)
			result, err := adapter.Deploy(t.Context(), rendered, agentruntime.DeployOptions{Reconcile: true})
			if err == nil || !strings.Contains(err.Error(), "reserves native suite ownership") || result.Ref.UID != "" {
				t.Fatalf("reserved Agent adopted or wrong refusal: %v", err)
			}
			if len(persistentOrkaCalls(t, dir)) != 0 {
				t.Fatal("write before reservation refusal")
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSuiteReservationLegacyDocumentBoundaries(t *testing.T) {
	for _, route := range []string{"desired reconcile", "matching desired", "matching live", "staged", "decoder", "copy Agent", "copy Provider", "live copy source"} {
		t.Run(route, func(t *testing.T) {
			adapter, rendered, dir := reconcileFixtureMode(t, true)
			bundle, err := orkaBundleFromRendered(rendered)
			if err != nil {
				t.Fatal(err)
			}
			doc := bundle.Agent
			if route == "copy Provider" {
				doc = bundle.Provider
			}
			reserveSuiteDoc(doc, suiteReservedKeys[0], nil)
			switch route {
			case "desired reconcile":
				_, err = adapter.app.inspectOrkaReconcile(t.Context(), adapter.create.Namespace, doc, rendered)
			case "matching desired":
				_, err = adapter.app.matchingOrkaResource(t.Context(), adapter.create.Namespace, doc)
			case "matching live":
				live := reconcileLive(t, dir, "Agent", rendered)
				reserveSuiteDoc(live, suiteReservedKeys[0], nil)
				seedReconcile(t, dir, live)
				clean, _ := orkaBundleFromRendered(rendered)
				_, err = adapter.app.matchingOrkaResource(t.Context(), adapter.create.Namespace, clean.Agent)
			case "staged":
				_, err = adapter.app.createOrkaStaged(t.Context(), *adapter.create, bundle, bundle.YAML, nil, nil)
			case "decoder":
				raw, e := yaml.Marshal(doc)
				if e != nil {
					t.Fatal(e)
				}
				_, err = decodeOrkaDocument(raw, "Agent")
			case "copy Agent", "copy Provider":
				_, err = portableLiftBundle(bundle.Agent, bundle.Provider, "sample", OrkaNamespace)
			case "live copy source":
				live := reconcileLive(t, dir, "Agent", rendered)
				reserveSuiteDoc(live, suiteReservedKeys[0], nil)
				seedReconcile(t, dir, live)
				seedReconcile(t, dir, reconcileLive(t, dir, "Provider", rendered))
				b := &orkaChatBackend{app: adapter.app, agent: "sample", namespace: OrkaNamespace}
				_, err = b.liftBundle(t.Context())
			}
			if err == nil || !strings.Contains(err.Error(), "reserves native suite ownership") {
				t.Fatalf("legacy boundary accepted suite reservation or wrong refusal: %v", err)
			}
			if len(persistentOrkaCalls(t, dir)) != 0 {
				t.Fatal("legacy boundary mutated resource")
			}
		})
	}
}

func TestSuiteReservationBundleLiftPlanAndApply(t *testing.T) {
	for _, plan := range []bool{true, false} {
		t.Run(map[bool]string{true: "plan", false: "apply"}[plan], func(t *testing.T) {
			a, opt, dir, _ := retireBundleFixture(t)
			bindings, err := a.liftProviderBindings(t.Context(), OrkaNamespace, "inference")
			if err != nil {
				t.Fatal(err)
			}
			rendered, err := RenderOrkaBundleFile(opt.BundleDir, bindings)
			if err != nil {
				t.Fatal(err)
			}
			live := reconcileLive(t, dir, "Agent", rendered)
			reserveSuiteDoc(live, suiteReservedKeys[0], nil)
			seedReconcile(t, dir, live)
			opt.Plan = plan
			if err := a.LiftAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "reserves native suite ownership") {
				t.Fatalf("Lift accepted reserved name or wrong refusal: %v", err)
			}
			if len(persistentOrkaCalls(t, dir)) != 0 {
				t.Fatal("Lift wrote before reservation refusal")
			}
		})
	}
}

func TestSuiteReservationActualBundleAuthorizationConsumers(t *testing.T) {
	for _, route := range []string{"run bundle", "evaluation ref", "runtime revision", "generic status", "generic run"} {
		t.Run(route, func(t *testing.T) {
			f := runFixture(t)
			path := filepath.Join(f.dir, "agent.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			reserveSuiteDoc(doc, suiteReservedKeys[0], nil)
			if err := os.WriteFile(path, mustJSON(t, doc), 0600); err != nil {
				t.Fatal(err)
			}
			ref := agentruntime.AgentRef{Runtime: agentruntime.Orka, Context: "kind-test", Namespace: OrkaNamespace, Name: f.name, UID: "agent-uid"}
			want := "bundle ownership marker"
			switch route {
			case "run bundle":
				err = f.app.RunAgent(runOption(f))
			case "evaluation ref":
				_, err = f.app.bundleEvaluationRef(t.Context(), f.name, OrkaNamespace, f.digest)
			case "runtime revision":
				err = f.app.orkaEvaluationRevisionError(t.Context(), ref, f.digest)
				want = "deployed revision differs"
			case "generic status":
				status, e := (orkaRuntimeAdapter{app: f.app}).Status(t.Context(), ref, agentruntime.StatusOptions{})
				if e != nil || !strings.Contains(statusFieldText(status), "ready=yes") {
					t.Fatalf("generic status broadened into ownership ban: %+v %v", status, e)
				}
				return
			case "generic run":
				opt := runOption(f)
				opt.BundleDir = ""
				opt.ToContext = ""
				opt.Agent = f.name
				opt.Namespace = OrkaNamespace
				f.answers[opt.Prompt] = "generic answer"
				if err := f.app.RunAgent(opt); err != nil || f.out.String() != "generic answer\n" {
					t.Fatalf("generic live execution prohibited: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("unexpected bundle authorization result: %v", err)
			}
			if f.taskCreates(t) != 0 {
				t.Fatal("Task executed after ownership refusal")
			}
		})
	}
}

func TestSuiteReservationStatusAndRetirementExceptions(t *testing.T) {
	for _, route := range []string{"status mixed", "status released", "retire", "previously released", "already retired"} {
		t.Run(route, func(t *testing.T) {
			adapter, rendered, dir := reconcileFixtureMode(t, true)
			live := reconcileLive(t, dir, "Agent", rendered)
			if route == "status mixed" || route == "retire" {
				addOrkaMarker(live, map[string]any{orkaBundleMarker: "sample", orkaPortableMarker: rendered.PortableDigest(), orkaRenderedMarker: rendered.RenderedDigest(), orkaOriginMarker: "created"})
			}
			reserveSuiteDoc(live, suiteReservedKeys[0], nil)
			seedReconcile(t, dir, live)
			raw := mustJSON(t, live)
			bundle := t.TempDir()
			writeStatusRetireReceipt(t, bundle, "kind-test", OrkaNamespace, "cluster-uid", "sample")
			switch route {
			case "status mixed":
				s := describeOrkaResource(raw)
				if s.Marked || s.PortableDigest != "" {
					t.Fatal("mixed ownership authorized bundle")
				}
			case "status released":
				observed := bundleTargetStatus{Agent: describeOrkaResource(raw)}
				if retiredReleasedResourcesMatch(bundle, bundleStatusTarget{Context: "kind-test", Namespace: OrkaNamespace}, "cluster-uid", "sample", observed) {
					t.Fatal("reservation mistaken for released resource")
				}
			case "retire":
				if _, err := adapter.app.inspectRetireObject(t.Context(), OrkaNamespace, "Agent", "sample", true); err == nil || !strings.Contains(err.Error(), "reserves native suite ownership") {
					t.Fatalf("retire accepted suite object or wrong refusal: %v", err)
				}
			case "previously released":
				if ok, err := adapter.app.inspectPreviouslyReleased(t.Context(), OrkaNamespace, "Agent", "sample", "agent-uid"); ok || err != nil {
					t.Fatalf("release exception: %v %v", ok, err)
				}
			case "already retired":
				if ok, err := adapter.app.alreadyRetiredBundle(t.Context(), bundle, "kind-test", OrkaNamespace, "cluster-uid", "sample"); ok || err != nil {
					t.Fatalf("already-retired exception: %v %v", ok, err)
				}
			}
			if len(persistentOrkaCalls(t, dir)) != 0 {
				t.Fatal("retirement mutated reserved resource")
			}
		})
	}
}
