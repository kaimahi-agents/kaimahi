package oras

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	agentsuite "github.com/agent-primitives/suite"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry"
)

// PushLayout copies a prepared suite's exact OCI graph without re-archiving its
// extracted content. Existing destination layouts retain unrelated references.
func PushLayout(ctx context.Context, source, destination, reference string, expected ocispec.Descriptor) (result PushResult, err error) {
	if reference == "" {
		return PushResult{}, errors.New("AgentSuite push reference is required")
	}
	store, descriptor, report, err := preparedLayout(source, expected)
	if err != nil {
		return PushResult{}, err
	}
	sourcePath, err := filepath.Abs(source)
	if err != nil {
		return PushResult{}, err
	}
	sourcePath, err = filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return PushResult{}, err
	}
	destinationPath, err := filepath.Abs(destination)
	if err != nil {
		return PushResult{}, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destinationPath))
	if err != nil {
		return PushResult{}, fmt.Errorf("resolve AgentSuite target parent: %w", err)
	}
	if pathWithin(sourcePath, filepath.Join(parent, filepath.Base(destinationPath))) {
		return PushResult{}, errors.New("AgentSuite target must be outside the prepared source layout")
	}
	release, err := lockLayoutPublication(ctx, destinationPath)
	if err != nil {
		return PushResult{}, err
	}
	defer finishLayoutPublication(release, &err)
	targetPath := destinationPath
	updated := false
	info, err := os.Lstat(destinationPath)
	if errors.Is(err, os.ErrNotExist) {
		targetPath, err = os.MkdirTemp(parent, ".agentsuite-publish-*")
		if err != nil {
			return PushResult{}, err
		}
		defer os.RemoveAll(targetPath)
	} else if err != nil {
		return PushResult{}, err
	} else if !info.IsDir() {
		return PushResult{}, errors.New("AgentSuite target must be a directory, not a symlink or file")
	} else {
		if err := requireOCILayout(destinationPath, "AgentSuite target"); err != nil {
			return PushResult{}, err
		}
		updated = true
	}
	target, err := oci.New(targetPath)
	if err != nil {
		return PushResult{}, err
	}
	if _, err := NewPusher(target).Push(ctx, store, descriptor, reference); err != nil {
		return PushResult{}, err
	}
	if !updated {
		if err := os.Rename(targetPath, destinationPath); err != nil {
			return PushResult{}, fmt.Errorf("publish AgentSuite target: %w", err)
		}
	}
	return PushResult{Path: destinationPath, Reference: reference, Updated: updated, Descriptor: descriptor, Report: report}, nil
}

// PushLayoutRegistry publishes an already prepared suite, preserving its manifest
// and content bytes. Tag replacement uses the existing non-atomic preflight policy.
func PushLayoutRegistry(ctx context.Context, source, reference string, plainHTTP, force bool, expected ocispec.Descriptor, options PushOptions) (PushResult, error) {
	parsed, err := registry.ParseReference(reference)
	if err != nil {
		return PushResult{}, fmt.Errorf("parse AgentSuite registry reference: %w", err)
	}
	if err := parsed.ValidateReferenceAsTag(); err != nil {
		return PushResult{}, fmt.Errorf("AgentSuite registry push requires a tag: %w", err)
	}
	store, descriptor, report, err := preparedLayout(source, expected)
	if err != nil {
		return PushResult{}, err
	}
	repository, tag, err := newRegistryRepository(reference, plainHTTP, nil)
	if err != nil {
		return PushResult{}, err
	}
	result := PushResult{Path: reference, Reference: reference, Descriptor: descriptor, Report: report}
	return pushRegistryGraph(ctx, store, repository, tag, force, options, result)
}

func preparedLayout(source string, expected ocispec.Descriptor) (*oci.Store, ocispec.Descriptor, *agentsuite.Report, error) {
	report, err := agentsuite.ValidatePath(source)
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, fmt.Errorf("validate prepared AgentSuite: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(source, "index.json"))
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, fmt.Errorf("prepared AgentSuite must be an OCI layout: %w", err)
	}
	var index ocispec.Index
	if err := json.Unmarshal(data, &index); err != nil || len(index.Manifests) != 1 {
		return nil, ocispec.Descriptor{}, nil, errors.New("prepared AgentSuite must contain exactly one manifest")
	}
	if !content.Equal(index.Manifests[0], expected) {
		return nil, ocispec.Descriptor{}, nil, errors.New("prepared AgentSuite descriptor differs from the selected suite")
	}
	store, err := oci.New(source)
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, err
	}
	return store, index.Manifests[0], report, nil
}
