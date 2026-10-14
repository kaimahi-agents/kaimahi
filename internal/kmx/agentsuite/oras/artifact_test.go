package oras

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
)

func TestReadArtifactKeepsExactDigestAndOwnedBytes(t *testing.T) {
	source := filepath.Join("..", "..", "..", "..", "agentsuite", "testdata", "minimal")
	layout := filepath.Join(t.TempDir(), "layout")
	p, err := Push(t.Context(), source, layout, "minimal:v1")
	if err != nil {
		t.Fatal(err)
	}
	a, err := ReadArtifactLayout(t.Context(), layout, p.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	if a.Descriptor().Digest != p.Descriptor.Digest {
		t.Fatal("artifact repacked")
	}
	files := a.Files()
	raw, err := os.ReadFile(filepath.Join(source, "agentsuite.json"))
	if err != nil || !bytes.Equal(files["agentsuite.json"], raw) {
		t.Fatal("changed payload", err)
	}
	files["agentsuite.json"][0] = 'X'
	delete(files, "agentsuite.json")
	if !bytes.Equal(a.Files()["agentsuite.json"], raw) {
		t.Fatal("caller changed retained artifact")
	}
	if _, err := ReadArtifactLayout(t.Context(), layout, "minimal:v1"); !errors.Is(err, ErrArtifact) {
		t.Fatalf("mutable reference: %v", err)
	}
	missing := "sha256:" + strings.Repeat("a", 64)
	if _, err := ReadArtifactLayout(t.Context(), layout, missing); !errors.Is(err, ErrArtifact) {
		t.Fatalf("missing digest: %v", err)
	}
}

// Real registry HTTP supplies original bytes; the reader must not publish anything.
func TestReadArtifactRegistryIsPullOnly(t *testing.T) {
	server, reg := newTestRegistryServer("", "")
	defer server.Close()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	ref := strings.TrimPrefix(server.URL, "http://") + "/team/suite:v1"
	p, err := PushRegistry(t.Context(), filepath.Join("..", "..", "..", "..", "agentsuite", "testdata", "minimal"), ref, true, false, PushOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reg.mu.RLock()
	uploads, puts := reg.uploads, reg.manifestPuts
	reg.mu.RUnlock()
	pinned := strings.Split(ref, ":v1")[0] + "@" + p.Descriptor.Digest.String()
	a, err := ReadArtifactRegistry(t.Context(), pinned, true)
	if err != nil || a.Descriptor().Digest != p.Descriptor.Digest {
		t.Fatal("registry identity changed", err)
	}
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	if uploads != reg.uploads || puts != reg.manifestPuts {
		t.Fatal("retrieval published content")
	}
}

type artifactFetchSpy struct {
	data  map[string][]byte
	calls []ocispec.Descriptor
}

func (s *artifactFetchSpy) Fetch(_ context.Context, d ocispec.Descriptor) (io.ReadCloser, error) {
	s.calls = append(s.calls, d)
	return io.NopCloser(bytes.NewReader(s.data[d.Digest.String()])), nil
}

func TestReadArtifactRefusesBudgetBeforeChildTransfer(t *testing.T) {
	for _, tc := range []string{"root", "layer", "config", "fanout", "urls", "digest", "overlong body", "cancel"} {
		t.Run(tc, func(t *testing.T) {
			config := content.NewDescriptorFromBytes(spec.MediaTypeEmptyConfig, []byte("{}"))
			layer := content.NewDescriptorFromBytes(spec.MediaTypeContent, []byte("not a gzip"))
			switch tc {
			case "layer":
				layer.Size = 64 << 20
			case "config":
				config.Size = 3
			case "urls":
				layer.URLs = []string{"https://example.invalid/content"}
			}
			m := ocispec.Manifest{MediaType: ocispec.MediaTypeImageManifest, ArtifactType: spec.MediaTypeArtifact, Config: config, Layers: []ocispec.Descriptor{layer}}
			m.SchemaVersion = 2
			if tc == "fanout" {
				m.Layers = append(m.Layers, layer)
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			root := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, raw)
			if tc == "root" {
				root.Size = 64 << 20
			}
			if tc == "digest" {
				raw[0] = 'X'
			}
			if tc == "overlong body" {
				raw = append(raw, ' ')
			}
			src := &artifactFetchSpy{data: map[string][]byte{root.Digest.String(): raw}}
			ctx := t.Context()
			if tc == "cancel" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			_, err = readArtifact(ctx, src, root, root.Digest.String())
			want := ErrArtifact
			if tc == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("wrong refusal: %v", err)
			}
			wantCalls := 1
			if tc == "root" || tc == "cancel" {
				wantCalls = 0
			}
			if len(src.calls) != wantCalls {
				t.Fatalf("fetched rejected graph children: %v", src.calls)
			}
		})
	}
}

func artifactArchive(t *testing.T, headers []*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := io.CopyN(tw, strings.NewReader(strings.Repeat("x", int(h.Size))), h.Size); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestArtifactArchiveBoundsAndClosedTypes(t *testing.T) {
	regular := func(name string, size int64) *tar.Header {
		return &tar.Header{Name: name, Mode: 0644, Typeflag: tar.TypeReg, Size: size}
	}
	for _, tc := range []string{"file", "total", "files", "directories", "link", "hardlink", "fifo", "traversal", "duplicate", "pax", "gnu metadata", "raw budget", "truncated", "trailing", "concatenated"} {
		t.Run(tc, func(t *testing.T) {
			hs := []*tar.Header{regular("agentsuite.json", 2)}
			switch tc {
			case "file":
				hs[0].Size = (1 << 20) + 1
			case "total":
				hs = nil
				for i := range 9 {
					hs = append(hs, regular(strings.Repeat("a", i+1), 1<<20))
				}
			case "files":
				hs = nil
				for i := range 136 {
					hs = append(hs, regular(strings.Repeat("a", i+1), 1))
				}
			case "directories":
				hs = nil
				for i := range 257 {
					hs = append(hs, &tar.Header{Name: strings.Repeat("a", i+1), Mode: 0755, Typeflag: tar.TypeDir})
				}
			case "link", "hardlink":
				kind := byte(tar.TypeSymlink)
				if tc == "hardlink" {
					kind = tar.TypeLink
				}
				hs = []*tar.Header{{Name: "link", Linkname: "agentsuite.json", Typeflag: kind}}
			case "fifo":
				hs = []*tar.Header{{Name: "pipe", Typeflag: tar.TypeFifo}}
			case "traversal":
				hs[0].Name = "../escape"
			case "duplicate":
				hs = append(hs, regular("agentsuite.json", 2))
			case "pax":
				hs[0].PAXRecords = map[string]string{"comment": strings.Repeat("x", 512<<10)}
			case "gnu metadata":
				hs[0].Name = strings.Repeat("x", 2000)
				hs[0].Format = tar.FormatGNU
			}
			raw := artifactArchive(t, hs)
			switch tc {
			case "raw budget":
				var b bytes.Buffer
				gz := gzip.NewWriter(&b)
				if _, err := gz.Write(bytes.Repeat([]byte{0}, 11<<20)); err != nil {
					t.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					t.Fatal(err)
				}
				raw = b.Bytes()
			case "truncated":
				raw = raw[:len(raw)-5]
			case "trailing":
				raw = append(raw, []byte("extra")...)
			case "concatenated":
				raw = append(raw, artifactArchive(t, []*tar.Header{regular("other", 1)})...)
			}
			if _, err := artifactFiles(t.Context(), raw); !errors.Is(err, ErrArtifact) {
				t.Fatalf("unsafe archive accepted: %v", err)
			}
		})
	}
}

func TestReadArtifactDoesNotMutateLayout(t *testing.T) {
	src := filepath.Join("..", "..", "..", "..", "agentsuite", "testdata", "minimal")
	layout := filepath.Join(t.TempDir(), "layout")
	p, err := Push(t.Context(), src, layout, "minimal:v1")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(layout, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The input must remain readable without owning its write permission.
	if err := os.Chmod(layout, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(layout, 0755) })
	if _, err := ReadArtifactLayout(t.Context(), layout, p.Descriptor.Digest.String()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(layout, "index.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source index mutated", err)
	}
	mem := memory.New()
	if _, err := readArtifact(t.Context(), mem, ocispec.Descriptor{}, p.Descriptor.Digest.String()); !errors.Is(err, ErrArtifact) {
		t.Fatalf("invalid descriptor: %v", err)
	}
}
