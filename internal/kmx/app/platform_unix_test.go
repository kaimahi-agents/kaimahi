//go:build !windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func testFIFO(t *testing.T, path string, mode uint32) {
	t.Helper()
	if err := unix.Mkfifo(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertOrkaForwardExited(t *testing.T, dir string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, "forward-pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("forward process %d still exists: %v", pid, err)
	}
}

func TestSuiteFailureStorePermissions(t *testing.T) {
	if mode := os.Getenv("KMX_FAILURE_UMASK"); mode != "" {
		mask := 0077
		if mode == "022" {
			mask = 0022
		}
		unix.Umask(mask)
		TestSuiteFailureStoreRetention(t)
		return
	}
	for _, mode := range []string{"077", "022"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSuiteFailureStorePermissions$", "-test.count=1")
		cmd.Env = append(os.Environ(), "KMX_FAILURE_UMASK="+mode)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("umask%s: %s %v", mode, out, err)
		}
	}
}
