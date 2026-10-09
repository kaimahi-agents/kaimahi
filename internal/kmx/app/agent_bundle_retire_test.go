package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRetireDeletesOwnedCreatedObjectsAndRemembersHistory(t *testing.T) {
	a, lift, dir, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	opt := RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext}
	var notes bytes.Buffer
	a.Err = &notes
	if err := a.RetireAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"agents", "providers"} {
		if _, err := os.Stat(filepath.Join(dir, kind+".core.orka.ai.json")); !os.IsNotExist(err) {
			t.Errorf("%s survived: %v", kind, err)
		}
	}
	selection, err := bundleLiftSelectionPath(lift.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(selection); !os.IsNotExist(err) {
		t.Errorf("remembered target survived: %v", err)
	}
	if _, err := os.Stat(bundleRetireReceiptPath(lift.BundleDir, "kind-test", OrkaNamespace, "cluster-uid")); err != nil {
		t.Errorf("retire receipt: %v", err)
	}
	notes.Reset()
	if err := a.RetireAgentBundle(opt); err != nil || !strings.Contains(notes.String(), "already retired") {
		t.Fatalf("second retire: %v; output %s", err, notes.String())
	}
}

func TestRetirePlanNeverClearsRememberedSelection(t *testing.T) {
	a, lift, _, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	opt := RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext}
	if err := a.RetireAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	path, _ := bundleLiftSelectionPath(lift.BundleDir)
	selection := bundleLiftSelection{Context: "kind-test", Namespace: OrkaNamespace, ClusterUID: "cluster-uid", Inference: "provider:inference"}
	if err := saveBundleLiftSelection(path, selection); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	opt.Plan = true
	if err := a.RetireAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("plan changed remembered selection")
	}
}

func TestRetireRetryClearsSelectionAfterRemovalFailure(t *testing.T) {
	a, lift, _, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	path, _ := bundleLiftSelectionPath(lift.BundleDir)
	t.Setenv("KMX_RETIRE_SELECTION_BLOCK", path)
	opt := RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext}
	if err := a.RetireAgentBundle(opt); err == nil {
		t.Fatal("selection removal failure accepted")
	}
	t.Setenv("KMX_RETIRE_SELECTION_BLOCK", "")
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := saveBundleLiftSelection(path, bundleLiftSelection{Context: "kind-test", Namespace: OrkaNamespace, ClusterUID: "cluster-uid", Inference: "provider:inference"}); err != nil {
		t.Fatal(err)
	}
	if err := a.RetireAgentBundle(opt); err != nil {
		t.Fatalf("retry must clear selection: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("retry left target selection: %v", err)
	}
}

func TestRetireResolvesUniqueRecordedNamespaceAfterSelectionCleared(t *testing.T) {
	bundle := t.TempDir()
	for _, ns := range []string{"custom"} {
		writeBundleReceipt(t, bundle, "kind-test", ns, "cluster-uid", "sample", "uncommitted")
	}
	ns, err := resolveRetireNamespace(bundle, "kind-test", "", bundleLiftSelection{})
	if err != nil || ns != "custom" {
		t.Fatalf("recorded namespace: %q %v", ns, err)
	}
	writeBundleReceipt(t, bundle, "kind-test", "other", "cluster-uid", "sample", "uncommitted")
	if _, err := resolveRetireNamespace(bundle, "kind-test", "", bundleLiftSelection{}); err == nil {
		t.Fatal("ambiguous namespaces silently selected")
	}
	ns, err = resolveRetireNamespace(bundle, "kind-test", "custom", bundleLiftSelection{})
	if err != nil || ns != "custom" {
		t.Fatalf("explicit namespace: %q %v", ns, err)
	}
}

