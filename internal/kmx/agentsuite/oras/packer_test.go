package oras_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	oraspack "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"
)

func TestPackerProducesValidOCILayout(t *testing.T) {
	src, contentDescriptor := newContentSource(t)
	validator := &recordingValidator{delegate: oraspack.LayoutValidator{}}
	packer := oraspack.New(validator)
	layout := t.TempDir()
	dst, err := oci.New(layout)
	if err != nil {
		t.Fatal(err)
	}

	result, err := packer.Pack(context.Background(), src, dst, contentDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	if validator.calls != 1 || !validator.validatedCAS {
		t.Fatalf("validator calls = %d, validated CAS = %t", validator.calls, validator.validatedCAS)
	}
	if result.Descriptor.MediaType != ocispec.MediaTypeImageManifest ||
		result.Descriptor.Digest.Validate() != nil ||
		result.Descriptor.Size <= 0 {
		t.Fatalf("invalid manifest descriptor: %+v", result.Descriptor)
	}
	if result.Report == nil || result.Report.Name != "minimal" {
		t.Fatalf("pack report = %+v, want suite minimal", result.Report)
	}
	report, err := agentsuite.ValidatePath(layout)
	if err != nil {
		t.Fatalf("packed layout is invalid: %v", err)
	}
	if report.Name != "minimal" {
		t.Fatalf("suite name = %q, want minimal", report.Name)
	}
	indexBytes, err := os.ReadFile(filepath.Join(layout, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index ocispec.Index
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Manifests) != 1 || index.Manifests[0].Digest != result.Descriptor.Digest {
		t.Fatalf("index manifests = %+v, want packed root %s", index.Manifests, result.Descriptor.Digest)
	}
	if _, tagged := index.Manifests[0].Annotations[ocispec.AnnotationRefName]; tagged {
		t.Fatal("packing assigned a tag")
	}
}

func TestPackerProducesExpectedManifest(t *testing.T) {
	src, contentDescriptor := newContentSource(t)
	dst := memory.New()
	result, err := oraspack.New(nil).Pack(context.Background(), src, dst, contentDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := content.FetchAll(context.Background(), dst, result.Descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ArtifactType != agentsuite.MediaTypeArtifact || manifest.Subject != nil {
		t.Fatalf("unexpected AgentSuite manifest: %+v", manifest)
	}
	if manifest.Config.MediaType != agentsuite.MediaTypeEmptyConfig {
		t.Fatalf("config media type = %q, want %q", manifest.Config.MediaType, agentsuite.MediaTypeEmptyConfig)
	}
	if len(manifest.Layers) != 1 ||
		manifest.Layers[0].MediaType != contentDescriptor.MediaType ||
		manifest.Layers[0].Digest != contentDescriptor.Digest ||
		manifest.Layers[0].Size != contentDescriptor.Size {
		t.Fatalf("layers = %+v, want content descriptor %+v", manifest.Layers, contentDescriptor)
	}
	config, err := content.FetchAll(context.Background(), dst, manifest.Config)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(config, []byte("{}")) {
		t.Fatalf("config = %q, want {}", config)
	}
}

func TestPackerIsDeterministic(t *testing.T) {
	src, contentDescriptor := newContentSource(t)
	packer := oraspack.New(nil)
	var descriptors []ocispec.Descriptor
	for range 2 {
		result, err := packer.Pack(context.Background(), src, memory.New(), contentDescriptor)
		if err != nil {
			t.Fatal(err)
		}
		descriptors = append(descriptors, result.Descriptor)
	}
	if descriptors[0].MediaType != descriptors[1].MediaType ||
		descriptors[0].Digest != descriptors[1].Digest ||
		descriptors[0].Size != descriptors[1].Size {
		t.Fatalf("pack descriptors differ: %+v != %+v", descriptors[0], descriptors[1])
	}
}

func TestPackerStopsWhenValidationFails(t *testing.T) {
	src, contentDescriptor := newContentSource(t)
	dst := &recordingStorage{}
	packer := oraspack.New(failingValidator{})
	if _, err := packer.Pack(context.Background(), src, dst, contentDescriptor); err == nil ||
		!errors.Is(err, errRejectedSuite) {
		t.Fatalf("Pack() error = %v, want validation failure", err)
	}
	if dst.pushes != 0 {
		t.Fatalf("destination received %d pushes after validation failure", dst.pushes)
	}
}

func TestPackerRejectsInvalidContentDescriptor(t *testing.T) {
	dst := &recordingStorage{}
	descriptor := content.NewDescriptorFromBytes("application/octet-stream", []byte("invalid"))
	if _, err := oraspack.New(nil).Pack(context.Background(), memory.New(), dst, descriptor); err == nil {
		t.Fatal("invalid content descriptor succeeded")
	}
	if dst.pushes != 0 {
		t.Fatalf("destination received %d pushes for invalid content", dst.pushes)
	}
}

func TestPackerRejectsNonSHA256ContentDescriptor(t *testing.T) {
	dst := &recordingStorage{}
	data := []byte("content")
	descriptor := ocispec.Descriptor{
		MediaType: agentsuite.MediaTypeContent,
		Digest:    godigest.NewDigestFromBytes(godigest.SHA512, data),
		Size:      int64(len(data)),
	}
	if _, err := oraspack.New(nil).Pack(context.Background(), memory.New(), dst, descriptor); err == nil {
		t.Fatal("non-SHA-256 content descriptor succeeded")
	}
	if dst.pushes != 0 {
		t.Fatalf("destination received %d pushes for invalid content", dst.pushes)
	}
}

func TestPackerRejectsMissingCASContent(t *testing.T) {
	dst := &recordingStorage{}
	descriptor := content.NewDescriptorFromBytes(agentsuite.MediaTypeContent, []byte("missing"))
	if _, err := oraspack.New(nil).Pack(context.Background(), memory.New(), dst, descriptor); err == nil {
		t.Fatal("missing source content succeeded")
	}
	if dst.pushes != 0 {
		t.Fatalf("destination received %d pushes for missing content", dst.pushes)
	}
}

func TestPackerRejectsNonCanonicalContentTimestamps(t *testing.T) {
	tests := []struct {
		name          string
		gzipTimestamp time.Time
		tarTimestamp  time.Time
		want          string
	}{
		{
			name:          "gzip",
			gzipTimestamp: time.Unix(1, 0).UTC(),
			tarTimestamp:  time.Unix(0, 0).UTC(),
			want:          "content gzip modification time must be unset",
		},
		{
			name:         "tar",
			tarTimestamp: time.Unix(1, 0).UTC(),
			want:         "tar modification time must be the Unix epoch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := buildContentLayerWithTimestamps(
				t,
				filepath.Join("..", "testdata", "minimal"),
				test.gzipTimestamp,
				test.tarTimestamp,
			)
			descriptor := content.NewDescriptorFromBytes(agentsuite.MediaTypeContent, data)
			src := memory.New()
			if err := src.Push(context.Background(), descriptor, bytes.NewReader(data)); err != nil {
				t.Fatal(err)
			}
			dst := &recordingStorage{}

			_, err := oraspack.New(nil).Pack(context.Background(), src, dst, descriptor)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Pack() error = %v, want %q", err, test.want)
			}
			if dst.pushes != 0 {
				t.Fatalf("destination received %d pushes for non-canonical content", dst.pushes)
			}
		})
	}
}

func TestORASStoreRejectsMismatchedContent(t *testing.T) {
	target := memory.New()
	descriptor := content.NewDescriptorFromBytes(agentsuite.MediaTypeContent, []byte("expected"))
	err := target.Push(context.Background(), descriptor, bytes.NewReader([]byte("different")))
	if err == nil {
		t.Fatal("mismatched content succeeded")
	}
}

func TestPackerPropagatesDestinationFailure(t *testing.T) {
	src, contentDescriptor := newContentSource(t)
	dst := &failingStorage{err: errDestinationRejected}
	_, err := oraspack.New(nil).Pack(context.Background(), src, dst, contentDescriptor)
	if !errors.Is(err, errDestinationRejected) {
		t.Fatalf("Pack() error = %v, want destination failure", err)
	}
}

func TestPackerAcceptsExistingCASContent(t *testing.T) {
	src, contentDescriptor := newContentSource(t)
	dst := &existingStorage{store: memory.New()}
	if _, err := oraspack.New(nil).Pack(context.Background(), src, dst, contentDescriptor); err != nil {
		t.Fatal(err)
	}
	if dst.pushes != 3 {
		t.Fatalf("pushes = %d, want 3", dst.pushes)
	}
}

func TestPackerRejectsCorruptExistingCASContent(t *testing.T) {
	src, contentDescriptor := newContentSource(t)
	dst := corruptExistingStorage{}
	if _, err := oraspack.New(nil).Pack(context.Background(), src, dst, contentDescriptor); err == nil {
		t.Fatal("corrupt existing content succeeded")
	}
}

func newContentSource(t *testing.T) (*memory.Store, ocispec.Descriptor) {
	t.Helper()
	data := buildContentLayer(t, filepath.Join("..", "testdata", "minimal"))
	descriptor := content.NewDescriptorFromBytes(agentsuite.MediaTypeContent, data)
	store := memory.New()
	if err := store.Push(context.Background(), descriptor, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	return store, descriptor
}

func buildContentLayer(t *testing.T, root string) []byte {
	return buildContentLayerWithTimestamps(
		t,
		root,
		time.Time{},
		time.Unix(0, 0).UTC(),
	)
}

func buildContentLayerWithTimestamps(
	t *testing.T,
	root string,
	gzipTimestamp time.Time,
	tarTimestamp time.Time,
) []byte {
	t.Helper()
	var names []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(name))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)

	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	gzipWriter.ModTime = gzipTimestamp
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		header := &tar.Header{
			Name:    name,
			Mode:    0o644,
			ModTime: tarTimestamp,
			Format:  tar.FormatPAX,
		}
		if info.IsDir() {
			header.Name += "/"
			header.Typeflag = tar.TypeDir
			header.Mode = 0o755
		} else {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(data))
			if err := tarWriter.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if _, err := tarWriter.Write(data); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

type recordingValidator struct {
	delegate     agentsuite.Validator
	calls        int
	validatedCAS bool
}

func (v *recordingValidator) Validate(
	ctx context.Context,
	src agentsuite.ReadOnlyStorage,
	root ocispec.Descriptor,
) (*agentsuite.Report, error) {
	v.calls++
	exists, err := src.Exists(ctx, root)
	if err != nil {
		return nil, err
	}
	v.validatedCAS = exists
	return v.delegate.Validate(ctx, src, root)
}

var errRejectedSuite = errors.New("rejected suite")

type failingValidator struct{}

func (failingValidator) Validate(
	context.Context,
	agentsuite.ReadOnlyStorage,
	ocispec.Descriptor,
) (*agentsuite.Report, error) {
	return nil, errRejectedSuite
}

type recordingStorage struct {
	pushes int
}

func (t *recordingStorage) Push(context.Context, ocispec.Descriptor, io.Reader) error {
	t.pushes++
	return nil
}

func (*recordingStorage) Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
	return nil, errors.New("unexpected fetch")
}

