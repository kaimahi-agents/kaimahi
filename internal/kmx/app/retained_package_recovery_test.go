//go:build !windows

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

func TestRetainedCleanupNamedStageRecovery(t *testing.T) {
	for _, name := range []string{"control", "ready", "released"} {
		for _, linked := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "-standalone", true: "-linked"}[linked], func(t *testing.T) {
				_, p := retainedPrepared(t)
				s := retainedStoreForTest(t)
				root := filepath.Join(s.path, retainedComponent(p.Handle))
				c := metadataControl(t, s, p.Handle)
				if name == "released" && linked {
					if err := retainedPublish(t.Context(), mustRetainedDirectory(t, root), "released.json", c); err != nil {
						t.Fatal(err)
					}
				}
				if linked {
					if err := os.Link(filepath.Join(root, name+".json"), filepath.Join(root, name+".stage")); err != nil {
						t.Fatal(err)
					}
				} else {
					raw, err := retainedEncode(c)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, name+".stage"), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				result, err := s.Release(t.Context(), p.Handle)
				if err != nil || result.LocalState != "removed" {
					t.Fatal("owned metadata remnant stranded", result, err)
				}
			})
		}
	}
}

func TestRetainedCleanupControlOnlyAndUnmarkedLock(t *testing.T) {
	for _, kind := range []string{"control", "control-stage", "unmarked-lock"} {
		t.Run(kind, func(t *testing.T) {
			_, p := retainedPrepared(t)
			s := retainedStoreForTest(t)
			root := filepath.Join(s.path, retainedComponent(p.Handle))
			c := metadataControl(t, s, p.Handle)
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "control", "control-stage":
				raw, err := retainedEncode(c)
				if err != nil {
					t.Fatal(err)
				}
				name := "control.json"
				if kind == "control-stage" {
					name = "control.stage"
				}
				if err := os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "unmarked-lock":
				if err := os.WriteFile(filepath.Join(root, "use.lock"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.Cleanup(t.Context(), p.Handle, "")
			if kind == "unmarked-lock" {
				if err == nil {
					t.Fatal("unmarked lock-only adopted")
				}
				if _, err := os.Stat(filepath.Join(root, "use.lock")); err != nil {
					t.Fatal("unmarked witness removed", err)
				}
				return
			}
			if err != nil || result.LocalState != "removed" {
				t.Fatal("valid controlled initialization stranded", result, err)
			}
		})
	}
}

func TestRetainedReleaseKeepsObservedVisibility(t *testing.T) {
	_, p := retainedPrepared(t)
	s := retainedStoreForTest(t)
	s.publish = func(ctx context.Context, dir *fileaccess.Directory, name string, data []byte, stage string) (fileaccess.PublicationResult, error) {
		result, err := fileaccess.PublishPrivateNamed(ctx, dir, name, data, stage)
		if err == nil && result.Visible {
			return result, errors.New("private persistence canary")
		}
		return result, err
	}
	result, err := s.Release(t.Context(), p.Handle)
	if err == nil || !result.Released || result.LocalState != "partial" {
		t.Fatal("visible revocation misreported", result, err)
	}
	s.publish = fileaccess.PublishPrivateNamed
	if result, err := s.Cleanup(t.Context(), p.Handle, ""); err != nil || result.LocalState != "removed" {
		t.Fatal("post-visibility cleanup", result, err)
	}
}

func TestRetainedListWithholdsDamagedMetadata(t *testing.T) {
	opt, p := retainedPrepared(t)
	s := retainedStoreForTest(t)
	healthy, err := (&App{}).PrepareRetainedFile(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.path, retainedComponent(p.Handle))
	if err := os.WriteFile(filepath.Join(root, "control.json"), []byte("corrupt private source canary"), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := s.List(t.Context(), "")
	if err != nil || page.Count != 2 || len(page.Packages) != 2 {
		t.Fatal("damaged entry broke bounded list", err)
	}
	for _, entry := range page.Packages {
		if entry.Handle == p.Handle {
			if entry != (RetainedEntrySummary{Handle: p.Handle, State: "incomplete"}) {
				t.Fatal("untrusted metadata escaped", entry)
			}
		} else if entry.Handle != healthy.Handle || !entry.Available || entry.State != "ready" || entry.Digest != healthy.Digest {
			t.Fatal("damaged entry poisoned healthy neighbor", entry)
		}
	}
}
