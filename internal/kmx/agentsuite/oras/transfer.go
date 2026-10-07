// Package oras binds AgentSuite transfer contracts to ORAS targets.
package oras

import (
	"context"
	"errors"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	oraslib "oras.land/oras-go/v2"
)

// Puller pulls artifacts from one bound ORAS target.
type Puller struct {
	source oraslib.ReadOnlyTarget
}

var _ agentsuite.ArtifactPuller = (*Puller)(nil)

// NewPuller binds pulls to source. Authentication and transport configuration
// remain properties of the supplied target.
func NewPuller(source oraslib.ReadOnlyTarget) *Puller {
	return &Puller{source: source}
}

// Pull resolves reference, copies its complete graph, and returns the resolved
// immutable root descriptor.
func (p *Puller) Pull(
	ctx context.Context,
	reference string,
	dst agentsuite.Storage,
) (ocispec.Descriptor, error) {
	if p == nil || p.source == nil {
		return ocispec.Descriptor{}, errors.New("AgentSuite pull source is required")
	}
	if dst == nil {
		return ocispec.Descriptor{}, errors.New("AgentSuite pull destination is required")
	}
	if reference == "" {
		return ocispec.Descriptor{}, errors.New("AgentSuite pull reference is required")
	}

	root, err := p.source.Resolve(ctx, reference)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("resolve AgentSuite %q: %w", reference, err)
	}
	if err := oraslib.CopyGraph(ctx, p.source, dst, root, oraslib.DefaultCopyGraphOptions); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("pull AgentSuite %q: %w", reference, err)
	}
	return root, nil
}

// Pusher pushes artifacts to one bound ORAS target.
type Pusher struct {
	destination oraslib.Target
}

var _ agentsuite.ArtifactPusher = (*Pusher)(nil)

// NewPusher binds pushes to destination. Authentication and transport
// configuration remain properties of the supplied target.
func NewPusher(destination oraslib.Target) *Pusher {
	return &Pusher{destination: destination}
}

// Push copies the complete graph rooted at root, adds or updates reference
// without removing unrelated references, and returns the unchanged immutable
// root descriptor.
func (p *Pusher) Push(
	ctx context.Context,
	src agentsuite.ReadOnlyStorage,
	root ocispec.Descriptor,
	reference string,
) (ocispec.Descriptor, error) {
	if p == nil || p.destination == nil {
		return ocispec.Descriptor{}, errors.New("AgentSuite push destination is required")
	}
	if src == nil {
		return ocispec.Descriptor{}, errors.New("AgentSuite push source is required")
	}
	if reference == "" {
		return ocispec.Descriptor{}, errors.New("AgentSuite push reference is required")
	}
	if err := oraslib.CopyGraph(ctx, src, p.destination, root, oraslib.DefaultCopyGraphOptions); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("push AgentSuite %q: %w", reference, err)
	}
	if err := p.destination.Tag(ctx, root, reference); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("tag AgentSuite %q: %w", reference, err)
	}
	return root, nil
}
