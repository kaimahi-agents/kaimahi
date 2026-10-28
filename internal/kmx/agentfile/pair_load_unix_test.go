//go:build !windows

package agentfile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestLoadPairRefusesFIFOAndPermissions(t *testing.T) {
	root := realTemp(t)
	agent := filepath.Join(root, "g.agent.md")
	tests := filepath.Join(root, "g.agent.test.md")
	pairWrite(t, agent, "Say hello.\n")
	if err := syscall.Mkfifo(tests, 0600); err != nil {
		t.Fatal(err)
	}
	pairRefusal(t, agent, "", "Tests", "regular")
	if err := os.Remove(tests); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("permission refusal requires an unprivileged user")
	}
	pairWrite(t, tests, testsStarter)
	if err := os.Chmod(tests, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(tests, 0600)
	pairRefusal(t, agent, "", "Tests", "readable")
	if err := os.Chmod(tests, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(tests); err != nil {
		t.Fatal(err)
	}
	// A default missing source below an inaccessible ancestor is not absence.
	agent = filepath.Join(root, ".github", "agents", "g.agent.md")
	pairWrite(t, agent, "Say hello.\n")
	parent := filepath.Join(root, ".github", "agent-tests")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0700)
	pairRefusal(t, agent, "", "Tests", "readable")
}
