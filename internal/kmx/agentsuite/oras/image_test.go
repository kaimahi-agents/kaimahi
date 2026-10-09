package oras

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
)

func TestResolveImageVerifiesContractAndPlatform(t *testing.T) {
	for _, scenario := range []string{"valid", "unmarked", "root", "wrong platform", "ambiguous index", "suite artifact"} {
		t.Run(scenario, func(t *testing.T) {
			store := memory.New()
			ctx := t.Context()
			push := func(media string, value any) ocispec.Descriptor {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				d := content.NewDescriptorFromBytes(media, data)
				if err := store.Push(ctx, d, bytes.NewReader(data)); err != nil {
					t.Fatal(err)
				}
				return d
			}
			platform := agentsuite.Platform{OS: "linux", Architecture: "amd64"}
			digest := "sha256:" + strings.Repeat("a", 64)
			record := agentsuite.ImageDeployment{SchemaVersion: agentsuite.SpecVersion, MediaType: agentsuite.ImageDeploymentMediaType, Agent: "hello", Platform: platform, SuiteReference: "registry.example/suite@" + digest, SuiteDigest: digest, CompositionDigest: digest, BuildProfile: "default", Execution: agentsuite.ExecutionContract{Kind: agentsuite.ExecutionHTTPV1, Protocol: "openai-chat-v1", Port: 8080, HealthPath: "/healthz", Inputs: []agentsuite.ExecutionInput{}}}
			label, err := agentsuite.EncodeImageDeployment(record)
			if err != nil {
				t.Fatal(err)
			}
			layer := push(ocispec.MediaTypeImageLayerGzip, "payload")
			config := ocispec.Image{Platform: ocispec.Platform{OS: "linux", Architecture: "amd64"}, Config: ocispec.ImageConfig{User: "1000", Entrypoint: []string{"/agent"}, Labels: map[string]string{agentsuite.ImageDeploymentLabel: label}}, RootFS: ocispec.RootFS{Type: "layers"}}
			config.RootFS.DiffIDs = append(config.RootFS.DiffIDs, layer.Digest)
			if scenario == "unmarked" {
				config.Config.Labels = nil
			}
			if scenario == "root" {
				config.Config.User = "0"
			}
			if scenario == "wrong platform" {
				config.Architecture = "arm64"
			}
			manifest := ocispec.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, Config: push(ocispec.MediaTypeImageConfig, config), Layers: []ocispec.Descriptor{layer}}
			if scenario == "suite artifact" {
				manifest.ArtifactType = agentsuite.MediaTypeArtifact
			}
			root := push(ocispec.MediaTypeImageManifest, manifest)
			root.Platform = &ocispec.Platform{OS: "linux", Architecture: "amd64"}
			entries := []ocispec.Descriptor{root}
			if scenario == "ambiguous index" {
				entries = append(entries, root)
			}
			index := push(ocispec.MediaTypeImageIndex, ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, Manifests: entries})
			if err := store.Tag(ctx, index, "v1"); err != nil {
				t.Fatal(err)
			}
			result, err := ResolveImage(context.Background(), store, "v1", platform)
			if scenario == "valid" {
				if err != nil || result.Descriptor.Digest != root.Digest {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			} else if err == nil {
				t.Fatal("invalid image accepted")
			}
		})
	}
}
