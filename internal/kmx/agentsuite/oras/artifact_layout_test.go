package oras

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestReadArtifactSelectsOnlyRequestedLayoutGraph(t *testing.T) {
	layout := filepath.Join(t.TempDir(), "layout")
	p, err := Push(t.Context(), filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), layout, "suite:v1")
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

func TestReadArtifactDuplicateRootMustBeIdentityEquivalent(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint("conflict-", conflict), func(t *testing.T) {
			layout := filepath.Join(t.TempDir(), "layout")
			p, err := Push(t.Context(), filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), layout, "suite:v1")
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
			duplicate := index.Manifests[0]
			duplicate.Annotations = map[string]string{"org.opencontainers.image.ref.name": "suite:other"}
			if conflict {
				duplicate.Data = []byte("conflicting embedded bytes")
			}
			index.Manifests = append(index.Manifests, duplicate)
			raw, err = json.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(indexPath, raw, 0o644); err != nil {
				t.Fatal(err)
			}
			_, err = ReadArtifactLayoutSelection(t.Context(), layout, "")
			if conflict && !errors.Is(err, ErrArtifact) {
				t.Fatalf("conflicting duplicate root accepted: %v", err)
			}
			if !conflict && err != nil {
				t.Fatalf("equivalent tagged root refused: %v", err)
			}
			if _, err := ReadArtifactLayout(t.Context(), layout, p.Descriptor.Digest.String()); conflict && !errors.Is(err, ErrArtifact) {
				t.Fatalf("explicit selection ignored conflicting duplicate: %v", err)
			}
		})
	}
}

func TestReadArtifactPreservesUnsupportedPlatformFieldsForValidation(t *testing.T) {
	layout := filepath.Join(t.TempDir(), "layout")
	if _, err := Push(t.Context(), filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), layout, "suite:v1"); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(layout, "index.json")
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]any
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	index["manifests"].([]any)[0].(map[string]any)["platform"] = map[string]any{
		"os": "linux", "architecture": "amd64", "os.version": "canary", "os.features": []any{"canary"},
	}
	raw, err = json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadArtifactLayoutSelection(t.Context(), layout, ""); !errors.Is(err, ErrArtifact) {
		t.Fatalf("unsupported platform fields were normalized away: %v", err)
	}
}

func TestReadArtifactRefusesLayoutFIFOWithoutBlocking(t *testing.T) {
	if layout := os.Getenv("KMX_TEST_ARTIFACT_FIFO_LAYOUT"); layout != "" {
		if _, err := ReadArtifactLayoutSelection(t.Context(), layout, ""); !errors.Is(err, ErrArtifact) {
			t.Fatalf("FIFO accepted: %v", err)
		}
		return
	}
	tool, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo unavailable")
	}
	for _, relative := range []string{"oci-layout", "index.json", "blobs/sha256/" + strings.Repeat("a", 64)} {
		t.Run(strings.ReplaceAll(relative, "/", "-"), func(t *testing.T) {
			layout := filepath.Join(t.TempDir(), "layout")
			p, err := Push(t.Context(), filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), layout, "suite:v1")
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(layout, filepath.FromSlash(relative))
			if strings.HasSuffix(relative, strings.Repeat("a", 64)) {
				name = filepath.Join(layout, "blobs", "sha256", p.Descriptor.Digest.Encoded())
			}
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(tool, name).CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReadArtifactRefusesLayoutFIFOWithoutBlocking$")
			command.Env = append(os.Environ(), "KMX_TEST_ARTIFACT_FIFO_LAYOUT="+layout)
			out, err := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal("layout reader blocked on FIFO")
			}
			if err != nil {
				t.Fatal(err, string(out))
			}
		})
	}
	for _, child := range []string{"config", "content"} {
		t.Run(child, func(t *testing.T) {
			layout := filepath.Join(t.TempDir(), "layout")
			p, err := Push(t.Context(), filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), layout, "suite:v1")
			if err != nil {
				t.Fatal(err)
			}
			manifestBytes, err := os.ReadFile(filepath.Join(layout, "blobs", "sha256", p.Descriptor.Digest.Encoded()))
			if err != nil {
				t.Fatal(err)
			}
			var manifest ocispec.Manifest
			if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
				t.Fatal(err)
			}
			descriptor := manifest.Config
			if child == "content" {
				descriptor = manifest.Layers[0]
			}
			name := filepath.Join(layout, "blobs", "sha256", descriptor.Digest.Encoded())
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(tool, name).CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReadArtifactRefusesLayoutFIFOWithoutBlocking$")
			command.Env = append(os.Environ(), "KMX_TEST_ARTIFACT_FIFO_LAYOUT="+layout)
			out, err := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal("layout reader blocked on FIFO")
			}
			if err != nil {
				t.Fatal(err, string(out))
			}
		})
	}
}

func TestReadArtifactRefusesLayoutSymlinkAndOversizedIndex(t *testing.T) {
	for _, tc := range []string{"index budget", "index symlink", "blob symlink"} {
		t.Run(tc, func(t *testing.T) {
			layout := filepath.Join(t.TempDir(), "layout")
			p, err := Push(t.Context(), filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), layout, "suite:v1")
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
