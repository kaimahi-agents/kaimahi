// Package oras provides an ORAS-backed AgentSuite packer.
package oras

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	oraslib "oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"
)

// Packer packages AgentSuite content using ORAS storage and manifest helpers.
type Packer struct {
	validator agentsuite.Validator
}

var _ agentsuite.Packer = (*Packer)(nil)

// New returns an ORAS-backed AgentSuite packer.
func New(validator agentsuite.Validator) *Packer {
	if validator == nil {
		validator = LayoutValidator{}
	}
	return &Packer{validator: validator}
}

func (p *Packer) Pack(
	ctx context.Context,
	src agentsuite.ReadOnlyStorage,
	dst agentsuite.Storage,
	contentDescriptor ocispec.Descriptor,
) (agentsuite.PackResult, error) {
	if src == nil {
		return agentsuite.PackResult{}, errors.New("AgentSuite pack source is required")
	}
	if dst == nil {
		return agentsuite.PackResult{}, errors.New("AgentSuite pack destination is required")
	}
	if p == nil || p.validator == nil {
		return agentsuite.PackResult{}, errors.New("AgentSuite pack validator is required")
	}
	if err := ctx.Err(); err != nil {
		return agentsuite.PackResult{}, err
	}
	if err := validateContentDescriptor(contentDescriptor); err != nil {
		return agentsuite.PackResult{}, fmt.Errorf("validate AgentSuite content descriptor: %w", err)
	}

	stageRoot, err := os.MkdirTemp("", "agentsuite-layout-*")
	if err != nil {
		return agentsuite.PackResult{}, err
	}
	defer os.RemoveAll(stageRoot)
	stage, err := oci.New(stageRoot)
	if err != nil {
		return agentsuite.PackResult{}, fmt.Errorf("create staged AgentSuite OCI layout: %w", err)
	}

	if err := pushStagedContent(ctx, src, stage, contentDescriptor); err != nil {
		return agentsuite.PackResult{}, fmt.Errorf("stage AgentSuite content: %w", err)
	}
	configBytes := []byte("{}")
	configDescriptor := content.NewDescriptorFromBytes(agentsuite.MediaTypeEmptyConfig, configBytes)
	if err := stage.Push(ctx, configDescriptor, bytes.NewReader(configBytes)); err != nil {
		return agentsuite.PackResult{}, fmt.Errorf("stage AgentSuite config: %w", err)
	}
	manifestDescriptor, err := oraslib.PackManifest(
		ctx,
		stage,
		oraslib.PackManifestVersion1_1,
		agentsuite.MediaTypeArtifact,
		oraslib.PackManifestOptions{
			Layers:           []ocispec.Descriptor{contentDescriptor},
			ConfigDescriptor: &configDescriptor,
			ManifestAnnotations: map[string]string{
				ocispec.AnnotationCreated: time.Unix(0, 0).UTC().Format(time.RFC3339),
			},
		},
	)
	if err != nil {
		return agentsuite.PackResult{}, fmt.Errorf("stage AgentSuite manifest: %w", err)
	}

	report, err := p.validator.Validate(ctx, stage, manifestDescriptor)
	if err != nil {
		return agentsuite.PackResult{}, fmt.Errorf("validate packed AgentSuite: %w", err)
	}
	if report == nil {
		return agentsuite.PackResult{}, errors.New("validate packed AgentSuite: validator returned no report")
	}

	for _, descriptor := range []ocispec.Descriptor{configDescriptor, contentDescriptor, manifestDescriptor} {
		if err := pushStagedContent(ctx, stage, dst, descriptor); err != nil {
			return agentsuite.PackResult{}, err
		}
	}
	return agentsuite.PackResult{Descriptor: manifestDescriptor, Report: report}, nil
}

func validateContentDescriptor(descriptor ocispec.Descriptor) error {
	if descriptor.MediaType != agentsuite.MediaTypeContent ||
		descriptor.Digest.Validate() != nil ||
		descriptor.Digest.Algorithm() != godigest.SHA256 ||
		descriptor.Size < 0 {
		return errors.New("media type, digest, or size is invalid")
	}
	if len(descriptor.URLs) != 0 {
		return errors.New("urls are not allowed")
	}
	if descriptor.Data != nil &&
		(int64(len(descriptor.Data)) != descriptor.Size ||
			godigest.FromBytes(descriptor.Data) != descriptor.Digest) {
		return errors.New("embedded data does not match descriptor")
	}
	return nil
}

func pushStagedContent(
	ctx context.Context,
	src agentsuite.Fetcher,
	dst agentsuite.Storage,
	descriptor ocispec.Descriptor,
) error {
	reader, err := src.Fetch(ctx, descriptor)
	if err != nil {
		return fmt.Errorf("fetch staged %s: %w", descriptor.MediaType, err)
	}
	pushErr := dst.Push(ctx, descriptor, reader)
	closeErr := reader.Close()
	if pushErr != nil &&
		!errors.Is(pushErr, agentsuite.ErrAlreadyExists) &&
		!errors.Is(pushErr, errdef.ErrAlreadyExists) {
		return fmt.Errorf("push AgentSuite %s: %w", descriptor.MediaType, pushErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close staged %s: %w", descriptor.MediaType, closeErr)
	}
	if pushErr != nil {
		if err := verifyExistingContent(ctx, dst, descriptor); err != nil {
			return fmt.Errorf("verify existing AgentSuite %s: %w", descriptor.MediaType, err)
		}
	}
	return ctx.Err()
}

func verifyExistingContent(
	ctx context.Context,
	src agentsuite.Fetcher,
	descriptor ocispec.Descriptor,
) error {
	reader, err := src.Fetch(ctx, descriptor)
	if err != nil {
		return err
	}
	verifier := descriptor.Digest.Verifier()
	written, copyErr := io.Copy(verifier, io.LimitReader(reader, descriptor.Size+1))
	closeErr := reader.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != descriptor.Size || !verifier.Verified() {
		return errors.New("existing content does not match descriptor")
	}
	return ctx.Err()
}
