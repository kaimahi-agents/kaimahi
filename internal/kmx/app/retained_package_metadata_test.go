//go:build !windows

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

func metadataControl(t *testing.T, s *retainedStore, h string) retainedControl {
	t.Helper()
	dir, err := fileaccess.OpenPrivateDirectory(t.Context(), filepath.Join(s.path, retainedComponent(h)))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	var c retainedControl
	if err := retainedRead(t.Context(), dir, "control.json", &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRetainedMetadataNamedStageLinks(t *testing.T) {
	for _, name := range []string{"control.json", "ready.json", "released.json"} {
		t.Run(name, func(t *testing.T) {
			retainedHome(t)
			opt := retainedOptions(t)
			p, err := (&App{}).PrepareRetainedFile(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			s, err := openRetainedStore(t.Context(), false)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			root := filepath.Join(s.path, retainedComponent(p.Handle))
			d, err := fileaccess.OpenPrivateDirectory(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if name == "released.json" {
				if err := retainedPublish(t.Context(), d, name, metadataControl(t, s, p.Handle)); err != nil {
					t.Fatal(err)
				}
			}
			stage := name[:len(name)-5] + ".stage"
			if err := os.Link(filepath.Join(root, name), filepath.Join(root, stage)); err != nil {
				t.Fatal(err)
			}
			var value any = &retainedControl{}
			if name == "ready.json" {
				value = &retainedReady{}
			}
			if err := retainedRead(t.Context(), d, name, value); err != nil {
				t.Fatal("owned final/stage link rejected", err)
			}
			if err := retainedRead(t.Context(), d, stage, value); err != nil {
				t.Fatal("owned stage/final link rejected", err)
			}
			if err := os.Link(filepath.Join(root, name), filepath.Join(t.TempDir(), "foreign")); err != nil {
				t.Fatal(err)
			}
			if err := retainedRead(t.Context(), d, name, value); err == nil {
				t.Fatal("unaccounted external hardlink admitted")
			}
		})
	}
}

func TestRetainedMetadataObservedVisibility(t *testing.T) {
	retainedHome(t)
	opt := retainedOptions(t)
	p, err := (&App{}).PrepareRetainedFile(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openRetainedStore(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir, err := fileaccess.OpenPrivateDirectory(t.Context(), filepath.Join(s.path, retainedComponent(p.Handle)))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	s.publish = func(ctx context.Context, d *fileaccess.Directory, name string, data []byte, stage string) (fileaccess.PublicationResult, error) {
		r, err := fileaccess.PublishPrivateNamed(ctx, d, name, data, stage)
		if err == nil {
			return r, errRetainedUnavailable
		}
		return r, err
	}
	result, err := s.publishMetadata(t.Context(), dir, "released.json", metadataControl(t, s, p.Handle))
	if err == nil || !result.Visible || !result.VisibilityKnown {
		t.Fatal("positive visibility lost on persistence error", result, err)
	}
}

func TestRetainedInitializationRecovery(t *testing.T) {
	for _, kind := range []string{"control.json", "control.stage", "use.lock", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			retainedHome(t)
			opt := retainedOptions(t)
			p, err := (&App{}).PrepareRetainedFile(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			s, err := openRetainedStore(t.Context(), false)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			c := metadataControl(t, s, p.Handle)
			root := filepath.Join(s.path, retainedComponent(p.Handle))
			entries, _ := os.ReadDir(root)
			for _, entry := range entries {
				if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
					t.Fatal(err)
				}
			}
			raw := []byte("witness")
			if kind == "control.json" || kind == "control.stage" {
				raw, err = retainedEncode(c)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, kind), raw, 0600); err != nil {
				t.Fatal(err)
			}
			removed, err := s.recoverInitialization(t.Context(), p.Handle)
			if kind == "use.lock" || kind == "foreign" {
				if removed {
					t.Fatal("unmarked nonempty directory adopted")
				}
				if _, err := os.Stat(filepath.Join(root, kind)); err != nil {
					t.Fatal("foreign witness removed", err)
				}
				return
			}
			if err != nil || !removed {
				t.Fatal("valid control-only recovery", removed, err)
			}
		})
	}
}

type controlFirstContext struct {
	context.Context
	cancel  context.CancelFunc
	observe func()
}

func (c *controlFirstContext) Err() error { c.observe(); return c.Context.Err() }
func TestRetainedAllocationControlBeforeUse(t *testing.T) {
	retainedHome(t)
	s, err := openRetainedStore(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := false
	ctx := &controlFirstContext{Context: base, cancel: cancel}
	ctx.observe = func() {
		entries, _ := os.ReadDir(s.path)
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(s.path, entry.Name())
			_, controlErr := os.Stat(filepath.Join(dir, "control.json"))
			_, lockErr := os.Stat(filepath.Join(dir, "use.lock"))
			if lockErr == nil && os.IsNotExist(controlErr) {
				t.Error("use.lock exists before control ownership")
				cancel()
			}
			if controlErr == nil && os.IsNotExist(lockErr) {
				observed = true
				cancel()
			}
		}
	}
	e, err := s.allocate(ctx)
	if e != nil {
		_ = e.Close()
	}
	if err == nil || !observed {
		t.Fatal("did not observe controlled initialization before use.lock", err)
	}
	entries, _ := os.ReadDir(s.path)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir, err := fileaccess.OpenPrivateDirectory(t.Context(), filepath.Join(s.path, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.control(t.Context(), dir, entry.Name()[4:])
		_ = dir.Close()
		if err != nil {
			t.Fatal("cancelled allocation lost valid ownership", err)
		}
	}
}
