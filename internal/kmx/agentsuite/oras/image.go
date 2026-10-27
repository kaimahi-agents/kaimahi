package oras

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	suitecore "github.com/agent-primitives/suite/core"
	kmxsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry"
)

// ResolvedImage retains the immutable platform manifest selected from a tag.
type ResolvedImage struct {
	Reference  string
	Descriptor ocispec.Descriptor
	Deployment kmxsuite.ImageDeployment
}

// PrepareImageBuildSource reads the exact published artifact and checks every
// packaged file against the local directory, including supplemental metadata and
// cases. The build consumes the published bytes, never a second read of source.
// Output must be a new directory inside a caller-owned temporary parent.
func PrepareImageBuildSource(ctx context.Context, source, reference, output string, plainHTTP bool) error {
	remote, err := ReadArtifactRegistry(ctx, reference, plainHTTP)
	if err != nil {
		return err
	}
	stage, store, packed, err := packDirectory(ctx, source)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	local, err := readArtifact(ctx, store, packed.Descriptor, packed.Descriptor.Digest.String())
	if err != nil {
		return err
	}
	if !local.SameContent(remote) {
		return errors.New("local and published suite content differs, including supplemental metadata and cases")
	}
	return remote.Materialize(ctx, output)
}

// ResolveImageRegistry reads verified manifests and config without extracting or
// executing the image. Kubernetes pulls its layers by the selected digest.
func ResolveImageRegistry(ctx context.Context, reference string, platform suitecore.Platform) (ResolvedImage, error) {
	return ResolveImageRegistryTransport(ctx, reference, platform, false)
}

func ResolveImageRegistryTransport(ctx context.Context, reference string, platform suitecore.Platform, plainHTTP bool) (ResolvedImage, error) {
	repository, ref, err := newRegistryRepositoryLimit(reference, plainHTTP, nil, 4<<20)
	if err != nil {
		return ResolvedImage{}, err
	}
	parsed, err := registry.ParseReference(reference)
	if err != nil {
		return ResolvedImage{}, err
	}
	result, err := ResolveImage(ctx, repository, ref, platform)
	if err != nil {
		return ResolvedImage{}, err
	}
	result.Reference = parsed.Registry + "/" + parsed.Repository + "@" + result.Descriptor.Digest.String()
	return result, nil
}

func ResolveImage(ctx context.Context, source kmxsuite.ReadOnlyTarget, reference string, platform suitecore.Platform) (ResolvedImage, error) {
	root, err := source.Resolve(ctx, reference)
	if err != nil {
		return ResolvedImage{}, fmt.Errorf("resolve agent image: %w", err)
	}
	if root.MediaType == ocispec.MediaTypeImageIndex {
		root, err = selectImageManifest(ctx, source, root, platform)
		if err != nil {
			return ResolvedImage{}, err
		}
	}
	if root.MediaType != ocispec.MediaTypeImageManifest {
		return ResolvedImage{}, errors.New("image resolution requires a runnable OCI image manifest or index")
	}
	data, err := imageJSON(ctx, source, root)
	if err != nil {
		return ResolvedImage{}, err
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ResolvedImage{}, err
	}
	if manifest.Subject != nil {
		return ResolvedImage{}, errors.New("OCI image manifest subjects are unsupported")
	}
	if manifest.SchemaVersion != 2 || manifest.ArtifactType != "" || manifest.Config.MediaType != ocispec.MediaTypeImageConfig || len(manifest.Layers) == 0 {
		return ResolvedImage{}, errors.New("image resolution requires a built runnable image, not an AgentSuite definition artifact")
	}
	for _, layer := range manifest.Layers {
		if err := imageDescriptor(layer); err != nil {
			return ResolvedImage{}, err
		}
		if layer.MediaType != ocispec.MediaTypeImageLayerGzip && layer.MediaType != ocispec.MediaTypeImageLayer && layer.MediaType != ocispec.MediaTypeImageLayerZstd {
			return ResolvedImage{}, errors.New("unsupported image layer media type")
		}
	}
	data, err = imageJSON(ctx, source, manifest.Config)
	if err != nil {
		return ResolvedImage{}, err
	}
	var config ocispec.Image
	if err := json.Unmarshal(data, &config); err != nil {
		return ResolvedImage{}, err
	}
	if config.OS != platform.OS || config.Architecture != platform.Architecture || config.Variant != platform.Variant {
		return ResolvedImage{}, errors.New("image config platform differs from destination")
	}
	label := config.Config.Labels[kmxsuite.ImageDeploymentLabel]
	if label == "" {
		return ResolvedImage{}, errors.New("image has no org.agentsuite.image-deployment record; rebuild with an execution contract")
	}
	record, err := kmxsuite.DecodeImageDeployment([]byte(label))
	if err != nil {
		return ResolvedImage{}, fmt.Errorf("image deployment record: %w", err)
	}
	if record.Platform != platform {
		return ResolvedImage{}, errors.New("image deployment platform differs from image config")
	}
	if len(config.Config.Entrypoint) == 0 {
		return ResolvedImage{}, errors.New("agent image requires an explicit entrypoint")
	}
	if record.Execution.Configuration == suitecore.AgentKitMountedConfig {
		if len(config.Config.Cmd) != 4 || config.Config.Cmd[0] != "--config" || config.Config.Cmd[1] != kmxsuite.AgentKitConfigPath || config.Config.Cmd[2] != "--protocol" || config.Config.Cmd[3] != "openai" {
			return ResolvedImage{}, errors.New("image does not activate the declared mounted configuration ABI")
		}
	}
	user, _, _ := strings.Cut(config.Config.User, ":")
	uid, err := strconv.ParseUint(user, 10, 32)
	if err != nil || uid == 0 {
		return ResolvedImage{}, errors.New("agent image requires an explicit numeric non-root user")
	}
	if config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != len(manifest.Layers) {
		return ResolvedImage{}, errors.New("image rootfs does not match layer count")
	}
	// Declared secret inputs must remain unbound in the built configuration.
	for _, input := range record.Execution.Inputs {
		for _, value := range config.Config.Env {
			if strings.HasPrefix(value, input.Environment+"=") && input.Secret && value != input.Environment+"=" {
				return ResolvedImage{}, errors.New("image embeds a non-empty secret input")
			}
		}
	}
	return ResolvedImage{Descriptor: root, Deployment: *record}, nil
}

