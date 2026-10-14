package oras_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	core "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	oraslib "oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	storeoci "oras.land/oras-go/v2/content/oci"
)

func TestImageReleaseCopiesCompleteGraphAndRefusesForeignAssociation(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	input := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(input, []byte("Answer briefly."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Create(ctx, root, workspace.CreateOptions{Name: "sample", InstructionsFile: input, Execution: workspace.ImageExecution, ContextTokens: 8192, OutputTokens: 1024}); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "cache")
	prepared, err := workspace.Prepare(ctx, root, cache)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := storeoci.New(prepared.Path)
	if err != nil {
		t.Fatal(err)
	}
	source, err := storeoci.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := oraslib.CopyGraph(ctx, layout, source, prepared.Descriptor, oraslib.DefaultCopyGraphOptions); err != nil {
		t.Fatal(err)
	}
	extracted := filepath.Join(t.TempDir(), "source")
	if _, err := suiteoras.Pull(ctx, prepared.Path, prepared.Descriptor.Digest.String(), extracted); err != nil {
		t.Fatal(err)
	}
	p, err := core.ResolveSandboxPlan(extracted, core.BuildSelection{Agent: "sample", Platform: "linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	label, err := core.EncodeImageDeployment(core.ImageDeployment{SchemaVersion: spec.SpecVersion, MediaType: core.ImageDeploymentMediaType, SuiteDigest: prepared.Descriptor.Digest.String(), Agent: "sample", Platform: p.Composition.Platform, CompositionDigest: p.CompositionDigest, BuildProfile: p.BuildProfile.ID, BuildProfileDigest: p.BuildProfileDigest, Execution: *p.BuildProfile.Execution, Inference: p.Agent.Model.Capabilities})
	if err != nil {
		t.Fatal(err)
	}
	push := func(media string, value any) oci.Descriptor {
		raw, err := spec.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		d := content.NewDescriptorFromBytes(media, raw)
		if err := source.Push(ctx, d, bytes.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
		return d
	}
	layer := push(oci.MediaTypeImageLayer, "synthetic layer")
	config := oci.Image{Platform: oci.Platform{OS: "linux", Architecture: "amd64"}, Config: oci.ImageConfig{User: "1000", Entrypoint: []string{"/agent"}, Cmd: []string{"--config", core.AgentKitConfigPath, "--protocol", "openai"}, Labels: map[string]string{core.ImageDeploymentLabel: label}}, RootFS: oci.RootFS{Type: "layers", DiffIDs: []digest.Digest{layer.Digest}}}
	image := push(oci.MediaTypeImageManifest, oci.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, Config: push(oci.MediaTypeImageConfig, config), Layers: []oci.Descriptor{layer}})
	selected := image
	selected.Platform = &oci.Platform{OS: "linux", Architecture: "amd64"}
	index := push(oci.MediaTypeImageIndex, oci.Index{Versioned: specs.Versioned{SchemaVersion: 2}, Manifests: []oci.Descriptor{selected}})
	suite := prepared.Descriptor
	suite.ArtifactType = ""
	suite.Annotations = nil
	release := suiteoras.ImageRelease{SchemaVersion: 1, Suite: suite, Images: []suiteoras.ImageReleaseSlot{{Member: "sample", Platform: p.Composition.Platform, Image: image, Graph: index}}}
	target, err := storeoci.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	published, err := suiteoras.PublishImageRelease(ctx, source, target, "release", release, false)
	if err != nil {
		t.Fatal(err)
	}
	// A different store has no authored workspace, original registry or KMX cache.
	copy, err := storeoci.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := oraslib.CopyGraph(ctx, target, copy, published, oraslib.DefaultCopyGraphOptions); err != nil {
		t.Fatal(err)
	}
	if _, err := suiteoras.ReadImageRelease(ctx, copy, published.Digest.String()); err != nil {
		t.Fatal(err)
	}
	for _, d := range []oci.Descriptor{suite, index, image, layer} {
		if exists, err := copy.Exists(ctx, d); err != nil || !exists {
			t.Fatal("copy omitted required graph", d.Digest, err)
		}
	}
	bad := release
	bad.Images = append([]suiteoras.ImageReleaseSlot(nil), release.Images...)
	bad.Images[0].Member = "foreign"
	if _, err := suiteoras.PublishImageRelease(ctx, source, target, "release", bad, true); err == nil {
		t.Fatal("foreign member accepted")
	}
	current, err := target.Resolve(ctx, "release")
	if err != nil || current.Digest != published.Digest {
		t.Fatal("failure moved final pointer", err)
	}
	bad = release
	bad.Images = append(bad.Images, bad.Images[0])
	if _, err := suiteoras.PublishImageRelease(ctx, source, target, "release", bad, true); err == nil {
		t.Fatal("duplicate slot accepted")
	}
}
