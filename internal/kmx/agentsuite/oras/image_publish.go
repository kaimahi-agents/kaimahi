package oras

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	suitecore "github.com/agent-primitives/suite/core"
	kmxsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry"
)

// PushImage publishes an already-built OCI archive unchanged. Image and suite
// publishing are distinct: this never repackages an image as suite content.
func PushImage(ctx context.Context, archive, reference string, platform suitecore.Platform, plainHTTP, force bool) (string, error) {
	file, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	inspected, err := kmxsuite.InspectImageArchive(ctx, file, info.Size(), platform, false, false)
	if err != nil {
		return "", err
	}
	source, err := oci.NewFromTar(ctx, archive)
	if err != nil {
		return "", err
	}
	graph, err := imagePublicationGraph(file, info.Size(), source, inspected.IndexDigest)
	if err != nil {
		return "", err
	}
	parsed, err := registry.ParseReference(reference)
	if err != nil {
		return "", err
	}
	if err := parsed.ValidateReferenceAsTag(); err != nil {
		return "", err
	}
	target, tag, err := newRegistryRepositoryLimit(reference, plainHTTP, nil, 4<<20)
	if err != nil {
		return "", err
	}
	// Keep generic registry/provenance readers on their smaller default budget.
	// Reuse tag policy without suite provenance or deployment admission. The
	// inspector has already selected and verified the single runnable platform.
	if _, err := pushRegistryGraph(ctx, graph, target, tag, force, PushOptions{}, PushResult{Reference: reference, Descriptor: graph.root}); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s@%s", parsed.Registry, parsed.Repository, inspected.Digest), nil
}

// imageGraph exposes a flat layout's index.json as a CAS root so its attestation
// siblings stay reachable. Wrapped layouts retain the inspected inner index.
type imageGraph struct {
	kmxsuite.ReadOnlyStorage
	root ocispec.Descriptor
	data []byte
}

func (g imageGraph) Fetch(ctx context.Context, d ocispec.Descriptor) (io.ReadCloser, error) {
	if g.data != nil && d.Digest == g.root.Digest {
		return io.NopCloser(bytes.NewReader(g.data)), ctx.Err()
	}
	return g.ReadOnlyStorage.Fetch(ctx, d)
}

func (g imageGraph) Exists(ctx context.Context, d ocispec.Descriptor) (bool, error) {
	if g.data != nil && d.Digest == g.root.Digest {
		return true, ctx.Err()
	}
	return g.ReadOnlyStorage.Exists(ctx, d)
}

func imagePublicationGraph(file io.ReaderAt, size int64, source kmxsuite.ReadOnlyStorage, expected string) (imageGraph, error) {
	tr := tar.NewReader(io.NewSectionReader(file, 0, size))
	for {
		h, err := tr.Next()
		if err != nil {
			return imageGraph{}, fmt.Errorf("read verified image index: %w", err)
		}
		if h.Name != "index.json" {
			continue
		}
		if h.Size > 64<<20 {
			return imageGraph{}, errors.New("image index exceeds metadata limit")
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return imageGraph{}, err
		}
		root := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, data)
		if root.Digest.String() == expected {
			return imageGraph{ReadOnlyStorage: source, root: root, data: data}, nil
		}
		var index ocispec.Index
		if err := json.Unmarshal(data, &index); err != nil || len(index.Manifests) != 1 || index.Manifests[0].Digest.String() != expected || index.Manifests[0].MediaType != ocispec.MediaTypeImageIndex {
			return imageGraph{}, errors.New("image index changed after inspection")
		}
		// The inner index is already a digest-addressed blob in the archive.
		return imageGraph{ReadOnlyStorage: source, root: index.Manifests[0]}, nil
	}
}
