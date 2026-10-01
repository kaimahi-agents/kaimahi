package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Local JSON has no version discriminator: readers accept unrecognized keys,
// but still require the identity fields they know and use.
func TestBundleLocalStateDoesNotNegotiateFormatVersions(t *testing.T) {
	bundle := t.TempDir()
	selectionPath := filepath.Join(bundle, "selection.json")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(selectionPath, `{"formatVersion":99,"context":"ctx","clusterUID":"cluster","namespace":"orka-system","inference":"provider:inference"}`)
	selection, err := loadBundleLiftSelection(selectionPath)
	if err != nil || selection.ClusterUID != "cluster" {
		t.Fatalf("unknown selection key prevented reading known identity: %+v, %v", selection, err)
	}
	write(selectionPath, `{"formatVersion":99}`)
	if _, err := loadBundleLiftSelection(selectionPath); err == nil || !strings.Contains(err.Error(), "ambiguous remembered bundle target") {
		t.Fatalf("missing selection identity was not refused: %v", err)
	}

	dir := filepath.Join(bundle, "receipts")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "foreign.json"), `{"formatVersion":99,"receipt":{"target":{"context":"ctx","namespace":"orka-system"}}}`)
	write(filepath.Join(dir, "invalid.json"), `{not json`)
	targets, err := loadBundleReceiptTargets(bundle)
	if err != nil || len(targets) != 1 || targets[0].Context != "ctx" {
		t.Fatalf("unreadable receipt should be skipped while known identity remains: %+v, %v", targets, err)
	}

	target := bundleTargetStatus{Context: "ctx", Namespace: "orka-system", ObservedClusterUID: "cluster", LiveDigest: "portable", Agent: bundleResourceStatus{UID: "agent"}}
	path := bundleEvaluationReceiptPath(bundle, "ctx", "orka-system", "cluster")
	write(path, `{"formatVersion":99,"portableDigest":"portable","casesDigest":"cases","target":{"context":"ctx","namespace":"orka-system","agentUID":"agent"},"result":"pass"}`)
	if got := bundleEvaluationStatus(bundle, target, "portable", "cases"); got != "none" {
		t.Fatalf("incomplete evaluation evidence with unknown key = %s, want none", got)
	}
	write(path, `{not json`)
	if got := bundleEvaluationStatus(bundle, target, "portable", "cases"); got != "none" {
		t.Fatalf("unreadable evaluation evidence = %s", got)
	}
}
