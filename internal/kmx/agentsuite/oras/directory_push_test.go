package oras

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/content/oci"
)

func TestPushDirectoryIsIndependentOfFilesystemTimestamps(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "agentsuite.json", []byte(`{"name":"example"}`), 0o644)
	mustWrite(t, root, "bin/tool", []byte("#!/bin/sh\n"), 0o755)

	firstStore := memory.New()
	first := pushDirectoryForTest(t, root, firstStore)
	timestamp := time.Unix(2_000_000_000, 0)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, timestamp, timestamp)
	}); err != nil {
		t.Fatal(err)
	}
	secondStore := memory.New()
	second := pushDirectoryForTest(t, root, secondStore)
	if !content.Equal(first, second) {
		t.Fatalf("descriptors differ after touching source: %+v != %+v", first, second)
	}
	firstBytes, err := content.FetchAll(context.Background(), firstStore, first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := content.FetchAll(context.Background(), secondStore, second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatal("archive bytes differ after touching source")
	}
}

func TestPushRejectsNonDirectorySource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentsuite.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "layout")
	if _, err := Push(context.Background(), path, target, "agentsuites/example:v1"); err == nil {
		t.Fatal("non-directory source succeeded")
	}
}

func TestPushRejectsNestedTargetThroughSymlinks(t *testing.T) {
	for _, test := range []struct {
		name   string
		source func(t *testing.T, realSource, link string) string
		target func(t *testing.T, realSource, link string) string
	}{
		{
			name: "source",
			source: func(t *testing.T, realSource, link string) string {
				t.Helper()
				mustSymlink(t, realSource, link)
				return link
			},
			target: func(_ *testing.T, realSource, _ string) string {
				return filepath.Join(realSource, "layout")
			},
		},
		{
			name: "target parent",
			source: func(_ *testing.T, realSource, _ string) string {
				return realSource
			},
			target: func(t *testing.T, realSource, link string) string {
				t.Helper()
				mustSymlink(t, realSource, link)
				return filepath.Join(link, "layout")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			realSource := filepath.Join(root, "suite")
			copyDirectory(t, filepath.Join("..", "testdata", "minimal"), realSource)
			link := filepath.Join(root, "suite-link")
			source := test.source(t, realSource, link)
			target := test.target(t, realSource, link)
			if _, err := Push(context.Background(), source, target, "agentsuites/minimal:v1"); err == nil {
				t.Fatal("push to symlinked nested target succeeded")
			}
			if _, err := os.Stat(filepath.Join(realSource, "layout")); !os.IsNotExist(err) {
				t.Fatalf("nested target exists after refusal: %v", err)
			}
		})
	}
}

func TestPushAddsReferencesToExistingLayout(t *testing.T) {
	source := filepath.Join("..", "testdata", "minimal")
	target := filepath.Join(t.TempDir(), "layout")
	references := []string{"agentsuites/alpha:v1", "agentsuites/beta:v1"}
	var first ocispec.Descriptor
	for i, reference := range references {
		result, err := Push(context.Background(), source, target, reference)
		if err != nil {
			t.Fatalf("push %s: %v", reference, err)
		}
		if result.Updated != (i > 0) {
			t.Fatalf("push %s updated=%t, want %t", reference, result.Updated, i > 0)
		}
		if i == 0 {
			first = result.Descriptor
		} else if !content.Equal(first, result.Descriptor) {
			t.Fatalf("same directory produced different roots: %+v != %+v", first, result.Descriptor)
		}
	}
	layout, err := oci.New(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range references {
		resolved, err := layout.Resolve(context.Background(), reference)
		if err != nil {
			t.Fatalf("resolve %s: %v", reference, err)
		}
		if !content.Equal(first, resolved) {
			t.Fatalf("resolved %s to %+v, want %+v", reference, resolved, first)
		}
		report, err := (LayoutValidator{}).Validate(context.Background(), layout, resolved)
		if err != nil {
			t.Fatalf("validate %s: %v", reference, err)
		}
		if report.Name != "minimal" {
			t.Fatalf("suite at %s = %q, want minimal", reference, report.Name)
		}
	}
	indexBytes, err := os.ReadFile(filepath.Join(target, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index ocispec.Index
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Manifests) != len(references) {
		t.Fatalf("layout has %d references, want %d", len(index.Manifests), len(references))
	}
}

func TestPushFailureLeavesNoNewTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "layout")
	if _, err := Push(context.Background(), t.TempDir(), target, "agentsuites/invalid:v1"); err == nil {
		t.Fatal("invalid AgentSuite pushed successfully")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("failed push left target behind: %v", err)
	}
}

func TestPushRefusesArbitraryExistingDirectory(t *testing.T) {
	source := filepath.Join("..", "testdata", "minimal")
	target := t.TempDir()
	keep := filepath.Join(target, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(context.Background(), source, target, "agentsuites/minimal:v1"); err == nil {
		t.Fatal("push to arbitrary directory succeeded")
	}
	data, err := os.ReadFile(keep)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing directory changed: data=%q err=%v", data, err)
	}
}

func pushDirectoryForTest(t *testing.T, root string, store *memory.Store) ocispec.Descriptor {
	t.Helper()
	descriptor, err := pushDirectory(context.Background(), root, store)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func mustWrite(t *testing.T, root, name string, data []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func copyDirectory(t *testing.T, source, destination string) {
	t.Helper()
	if err := filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
}
