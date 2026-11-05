//go:build !windows

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRetainedInspectRequiresSavedIntegrity(t *testing.T) {
	for _, kind := range []string{"missing-layout", "corrupt-index", "missing-lock", "linked-lock", "public-layout-file", "symlink-layout-file", "over-capacity", "fingerprint", "model", "descriptor"} {
		t.Run(kind, func(t *testing.T) {
			_, p := retainedPrepared(t)
			s := retainedStoreForTest(t)
			root := filepath.Join(s.path, retainedComponent(p.Handle))
			index := filepath.Join(root, "layout", "index.json")
			switch kind {
			case "missing-layout":
				if err := os.RemoveAll(filepath.Join(root, "layout")); err != nil {
					t.Fatal(err)
				}
			case "corrupt-index":
				if err := os.WriteFile(index, []byte("broken saved layout"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-lock":
				if err := os.Remove(filepath.Join(root, "use.lock")); err != nil {
					t.Fatal(err)
				}
			case "linked-lock":
				if err := os.Link(filepath.Join(root, "use.lock"), filepath.Join(t.TempDir(), "witness")); err != nil {
					t.Fatal(err)
				}
			case "public-layout-file":
				if err := os.Chmod(index, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink-layout-file":
				w := filepath.Join(t.TempDir(), "witness")
				if err := os.WriteFile(w, []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(index); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(w, index); err != nil {
					t.Fatal(err)
				}
			case "fingerprint", "model", "descriptor":
				var ready retainedReady
				if err := retainedRead(t.Context(), mustRetainedDirectory(t, root), "ready.json", &ready); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "fingerprint":
					ready.Package.Fingerprint = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
				case "model":
					ready.Package.Model = "different-model"
				case "descriptor":
					ready.Package.Descriptor.Size++
				}
				raw, err := retainedEncode(ready)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "ready.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "over-capacity":
				f, err := os.OpenFile(filepath.Join(root, "layout", "oversize"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				err = f.Truncate(retainedEntryBytes)
				err = errors.Join(err, f.Close())
				if err != nil {
					t.Fatal(err)
				}
			}
			inspected, _ := s.Inspect(t.Context(), p.Handle, "")
			if inspected.Package.Available || inspected.Package.Digest != "" || inspected.SourceRevision != "" || inspected.Fingerprint != "" {
				t.Fatal("damaged saved entry advertised", kind, inspected)
			}
			page, err := s.List(t.Context(), "")
			if err != nil || len(page.Packages) != 1 || page.Count != 1 {
				t.Fatal(page, err)
			}
			if page.Packages[0] != (RetainedEntrySummary{Handle: p.Handle, State: "incomplete"}) {
				t.Fatal("damaged list metadata escaped", page.Packages[0])
			}
		})
	}
}

func TestRetainedInspectIndependentOfSourceAndUse(t *testing.T) {
	opt, p := retainedPrepared(t)
	s := retainedStoreForTest(t)
	u, err := s.OpenVerifiedUse(t.Context(), p.Handle, p.SourceRevision)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(opt.File); err != nil {
		t.Fatal(err)
	}
	inspected, err := s.Inspect(t.Context(), p.Handle, "")
	if err != nil || !inspected.Package.Available || inspected.ActiveLock != nil {
		t.Fatal("offline inspection borrowed source or exclusive use", inspected, err)
	}
	page, err := s.List(t.Context(), "")
	if err != nil || len(page.Packages) != 1 || !page.Packages[0].Available {
		t.Fatal(page, err)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedRevokedStageFailureKeepsObservation(t *testing.T) {
	_, p := retainedPrepared(t)
	s := retainedStoreForTest(t)
	_, err := s.reclaim(t.Context(), p.Handle, true, func(at string) error {
		if at == "revoked" {
			return errors.New("stop after revocation")
		}
		return nil
	})
	if err == nil {
		t.Fatal("revocation point not reached")
	}
	root := filepath.Join(s.path, retainedComponent(p.Handle))
	if err := os.Mkdir(filepath.Join(root, "ready.stage"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := s.Cleanup(t.Context(), p.Handle, "")
	if err == nil || !result.Released || result.LocalState != "partial" {
		t.Fatal("confirmed revocation lost on stage refusal", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "ready.stage")); err != nil {
		t.Fatal("unsafe stage witness changed", err)
	}
}

func TestRetainedTerminalRemovalKeepsPartialEffect(t *testing.T) {
	_, p := retainedPrepared(t)
	s := retainedStoreForTest(t)
	_, err := s.reclaim(t.Context(), p.Handle, true, func(at string) error {
		if at == "lock" {
			return errors.New("stop at marker-only")
		}
		return nil
	})
	if err == nil {
		t.Fatal("terminal point not reached")
	}
	marker := filepath.Join(s.path, retainedComponent(p.Handle), "released.json")
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &controlFirstContext{Context: base, observe: func() {
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			cancel()
		}
	}}
	result, err := s.Cleanup(ctx, p.Handle, "")
	if err == nil || !result.Released || result.RemovedFiles != 1 || result.LocalState != "partial" {
		t.Fatal("terminal mutation misreported", result, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("marker removal not exercised", err)
	}
}