// selectImageManifest shares one descriptor/depth budget and ambiguity count
// across the whole index tree, including branches without platform metadata.
func selectImageManifest(ctx context.Context, source kmxsuite.Fetcher, root ocispec.Descriptor, platform suitecore.Platform) (ocispec.Descriptor, error) {
	// Match archive inspection's 1000 descendants and 100 nested index levels.
	const maxDescriptors, maxIndexDepth = 1001, 100
	var selected ocispec.Descriptor
	count, matches := 0, 0
	var visit func(ocispec.Descriptor, int) error
	visit = func(d ocispec.Descriptor, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > maxDescriptors || d.MediaType == ocispec.MediaTypeImageIndex && depth > maxIndexDepth {
			return errors.New("image index traversal limit exceeded")
		}
		if d.MediaType != ocispec.MediaTypeImageIndex {
			match, err := imageDescriptorMatchesPlatform(ctx, source, d, platform)
			if err != nil {
				return err
			}
			if match {
				selected = d
				matches++
				if matches > 1 {
					return errors.New("image index must select exactly one target platform manifest")
				}
			}
			return nil
		}
		data, err := imageJSON(ctx, source, d)
		if err != nil {
			return err
		}
		var index ocispec.Index
		if err := json.Unmarshal(data, &index); err != nil {
			return err
		}
		if index.SchemaVersion != 2 || index.Subject != nil {
			return errors.New("invalid or unsupported OCI image index")
		}
		for _, child := range index.Manifests {
			if err := visit(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root, 0); err != nil {
		return ocispec.Descriptor{}, err
	}
	if matches != 1 {
		return ocispec.Descriptor{}, errors.New("image index must select exactly one target platform manifest")
	}
	return selected, nil
}

func imageDescriptorMatchesPlatform(ctx context.Context, source kmxsuite.Fetcher, candidate ocispec.Descriptor, platform suitecore.Platform) (bool, error) {
	if candidate.MediaType != ocispec.MediaTypeImageManifest || candidate.Annotations["vnd.docker.reference.type"] == "attestation-manifest" {
		return false, nil
	}
	if candidate.Platform != nil {
		return candidate.Platform.OS == platform.OS &&
			candidate.Platform.Architecture == platform.Architecture &&
			candidate.Platform.Variant == platform.Variant, nil
	}
	data, err := imageJSON(ctx, source, candidate)
	if err != nil {
		return false, err
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false, err
	}
	if manifest.SchemaVersion != 2 || manifest.ArtifactType != "" || manifest.Config.MediaType != ocispec.MediaTypeImageConfig {
		return false, errors.New("platform-less image index entry is not a runnable OCI image manifest")
	}
	data, err = imageJSON(ctx, source, manifest.Config)
	if err != nil {
		return false, err
	}
	var config ocispec.Image
	if err := json.Unmarshal(data, &config); err != nil {
		return false, err
	}
	return config.OS == platform.OS &&
		config.Architecture == platform.Architecture &&
		config.Variant == platform.Variant, nil
}

func imageDescriptor(d ocispec.Descriptor) error {
	if d.Digest.Validate() != nil || d.Digest.Algorithm() != digest.SHA256 || d.Size < 0 || len(d.URLs) != 0 || len(d.Data) != 0 {
		return errors.New("image descriptor must pin local SHA-256 content without URLs or embedded data")
	}
	return nil
}

func imageJSON(ctx context.Context, source kmxsuite.Fetcher, d ocispec.Descriptor) ([]byte, error) {
	if err := imageDescriptor(d); err != nil {
		return nil, err
	}
	if d.Size > 4<<20 {
		return nil, errors.New("image metadata exceeds 4 MiB")
	}
	r, err := source.Fetch(ctx, d)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(r, d.Size+1))
	closeErr := r.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if int64(len(data)) != d.Size || digest.FromBytes(data) != d.Digest {
		return nil, errors.New("image metadata digest or size mismatch")
	}
	return data, nil
}
