package oras

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
)

// PullResult identifies an AgentSuite extracted from a referenced OCI target.
type PullResult struct {
	Path       string
	Reference  string
	Descriptor ocispec.Descriptor
	Report     *agentsuite.Report
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
	if _, err := os.Lstat(outputPath); err == nil {
		return PullResult{}, fmt.Errorf("AgentSuite output already exists: %s", output)
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

	sourceStore, err := oci.New(sourcePath)
	if err != nil {
		return PullResult{}, fmt.Errorf("open AgentSuite OCI source: %w", err)
	}
	pulledRoot, err := os.MkdirTemp("", "agentsuite-pulled-*")
	if err != nil {
		return PullResult{}, err
	}
	defer os.RemoveAll(pulledRoot)
	pulledStore, err := oci.New(pulledRoot)
	if err != nil {
		return PullResult{}, fmt.Errorf("create AgentSuite pull CAS: %w", err)
	}
	root, err := NewPuller(sourceStore).Pull(ctx, reference, pulledStore)
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
		Path:       outputPath,
		Reference:  reference,
		Descriptor: root,
		Report:     report,
	}, nil
}

func contentLayerDescriptor(
	ctx context.Context,
	src agentsuite.Fetcher,
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

func extractContentLayer(ctx context.Context, src io.Reader, destination string) (err error) {
	gzipReader, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("open AgentSuite content gzip: %w", err)
	}
	defer func() {
		err = errors.Join(err, gzipReader.Close())
	}()
	root, err := os.OpenRoot(destination)
	if err != nil {
		return fmt.Errorf("open AgentSuite extraction root: %w", err)
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	tarReader := tar.NewReader(gzipReader)
	var directories []extractedDirectory
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read AgentSuite content tar: %w", err)
		}
		name, err := extractionPath(header)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return fmt.Errorf("create AgentSuite directory %s: %w", header.Name, err)
			}
			directories = append(directories, extractedDirectory{name: name, mode: os.FileMode(header.Mode & 0o777)})
		case tar.TypeReg:
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return fmt.Errorf("create AgentSuite parent for %s: %w", header.Name, err)
			}
			file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode&0o777))
			if err != nil {
				return fmt.Errorf("create AgentSuite file %s: %w", header.Name, err)
			}
			written, copyErr := io.CopyN(file, &contextReader{ctx: ctx, reader: tarReader}, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("extract AgentSuite file %s: %w", header.Name, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close AgentSuite file %s: %w", header.Name, closeErr)
			}
			if written != header.Size {
				return fmt.Errorf("AgentSuite file %s size does not match its header", header.Name)
			}
			if err := root.Chmod(name, os.FileMode(header.Mode&0o777)); err != nil {
				return fmt.Errorf("set AgentSuite file mode %s: %w", header.Name, err)
			}
			if err := root.Chtimes(name, time.Unix(0, 0), time.Unix(0, 0)); err != nil {
				return fmt.Errorf("set AgentSuite file time %s: %w", header.Name, err)
			}
		case tar.TypeSymlink:
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return fmt.Errorf("create AgentSuite parent for %s: %w", header.Name, err)
			}
			if err := root.Symlink(filepath.FromSlash(header.Linkname), name); err != nil {
				return fmt.Errorf("create AgentSuite symlink %s: %w", header.Name, err)
			}
		case tar.TypeLink:
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return fmt.Errorf("create AgentSuite parent for %s: %w", header.Name, err)
			}
			if err := root.Link(filepath.FromSlash(header.Linkname), name); err != nil {
				return fmt.Errorf("create AgentSuite hardlink %s: %w", header.Name, err)
			}
		default:
			return fmt.Errorf("AgentSuite content %s has unsupported tar type %d", header.Name, header.Typeflag)
		}
	}
	for i := len(directories) - 1; i >= 0; i-- {
		directory := directories[i]
		if err := root.Chmod(directory.name, directory.mode); err != nil {
			return fmt.Errorf("set AgentSuite directory mode %s: %w", directory.name, err)
		}
		if err := root.Chtimes(directory.name, time.Unix(0, 0), time.Unix(0, 0)); err != nil {
			return fmt.Errorf("set AgentSuite directory time %s: %w", directory.name, err)
		}
	}
	return nil
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