func (*recordingStorage) Exists(context.Context, ocispec.Descriptor) (bool, error) {
	return false, nil
}

var errDestinationRejected = errors.New("destination rejected content")

type failingStorage struct {
	err error
}

func (p *failingStorage) Push(context.Context, ocispec.Descriptor, io.Reader) error {
	return p.err
}

func (*failingStorage) Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
	return nil, errors.New("unexpected fetch")
}

func (*failingStorage) Exists(context.Context, ocispec.Descriptor) (bool, error) {
	return false, nil
}

type existingStorage struct {
	store  *memory.Store
	pushes int
}

func (p *existingStorage) Push(
	ctx context.Context,
	descriptor ocispec.Descriptor,
	reader io.Reader,
) error {
	p.pushes++
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if err := p.store.Push(ctx, descriptor, bytes.NewReader(data)); err != nil &&
		!errors.Is(err, errdef.ErrAlreadyExists) {
		return err
	}
	return agentsuite.ErrAlreadyExists
}

func (p *existingStorage) Fetch(
	ctx context.Context,
	descriptor ocispec.Descriptor,
) (io.ReadCloser, error) {
	return p.store.Fetch(ctx, descriptor)
}

func (p *existingStorage) Exists(
	ctx context.Context,
	descriptor ocispec.Descriptor,
) (bool, error) {
	return p.store.Exists(ctx, descriptor)
}

type corruptExistingStorage struct{}

func (corruptExistingStorage) Push(context.Context, ocispec.Descriptor, io.Reader) error {
	return agentsuite.ErrAlreadyExists
}

func (corruptExistingStorage) Fetch(
	context.Context,
	ocispec.Descriptor,
) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("corrupt"))), nil
}

func (corruptExistingStorage) Exists(context.Context, ocispec.Descriptor) (bool, error) {
	return true, nil
}
