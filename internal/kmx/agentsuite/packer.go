package agentsuite

import (
	"context"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Validator validates a packed AgentSuite rooted in content-addressed storage.
type Validator interface {
	Validate(
		context.Context,
		ReadOnlyStorage,
		ocispec.Descriptor,
	) (*Report, error)
}

// PackResult identifies a packed AgentSuite and reports its validated contents.
type PackResult struct {
	Descriptor ocispec.Descriptor
	Report     *Report
}

// Packer packages AgentSuite content from one CAS into another.
//
// Pack stages and validates a complete OCI image layout before pushing any
// content to the destination. The destination must enforce the descriptors
// passed to Pusher. Packing does not assign a tag or other reference.
type Packer interface {
	Pack(
		context.Context,
		ReadOnlyStorage,
		Pusher,
		ocispec.Descriptor,
	) (PackResult, error)
}
