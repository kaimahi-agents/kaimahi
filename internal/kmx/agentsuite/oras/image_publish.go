package oras

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry"
)

// PushImage publishes an already-built OCI archive unchanged. Image and suite
// publishing are distinct: this never repackages an image as suite content.
func PushImage(ctx context.Context, archive, reference string, platform agentsuite.Platform, plainHTTP bool) (string, error) {
	file, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if _, err := agentsuite.InspectImageArchive(ctx, file, info.Size(), platform, false, false); err != nil {
		return "", err
	}
	source, err := oci.NewFromTar(ctx, archive)
	if err != nil {
		return "", err
	}
	var roots []ocispec.Descriptor
	if err := source.Tags(ctx, "", func(tags []string) error {
		for _, tag := range tags {
			d, err := source.Resolve(ctx, tag)
			if err != nil {
				return err
			}
			roots = append(roots, d)
		}
		return nil
	}); err != nil {
		return "", err
	}
	if len(roots) != 1 {
		return "", errors.New("built archive must contain exactly one named image")
	}
	selected, err := ResolveImage(ctx, source, roots[0].Digest.String(), platform)
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
	target, tag, err := newRegistryRepository(reference, plainHTTP, nil)
	if err != nil {
		return "", err
	}
	// Publish the root graph, including attestation siblings, while returning the
	// runnable platform digest for deployment. Publishing only selected drops evidence.
	if _, err := NewPusher(target).Push(ctx, source, roots[0], tag); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s@%s", parsed.Registry, parsed.Repository, selected.Descriptor.Digest), nil
}
