//go:build !windows

package agentfile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOrkaPairRefusesSymlinksAndFIFO(t *testing.T) {
	root := realTemp(t)
	agent := filepath.Join(root, "reader.agent.md")
	tests := filepath.Join(root, "reader.agent.test.md")
	pairWrite(t, agent, orkaExample)
	if err := syscall.Mkfifo(tests, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrkaPair(agent, ""); err == nil {
		t.Fatal("FIFO accepted as tests")
	}
	if err := os.Remove(tests); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "actual.md")
	pairWrite(t, target, testsStarter)
	if err := os.Symlink(target, tests); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrkaPair(agent, ""); err == nil {
		t.Fatal("symlink accepted as tests")
	}
	if err := os.Remove(tests); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(agent); err != nil {
		t.Fatal(err)
	}
	pairWrite(t, target, orkaExample)
	if err := os.Symlink(target, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrkaPair(agent, ""); err == nil {
		t.Fatal("symlink accepted as agent")
	}
}
