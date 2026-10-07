package oras

import (
	"context"
	"fmt"
	"os"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	oraslib "oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
)

// LayoutValidator validates an AgentSuite artifact from content-addressed
// storage using the existing OCI layout validator.
type LayoutValidator struct{}

var _ agentsuite.Validator = LayoutValidator{}

func (LayoutValidator) Validate(
	ctx context.Context,
	src agentsuite.ReadOnlyStorage,
	root ocispec.Descriptor,
) (*agentsuite.Report, error) {
	if src == nil {
		return nil, fmt.Errorf("AgentSuite validation source is required")
	}
	stageRoot, err := os.MkdirTemp("", "agentsuite-validation-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stageRoot)
	stage, err := oci.New(stageRoot)
	if err != nil {
		return nil, fmt.Errorf("create AgentSuite validation layout: %w", err)
	}
	if err := oraslib.CopyGraph(ctx, src, stage, root, oraslib.DefaultCopyGraphOptions); err != nil {
		return nil, fmt.Errorf("materialize AgentSuite validation graph: %w", err)
	}
	if err := stage.Tag(ctx, root, "validation"); err != nil {
		return nil, fmt.Errorf("index AgentSuite validation root: %w", err)
	}
	return agentsuite.ValidatePath(stageRoot)
}
