package mcp

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

func TestLockProcess(t *testing.T) {
	if os.Getenv("KMX_MCP_LOCK_FIXTURE") != "1" {
		return
	}
	root, err := os.OpenRoot(os.Getenv("KMX_MCP_LOCK_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openTarget(root, "x.agent.md")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	unlock, err := lockTarget(file)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	fmt.Println("locked")
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
}
func TestFilesLockReleasedOnProcessExit(t *testing.T) {
	f, root := filesFixture(t)
	a, err := f.Create(context.Background(), "x.agent.md", []byte("valid:old"))
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestLockProcess$")
	cmd.Env = append(os.Environ(), "KMX_MCP_LOCK_FIXTURE=1", "KMX_MCP_LOCK_ROOT="+root)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "locked" {
		t.Fatal("helper did not acquire lock")
	}
	_, err = f.Edit(t.Context(), a.Path, a.Revision, []byte("valid:new"))
	requireCode(t, err, "busy")
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if _, err := f.Edit(t.Context(), a.Path, a.Revision, []byte("valid:new")); err != nil {
		t.Fatalf("crash retained lock: %v", err)
	}
}
func TestFilesOldInodeLockCannotAuthorizeReplacement(t *testing.T) {
	f, root := filesFixture(t)
	a, err := f.Create(t.Context(), "x.agent.md", []byte("valid:old"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := OpenFiles(root, fixtureValidator)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	// Simulate an independent writer publishing after our initial snapshot and
	// validation, but before our lock/check/replace section.
	f.validate = func(_ string, b []byte) error {
		if string(b) == "valid:old" {
			_, err := other.Edit(t.Context(), a.Path, a.Revision, []byte("valid:independent"))
			if err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	_, err = f.Edit(t.Context(), a.Path, a.Revision, []byte("valid:ours"))
	requireCode(t, err, "stale_revision")
}
