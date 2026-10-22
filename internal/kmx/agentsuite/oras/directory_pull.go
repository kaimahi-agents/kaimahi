package oras

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	agentsuite "github.com/agent-primitives/suite"
	kmxsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
)

// PullResult identifies an AgentSuite extracted from a referenced OCI target.
type PullResult struct {
	Path       string
	Reference  string
	Descriptor ocispec.Descriptor
	Report     *agentsuite.Report
	// Provenance is informational and never blocks a pull.
	Provenance []Provenance
	// ProvenanceChecked is false if referrers could not be listed.
	ProvenanceChecked bool
	Warnings          []string
}

// Pull resolves and validates an AgentSuite from an OCI image layout, then
// atomically extracts its content into a new directory.
func Pull(
	ctx context.Context,
	source string,
	reference string,
	output string,
) (PullResult, error) {
	if reference == "" {
		return PullResult{}, errors.New("AgentSuite pull reference is required")
	}
	sourcePath, err := filepath.Abs(source)
	if err != nil {
		return PullResult{}, err
	}
	outputPath, err := filepath.Abs(output)
	if err != nil {
		return PullResult{}, err
	}
	if pathWithin(sourcePath, outputPath) {
		return PullResult{}, errors.New("AgentSuite output must be outside the source OCI layout")
	}
	if err := requireOCILayout(sourcePath, "AgentSuite source"); err != nil {
		return PullResult{}, err
	}
	sourceStore, err := newOCIStore(sourcePath)
	if err != nil {
		return PullResult{}, fmt.Errorf("open AgentSuite OCI source: %w", err)
	}
	return pullToDirectory(ctx, sourceStore, reference, outputPath)
}

func pullToDirectory(
	ctx context.Context,
	source kmxsuite.ReadOnlyTarget,
	reference string,
	outputPath string,
) (PullResult, error) {
	if source == nil {
		return PullResult{}, errors.New("AgentSuite pull source is required")
	}
	if reference == "" {
		return PullResult{}, errors.New("AgentSuite pull reference is required")
	}
	if _, err := os.Lstat(outputPath); err == nil {
		return PullResult{}, fmt.Errorf("AgentSuite output already exists: %s", outputPath)
	} else if !os.IsNotExist(err) {
		return PullResult{}, fmt.Errorf("inspect AgentSuite output: %w", err)
	}
	outputParent := filepath.Dir(outputPath)
	parentInfo, err := os.Stat(outputParent)
	if err != nil {
		return PullResult{}, fmt.Errorf("stat AgentSuite output parent: %w", err)
	}
	if !parentInfo.IsDir() {
		return PullResult{}, fmt.Errorf("AgentSuite output parent %s is not a directory", outputParent)
	}

	pulledRoot, err := os.MkdirTemp("", "agentsuite-pulled-*")
	if err != nil {
		return PullResult{}, err
	}
	defer os.RemoveAll(pulledRoot)
	pulledStore, err := newOCIStore(pulledRoot)
	if err != nil {
		return PullResult{}, fmt.Errorf("create AgentSuite pull CAS: %w", err)
	}
	root, err := NewPuller(source).Pull(ctx, reference, pulledStore)
	if err != nil {
		return PullResult{}, err
	}
	report, err := (LayoutValidator{}).Validate(ctx, pulledStore, root)
	if err != nil {
		return PullResult{}, fmt.Errorf("validate pulled AgentSuite: %w", err)
	}
	layer, err := contentLayerDescriptor(ctx, pulledStore, root)
	if err != nil {
		return PullResult{}, err
	}
	provenance, warnings, listErr := provenanceFor(ctx, source, root)
	if listErr != nil {
		warnings = append(warnings, listErr.Error())
	}

	stageRoot, err := os.MkdirTemp(outputParent, "."+filepath.Base(outputPath)+"-*")
	if err != nil {
		return PullResult{}, fmt.Errorf("create staged AgentSuite output: %w", err)
	}
	publishOutput := true
	defer func() {
		if publishOutput {
			_ = os.RemoveAll(stageRoot)
		}
	}()
	layerReader, err := pulledStore.Fetch(ctx, layer)
	if err != nil {
		return PullResult{}, fmt.Errorf("fetch AgentSuite content layer: %w", err)
	}
	extractErr := extractContentLayer(ctx, layerReader, stageRoot)
	closeErr := layerReader.Close()
	if extractErr != nil {
		return PullResult{}, extractErr
	}
	if closeErr != nil {
		return PullResult{}, fmt.Errorf("close AgentSuite content layer: %w", closeErr)
	}
	if err := os.Chmod(stageRoot, 0o755); err != nil {
		return PullResult{}, fmt.Errorf("set AgentSuite output mode: %w", err)
	}
	if err := os.Rename(stageRoot, outputPath); err != nil {
		return PullResult{}, fmt.Errorf("publish extracted AgentSuite: %w", err)
	}
	publishOutput = false
	return PullResult{
		Path:              outputPath,
		Reference:         reference,
		Descriptor:        root,
		Report:            report,
		Provenance:        provenance,
		ProvenanceChecked: listErr == nil,
		Warnings:          warnings,
	}, nil
}

func contentLayerDescriptor(
	ctx context.Context,
	src kmxsuite.Fetcher,
	root ocispec.Descriptor,
) (ocispec.Descriptor, error) {
	manifestBytes, err := content.FetchAll(ctx, src, root)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("fetch AgentSuite manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("decode AgentSuite manifest: %w", err)
	}
	if len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != agentsuite.MediaTypeContent {
		return ocispec.Descriptor{}, errors.New("AgentSuite manifest must contain exactly one content layer")
	}
	return manifest.Layers[0], nil
}

type extractedDirectory struct {
	name string
	mode os.FileMode
}

func extractionPath(header *tar.Header) (string, error) {
	name := header.Name
	if header.Typeflag == tar.TypeDir {
		name = strings.TrimSuffix(name, "/")
	}
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("unsafe AgentSuite content path %q", header.Name)
	}
	clean := path.Clean(name)
	if clean != name || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe AgentSuite content path %q", header.Name)
	}
	return filepath.FromSlash(clean), nil
}
