package oras

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry"
)

// ErrArtifact deliberately excludes payload-bearing transport/validation errors.
var ErrArtifact = errors.New("bounded exact-digest suite retrieval refused")

const (
	artifactManifestBytes   = 1 << 20
	artifactCompressedBytes = 9 << 20
	artifactTarBytes        = 10 << 20
	artifactFileBytes       = 1 << 20
	artifactContentBytes    = 8 << 20
	artifactEntriesLimit    = 256
)

// MaxArtifactFiles fits 4 shared files, 4 files per member (including its eval
// index), and 128 total cases. One member needs 136 files; 16 need at most 196.
// Consumers still validate complete membership and per-suite case limits.
const MaxArtifactFiles = 4 + 4*16 + 128

// Artifact retains verified original packaged bytes, not source or a re-archive.
// The bounds cover native inventories; execution support is consumer-owned.
type Artifact struct {
	descriptor ocispec.Descriptor
	files      map[string][]byte
}

func (a *Artifact) Descriptor() ocispec.Descriptor { return a.descriptor }

// Files gives each consumer its own bytes; a review cannot mutate retained input.
func (a *Artifact) Files() map[string][]byte {
	files := make(map[string][]byte, len(a.files))
	for name, data := range a.files {
		files[name] = bytes.Clone(data)
	}
	return files
}

// ReadArtifactLayout selects by SHA256 digest, with no source-layout writes.
func ReadArtifactLayout(ctx context.Context, layout, digest string) (a *Artifact, err error) {
	defer func() { err = artifactError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !artifactDigest(digest) {
		return nil, ErrArtifact
	}
	root, err := os.OpenRoot(layout)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	source := artifactLayoutFS{root}
	layoutBytes, err := source.read(ctx, "oci-layout")
	if err != nil {
		return nil, err
	}
	var layoutRecord ocispec.ImageLayout
	if spec.Unmarshal(layoutBytes, &layoutRecord) != nil || layoutRecord.Version != ocispec.ImageLayoutVersion {
		return nil, ErrArtifact
	}
	indexBytes, err := source.read(ctx, "index.json")
	if err != nil {
		return nil, err
	}
	var index ocispec.Index
	if spec.Unmarshal(indexBytes, &index) != nil || index.SchemaVersion != 2 || (index.MediaType != "" && index.MediaType != ocispec.MediaTypeImageIndex) || len(index.Manifests) > 1000 {
		return nil, ErrArtifact
	}
	// ORAS read-only-store initialization indexes EVERY graph. Select the
	// descriptor ourselves so unrelated layout references cannot force reads.
	var selected ocispec.Descriptor
	for _, d := range index.Manifests {
		if d.Digest.String() != digest {
			continue
		}
		if selected.Digest != "" && (!content.Equal(selected, d) || selected.ArtifactType != d.ArtifactType) {
			return nil, ErrArtifact
		}
		selected = d
	}
	return readArtifact(ctx, source, selected, digest)
}

// ReadArtifactRegistry uses existing registry credentials/transport for pull-only
// operations, without publication, tags, referrers or external descriptor URLs.
func ReadArtifactRegistry(ctx context.Context, reference string, plainHTTP bool) (a *Artifact, err error) {
	defer func() { err = artifactError(ctx, err) }()
	parsed, err := registry.ParseReference(reference)
	if err != nil || parsed.ValidateReferenceAsDigest() != nil || !strings.Contains(reference, "@sha256:") || !artifactDigest(parsed.Reference) {
		return nil, ErrArtifact
	}
	repository, ref, err := newRegistryRepository(reference, plainHTTP, nil)
	if err != nil {
		return nil, err
	}
	repository.MaxMetadataBytes = artifactManifestBytes
	d, err := repository.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	return readArtifact(ctx, repository, d, parsed.Reference)
}

func artifactDigest(d string) bool {
	parsed := godigest.Digest(d)
	return parsed.Validate() == nil && parsed.Algorithm() == godigest.SHA256
}

func artifactError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return errors.Join(ErrArtifact, ctx.Err())
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, cause) {
			return errors.Join(ErrArtifact, cause)
		}
	}
	return ErrArtifact
}

func artifactDescriptor(d ocispec.Descriptor, media string, limit int64) bool {
	return d.MediaType == media && artifactDigest(d.Digest.String()) && d.Size >= 0 && d.Size <= limit && d.URLs == nil &&
		(d.Data == nil || int64(len(d.Data)) == d.Size && godigest.FromBytes(d.Data) == d.Digest)
}

func artifactRead(ctx context.Context, src agentsuite.Fetcher, d ocispec.Descriptor) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := src.Fetch(ctx, d)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: r}, d.Size+1))
	if err := errors.Join(readErr, r.Close()); err != nil {
		return nil, err
	}
	if int64(len(raw)) != d.Size || godigest.FromBytes(raw) != d.Digest || (d.Data != nil && !bytes.Equal(raw, d.Data)) {
		return nil, ErrArtifact
	}
	return raw, ctx.Err()
}

