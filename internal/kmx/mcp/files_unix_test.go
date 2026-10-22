//go:build linux || darwin

package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFilesCreateHonorsUmask(t *testing.T) {
	if os.Getenv("KMX_MCP_UMASK_FIXTURE") == "1" {
		syscall.Umask(0077)
		f, root := filesFixture(t)
		if _, err := f.Create(t.Context(), "x.agent.md", []byte("valid:x")); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(root, "x.agent.md"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("umask bypassed: %o", info.Mode().Perm())
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestFilesCreateHonorsUmask$")
	cmd.Env = append(os.Environ(), "KMX_MCP_UMASK_FIXTURE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("umask fixture: %v %s", err, out)
	}
}

func TestFilesPermissionsAndSpecialTargets(t *testing.T) {
	f, root := filesFixture(t)
	ctx := context.Background()
	a, err := f.Create(ctx, "x.agent.md", []byte("valid:old"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, a.Path), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Edit(ctx, a.Path, a.Revision, []byte("valid:new")); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(root, a.Path))
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	if err := os.Link(filepath.Join(root, a.Path), filepath.Join(root, "alias.agent.md")); err != nil {
		t.Fatal(err)
	}
	_, err = f.Edit(ctx, a.Path, revision([]byte("valid:new")), []byte("valid:linked"))
	requireCode(t, err, "unsafe_path")
	if err := syscall.Mkfifo(filepath.Join(root, "fifo.agent.md"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = f.Edit(ctx, "fifo.agent.md", "", []byte("valid:fifo"))
	requireCode(t, err, "unsafe_path")
}
