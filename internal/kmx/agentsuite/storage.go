package agentsuite

import (
	"context"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// These contracts intentionally contain no KMX, filesystem, ORAS, registry,
// cloud-provider, authentication, or UI concepts. Implementations supply those
// concerns without changing AgentSuite callers.

// Fetcher retrieves descriptor-verified content from content-addressed storage.
type Fetcher interface {
	Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error)
}

// Pusher writes descriptor-verified content to content-addressed storage.
type Pusher interface {
	Push(context.Context, ocispec.Descriptor, io.Reader) error
}

// ReadOnlyStorage is the source-side CAS contract used while packing.
type ReadOnlyStorage interface {
	Fetcher
	Exists(context.Context, ocispec.Descriptor) (bool, error)
}
