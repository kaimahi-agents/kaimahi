//go:build !windows

package agentfile

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestLoadRefusesFIFOWithoutOpening(t *testing.T) {
	path := filepath.Join(realTemp(t), "fifo.md")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if a, err := Load(path); err == nil || a != nil {
		t.Fatal("FIFO accepted")
	}
}
