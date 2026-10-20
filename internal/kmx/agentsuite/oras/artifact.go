package oras

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/localartifact"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry"
)

var (
	ErrArtifact          = localartifact.ErrArtifact
	ErrArtifactLimit     = localartifact.ErrArtifactLimit
	ErrArtifactAmbiguous = localartifact.ErrArtifactAmbiguous
	ErrArtifactNotFound  = localartifact.ErrArtifactNotFound
)

const MaxArtifactFiles = localartifact.MaxFiles

type Artifact = localartifact.Artifact

func ReadArtifactLayout(ctx context.Context, layout, digest string) (*Artifact, error) {
	if digest == "" {
		return nil, ErrArtifact
	}
	artifact, err := localartifact.ReadLayout(ctx, layout, digest)
	return artifact, registryArtifactError(ctx, err)
}

func ReadArtifactLayoutSelection(ctx context.Context, layout, digest string) (*Artifact, error) {
	artifact, err := localartifact.ReadLayout(ctx, layout, digest)
	return artifact, registryArtifactError(ctx, err)
}

func ReadArtifactArchiveSelection(ctx context.Context, source io.ReaderAt, size int64, digest string) (*Artifact, error) {
	artifact, err := localartifact.ReadArchive(ctx, source, size, digest)
	return artifact, registryArtifactError(ctx, err)
}

// Kept private for focused ingestion tests and internal compatibility.
func readArtifact(ctx context.Context, source localartifact.Fetcher, root ocispec.Descriptor, expected string) (*Artifact, error) {
	artifact, err := localartifact.Read(ctx, source, root, expected)
	return artifact, registryArtifactError(ctx, err)
}

func artifactFiles(ctx context.Context, compressed []byte) (map[string][]byte, error) {
	files, err := localartifact.Files(ctx, compressed)
	return files, registryArtifactError(ctx, err)
}

// ReadArtifactRegistry retains KMX's internal credential-aware exact-digest
// registry path. Public suite recipe loading never calls this adapter.
func ReadArtifactRegistry(ctx context.Context, reference string, plainHTTP bool) (*Artifact, error) {
	parsed, err := registry.ParseReference(reference)
	if err != nil || parsed.ValidateReferenceAsDigest() != nil || !strings.Contains(reference, "@sha256:") {
		return nil, ErrArtifact
	}
	repository, ref, err := newRegistryRepository(reference, plainHTTP, nil)
	if err != nil {
		return nil, ErrArtifact
	}
	repository.MaxMetadataBytes = 1 << 20
	descriptor, err := repository.Resolve(ctx, ref)
	if err != nil {
		return nil, registryArtifactError(ctx, err)
	}
	return localartifact.Read(ctx, repository, cloneRegistryDescriptor(descriptor), parsed.Reference)
}

func registryArtifactError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if cause := ctx.Err(); cause != nil {
		return errors.Join(ErrArtifact, cause)
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, ErrArtifactLimit, ErrArtifactAmbiguous, ErrArtifactNotFound} {
		if errors.Is(err, cause) {
			return errors.Join(ErrArtifact, cause)
		}
	}
	return ErrArtifact
}

func cloneRegistryDescriptor(descriptor ocispec.Descriptor) ocispec.Descriptor {
	descriptor.Data = append([]byte(nil), descriptor.Data...)
	descriptor.URLs = append([]string(nil), descriptor.URLs...)
	if descriptor.Platform != nil {
		platform := *descriptor.Platform
		platform.OSFeatures = append([]string(nil), descriptor.Platform.OSFeatures...)
		descriptor.Platform = &platform
	}
	if descriptor.Annotations != nil {
		annotations := make(map[string]string, len(descriptor.Annotations))
		for key, value := range descriptor.Annotations {
			annotations[key] = value
		}
		descriptor.Annotations = annotations
	}
	return descriptor
}
