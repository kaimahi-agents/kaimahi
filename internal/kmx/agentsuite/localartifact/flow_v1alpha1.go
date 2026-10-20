package localartifact

import (
	"context"
	"io"

	agentsuite "github.com/agent-primitives/suite"
)

// FlowV1Alpha1 returns the immutable validated graph, or nil for strict reads.
// Graph accessors return caller-owned copies of definitions and inventory.
func (a *Artifact) FlowV1Alpha1() *agentsuite.FlowV1Alpha1Graph {
	if a.graph == nil {
		return nil
	}
	graph := *a.graph
	return &graph
}

// ReadLayoutFlowV1Alpha1 explicitly selects the experimental flow contract.
// It validates the original selected graph without rewriting its OCI identity.
func ReadLayoutFlowV1Alpha1(ctx context.Context, layout, digest string) (*Artifact, error) {
	return readLayout(ctx, layout, digest, true)
}

// ReadArchiveFlowV1Alpha1 freezes an uncompressed OCI layout archive and
// validates its original selected graph under the experimental flow contract.
func ReadArchiveFlowV1Alpha1(ctx context.Context, source io.ReaderAt, size int64, digest string) (*Artifact, error) {
	return readArchive(ctx, source, size, digest, true)
}
