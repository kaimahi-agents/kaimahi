package agentfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLoadPairRefusesWindowsJunction(t *testing.T) {
	root := realTemp(t)
	agent := filepath.Join(root, ".github", "agents", "g.agent.md")
	pairWrite(t, agent, "Say hello.\n")
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	pairWrite(t, filepath.Join(target, "g.agent.test.md"), testsStarter)
	junction := filepath.Join(root, ".github", "agent-tests")
	// Same platform fixture as Load's junction regression, including its creation
	// requirements. Missing descendants must not hide an existing reparse ancestor.
	if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Fatalf("create test junction: %v: %s", err, output)
	}
	pairRefusal(t, agent, "", "Tests", "symlinks or reparse")
	pairRefusal(t, agent, filepath.Join(junction, "missing", "suite.md"), "Tests", "symlinks or reparse")
}
