//go:build windows

package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsSuitePassingExportLifecycle(t *testing.T) {
	root, prepared, suite, placement, observations := passingFixture(t)
	receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	parent := privateSuiteDir(t)
	destination := filepath.Join(parent, "receipt.json")
	output, err := preflightSuitePassingExport(t.Context(), destination, root, filepath.Dir(prepared.Path))
	if err != nil {
		t.Fatal(err)
	}
	if err := output.publish(t.Context(), receipt, suite); err != nil {
		t.Fatal(err)
	}
	if !output.linked || !output.visibilityKnown {
		t.Fatalf("publication state = linked:%t known:%t", output.linked, output.visibilityKnown)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readSuitePassingReceipt(t.Context(), destination); err != nil {
		t.Fatal(err)
	}
	if second, err := preflightSuitePassingExport(t.Context(), destination, root, filepath.Dir(prepared.Path)); err == nil {
		_ = second.Close()
		t.Fatal("existing receipt accepted")
	}
}

func TestWindowsSuitePassStoreLifecycleAndLock(t *testing.T) {
	store, suite, raw := passStoreFixture(t)
	if competing, err := openSuitePassStore(t.Context(), store.workspace, store.cache, suite.Digest); err == nil {
		_ = competing.Close()
		t.Fatal("competing workspace store acquired the lock")
	}
	record := commitStoreReceipt(t, store, suite, raw)
	if _, selected, err := store.latest(t.Context(), suite); err != nil || selected != record {
		t.Fatalf("latest = %q, %v", selected, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openSuitePassStore(t.Context(), store.workspace, store.cache, suite.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, selected, err := reopened.latest(t.Context(), suite); err != nil || selected != record {
		t.Fatalf("reopened latest = %q, %v", selected, err)
	}
}

func TestWindowsSuitePassStoreRefusesUnsafeStateBeforeRecord(t *testing.T) {
	home := privateSuiteDir(t)
	workspace := filepath.Join(home, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(workspace, "state")
	t.Setenv("KMX_HOME", state)
	if _, err := openSuitePassStore(t.Context(), workspace, filepath.Join(home, "cache"), "sha256:"+strings.Repeat("a", 64)); !errors.Is(err, errSuitePassStore) {
		t.Fatalf("state inside workspace = %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("refusal created state: %v", err)
	}
}
