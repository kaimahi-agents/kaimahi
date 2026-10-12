package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLegacyOwnershipQuickstartAndLiveCopyReuseRefuseSuite(t *testing.T) {
	for _, path := range []string{"quickstart", "live-copy"} {
		for _, kind := range []string{"Provider", "Agent"} {
			t.Run(path+"/"+kind, func(t *testing.T) {
				adapter, rendered, dir := reconcileFixtureMode(t, true)
				bundle, err := orkaBundleFromRendered(rendered)
				if err != nil {
					t.Fatal(err)
				}
				if path == "quickstart" {
					adapter.app.Cfg.Model = adapter.create.Model
					bundle, err = createOrkaBundle(adapter.app.quickstartCreateOptions())
					if err != nil {
						t.Fatal(err)
					}
				}
				for _, doc := range []map[string]any{bundle.Provider, bundle.Agent} {
					meta := doc["metadata"].(map[string]any)
					meta["uid"] = "source-uid"
					meta["resourceVersion"] = "1"
					meta["generation"] = 1
					if doc["kind"] == kind {
						addOrkaMarker(doc, map[string]any{"kaimahi.dev/suite-rendered-digest": nil})
					}
					seedReconcile(t, dir, doc)
				}
				if path == "quickstart" {
					err = adapter.app.stepQuickstartAgent()
				} else {
					// Rebuild unmarked desired documents; the source ownership belongs only to live objects.
					bundle, err = orkaBundleFromRendered(rendered)
					if err != nil {
						t.Fatal(err)
					}
					adapter.app.liftReuse = true
					err = adapter.app.createOrkaOnline(t.Context(), *adapter.create, bundle)
				}
				if err == nil || !strings.Contains(err.Error(), "suite") || !strings.Contains(err.Error(), kind+"/") {
					t.Fatalf("%s reused suite ownership: %v", path, err)
				}
				assertNoOwnershipWrites(t, dir, 0, false)
			})
		}
	}
}

func TestLegacyOwnershipStatusRejectsMixedProviderOrAgent(t *testing.T) {
	for _, kind := range []string{"Provider", "Agent"} {
		t.Run(kind, func(t *testing.T) {
			a, opt, dir, rendered, name := bundleStatusFixture(t)
			a.captureCommand = reconcileCaptureFixture
			seedBundleLiveResources(t, dir, rendered, name, nil)
			live := reconcileLive(t, dir, kind, rendered)
			markBundleOwned(live, name, rendered)
			addOrkaMarker(live, map[string]any{"kaimahi.dev/suite-binding-digest": nil})
			seedReconcile(t, dir, live)
			opt.Context, opt.Namespace = "kind-test", "orka-system"
			report, err := a.bundleStatusReport(opt)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Targets) != 1 {
				t.Fatalf("unexpected targets: %+v", report)
			}
			target := report.Targets[0]
			if target.State != bundleStateUnknown || target.LiveDigest != "" || !strings.Contains(target.Detail, "suite") {
				t.Fatalf("mixed owner accepted by status: %+v", target)
			}
			// Domain presence is private; no status schema extension.
			raw, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "suiteOwned") {
				t.Fatal("private domain flag leaked")
			}
			assertNoOwnershipWrites(t, dir, 0, false)
		})
	}
}

