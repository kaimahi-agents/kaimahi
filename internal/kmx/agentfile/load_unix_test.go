//go:build !windows

package agentfile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStandaloneParseWithoutCurrentDirectory(t *testing.T) {
	source := []byte(sample(t))
	dir := realTemp(t)
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse("standalone.agent.md", source); err != nil {
		t.Fatalf("standalone parsing needs cwd: %v", err)
	}
	if _, cwdErr := os.Getwd(); cwdErr != nil {
		if _, err := Parse("charter.md", source); err == nil {
			t.Fatal("relative Squad placement must resolve cwd")
		}
	}
}

func TestLoadRefusesFIFOWithoutOpening(t *testing.T) {
	path := filepath.Join(realTemp(t), "fifo.md")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if a, err := Load(path); err == nil || a != nil {
		t.Fatal("FIFO accepted")
	}
}