func readArtifact(ctx context.Context, src agentsuite.Fetcher, root ocispec.Descriptor, expected string) (a *Artifact, err error) {
	defer func() { err = artifactError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if src == nil || !artifactDigest(expected) || root.Digest.String() != expected || (root.ArtifactType != "" && root.ArtifactType != spec.MediaTypeArtifact) || !artifactDescriptor(root, ocispec.MediaTypeImageManifest, artifactManifestBytes) {
		return nil, ErrArtifact
	}
	manifestBytes, err := artifactRead(ctx, src, root)
	if err != nil {
		return nil, err
	}
	var m ocispec.Manifest
	if spec.Unmarshal(manifestBytes, &m) != nil || m.SchemaVersion != 2 || (m.MediaType != "" && m.MediaType != ocispec.MediaTypeImageManifest) || m.ArtifactType != spec.MediaTypeArtifact || m.Subject != nil || len(m.Layers) != 1 {
		return nil, ErrArtifact
	}
	// Validate the entire small graph BEFORE fetching any children.
	if !artifactDescriptor(m.Config, spec.MediaTypeEmptyConfig, 2) || m.Config.Size != 2 || m.Config.Digest != godigest.FromString("{}") || !artifactDescriptor(m.Layers[0], spec.MediaTypeContent, artifactCompressedBytes) {
		return nil, ErrArtifact
	}
	config, err := artifactRead(ctx, src, m.Config)
	if err != nil {
		return nil, err
	}
	layer, err := artifactRead(ctx, src, m.Layers[0])
	if err != nil {
		return nil, err
	}
	files, err := artifactFiles(ctx, layer)
	if err != nil {
		return nil, err
	}
	// Public validation receives exactly the original three objects, after the
	// stricter native ingestion budget. No graph traversal, extraction or repack.
	stage, err := os.MkdirTemp("", "kmx-artifact-*")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	store, err := oci.NewWithContext(ctx, stage)
	if err != nil {
		return nil, err
	}
	root = ocispec.Descriptor{MediaType: root.MediaType, Digest: root.Digest, Size: root.Size, ArtifactType: m.ArtifactType}
	for _, item := range []struct {
		d   ocispec.Descriptor
		raw []byte
	}{{root, manifestBytes}, {m.Config, config}, {m.Layers[0], layer}} {
		if err := store.Push(ctx, item.d, bytes.NewReader(item.raw)); err != nil {
			return nil, err
		}
	}
	if err := store.Tag(ctx, root, "artifact"); err != nil {
		return nil, err
	}
	if _, err := spec.ValidatePath(stage); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Artifact{descriptor: root, files: files}, nil
}

func artifactFiles(ctx context.Context, compressed []byte) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(compressed) > artifactCompressedBytes {
		return nil, ErrArtifact
	}
	input := bytes.NewReader(compressed)
	gz, err := gzip.NewReader(input)
	if err != nil {
		return nil, ErrArtifact
	}
	gz.Multistream(false)
	raw, readErr := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: gz}, artifactTarBytes+1))
	if err := errors.Join(readErr, gz.Close()); err != nil {
		return nil, artifactError(ctx, err)
	}
	if len(raw) > artifactTarBytes || input.Len() != 0 {
		return nil, ErrArtifact
	}
	stream := bytes.NewReader(raw)
	reader := tar.NewReader(stream)
	files := map[string][]byte{}
	seen := map[string]bool{}
	total := int64(0)
	for entries := 0; ; entries++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Next consumes PAX/GNU metadata internally, so inspect each physical
		// header before allowing it to hide records from type/count checks.
		offset := (len(raw) - stream.Len() + 511) / 512 * 512
		if offset+512 <= len(raw) {
			kind := raw[offset+156]
			// A NUL type byte is the legacy regular-file encoding.
			if kind != tar.TypeReg && kind != 0 && kind != tar.TypeDir {
				return nil, ErrArtifact
			}
		}
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || entries >= artifactEntriesLimit {
			return nil, ErrArtifact
		}
		name, err := extractionPath(h)
		if err != nil || len(h.Name) > 1024 || seen[name] || len(h.PAXRecords) != 0 {
			return nil, ErrArtifact
		}
		seen[name] = true
		switch h.Typeflag {
		case tar.TypeDir:
			if h.Size != 0 {
				return nil, ErrArtifact
			}
		case tar.TypeReg:
			if len(files) >= MaxArtifactFiles || h.Size < 0 || h.Size > artifactFileBytes || h.Size > artifactContentBytes-total {
				return nil, ErrArtifact
			}
			data, err := io.ReadAll(reader)
			if err != nil || int64(len(data)) != h.Size {
				return nil, ErrArtifact
			}
			files[strings.ReplaceAll(name, string(os.PathSeparator), "/")] = data
			total += h.Size
		default:
			return nil, ErrArtifact
		}
	}
	// tar EOF may hide another archive/record. Only zero padding is permitted.
	rest := raw[len(raw)-stream.Len():]
	if len(bytes.Trim(rest, "\x00")) != 0 {
		return nil, ErrArtifact
	}
	return files, nil
}

// Bound layout/index opens as well as blobs and reject symlink components;
// os.Root also confines concurrent path replacement.
type artifactLayoutFS struct{ root *os.Root }

func (s artifactLayoutFS) read(ctx context.Context, name string) ([]byte, error) {
	f, err := s.Open(name)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(&contextReader{ctx: ctx, reader: f})
	return data, errors.Join(readErr, f.Close())
}

func (s artifactLayoutFS) Fetch(ctx context.Context, d ocispec.Descriptor) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !artifactDigest(d.Digest.String()) {
		return nil, ErrArtifact
	}
	return s.Open("blobs/sha256/" + d.Digest.Encoded())
}

func (s artifactLayoutFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, ErrArtifact
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := s.root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrArtifact
		}
	}
	f, err := s.root.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	limit := int64(artifactCompressedBytes)
	if name == "index.json" {
		limit = artifactManifestBytes
	}
	if name == "oci-layout" {
		limit = 1024
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		f.Close()
		return nil, ErrArtifact
	}
	return &artifactLayoutFile{File: f, remaining: limit + 1}, nil
}

type artifactLayoutFile struct {
	*os.File
	remaining int64
}

func (f *artifactLayoutFile) Read(p []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, ErrArtifact
	}
	if int64(len(p)) > f.remaining {
		p = p[:f.remaining]
	}
	n, err := f.File.Read(p)
	f.remaining -= int64(n)
	return n, err
}
