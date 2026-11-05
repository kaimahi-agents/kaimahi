//go:build !windows

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

func retainedPrepared(t *testing.T) (RetainedPrepareOptions, RetainedPackageSummary) {
	t.Helper()
	retainedHome(t)
	opt := retainedOptions(t)
	summary, err := (&App{}).PrepareRetainedFile(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	return opt, summary
}
func retainedStoreForTest(t *testing.T) *retainedStore {
	t.Helper()
	s, err := openRetainedStore(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRetainedUseRestartAndContention(t *testing.T) {
	opt, p := retainedPrepared(t)
	s := retainedStoreForTest(t)
	use, err := s.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision)
	if err != nil {
		t.Fatal(err)
	}
	other := retainedStoreForTest(t)
	for _, operation := range []func() error{
		func() error { _, err := other.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision); return err },
		func() error { _, err := other.Release(t.Context(), p.Handle); return err },
		func() error { _, err := other.Cleanup(t.Context(), p.Handle, ""); return err },
	} {
		var busy *PackageBusy
		if err := operation(); !errors.As(err, &busy) {
			t.Fatalf("actual contention = %v", err)
		}
	}
	inspected, err := other.Inspect(t.Context(), p.Handle, "")
	if err != nil || inspected.Package.State != "ready" || inspected.ActiveLock != nil {
		t.Fatal("inspect acquired/probed exclusive use", err)
	}
	if err := use.Close(); err != nil {
		t.Fatal(err)
	}
	if err := use.Close(); err != nil {
		t.Fatal("consuming close", err)
	}
	if err := use.check(t.Context()); err == nil {
		t.Fatal("closed use retained authority")
	}
	fresh, err := other.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision)
	if err != nil {
		t.Fatal("reopen after close", err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := other.OpenVerifiedUse(t.Context(), p.Handle, "wrong"); err == nil {
		t.Fatal("wrong R accepted")
	}
	if _, err := other.OpenVerifiedUse(t.Context(), "pkg-"+p.Handle, p.SourceRevision); err == nil {
		t.Fatal("directory component accepted as H")
	}
	if err := os.WriteFile(opt.File, []byte(retainedAgentFixture+"# change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := other.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision); err == nil {
		t.Fatal("stale source accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal("idempotent store close", err)
	}
}

func TestRetainedUseConsumesFailedClose(t *testing.T) {
	_, p := retainedPrepared(t)
	s := retainedStoreForTest(t)
	u, err := s.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.entry.lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := u.Close(); err == nil {
		t.Fatal("required close failure hidden")
	}
	if err := u.Close(); err != nil {
		t.Fatal("failed close not consuming", err)
	}
	if err := u.check(t.Context()); err == nil {
		t.Fatal("failed close retained authority")
	}
	fresh, err := s.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedUseClosedProfileAndFingerprint(t *testing.T) {
	for _, kind := range []string{"fingerprint", "handle", "schema", "unknown", "layout", "source-mode", "control-replacement"} {
		t.Run(kind, func(t *testing.T) {
			_, p := retainedPrepared(t)
			s := retainedStoreForTest(t)
			dir := filepath.Join(s.path, retainedComponent(p.Handle))
			if kind == "layout" {
				if err := os.WriteFile(filepath.Join(dir, "layout", "index.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if kind == "control-replacement" {
				if err := os.Rename(dir, dir+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"control.json", "use.lock", "ready.json"} {
					raw, err := os.ReadFile(filepath.Join(dir+"-old", name))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				var ready retainedReady
				if err := retainedRead(t.Context(), mustRetainedDirectory(t, dir), "ready.json", &ready); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "fingerprint":
					ready.Package.Fingerprint = "sha256:" + strings.Repeat("0", 64)
				case "handle":
					ready.Package.Handle = strings.Repeat("0", 32)
				case "schema":
					ready.Version = 2
				case "source-mode":
					ready.Source.Explicit = true
					ready.Source.Tests += ".other"
				}
				raw, err := retainedEncode(ready)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "unknown" {
					raw = append([]byte(`{"extra":true,`), raw[1:]...)
				}
				if err := os.WriteFile(filepath.Join(dir, "ready.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if u, err := s.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision); err == nil {
				_ = u.Close()
				t.Fatal("corrupt retained state accepted")
			}
		})
	}
}
func mustRetainedDirectory(t *testing.T, path string) *fileaccess.Directory {
	t.Helper()
	d, err := fileaccess.OpenPrivateDirectory(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestRetainedReleaseRevocation(t *testing.T) {
	opt, p := retainedPrepared(t)
	s := retainedStoreForTest(t)
	source, err := os.ReadFile(opt.File)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Release(t.Context(), p.Handle)
	if err != nil || result.LocalState != "removed" || result.RemovedFiles == 0 {
		t.Fatal("release", result, err)
	}
	if _, err := os.Stat(filepath.Join(s.path, retainedComponent(p.Handle))); !os.IsNotExist(err) {
		t.Fatal("entry survived", err)
	}
	if _, err := s.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision); err == nil {
		t.Fatal("released identity revived")
	}
	actual, err := os.ReadFile(opt.File)
	if err != nil || string(actual) != string(source) {
		t.Fatal("source changed", err)
	}
}

func TestRetainedCleanupIdentity(t *testing.T) {
	_, p := retainedPrepared(t)
	s := retainedStoreForTest(t)
	if _, err := s.Cleanup(t.Context(), p.Handle, ""); !errors.Is(err, errRetainedReady) {
		t.Fatalf("cleanup silently released ready = %v", err)
	}
	if _, err := s.Cleanup(t.Context(), p.Handle, strings.Repeat("a", 32)); err == nil {
		t.Fatal("nonexistent reservation admitted")
	}
	if err := retainedPublish(t.Context(), mustRetainedDirectory(t, filepath.Join(s.path, retainedComponent(p.Handle))), "released.json", readRetainedControl(t, s, p.Handle)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision); err == nil {
		t.Fatal("durable revocation ignored")
	}
	if result, err := s.Cleanup(t.Context(), p.Handle, ""); err != nil || result.LocalState != "removed" {
		t.Fatal(result, err)
	}
	for _, nonempty := range []bool{false, true} {
		h := ""
		if nonempty {
			h = strings.Repeat("b", 32)
		} else {
			h = strings.Repeat("a", 32)
		}
		path := filepath.Join(s.path, retainedComponent(h))
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if nonempty {
			if err := os.WriteFile(filepath.Join(path, "foreign"), []byte("witness"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		_, err := s.Cleanup(t.Context(), h, "")
		if nonempty && err == nil {
			t.Fatal("nonempty unmarked adopted")
		}
		if !nonempty && err != nil {
			t.Fatal("empty observed allocation refused", err)
		}
	}
}
func readRetainedControl(t *testing.T, s *retainedStore, h string) retainedControl {
	t.Helper()
	var c retainedControl
	if err := retainedRead(t.Context(), mustRetainedDirectory(t, filepath.Join(s.path, retainedComponent(h))), "control.json", &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRetainedOfflineCapacity(t *testing.T) {
	opt, p := retainedPrepared(t)
	s := retainedStoreForTest(t)
	if err := os.Remove(opt.File); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 255; i++ {
		h := fmt.Sprintf("%032x", i)
		if err := os.Mkdir(filepath.Join(s.path, retainedComponent(h)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	total := 0
	after := ""
	for {
		page, err := s.List(t.Context(), after)
		if err != nil {
			t.Fatal(err)
		}
		if page.Count != 256 || len(page.Packages) > 32 {
			t.Fatal("unbounded/incomplete inventory")
		}
		total += len(page.Packages)
		if page.NextCursor == nil {
			break
		}
		if *page.NextCursor <= after {
			t.Fatal("cursor did not progress")
		}
		after = *page.NextCursor
	}
	if total != 256 {
		t.Fatal("lost page entries", total)
	}
	for _, bad := range []string{"../entry", "pkg-" + p.Handle, " "} {
		if _, err := s.List(t.Context(), bad); err == nil {
			t.Fatal("invalid cursor")
		}
	}
	inspected, err := s.Inspect(t.Context(), p.Handle, "")
	if err != nil || inspected.Package.State != "ready" || inspected.RunCount != 0 || len(inspected.Runs) != 0 || inspected.NextCursor != nil {
		t.Fatal("offline inspection used live source", err)
	}
	if _, err := s.Release(t.Context(), p.Handle); err != nil {
		t.Fatal("full-store source-free release", err)
	}
	page, err := s.List(t.Context(), "")
	if err != nil || page.Count != 255 {
		t.Fatal("capacity not freed", err)
	}
}

func TestRetainedUseFamilyWaitAndCancellation(t *testing.T) {
	_, p := retainedPrepared(t)
	s := retainedStoreForTest(t)
	held, err := s.familyLock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.familyLock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellable family wait = %v", err)
	}
	start := time.Now()
	var busy *FamilyBusy
	if _, err := s.familyLock(t.Context()); !errors.As(err, &busy) || time.Since(start) > 2*time.Second {
		t.Fatalf("bounded family wait = %v", err)
	}
	use, err := s.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision)
	if err != nil {
		t.Fatal("family lock spanned verified use", err)
	}
	if err := use.Close(); err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
}
