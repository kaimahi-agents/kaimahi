package oras

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"github.com/opencontainers/image-spec/specs-go"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	oraslib "oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	storeoci "oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"
)

const ImageReleaseMediaType = "application/vnd.kaimahi.image-release.v1+json"

// ImageRelease is a KMX-private association, not a new AgentSuite source format.
// Its descriptors are resolved from the release's own OCI repository or layout.
// Referencing full graphs as manifest children makes ordinary OCI copying retain
// source, images and attestation siblings without embedding retrieval locations.
type ImageRelease struct {
	SchemaVersion int                `json:"schemaVersion"`
	Suite         oci.Descriptor     `json:"suite"`
	Images        []ImageReleaseSlot `json:"images"`
}

type ImageReleaseSlot struct {
	Member   string         `json:"member"`
	Platform spec.Platform  `json:"platform"`
	Image    oci.Descriptor `json:"image"`
	Graph    oci.Descriptor `json:"graph"`
}

func releaseDescriptor(d oci.Descriptor) bool {
	return imageDescriptor(d) == nil && d.MediaType == oci.MediaTypeImageManifest && d.Size > 0 && d.Size <= 4<<20 && d.Annotations == nil && d.Platform == nil && d.ArtifactType == ""
}

func plainDescriptor(d oci.Descriptor) oci.Descriptor {
	return oci.Descriptor{MediaType: d.MediaType, Digest: d.Digest, Size: d.Size}
}

func (r ImageRelease) validate() error {
	if r.SchemaVersion != 1 || !releaseDescriptor(r.Suite) || len(r.Images) != 1 {
		return errors.New("image release requires one suite and the closed one-member image profile")
	}
	for _, slot := range r.Images {
		graph := slot.Graph
		if graph.MediaType == oci.MediaTypeImageIndex {
			graph.MediaType = oci.MediaTypeImageManifest
		}
		if slot.Member == "" || slot.Platform != (spec.Platform{OS: "linux", Architecture: "amd64"}) || !releaseDescriptor(slot.Image) || !releaseDescriptor(graph) {
			return errors.New("invalid image release slot")
		}
	}
	return nil
}

// ValidateImageRelease proves graph completeness and exact profile/source links.
// It performs no runtime calls, grants or evaluation and does not trust labels
// as sandbox conformance or signer evidence.
func ValidateImageRelease(ctx context.Context, source agentsuite.ReadOnlyTarget, r ImageRelease) error {
	if err := r.validate(); err != nil {
		return err
	}
	artifact, err := readArtifact(ctx, source, r.Suite, r.Suite.Digest.String())
	if err != nil {
		return err
	}
	files := artifact.Files()
	var suite spec.Suite
	if spec.Unmarshal(files["agentsuite.json"], &suite) != nil || len(suite.Agents) != 1 || suite.Agents[0].ID != r.Images[0].Member {
		return errors.New("release image slots differ from suite membership")
	}
	// Reuse public source projection here; the application separately validates
	// the closed KMX execution profile before evaluating or lifting.
	stage, err := os.MkdirTemp("", "kmx-release-check-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for name, raw := range files {
		path := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			return err
		}
	}
	return validateReleaseSlots(ctx, source, r, stage)
}

func validateReleaseSlots(ctx context.Context, source agentsuite.ReadOnlyTarget, r ImageRelease, root string) error {
	for _, slot := range r.Images {
		selected, err := ResolveImage(ctx, source, slot.Graph.Digest.String(), slot.Platform)
		if err != nil {
			return err
		}
		if !content.Equal(selected.Descriptor, slot.Image) || selected.Deployment.SuiteDigest != r.Suite.Digest.String() || selected.Deployment.Agent != slot.Member {
			return errors.New("image release source/member/platform association mismatch")
		}
		if err := agentsuite.ValidateImageSource(root, selected.Deployment); err != nil {
			return err
		}
	}
	return nil
}