func TestRetireAlreadyMissingObjectsRecordsCompletionAndForgetsSelection(t *testing.T) {
	a, lift, dir, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"agents", "providers"} {
		if err := os.Remove(filepath.Join(dir, kind+".core.orka.ai.json")); err != nil {
			t.Fatal(err)
		}
	}
	selection, _ := bundleLiftSelectionPath(lift.BundleDir)
	if err := a.RetireAgentBundle(RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(selection); !os.IsNotExist(err) {
		t.Errorf("remembered target remained: %v", err)
	}
	raw, err := os.ReadFile(bundleRetireReceiptPath(lift.BundleDir, "kind-test", OrkaNamespace, "cluster-uid"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt bundleRetireReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Complete {
		t.Fatal("missing-resource retirement was not completed")
	}
}

func TestRetirePlanUsesSameInspectionAndWritesNothing(t *testing.T) {
	a, lift, dir, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	var notes bytes.Buffer
	a.Err = &notes
	opt := RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext, Plan: true}
	if err := a.RetireAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	plan := notes.String()
	for _, want := range []string{"Agent/sample", "Provider/sample", "delete", "origin created"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan lacks %q: %s", want, plan)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "agents.core.orka.ai.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundleRetireReceiptPath(lift.BundleDir, "kind-test", OrkaNamespace, "cluster-uid")); !os.IsNotExist(err) {
		t.Errorf("plan wrote receipt: %v", err)
	}
	notes.Reset()
	opt.Plan = false
	if err := a.RetireAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Agent/sample", "Provider/sample", "delete", "origin created"} {
		if !strings.Contains(notes.String(), want) {
			t.Errorf("run lacks %q: %s", want, notes.String())
		}
	}
}

