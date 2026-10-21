package agentfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLoadRefusesWindowsJunction(t *testing.T) {
	root := realTemp(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "a.md"), []byte(sample(t)), 0600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(root, "junction")
	if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Fatalf("create test junction: %v: %s", err, output)
	}
	if a, err := Load(filepath.Join(junction, "a.md")); err == nil || a != nil {
		t.Fatal("directory junction accepted")
	}
}
