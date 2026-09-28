package app

import (
	"os"
	"reflect"
	"testing"
)

func TestLiftPreferencesPersistScopedSelections(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	for scope, value := range map[string]string{"subscription": "sub-a", "cluster/sub-a": "rg/cluster", "cluster/sub-b": "rg/other", "context": "aks-context"} {
		if err := saveLiftPreference(scope, value); err != nil {
			t.Fatal(err)
		}
	}
	prefs, err := loadLiftPreferences()
	if err != nil {
		t.Fatal(err)
	}
	if prefs["cluster/sub-a"] != "rg/cluster" || prefs["cluster/sub-b"] != "rg/other" || prefs["context"] != "aks-context" {
		t.Fatalf("prefs=%v", prefs)
	}
	path, _ := liftPreferencesPath()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions=%v err=%v", info, err)
	}
}

func TestRecentLiftItemsPrioritizeIdentityWithoutChangingSource(t *testing.T) {
	items := []chatPickerItem{{name: "same", detail: "rg-a"}, {name: "same", detail: "rg-b"}, {name: "third"}}
	keys := []string{"rg-a/same", "rg-b/same", "rg/third"}
	ordered, indices := recentLiftItems(items, keys, "rg-b/same")
	if !reflect.DeepEqual(indices, []int{1, 0, 2}) || ordered[0].detail != "last selected · rg-b" || items[1].detail != "rg-b" {
		t.Fatalf("ordered=%v indices=%v", ordered, indices)
	}
	_, indices = recentLiftItems(items, keys, "deleted-cluster")
	if !reflect.DeepEqual(indices, []int{0, 1, 2}) {
		t.Fatalf("stale selection reordered: %v", indices)
	}
}