// PublishImageRelease copies all required immutable graphs to the destination
// before exposing its final tag. A failed copy or validation never moves it.
// As with suite push, force/tag preflight is not a concurrent-writer transaction.
func PublishImageRelease(ctx context.Context, source agentsuite.ReadOnlyTarget, target agentsuite.Target, reference string, r ImageRelease, force bool) (oci.Descriptor, error) {
	if err := ValidateImageRelease(ctx, source, r); err != nil {
		return oci.Descriptor{}, err
	}
	raw, err := spec.Marshal(r)
	if err != nil {
		return oci.Descriptor{}, err
	}
	layers := []oci.Descriptor{r.Suite}
	for _, slot := range r.Images {
		layers = append(layers, slot.Graph)
	}
	manifest := oci.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: oci.MediaTypeImageIndex, ArtifactType: ImageReleaseMediaType, Manifests: layers, Annotations: map[string]string{"kaimahi.dev/image-release": string(raw)}}
	body, err := spec.Marshal(manifest)
	if err != nil {
		return oci.Descriptor{}, err
	}
	root := content.NewDescriptorFromBytes(oci.MediaTypeImageIndex, body)
	if old, err := target.Resolve(ctx, reference); err == nil {
		if old.Digest != root.Digest && !force {
			return oci.Descriptor{}, errors.New("release tag already identifies different content; use --force")
		}
	} else if !errors.Is(err, errdef.ErrNotFound) {
		return oci.Descriptor{}, err
	}
	for _, child := range layers {
		if err := oraslib.CopyGraph(ctx, source, target, child, oraslib.DefaultCopyGraphOptions); err != nil {
			return oci.Descriptor{}, err
		}
	}
	for _, item := range []struct {
		d oci.Descriptor
		b []byte
	}{{root, body}} {
		if err := target.Push(ctx, item.d, bytes.NewReader(item.b)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
			return oci.Descriptor{}, err
		}
	}
	if err := target.Tag(ctx, root, reference); err != nil {
		return oci.Descriptor{}, err
	}
	return root, nil
}

func ReadImageRelease(ctx context.Context, source agentsuite.ReadOnlyTarget, digest string) (ImageRelease, error) {
	var result ImageRelease
	if !spec.IsValidDigest(digest) {
		return result, errors.New("release requires SHA256 identity")
	}
	d, err := source.Resolve(ctx, digest)
	if err != nil || d.Digest.String() != digest || d.MediaType != oci.MediaTypeImageIndex {
		return result, errors.New("release manifest not found or mismatched")
	}
	body, err := imageJSON(ctx, source, d)
	if err != nil {
		return result, err
	}
	var manifest oci.Index
	if spec.Unmarshal(body, &manifest) != nil || manifest.SchemaVersion != 2 || manifest.MediaType != oci.MediaTypeImageIndex || manifest.ArtifactType != ImageReleaseMediaType || manifest.Subject != nil {
		return result, errors.New("invalid image release manifest")
	}
	raw := []byte(manifest.Annotations["kaimahi.dev/image-release"])
	if len(raw) > 64<<10 || spec.Unmarshal(raw, &result) != nil || result.validate() != nil || len(manifest.Manifests) != 1+len(result.Images) || !content.Equal(manifest.Manifests[0], result.Suite) {
		return result, errors.New("invalid image release association")
	}
	for i, slot := range result.Images {
		if !content.Equal(manifest.Manifests[i+1], slot.Graph) {
			return result, errors.New("release graph differs from association")
		}
	}
	return result, ValidateImageRelease(ctx, source, result)
}

func ReadImageReleaseRegistry(ctx context.Context, reference string, plainHTTP bool) (ImageRelease, string, error) {
	parsed, err := registry.ParseReference(reference)
	if err != nil || parsed.ValidateReferenceAsDigest() != nil {
		return ImageRelease{}, "", errors.New("immutable release reference required")
	}
	source, digest, err := newRegistryRepository(reference, plainHTTP, nil)
	if err != nil {
		return ImageRelease{}, "", err
	}
	r, err := ReadImageRelease(ctx, source, digest)
	return r, fmt.Sprintf("%s/%s", parsed.Registry, parsed.Repository), err
}

