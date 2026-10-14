package oras

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestReadArtifactSelectsOnlyRequestedLayoutGraph(t *testing.T) {
	layout := filepath.Join(t.TempDir(), "layout")
	p, err := Push(t.Context(), filepath.Join("..", "..", "..", "..", "agentsuite", "testdata", "minimal"), layout, "suite:v1")
	if err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(layout, "index.json")
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var index ocispec.Index
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	// Unrelated graph is intentionally unavailable/oversized. Exact selection must
	// neither traverse it nor reinterpret another tag as a candidate.
	index.Manifests = append(index.Manifests, ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: godigest.Digest("sha256:" + strings.Repeat("a", 64)), Size: 1 << 30})
	raw, err = json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, raw, 0644); err != nil {
		t.Fatal(err)
	}
	a, err := ReadArtifactLayout(t.Context(), layout, p.Descriptor.Digest.String())
	if err != nil || a.Descriptor().Digest != p.Descriptor.Digest {
		t.Fatalf("unrelated graph affected exact selection: %v", err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("index changed", err)
	}
}

func TestReadArtifactRefusesLayoutSymlinkAndOversizedIndex(t *testing.T) {
	for _, tc := range []string{"index budget", "index symlink", "blob symlink"} {
		t.Run(tc, func(t *testing.T) {
			layout := filepath.Join(t.TempDir(), "layout")
			p, err := Push(t.Context(), filepath.Join("..", "..", "..", "..", "agentsuite", "testdata", "minimal"), layout, "suite:v1")
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(layout, "index.json")
			if tc == "blob symlink" {
				name = filepath.Join(layout, "blobs", "sha256", p.Descriptor.Digest.Encoded())
			}
			raw, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if tc == "index budget" {
				if err := os.WriteFile(name, bytes.Repeat([]byte(" "), 2<<20), 0644); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(t.TempDir(), "untrusted")
				if err := os.WriteFile(target, raw, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, name); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadArtifactLayout(t.Context(), layout, p.Descriptor.Digest.String()); !errors.Is(err, ErrArtifact) {
				t.Fatalf("unsafe source accepted: %v", err)
			}
		})
	}
}