func TestRetireRefusesSameNamedBundleWithoutMatchingReceipt(t *testing.T) {
	a, lift, dir, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other-bundle")
	source, err := os.ReadFile(filepath.Join(lift.BundleDir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := os.ReadFile(filepath.Join(lift.BundleDir, "bindings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeOrkaBundle(other, source, bindings); err != nil {
		t.Fatal(err)
	}
	err = a.RetireAgentBundle(RetireAgentBundleOptions{BundleDir: other, ToContext: lift.ToContext})
	if err == nil || !strings.Contains(err.Error(), "receipt") {
		t.Fatalf("same-name unrelated bundle allowed to retire: %v", err)
	}
	for _, kind := range []string{"agents", "providers"} {
		if _, err := os.Stat(filepath.Join(dir, kind+".core.orka.ai.json")); err != nil {
			t.Errorf("%s deleted: %v", kind, err)
		}
	}
}

func TestRetireRefusesMarkersChangedSinceLastReceipt(t *testing.T) {
	a, lift, dir, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agents.core.orka.ai.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	doc["metadata"].(map[string]any)["annotations"].(map[string]any)[orkaPortableMarker] = strings.Repeat("f", 64)
	body, _ := json.Marshal(doc)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	err = a.RetireAgentBundle(RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext})
	if err == nil || !strings.Contains(err.Error(), "rerun lift, then retire") {
		t.Fatalf("changed marker accepted without recovery advice: %v", err)
	}
}

func TestRetireRefusesForeignAgentWithoutDeletingProvider(t *testing.T) {
	a, lift, dir, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agents.core.orka.ai.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	obj["metadata"].(map[string]any)["annotations"].(map[string]any)[orkaBundleMarker] = "another-bundle"
	body, _ := json.Marshal(obj)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	var notes bytes.Buffer
	a.Err = &notes
	for _, plan := range []bool{true, false} {
		err := a.RetireAgentBundle(RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext, Plan: plan})
		if err == nil || !strings.Contains(err.Error(), "not owned") {
			t.Errorf("plan %v foreign Agent accepted: %v", plan, err)
		}
		if !strings.Contains(notes.String(), "Provider/sample: refused") {
			t.Errorf("plan %v hid refused Provider when Agent foreign: %s", plan, notes.String())
		}
		notes.Reset()
	}
	if _, err := os.Stat(filepath.Join(dir, "providers.core.orka.ai.json")); err != nil {
		t.Fatalf("provider was removed: %v", err)
	}
}

func TestRetireDoesNotCompleteWhileDeleteIsPending(t *testing.T) {
	a, lift, dir, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_RETIRE_PERSIST_DELETE_KIND", "agents.core.orka.ai")
	a.waitTiming = fastWaitTiming()
	a.waitTiming.timeout = func(parent context.Context, phase string, timeout time.Duration) (context.Context, context.CancelFunc) {
		if phase == "retire-deletion" {
			timeout = 100 * time.Millisecond
		}
		if timeout == 0 {
			return parent, func() {}
		}
		return context.WithTimeout(parent, timeout)
	}
	err := a.RetireAgentBundle(RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext})
	if err == nil {
		t.Fatal("pending deletion reported complete")
	}
	raw, err := os.ReadFile(bundleRetireReceiptPath(lift.BundleDir, "kind-test", OrkaNamespace, "cluster-uid"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt bundleRetireReceipt
	_ = json.Unmarshal(raw, &receipt)
	if receipt.Complete {
		t.Fatal("pending deletion receipt marked complete")
	}
	if _, err := os.Stat(filepath.Join(dir, "agents.core.orka.ai.json")); err != nil {
		t.Fatalf("simulated finalizer disappeared: %v", err)
	}
}

func TestRetireRechecksDependentsAfterGuardBeforeWriting(t *testing.T) {
	a, lift, dir, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	start := len(orkaCalls(t, dir))
	if err := a.RetireAgentBundle(RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext}); err != nil {
		t.Fatal(err)
	}
	lists := 0
	for _, call := range orkaCalls(t, dir)[start:] {
		if slices.Contains(call.Args, "get") && slices.Contains(call.Args, "gatewaybindings.gateway.orka.ai") && slices.Contains(call.Args, "--all-namespaces") {
			lists++
		}
	}
	if lists < 2 {
		t.Fatalf("Task dependents were only listed %d time(s); confirmation can outlive the first inventory", lists)
	}
}

func TestRetireRetriesPartialReleaseWithoutTakingUnownedObject(t *testing.T) {
	a, lift, dir, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"agents", "providers"} {
		path := filepath.Join(dir, kind+".core.orka.ai.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatal(err)
		}
		obj["metadata"].(map[string]any)["annotations"].(map[string]any)[orkaOriginMarker] = "adopted"
		body, _ := json.Marshal(obj)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KMX_RETIRE_FAIL_PATCH_ONCE", "providers.core.orka.ai")
	opt := RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext}
	if err := a.RetireAgentBundle(opt); err == nil || !strings.Contains(err.Error(), "rerun retire") {
		t.Fatalf("partial release lacked retry advice: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agents.core.orka.ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	_ = json.Unmarshal(raw, &obj)
	if obj["metadata"].(map[string]any)["annotations"].(map[string]any)[orkaBundleMarker] != nil {
		t.Fatal("first release not applied")
	}
	if err := a.RetireAgentBundle(opt); err != nil {
		t.Fatalf("partial retirement not recoverable: %v", err)
	}
	if _, err := os.Stat(bundleRetireReceiptPath(lift.BundleDir, "kind-test", OrkaNamespace, "cluster-uid")); err != nil {
		t.Fatal(err)
	}
}

func TestRetireExplicitDeleteAdopted(t *testing.T) {
	a, lift, dir, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"agents", "providers"} {
		path := filepath.Join(dir, kind+".core.orka.ai.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatal(err)
		}
		obj["metadata"].(map[string]any)["annotations"].(map[string]any)["kaimahi.dev/origin"] = "adopted"
		body, _ := json.Marshal(obj)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.RetireAgentBundle(RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext, DeleteAdopted: true}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"agents", "providers"} {
		if _, err := os.Stat(filepath.Join(dir, kind+".core.orka.ai.json")); !os.IsNotExist(err) {
			t.Errorf("%s remained: %v", kind, err)
		}
	}
}

func TestRetireBlocksScheduledTaskWithoutWrites(t *testing.T) {
	a, lift, dir, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"items":[{"metadata":{"name":"queued","namespace":"orka-system"},"spec":{"agentRef":{"name":"sample"}},"status":{"phase":"Scheduled"}}]}`)
	if err := os.WriteFile(filepath.Join(dir, "list-tasks.core.orka.ai.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var notes bytes.Buffer
	a.Err = &notes
	for _, plan := range []bool{true, false} {
		err := a.RetireAgentBundle(RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext, Plan: plan})
		if err == nil || !strings.Contains(err.Error(), "dependent") {
			t.Errorf("plan %v scheduled Task accepted: %v", plan, err)
		}
		for _, want := range []string{"Task orka-system/queued (Scheduled)", "Agent/sample: refused", "Provider/sample: refused"} {
			if !strings.Contains(notes.String(), want) {
				t.Errorf("plan %v omitted %q: %s", plan, want, notes.String())
			}
		}
		notes.Reset()
	}
	for _, kind := range []string{"agents", "providers"} {
		if _, err := os.Stat(filepath.Join(dir, kind+".core.orka.ai.json")); err != nil {
			t.Errorf("%s removed: %v", kind, err)
		}
	}
}

func TestRetireReleasesCreatedProviderWhenAgentSurvives(t *testing.T) {
	a, lift, dir, _ := liftBundleFixture(t)
	if err := a.LiftAgentBundle(lift); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(dir, "agents.core.orka.ai.json")
	raw, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	var agent map[string]any
	if err := json.Unmarshal(raw, &agent); err != nil {
		t.Fatal(err)
	}
	delete(agent["metadata"].(map[string]any)["annotations"].(map[string]any), orkaOriginMarker)
	body, _ := json.Marshal(agent)
	if err := os.WriteFile(agentPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	var notes bytes.Buffer
	a.Err = &notes
	opt := RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext, Plan: true}
	if err := a.RetireAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notes.String(), "Provider/sample: release") {
		t.Fatalf("plan deletes surviving Agent's Provider: %s", notes.String())
	}
	notes.Reset()
	opt.Plan = false
	if err := a.RetireAgentBundle(opt); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"agents", "providers"} {
		raw, err := os.ReadFile(filepath.Join(dir, kind+".core.orka.ai.json"))
		if err != nil {
			t.Fatalf("%s removed: %v", kind, err)
		}
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		if _, ok := doc["metadata"].(map[string]any)["annotations"].(map[string]any)[orkaBundleMarker]; ok {
			t.Errorf("%s marker remains", kind)
		}
	}
}

func TestRetireReleasesLegacyAndAdoptedObjects(t *testing.T) {
	for _, origin := range []string{"", "adopted"} {
		t.Run(origin, func(t *testing.T) {
			a, lift, dir, _ := liftBundleFixture(t)
			if err := a.LiftAgentBundle(lift); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"agents", "providers"} {
				path := filepath.Join(dir, kind+".core.orka.ai.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var obj map[string]any
				if err := json.Unmarshal(raw, &obj); err != nil {
					t.Fatal(err)
				}
				annotations := obj["metadata"].(map[string]any)["annotations"].(map[string]any)
				if origin == "" {
					delete(annotations, "kaimahi.dev/origin")
				} else {
					annotations["kaimahi.dev/origin"] = origin
				}
				body, err := json.Marshal(obj)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var notes bytes.Buffer
			a.Err = &notes
			if err := a.RetireAgentBundle(RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext}); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"agents", "providers"} {
				raw, err := os.ReadFile(filepath.Join(dir, kind+".core.orka.ai.json"))
				if err != nil {
					t.Fatal(err)
				}
				var obj map[string]any
				if err := json.Unmarshal(raw, &obj); err != nil {
					t.Fatal(err)
				}
				annotations := obj["metadata"].(map[string]any)["annotations"].(map[string]any)
				if _, ok := annotations[orkaBundleMarker]; ok {
					t.Errorf("%s remains owned", kind)
				}
			}
			if !strings.Contains(notes.String(), "release") {
				t.Fatalf("release unreported: %s", notes.String())
			}
			notes.Reset()
			if err := a.RetireAgentBundle(RetireAgentBundleOptions{BundleDir: lift.BundleDir, ToContext: lift.ToContext}); err != nil || !strings.Contains(notes.String(), "already retired") {
				t.Fatalf("second release retirement: %v; %s", err, notes.String())
			}
		})
	}
}
