package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBundleStatusRememberedUIDChangedReadsNoObjects(t *testing.T) {
	a, opt, dir, _, _ := bundleStatusFixture(t)
	selection, err := bundleLiftSelectionPath(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveBundleLiftSelection(selection, bundleLiftSelection{Context: "kind-test", Namespace: "orka-system", ClusterUID: "previous-cluster", Inference: "provider:inference"}); err != nil {
		t.Fatal(err)
	}
	before := len(orkaCalls(t, dir))
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Targets) != 1 || report.Targets[0].State != bundleStateChanged || !report.Targets[0].NoReceipt {
		t.Fatalf("remembered UID was not guarded: %+v", report.Targets)
	}
	for _, call := range orkaCalls(t, dir)[before:] {
		if slices.Contains(call.Args, "agents.core.orka.ai") || slices.Contains(call.Args, "providers.core.orka.ai") {
			t.Fatalf("read object after UID mismatch: %+v", call)
		}
	}
}

func TestBundleStatusRenderedMarkersDisagree(t *testing.T) {
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	seedBundleLiveResources(t, dir, rendered, name, func(agent map[string]any) {
		agent["metadata"].(map[string]any)["annotations"].(map[string]any)[orkaRenderedMarker] = strings.Repeat("a", 64)
	})
	opt.Context = "kind-test"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if report.Targets[0].State != bundleStateUnknown || !strings.Contains(report.Targets[0].Detail, "disagree") {
		t.Fatalf("rendered marker mismatch: %+v", report.Targets[0])
	}
}

func TestBundleStatusForbiddenAgentReadIsUnknown(t *testing.T) {
	a, opt, _, _, _ := bundleStatusFixture(t)
	t.Setenv("KMX_STATUS_FORBIDDEN", "1")
	opt.Context = "kind-test"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Targets) != 1 || report.Targets[0].State != bundleStateUnknown || strings.Contains(report.Targets[0].Detail, "private-token-must-not-escape") {
		t.Fatalf("forbidden agent read was leaked or treated as fatal: %+v", report.Targets)
	}
}

func TestBundleStatusRecordedNamespaceSelection(t *testing.T) {
	_, opt, _, _, name := bundleStatusFixture(t)
	writeBundleReceipt(t, opt.BundleDir, "kind-test", "other-namespace", "cluster-uid", name, "uncommitted")
	opt.Context = "kind-test"
	targets, err := bundleStatusTargets(opt.BundleDir, opt)
	if err != nil || len(targets) != 1 || targets[0].Namespace != "other-namespace" {
		t.Fatalf("unique recorded namespace: %+v %v", targets, err)
	}
	writeBundleReceipt(t, opt.BundleDir, "kind-test", "orka-system", "cluster-uid", name, "uncommitted")
	if targets, err = bundleStatusTargets(opt.BundleDir, opt); err != nil || len(targets) != 2 {
		t.Fatalf("context should list both recorded namespaces: %+v %v", targets, err)
	}
	opt.Namespace = "other-namespace"
	if targets, err = bundleStatusTargets(opt.BundleDir, opt); err != nil || len(targets) != 1 || targets[0].Namespace != opt.Namespace {
		t.Fatalf("explicit namespace: %+v %v", targets, err)
	}
}

func TestBundleStatusNeverWritesResourcesReceiptsOrSelection(t *testing.T) {
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	seedBundleLiveResources(t, dir, rendered, name, nil)
	writeBundleReceipt(t, opt.BundleDir, "kind-test", "orka-system", "cluster-uid", name, "uncommitted")
	selection, err := bundleLiftSelectionPath(opt.BundleDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveBundleLiftSelection(selection, bundleLiftSelection{Context: "kind-test", Namespace: "orka-system", ClusterUID: "cluster-uid", Inference: "provider:inference"}); err != nil {
		t.Fatal(err)
	}
	selectionBefore, err := os.ReadFile(selection)
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := os.ReadDir(filepath.Join(opt.BundleDir, "receipts"))
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipt setup: %v %v", receipts, err)
	}
	receiptPath := filepath.Join(opt.BundleDir, "receipts", receipts[0].Name())
	receiptBefore, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	before := len(orkaCalls(t, dir))
	opt.Output = "json"
	var output bytes.Buffer
	a.Out = &output
	if err := a.BundleStatus(opt); err != nil {
		t.Fatal(err)
	}
	var report bundleStatusReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || len(report.Targets) != 1 || report.Targets[0].State != bundleStateInSync {
		t.Fatalf("status: %+v %v", report, err)
	}
	for _, call := range orkaCalls(t, dir)[before:] {
		if slices.Contains(call.Args, "patch") || call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatalf("status wrote resource: %+v", call)
		}
		if !slices.Equal(call.Args[:2], []string{"--context", "kind-test"}) {
			t.Fatalf("un-pinned kubectl: %+v", call)
		}
	}
	selectionAfter, err := os.ReadFile(selection)
	if err != nil {
		t.Fatal(err)
	}
	receiptAfter, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(selectionBefore, selectionAfter) || !bytes.Equal(receiptBefore, receiptAfter) {
		t.Fatal("status modified local state")
	}
}

// A matching live digest need not be in Git: status can still compare fields
// while reporting that no deployed commit was found.
func TestBundleStatusUncommittedRevisionNoGitMatch(t *testing.T) {
	a, opt, dir, rendered, name := bundleStatusFixture(t)
	seedBundleLiveResources(t, dir, rendered, name, nil)
	opt.Context = "kind-test"
	report, err := a.bundleStatusReport(opt)
	if err != nil {
		t.Fatal(err)
	}
	if report.GitCommit != "uncommitted" || report.Targets[0].State != bundleStateInSync || report.Targets[0].DeployedCommit != "" || !strings.Contains(report.Targets[0].GitNote, "not found in Git") {
		t.Fatalf("uncommitted revision: %+v", report)
	}
}
