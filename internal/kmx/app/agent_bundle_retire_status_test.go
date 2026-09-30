package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The retire receipt is intentionally independent of lift's historical
// receipt. The fixture uses the on-disk contract rather than a retire writer.
func writeStatusRetireReceipt(t *testing.T, bundle, context, namespace, clusterUID, agent string) {
	t.Helper()
	key := sha256.Sum256([]byte(context + "\x00" + namespace + "\x00" + clusterUID))
	path := filepath.Join(bundle, "receipts", fmt.Sprintf("retire-%x.json", key))
	body, err := json.Marshal(map[string]any{
		"target":   map[string]string{"context": context, "namespace": namespace, "clusterUID": clusterUID, "agent": agent},
		"bundle":   agent,
		"complete": true,
		"at":       "2026-01-01T00:00:00Z",
		"resources": []map[string]string{
			{"kind": "Agent", "name": agent, "uid": "agent-uid", "action": "release", "reason": "origin adopted"},
			{"kind": "Provider", "name": agent, "uid": "provider-uid", "action": "release", "reason": "origin adopted"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func seedStatusUnmanagedResources(t *testing.T, dir string, provider, agent map[string]any) {
	t.Helper()
	seedReconcile(t, dir, provider)
	seedReconcile(t, dir, agent)
}

func TestBundleStatusRetiredReleasedAgentShowsNotDeployedWithAdoptionNoteAndLiftHistory(t *testing.T) {
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	provider := reconcileLive(t, dir, "Provider", rendered)
	agent := reconcileLive(t, dir, "Agent", rendered)
	seedStatusUnmanagedResources(t, dir, provider, agent)
	writeBundleReceipt(t, opt.BundleDir, "kind-test", "orka-system", "cluster-uid", name, "uncommitted")
	opt.Context, opt.Namespace = "kind-test", "orka-system"

	before, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Targets) != 1 || before.Targets[0].State != bundleStateUnknown || !strings.Contains(before.Targets[0].Detail, "missing ownership marker") {
		t.Fatalf("unmarked Agent before retire must remain unknown: %+v", before.Targets)
	}
	originalReceiptID := before.Targets[0].ReceiptID
	writeStatusRetireReceipt(t, opt.BundleDir, "kind-test", "orka-system", "cluster-uid", name)

	var output strings.Builder
	a.Out = &output
	opt.Output = "json"
	if err := a.BundleStatus(opt); err != nil {
		t.Fatal(err)
	}
	var report bundleStatusReport
	if err := json.Unmarshal([]byte(output.String()), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Targets) != 1 {
		t.Fatalf("retire receipt must not become a second lift target: %+v", report.Targets)
	}
	target := report.Targets[0]
	if target.State != bundleStateNotDeployed || !strings.Contains(target.Detail, "unmanaged Agent of that name still exists; a later lift would adopt it") {
		t.Fatalf("released Agent must be not deployed with adoption warning: %+v", target)
	}
	if !target.Recorded || target.NoReceipt || target.ReceiptID != originalReceiptID || originalReceiptID == "" {
		t.Fatalf("retire erased historical lift receipt: before %+v; after %+v", before.Targets[0], target)
	}
	if target.Agent.Marked || !target.Agent.Found {
		t.Fatalf("released Agent should be visible but unmanaged: %+v", target.Agent)
	}
	output.Reset()
	opt.Output = "table"
	if err := a.BundleStatus(opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), target.Detail) || !strings.Contains(output.String(), "recorded:") {
		t.Fatalf("table omits release note or lift history: %s", output.String())
	}
}

func TestBundleStatusMixedDeletedAgentReleasedProviderIsNotDeployed(t *testing.T) {
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	provider := reconcileLive(t, dir, "Provider", rendered)
	seedReconcile(t, dir, provider)
	writeBundleReceipt(t, opt.BundleDir, "kind-test", "orka-system", "cluster-uid", name, "uncommitted")
	writeStatusRetireReceipt(t, opt.BundleDir, "kind-test", "orka-system", "cluster-uid", name)
	path := bundleRetireReceiptPath(opt.BundleDir, "kind-test", "orka-system", "cluster-uid")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt["resources"].([]any)[0].(map[string]any)["action"] = "delete"
	body, _ := json.Marshal(receipt)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	opt.Context = "kind-test"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Targets) != 1 || report.Targets[0].State != bundleStateNotDeployed {
		t.Fatalf("mixed retirement incorrectly deployed: %+v", report.Targets)
	}
}

func TestBundleStatusRetireReceiptIsNotLiftHistoryOnExplicitTarget(t *testing.T) {
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	provider := reconcileLive(t, dir, "Provider", rendered)
	agent := reconcileLive(t, dir, "Agent", rendered)
	seedStatusUnmanagedResources(t, dir, provider, agent)
	writeStatusRetireReceipt(t, opt.BundleDir, "kind-test", "orka-system", "cluster-uid", name)
	opt.Context, opt.Namespace = "kind-test", "orka-system"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Targets) != 1 || report.Targets[0].State != bundleStateNotDeployed || !report.Targets[0].NoReceipt || report.Targets[0].Recorded || report.Targets[0].ReceiptID != "" {
		t.Fatalf("retire receipt should not masquerade as lift history: %+v", report.Targets)
	}
	if !strings.Contains(report.Targets[0].Detail, "a later lift would adopt it") {
		t.Fatalf("explicit target did not show release warning: %+v", report.Targets[0])
	}
}

func TestBundleStatusRetireReceiptDoesNotChangeMissingOrDifferentClusterStatus(t *testing.T) {
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	provider := reconcileLive(t, dir, "Provider", rendered)
	agent := reconcileLive(t, dir, "Agent", rendered)
	seedStatusUnmanagedResources(t, dir, provider, agent)
	opt.Context, opt.Namespace = "kind-test", "orka-system"
	writeStatusRetireReceipt(t, opt.BundleDir, "kind-test", "orka-system", "former-cluster", name)

	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Targets) != 1 || report.Targets[0].State != bundleStateUnknown || !strings.Contains(report.Targets[0].Detail, "missing ownership marker") {
		t.Fatalf("retire receipt from a different physical cluster changed status: %+v", report.Targets)
	}
	writeStatusRetireReceipt(t, opt.BundleDir, "kind-test", "orka-system", "cluster-uid", name)
	agent["metadata"].(map[string]any)["uid"] = "unrelated-agent"
	seedReconcile(t, dir, agent)
	report, err = a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if report.Targets[0].State != bundleStateUnknown || !strings.Contains(report.Targets[0].Detail, "missing ownership marker") {
		t.Fatalf("unrelated unowned Agent after retire must remain unknown: %+v", report.Targets)
	}
}
