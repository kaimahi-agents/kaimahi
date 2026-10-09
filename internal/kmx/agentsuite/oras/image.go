package oras

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry"
)

// ResolvedImage retains the immutable platform manifest selected from a tag.
type ResolvedImage struct {
	Reference  string
	Descriptor ocispec.Descriptor
	Deployment agentsuite.ImageDeployment
}

// ResolveImageRegistry reads verified manifests and config without extracting or
// executing the image. Kubernetes pulls its layers by the selected digest.
func ResolveImageRegistry(ctx context.Context, reference string, platform agentsuite.Platform) (ResolvedImage, error) {
	return ResolveImageRegistryTransport(ctx, reference, platform, false)
}

func ResolveImageRegistryTransport(ctx context.Context, reference string, platform agentsuite.Platform, plainHTTP bool) (ResolvedImage, error) {
	repository, ref, err := newRegistryRepository(reference, plainHTTP, nil)
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

func ResolveImage(ctx context.Context, source agentsuite.ReadOnlyTarget, reference string, platform agentsuite.Platform) (ResolvedImage, error) {
	root, err := source.Resolve(ctx, reference)
	if err != nil {
		return ResolvedImage{}, fmt.Errorf("resolve agent image: %w", err)
	}
	data, err := imageJSON(ctx, source, root)
	if err != nil {
		return ResolvedImage{}, err
	}
	if root.MediaType == ocispec.MediaTypeImageIndex {
		var index ocispec.Index
		if err := json.Unmarshal(data, &index); err != nil {
			return ResolvedImage{}, err
		}
		matches := 0
		for _, candidate := range index.Manifests {
			p := candidate.Platform
			if p != nil && p.OS == platform.OS && p.Architecture == platform.Architecture && p.Variant == platform.Variant {
				root = candidate
				matches++
			}
		}
		if matches != 1 {
			return ResolvedImage{}, errors.New("image index must select exactly one target platform manifest")
		}
		data, err = imageJSON(ctx, source, root)
		if err != nil {
			return ResolvedImage{}, err
		}
	}
	if root.MediaType != ocispec.MediaTypeImageManifest {
		return ResolvedImage{}, errors.New("lift requires a runnable OCI image manifest or index")
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ResolvedImage{}, err
	}
	if manifest.SchemaVersion != 2 || manifest.ArtifactType != "" || manifest.Config.MediaType != ocispec.MediaTypeImageConfig || len(manifest.Layers) == 0 {
		return ResolvedImage{}, errors.New("lift requires a built runnable image, not an AgentSuite definition artifact")
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
	label := config.Config.Labels[agentsuite.ImageDeploymentLabel]
	if label == "" {
		return ResolvedImage{}, errors.New("image has no org.agentsuite.image-deployment record; rebuild with an execution contract")
	}
	record, err := agentsuite.DecodeImageDeployment([]byte(label))
	if err != nil {
		return ResolvedImage{}, fmt.Errorf("image deployment record: %w", err)
	}
	if record.Platform != platform {
		return ResolvedImage{}, errors.New("image deployment platform differs from image config")
	}
	if len(config.Config.Entrypoint) == 0 {
		return ResolvedImage{}, errors.New("agent image requires an explicit entrypoint")
	}
	if record.Execution.Configuration == agentsuite.AgentKitMountedConfig {
		if len(config.Config.Cmd) != 4 || config.Config.Cmd[0] != "--config" || config.Config.Cmd[1] != agentsuite.AgentKitConfigPath || config.Config.Cmd[2] != "--protocol" || config.Config.Cmd[3] != "openai" {
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
	// Builders must expose the service without lift overriding baked configuration.
	for _, input := range record.Execution.Inputs {
		for _, value := range config.Config.Env {
			if strings.HasPrefix(value, input.Environment+"=") && input.Secret && value != input.Environment+"=" {
				return ResolvedImage{}, errors.New("image embeds a non-empty secret input")
			}
		}
	}
	return ResolvedImage{Descriptor: root, Deployment: *record}, nil
}

func imageDescriptor(d ocispec.Descriptor) error {
	if d.Digest.Validate() != nil || d.Digest.Algorithm() != digest.SHA256 || d.Size < 0 || len(d.URLs) != 0 || len(d.Data) != 0 {
		return errors.New("image descriptor must pin local SHA-256 content without URLs or embedded data")
	}
	return nil
}

func imageJSON(ctx context.Context, source agentsuite.Fetcher, d ocispec.Descriptor) ([]byte, error) {
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
