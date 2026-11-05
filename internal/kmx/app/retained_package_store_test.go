//go:build !windows

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func retainedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", home)
	return home
}
func retainedOptions(t *testing.T) RetainedPrepareOptions {
	t.Helper()
	a, _, r := retainedFileFixture(t, 1)
	return RetainedPrepareOptions{File: a, ExpectedRevision: r}
}
func retainedFamily(t *testing.T) string {
	t.Helper()
	root, err := config.ExistingStateDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "retained-packages", "v1")
}

func TestRetainedPreparePrefixedPath(t *testing.T) {
	home := retainedHome(t)
	opt := retainedOptions(t)
	snapshot, err := loadRetainedFile(t.Context(), opt.File, "", opt.ExpectedRevision)
	if err != nil {
		t.Fatal(err)
	}
	handle := strings.Repeat("a1b2", 8)
	if retainedComponent(handle) != "pkg-"+handle || len(handle) != 32 {
		t.Fatal("handle or directory protocol changed")
	}
	parent := filepath.Join(home, "retained-packages", "v1", retainedComponent(handle), "build")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	compiled, err := workspace.CompileMapped(t.Context(), snapshot.input, parent)
	if err != nil {
		t.Fatal("prefixed store path refused", err)
	}
	if err := compiled.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedPrepareLifecycle(t *testing.T) {
	retainedHome(t)
	opt := retainedOptions(t)
	app := &App{}
	first, err := app.PrepareRetainedFile(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.PrepareRetainedFile(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Handle) != 32 || first.Handle == second.Handle || first.Digest != second.Digest || first.Fingerprint != second.Fingerprint || first.SourceRevision != opt.ExpectedRevision || first.PackageProfile != "orka-source/v1" || first.MemberID != "greeter" || first.Runtime != "orka" || first.Model != "qwen-3.5-2b" || first.Descriptor.Digest.String() != first.Digest {
		t.Fatal("wrong retained identity")
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), opt.File) || strings.Contains(string(encoded), "Say hello") || strings.Contains(string(encoded), "layout") {
		t.Fatal("summary exposed managed access")
	}
	dir := filepath.Join(retainedFamily(t), "pkg-"+first.Handle)
	for _, name := range []string{"control.json", "use.lock", "ready.json", "layout/index.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(name, err)
		}
	}
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Errorf("nonprivate retained object %s", filepath.Base(path))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedPrepareExpectedRevisionBeforeState(t *testing.T) {
	home := retainedHome(t)
	opt := retainedOptions(t)
	opt.ExpectedRevision = "wrong"
	if _, err := (&App{}).PrepareRetainedFile(t.Context(), opt); !errors.Is(err, errRetainedSourceChanged) {
		t.Fatalf("stale = %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("state created on stale source", err)
	}
}

func TestRetainedPrepareCapacity(t *testing.T) {
	retainedHome(t)
	s, err := openRetainedStore(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	family := retainedFamily(t)
	for i := 0; i < 256; i++ {
		if err := os.Mkdir(filepath.Join(family, fmt.Sprintf("pkg-%032x", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&App{}).PrepareRetainedFile(t.Context(), retainedOptions(t)); !errors.Is(err, errRetainedCapacity) {
		t.Fatalf("capacity = %v", err)
	}
	entries, _ := os.ReadDir(family)
	if len(entries) != 257 {
		t.Fatalf("allocated beyond capacity: %d", len(entries))
	}
	if retainedConstructionBudget > retainedEntryBytes || retainedConstructionBudget < 31<<20 || retainedEntryBytes != 32<<20 {
		t.Fatal("construction budget not conservative")
	}
}

func TestRetainedPrepareCrashBoundaries(t *testing.T) {
	for _, point := range []string{"owned", "copied", "closed", "flushed", "ready"} {
		t.Run(point, func(t *testing.T) {
			retainedHome(t)
			opt := retainedOptions(t)
			sentinel := errors.New("private fault canary")
			hit := false
			_, err := prepareRetainedFile(t.Context(), opt, func(at string) error {
				if at != point {
					return nil
				}
				hit = true
				family := retainedFamily(t)
				entries, err := os.ReadDir(family)
				if err != nil {
					return err
				}
				for _, e := range entries {
					if !e.IsDir() {
						continue
					}
					dir := filepath.Join(family, e.Name())
					if _, err := os.Stat(filepath.Join(dir, "control.json")); err != nil {
						t.Fatal("bulk before ownership", err)
					}
					if point == "owned" {
						children, _ := os.ReadDir(dir)
						if len(children) != 2 {
							t.Fatal("bulk before durable control")
						}
					}
				}
				return sentinel
			})
			if !hit {
				t.Fatal("fault point never reached", err)
			}
			if err == nil || strings.Contains(err.Error(), "private fault canary") {
				t.Fatalf("fault acknowledged or leaked = %v", err)
			}
			entries, _ := os.ReadDir(retainedFamily(t))
			found := 0
			for _, e := range entries {
				if e.IsDir() {
					found++
					_, readyErr := os.Stat(filepath.Join(retainedFamily(t), e.Name(), "ready.json"))
					if point != "ready" && !os.IsNotExist(readyErr) {
						t.Fatal("completion before required checks")
					}
				}
			}
			if found != 1 {
				t.Fatal("owned remnant disappeared")
			}
		})
	}
}

func TestRetainedPrepareCancellationAndForeignState(t *testing.T) {
	retainedHome(t)
	opt := retainedOptions(t)
	if _, err := prepareRetainedFile(t.Context(), opt, func(at string) error {
		if at == "owned" {
			if err := os.WriteFile(opt.File, []byte(retainedAgentFixture+"# edit\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}); !errors.Is(err, errRetainedSourceChanged) {
		t.Fatalf("source race = %v", err)
	}
	family := retainedFamily(t)
	if err := os.WriteFile(filepath.Join(family, "foreign"), []byte("witness"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).PrepareRetainedFile(t.Context(), retainedOptions(t)); err == nil {
		t.Fatal("adopted unknown state")
	}
	b, err := os.ReadFile(filepath.Join(family, "foreign"))
	if err != nil || string(b) != "witness" {
		t.Fatal("foreign state changed")
	}
}
