package oras

import (
	"context"
	"fmt"
	"os"

	suitecore "github.com/agent-primitives/suite/core"
	suiteflow "github.com/agent-primitives/suite/experimental/flow"
	suiteoci "github.com/agent-primitives/suite/oci"
	kmxsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	oraslib "oras.land/oras-go/v2"
)

// LayoutValidator validates an AgentSuite artifact from content-addressed
// storage using the existing OCI layout validator.
type LayoutValidator struct{}

var _ kmxsuite.Validator = LayoutValidator{}

func (LayoutValidator) Validate(
	ctx context.Context,
	src kmxsuite.ReadOnlyStorage,
	root ocispec.Descriptor,
) (*suitecore.Report, error) {
	return validateLayout(ctx, src, root, false)
}

type flowV1Alpha1LayoutValidator struct{}

func (flowV1Alpha1LayoutValidator) Validate(ctx context.Context, src kmxsuite.ReadOnlyStorage, root ocispec.Descriptor) (*suitecore.Report, error) {
	return validateLayout(ctx, src, root, true)
}

func validateLayout(ctx context.Context, src kmxsuite.ReadOnlyStorage, root ocispec.Descriptor, flowMode bool) (*suitecore.Report, error) {
	if src == nil {
		return nil, fmt.Errorf("AgentSuite validation source is required")
	}
	stageRoot, err := os.MkdirTemp("", "agentsuite-validation-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stageRoot)
	stage, err := newOCIStore(stageRoot)
	if err != nil {
		return nil, fmt.Errorf("create AgentSuite validation layout: %w", err)
	}
	if err := oraslib.CopyGraph(ctx, src, stage, root, oraslib.DefaultCopyGraphOptions); err != nil {
		return nil, fmt.Errorf("materialize AgentSuite validation graph: %w", err)
	}
	if err := stage.Tag(ctx, root, "validation"); err != nil {
		return nil, fmt.Errorf("index AgentSuite validation root: %w", err)
	}
	if flowMode {
		graph, err := suiteflow.ValidatePathFlowV1Alpha1(stageRoot)
		if err != nil {
			return nil, err
		}
		report := graph.Report()
		return &report, nil
	}
	return suiteoci.ValidatePath(stageRoot)
}
