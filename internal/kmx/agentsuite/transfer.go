package agentsuite

import (
	"context"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ArtifactPuller pulls one referenced artifact graph into content storage.
//
// Registry location, authentication, retries, and transport policy are bound
// by the implementation rather than represented in this contract.
type ArtifactPuller interface {
	Pull(context.Context, string, Storage) (ocispec.Descriptor, error)
}

// ArtifactPusher pushes one descriptor-rooted artifact graph and assigns its
// destination reference without removing unrelated destination references.
//
// Registry location, authentication, retries, and transport policy are bound
// by the implementation rather than represented in this contract.
type ArtifactPusher interface {
	Push(
		context.Context,
		ReadOnlyStorage,
		ocispec.Descriptor,
		string,
	) (ocispec.Descriptor, error)
}