func TestLegacyOwnershipRetirementRecoveryRejectsSuiteReservations(t *testing.T) {
	for _, key := range suiteOwnershipTestKeys {
		t.Run(key, func(t *testing.T) {
			adapter, rendered, dir := reconcileFixtureMode(t, true)
			live := reconcileLive(t, dir, "Agent", rendered)
			addOrkaMarker(live, map[string]any{key: nil})
			seedReconcile(t, dir, live)
			if released, err := adapter.app.inspectPreviouslyReleased(t.Context(), "orka-system", "Agent", "sample", "agent-uid"); err != nil || released {
				t.Fatalf("suite owner considered previously released: %v %v", released, err)
			}
			bundle := t.TempDir()
			receipt := bundleRetireReceipt{Bundle: "sample", At: time.Now().UTC(), Complete: true, Resources: []retireDecisionRecord{{Kind: "Agent", Name: "sample", UID: "agent-uid", Action: "release"}}}
			receipt.Target.Context, receipt.Target.Namespace, receipt.Target.ClusterUID, receipt.Target.Agent = "kind-test", "orka-system", "cluster-uid", "sample"
			if err := writeBundleRetireReceipt(bundle, receipt); err != nil {
				t.Fatal(err)
			}
			if retired, err := adapter.app.alreadyRetiredBundle(t.Context(), bundle, "kind-test", "orka-system", "cluster-uid", "sample"); err != nil || retired {
				t.Fatalf("suite owner considered already retired: %v %v", retired, err)
			}
			raw, err := adapter.app.orkaCapture(t.Context(), nil, "-n", "orka-system", "get", orkaPlural("Agent"), "sample", "-o", "json")
			if err != nil {
				t.Fatal(err)
			}
			observed := bundleTargetStatus{Agent: describeOrkaResource(raw)}
			if retiredReleasedResourcesMatch(bundle, bundleStatusTarget{Context: "kind-test", Namespace: "orka-system"}, "cluster-uid", "sample", observed) {
				t.Fatal("suite owner recognized as retired released bundle")
			}
			assertNoOwnershipWrites(t, dir, 0, true)
		})
	}
}

func TestSuiteOwnershipInvalidExpectedIdentityRefusesBeforeRead(t *testing.T) {
	for _, field := range []string{"suite", "digest", "member", "renderer", "binding", "rendered"} {
		t.Run(field, func(t *testing.T) {
			adapter, rendered, dir := reconcileFixtureMode(t, true)
			bundle, err := orkaBundleFromRendered(rendered)
			if err != nil {
				t.Fatal(err)
			}
			owner := suiteOwnershipTestOwner()
			switch field {
			case "suite":
				owner.Suite = ""
			case "digest":
				owner.Digest = strings.Repeat("a", 64)
			case "member":
				owner.Member = ""
			case "renderer":
				owner.Renderer = "other"
			case "binding":
				owner.BindingDigest = ""
			case "rendered":
				owner.RenderedDigest = ""
			}
			if _, err := adapter.app.inspectSuiteOrkaReconcile(t.Context(), "orka-system", bundle.Agent, owner); err == nil || !strings.Contains(err.Error(), "ownership identity") {
				t.Fatalf("invalid expected owner accepted by inspect: %v", err)
			}
			if _, err := adapter.app.planSuiteOrkaReconcile(t.Context(), bundle, "orka-system", owner); err == nil || !strings.Contains(err.Error(), "ownership identity") {
				t.Fatalf("invalid expected owner accepted by plan: %v", err)
			}
			if err := adapter.app.verifySuiteOrkaReconcile(t.Context(), "orka-system", bundle.Agent, owner, orkaIdentity{}); err == nil || !strings.Contains(err.Error(), "ownership identity") {
				t.Fatalf("invalid expected owner accepted by verify: %v", err)
			}
			if len(orkaCalls(t, dir)) != 0 {
				t.Fatal("invalid expected ownership reached cluster")
			}
		})
	}
}

func TestSuiteOwnershipDesiredMarkersRefuseWithoutStripping(t *testing.T) {
	for _, key := range append(append([]string(nil), suiteOwnershipTestKeys...), orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker) {
		t.Run(key, func(t *testing.T) {
			adapter, rendered, dir := reconcileFixtureMode(t, true)
			bundle, err := orkaBundleFromRendered(rendered)
			if err != nil {
				t.Fatal(err)
			}
			addOrkaMarker(bundle.Agent, map[string]any{key: nil})
			before, err := json.Marshal(bundle.Agent)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.app.inspectSuiteOrkaReconcile(t.Context(), "orka-system", bundle.Agent, suiteOwnershipTestOwner()); err == nil || !strings.Contains(err.Error(), "ownership") {
				t.Fatalf("pre-marked desired accepted: %v", err)
			}
			after, err := json.Marshal(bundle.Agent)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) || len(orkaCalls(t, dir)) != 0 {
				t.Fatal("reserved annotations were stripped or reached cluster")
			}
		})
	}
}
