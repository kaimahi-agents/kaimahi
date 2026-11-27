package oras

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/localartifact"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	oraslib "oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
)

var errGeneratedArtifact = errors.New("generated AgentSuite artifact is unavailable")

// GeneratedArtifact owns one untagged, invocation-local OCI graph. It grants no
// filesystem layout, registry, reference, publication, or transfer authority.
type GeneratedArtifact struct {
	mu         sync.Mutex
	descriptor ocispec.Descriptor
	blobs      map[string][]byte
	closed     bool
}

// PackGeneratedFiles packages an already validated generated file map using the
// same deterministic content metadata as loose-directory publication.
func PackGeneratedFiles(ctx context.Context, files map[string][]byte) (*GeneratedArtifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	frozen, names, err := generatedFileSet(files)
	if err != nil {
		return nil, errGeneratedArtifact
	}
	var archive bytes.Buffer
	if err := writeGeneratedArchive(ctx, frozen, names, &archive); err != nil {
		return nil, err
	}
	layerBytes := archive.Bytes()
	layer := content.NewDescriptorFromBytes(suitecore.MediaTypeContent, layerBytes)
	store := memory.New()
	if err := store.Push(ctx, layer, bytes.NewReader(layerBytes)); err != nil {
		return nil, errGeneratedArtifact
	}
	configBytes := []byte("{}")
	config := content.NewDescriptorFromBytes(suitecore.MediaTypeEmptyConfig, configBytes)
	if err := store.Push(ctx, config, bytes.NewReader(configBytes)); err != nil {
		return nil, errGeneratedArtifact
	}
	manifest, err := oraslib.PackManifest(ctx, store, oraslib.PackManifestVersion1_1, suitecore.MediaTypeArtifact, oraslib.PackManifestOptions{
		Layers:           []ocispec.Descriptor{layer},
		ConfigDescriptor: &config,
		ManifestAnnotations: map[string]string{
			ocispec.AnnotationCreated: time.Unix(0, 0).UTC().Format(time.RFC3339),
		},
	})
	if err != nil {
		return nil, errGeneratedArtifact
	}
	manifestBytes, err := content.FetchAll(ctx, store, manifest)
	if err != nil {
		return nil, errGeneratedArtifact
	}
	decoded, err := localartifact.Files(ctx, layerBytes)
	if err != nil || !equalGeneratedFiles(frozen, decoded) {
		return nil, errGeneratedArtifact
	}
	return &GeneratedArtifact{
		descriptor: cloneGeneratedDescriptor(manifest),
		blobs: map[string][]byte{
			manifest.Digest.String(): bytes.Clone(manifestBytes),
			config.Digest.String():   bytes.Clone(configBytes),
			layer.Digest.String():    bytes.Clone(layerBytes),
		},
	}, nil
}

func generatedFileSet(files map[string][]byte) (map[string][]byte, []string, error) {
	if len(files) == 0 || len(files) > MaxArtifactFiles {
		return nil, nil, errGeneratedArtifact
	}
	frozen := make(map[string][]byte, len(files))
	directories := map[string]bool{}
	total := 0
	for name, data := range files {
		if name == "" || !fs.ValidPath(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "../") || name == "." {
			return nil, nil, errGeneratedArtifact
		}
		total += len(data)
		if len(data) > 4<<20 || total > 8<<20 {
			return nil, nil, errGeneratedArtifact
		}
		frozen[name] = bytes.Clone(data)
		for directory := path.Dir(name); directory != "."; directory = path.Dir(directory) {
			directories[directory] = true
		}
	}
	names := make([]string, 0, len(directories)+len(frozen))
	for name := range directories {
		if _, conflict := frozen[name]; conflict {
			return nil, nil, errGeneratedArtifact
		}
		names = append(names, name)
	}
	for name := range frozen {
		names = append(names, name)
	}
	slices.Sort(names)
	return frozen, names, nil
}

func writeGeneratedArchive(ctx context.Context, files map[string][]byte, names []string, dst io.Writer) (err error) {
	gzipWriter := gzip.NewWriter(dst)
	gzipWriter.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	defer func() { err = errors.Join(err, tarWriter.Close(), gzipWriter.Close()) }()
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		header := contentArchiveHeader(name)
		data, regular := files[name]
		if regular {
			header.Typeflag = tar.TypeReg
			header.Mode = 0o644
			header.Size = int64(len(data))
		} else {
			header.Name += "/"
			header.Typeflag = tar.TypeDir
			header.Mode = 0o755
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if regular {
			if _, err := tarWriter.Write(data); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *GeneratedArtifact) Descriptor() ocispec.Descriptor {
	if a == nil {
		return ocispec.Descriptor{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ocispec.Descriptor{}
	}
	return cloneGeneratedDescriptor(a.descriptor)
}

func (a *GeneratedArtifact) Verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil {
		return errGeneratedArtifact
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.descriptor.Digest == "" || len(a.blobs) != 3 {
		return errGeneratedArtifact
	}
	for digest, raw := range a.blobs {
		if godigest.FromBytes(raw).String() != digest {
			return errGeneratedArtifact
		}
	}
	manifest := a.blobs[a.descriptor.Digest.String()]
	if int64(len(manifest)) != a.descriptor.Size {
		return errGeneratedArtifact
	}
	return ctx.Err()
}

func (a *GeneratedArtifact) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	a.descriptor = ocispec.Descriptor{}
	for digest := range a.blobs {
		delete(a.blobs, digest)
	}
	return nil
}

func equalGeneratedFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for name, raw := range a {
		if !bytes.Equal(raw, b[name]) {
			return false
		}
	}
	return true
}

func cloneGeneratedDescriptor(descriptor ocispec.Descriptor) ocispec.Descriptor {
	descriptor.Data = bytes.Clone(descriptor.Data)
	descriptor.URLs = slices.Clone(descriptor.URLs)
	if descriptor.Annotations != nil {
		annotations := make(map[string]string, len(descriptor.Annotations))
		for key, value := range descriptor.Annotations {
			annotations[key] = value
		}
		descriptor.Annotations = annotations
	}
	return descriptor
}
