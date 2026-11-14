//go:build !windows

package modelaccess

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRelativeProfileStillRefusesSymlinks(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	if err := os.Mkdir("profiles", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("profiles/model.json", []byte(`{"route":{"provider":"fake","model":"fake","auth":"none"},"capabilities":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("profiles", "alias"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("profiles/model.json", "model.json"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"alias/model.json", "./model.json"} {
		if _, err := LoadProfile(path); err == nil {
			t.Fatalf("followed symlink in %s", path)
		}
	}
}
