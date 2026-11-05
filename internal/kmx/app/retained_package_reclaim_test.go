//go:build !windows

package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRetainedCleanupCrashWindows(t *testing.T) {
	for _, point := range []string{"revoked", "data", "control", "lock", "revocation", "directory"} {
		t.Run(point, func(t *testing.T) {
			_, p := retainedPrepared(t)
			s := retainedStoreForTest(t)
			hit := false
			result, err := s.reclaim(t.Context(), p.Handle, true, func(at string) error {
				if at == point {
					hit = true
					return errors.New("private canary")
				}
				return nil
			})
			if !hit || err == nil || !result.Released || strings.Contains(err.Error(), "private canary") {
				t.Fatal("wrong interruption", hit, result, err)
			}
			if u, err := s.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision); err == nil {
				_ = u.Close()
				t.Fatal("interrupted release revived authority")
			}
			path := filepath.Join(s.path, retainedComponent(p.Handle))
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return
			}
			result, err = s.Cleanup(t.Context(), p.Handle, "")
			if err != nil || result.LocalState != "removed" {
				t.Fatal("restart cleanup", result, err)
			}
		})
	}
}

func TestRetainedCleanupRefusesForeignTargets(t *testing.T) {
	for _, kind := range []string{"symlink", "public-file", "public-directory", "unknown-root"} {
		t.Run(kind, func(t *testing.T) {
			opt, p := retainedPrepared(t)
			s := retainedStoreForTest(t)
			root := filepath.Join(s.path, retainedComponent(p.Handle))
			foreign := filepath.Join(root, "layout", "foreign")
			switch kind {
			case "symlink":
				if err := os.Symlink(opt.File, foreign); err != nil {
					t.Fatal(err)
				}
			case "public-file":
				if err := os.WriteFile(foreign, []byte("witness"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(foreign, 0644); err != nil {
					t.Fatal(err)
				}
			case "public-directory":
				if err := os.Mkdir(foreign, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(foreign, 0755); err != nil {
					t.Fatal(err)
				}
			case "unknown-root":
				foreign = filepath.Join(root, "not-owned")
				if err := os.Mkdir(foreign, 0700); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.Release(t.Context(), p.Handle)
			if err == nil || !result.Released || result.LocalState != "partial" {
				t.Fatal("unsafe traversal", result, err)
			}
			if _, err := os.Lstat(foreign); err != nil {
				t.Fatal("foreign object removed", err)
			}
			if u, err := s.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision); err == nil {
				_ = u.Close()
				t.Fatal("failed release restored eligibility")
			}
			raw, err := os.ReadFile(opt.File)
			if err != nil || len(raw) == 0 {
				t.Fatal("source witness changed", err)
			}
		})
	}
}

func TestRetainedUseSubprocessHelper(t *testing.T) {
	mode := os.Getenv("KMX_RETAINED_TEST_MODE")
	if mode == "" {
		return
	}
	s, err := openRetainedStore(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, err := s.OpenVerifiedUse(t.Context(), os.Getenv("KMX_RETAINED_TEST_HANDLE"), os.Getenv("KMX_RETAINED_TEST_REVISION"))
	if mode == "busy" {
		var busy *PackageBusy
		if !errors.As(err, &busy) {
			t.Fatal("not actual contention", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	fmt.Println("held")
	for {
		time.Sleep(time.Hour)
	}
}

func TestRetainedUseProcessExitAndRestart(t *testing.T) {
	_, p := retainedPrepared(t)
	s := retainedStoreForTest(t)
	u, err := s.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := func(mode string) *exec.Cmd {
		c := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRetainedUseSubprocessHelper$")
		c.Env = append(os.Environ(), "KMX_RETAINED_TEST_MODE="+mode, "KMX_RETAINED_TEST_HANDLE="+p.Handle, "KMX_RETAINED_TEST_REVISION="+p.SourceRevision)
		return c
	}
	if out, err := command("busy").CombinedOutput(); err != nil {
		t.Fatalf("cross-process contention: %v %s", err, out)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	child := command("hold")
	pipe, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	scanner := bufio.NewScanner(pipe)
	if !scanner.Scan() || scanner.Text() != "held" {
		t.Fatal("child did not acquire retained use", scanner.Err())
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait() // intentional crash injection, not a passing child test
	fresh, err := s.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision)
	if err != nil {
		t.Fatal("OS lock did not release on process death", err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
}
