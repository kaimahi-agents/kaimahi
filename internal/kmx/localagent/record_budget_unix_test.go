//go:build !windows

package localagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOversizedActivationLeavesInventoryUsable(t *testing.T) {
	pair := localRegistrationPair(t)
	_, _, err := Activate(t.Context(), "", pair, "compatible", "https://example.test/"+strings.Repeat("x", 40<<10), "unit")
	if err == nil {
		t.Fatal("oversized endpoint accepted")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("KMX_HOME"), "local-agents")); !os.IsNotExist(err) {
		t.Fatal("oversized route created state")
	}
	oversized := *pair
	oversized.Tests.Selection.Path = strings.Repeat("x", 40<<10)
	_, _, err = Activate(t.Context(), "", &oversized, "fake", "", "fake")
	if err == nil || !strings.Contains(err.Error(), "storage limit") {
		t.Fatalf("serialized record not checked: %v", err)
	}
	if err := filepath.WalkDir(os.Getenv("KMX_HOME"), func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if strings.HasPrefix(d.Name(), "snapshot-") || strings.HasPrefix(d.Name(), "start-") {
			t.Errorf("oversized record cached/published %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadInstances(false)
	if err != nil || len(rows) != 0 {
		t.Fatalf("inventory poisoned: %v %v", rows, err)
	}
	record, _, err := Activate(t.Context(), "", pair, "fake", "", "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(t.Context(), record.ID)
	rows, err = ReadInstances(false)
	if err != nil || len(rows) != 1 {
		t.Fatalf("valid activation blocked: %v %v", rows, err)
	}
}