func ReadImageReleaseArtifact(ctx context.Context, reference, layout string, plainHTTP bool) (ImageRelease, *Artifact, string, error) {
	var r ImageRelease
	parsed, err := registry.ParseReference(reference)
	if err != nil || parsed.ValidateReferenceAsDigest() != nil {
		return r, nil, "", errors.New("immutable image release reference required")
	}
	var source agentsuite.ReadOnlyTarget
	repository := parsed.Registry + "/" + parsed.Repository
	if layout != "" {
		store, err := storeoci.NewFromFS(ctx, os.DirFS(layout))
		if err != nil {
			return r, nil, "", err
		}
		source = store
	} else {
		store, _, err := newRegistryRepository(reference, plainHTTP, nil)
		if err != nil {
			return r, nil, "", err
		}
		source = store
	}
	r, err = ReadImageRelease(ctx, source, parsed.Reference)
	if err != nil {
		return r, nil, "", err
	}
	a, err := readArtifact(ctx, source, r.Suite, r.Suite.Digest.String())
	return r, a, repository, err
}

func ResolveImageReleaseMember(ctx context.Context, reference, layout string, plainHTTP bool, slot ImageReleaseSlot) (ResolvedImage, error) {
	if layout != "" {
		store, err := storeoci.NewFromFS(ctx, os.DirFS(layout))
		if err != nil {
			return ResolvedImage{}, err
		}
		image, err := ResolveImage(ctx, store, slot.Image.Digest.String(), slot.Platform)
		if err != nil {
			return image, err
		}
		parsed, err := registry.ParseReference(reference)
		if err != nil {
			return image, err
		}
		image.Reference = parsed.Registry + "/" + parsed.Repository + "@" + slot.Image.Digest.String()
		return image, nil
	}
	parsed, err := registry.ParseReference(reference)
	if err != nil {
		return ResolvedImage{}, err
	}
	return ResolveImageRegistryTransport(ctx, parsed.Registry+"/"+parsed.Repository+"@"+slot.Image.Digest.String(), slot.Platform, plainHTTP)
}

// PublishImageReleaseRegistry co-locates immutable member graphs with their suite
// so the release remains traversable after a repository-to-repository OCI copy.
func PublishImageReleaseRegistry(ctx context.Context, reference, suiteReference string, images, indexes map[string]string, platform spec.Platform, plainHTTP, force bool) (string, error) {
	target, tag, err := newRegistryRepository(reference, plainHTTP, nil)
	if err != nil {
		return "", err
	}
	parsed, err := registry.ParseReference(reference)
	if err != nil || parsed.ValidateReferenceAsTag() != nil {
		return "", errors.New("release destination must be a tag")
	}
	suiteSource, suiteDigest, err := newRegistryRepository(suiteReference, plainHTTP, nil)
	if err != nil {
		return "", err
	}
	suite, err := suiteSource.Resolve(ctx, suiteDigest)
	if err != nil {
		return "", err
	}
	if err := oraslib.CopyGraph(ctx, suiteSource, target, suite, oraslib.DefaultCopyGraphOptions); err != nil {
		return "", err
	}
	r := ImageRelease{SchemaVersion: 1, Suite: plainDescriptor(suite)}
	if len(images) != 1 || len(indexes) != 1 {
		return "", errors.New("release requires exactly one complete image slot")
	}
	for member, imageRef := range images {
		imageSource, imageDigest, err := newRegistryRepository(imageRef, plainHTTP, nil)
		if err != nil {
			return "", err
		}
		image, err := imageSource.Resolve(ctx, imageDigest)
		if err != nil {
			return "", err
		}
		graphSource, graphDigest, err := newRegistryRepository(indexes[member], plainHTTP, nil)
		if err != nil {
			return "", err
		}
		graph, err := graphSource.Resolve(ctx, graphDigest)
		if err != nil {
			return "", err
		}
		if err := oraslib.CopyGraph(ctx, graphSource, target, graph, oraslib.DefaultCopyGraphOptions); err != nil {
			return "", err
		}
		r.Images = append(r.Images, ImageReleaseSlot{Member: member, Platform: platform, Image: plainDescriptor(image), Graph: plainDescriptor(graph)})
	}
	d, err := PublishImageRelease(ctx, target, target, tag, r, force)
	if err != nil {
		return "", err
	}
	return parsed.Registry + "/" + parsed.Repository + "@" + d.Digest.String(), nil
}
