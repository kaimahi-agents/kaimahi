package agentsuite

import (
	"context"
	"errors"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ErrAlreadyExists reports that content is already present at its descriptor
// address. Callers must verify the existing bytes before treating it as
// successful idempotency.
var ErrAlreadyExists = errors.New("content already exists")

// These contracts intentionally contain no host-application, filesystem,
// ORAS, registry, cloud-provider, authentication, or UI concepts.
// Implementations supply those concerns without changing AgentSuite callers.

// Fetcher retrieves descriptor-verified content from content-addressed storage.
type Fetcher interface {
	Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error)
}

// Pusher writes descriptor-verified content to content-addressed storage.
type Pusher interface {
	Push(context.Context, ocispec.Descriptor, io.Reader) error
}

// ReadOnlyStorage is descriptor-addressed, read-only content storage.
type ReadOnlyStorage interface {
	Fetcher
	Exists(context.Context, ocispec.Descriptor) (bool, error)
}

// Storage is a readable and writable content-addressed store.
type Storage interface {
	ReadOnlyStorage
	Pusher
}

// Resolver resolves a reference to its root descriptor.
type Resolver interface {
	Resolve(context.Context, string) (ocispec.Descriptor, error)
}

// Tagger associates a reference with a root descriptor.
type Tagger interface {
	Tag(context.Context, ocispec.Descriptor, string) error
}

// ReadOnlyTarget is read-only content storage with reference resolution.
type ReadOnlyTarget interface {
	ReadOnlyStorage
	Resolver
}

// Target is content storage with reference resolution and tagging.
type Target interface {
	Storage
	Resolver
	Tagger
}
