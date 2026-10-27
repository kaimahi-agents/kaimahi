package oras

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	suiteoci "github.com/agent-primitives/suite/oci"
	kmxsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
)

func TestPushImagePreservesGraphWithoutDeploymentAdmission(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprintf("nested=%t", nested), func(t *testing.T) {
			server, reg := newTestRegistryServer("", "")
			defer server.Close()
			t.Setenv("DOCKER_CONFIG", t.TempDir())
			archive, image, root, blobs := publicationArchive(t, nested, "first", "", false)
			ref := strings.TrimPrefix(server.URL, "http://") + "/team/image:v1"
			platform := suitecore.Platform{OS: "linux", Architecture: "amd64"}
			result, err := PushImage(t.Context(), archive, ref, platform, true, false)
			if err != nil || result != strings.TrimSuffix(ref, ":v1")+"@"+image {
				t.Fatalf("result=%s error=%v", result, err)
			}
			repository, tag, err := newRegistryRepository(ref, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			published, err := repository.Resolve(t.Context(), tag)
			if err != nil || published.Digest.String() != root {
				t.Fatalf("published=%+v error=%v", published, err)
			}
			for _, d := range blobs {
				data, err := content.FetchAll(t.Context(), repository, d)
				if err != nil || content.NewDescriptorFromBytes(d.MediaType, data).Digest != d.Digest {
					t.Fatalf("graph lost %s: %v", d.Digest, err)
				}
			}
			// The same ordinary image remains ineligible for declaration consumers.
			if _, err := ResolveImage(t.Context(), repository, tag, platform); err == nil || !strings.Contains(err.Error(), "image-deployment") {
				t.Fatalf("deployment admission accepted unmarked image: %v", err)
			}
			reg.mu.RLock()
			uploads, puts := reg.uploads, reg.manifestPuts
			reg.mu.RUnlock()
			for _, force := range []bool{false, true} {
				if _, err := PushImage(t.Context(), archive, ref, platform, true, force); err != nil {
					t.Fatal(err)
				}
			}
			changed, _, changedRoot, _ := publicationArchive(t, nested, "second", "", false)
			if _, err := PushImage(t.Context(), changed, ref, platform, true, false); err == nil || !strings.Contains(err.Error(), "--force") {
				t.Fatalf("tag replacement accepted: %v", err)
			}
			if _, err := PushImage(t.Context(), archive, ref, suitecore.Platform{OS: "linux", Architecture: "arm64"}, true, true); err == nil {
				t.Fatal("wrong platform accepted")
			}
			reg.mu.RLock()
			unchanged := reg.uploads == uploads && reg.manifestPuts == puts
			reg.mu.RUnlock()
			if !unchanged {
				t.Fatal("repeat or refusal wrote to registry")
			}
			if _, err := PushImage(t.Context(), changed, ref, platform, true, true); err != nil {
				t.Fatal(err)
			}
			published, err = repository.Resolve(t.Context(), tag)
			if err != nil || published.Digest.String() != changedRoot {
				t.Fatalf("forced publication=%+v error=%v", published, err)
			}
		})
	}
}

type publicationArchiveOptions struct {
	subject        string
	missingSubject bool
	indexDepth     int
}

func publicationArchive(t *testing.T, nested bool, marker, deploymentLabel string, omitPlatform bool, options ...publicationArchiveOptions) (string, string, string, []ocispec.Descriptor) {
	t.Helper()
	var opt publicationArchiveOptions
	if len(options) != 0 {
		opt = options[0]
	}
	blobs := map[string][]byte{}
	var descriptors []ocispec.Descriptor
	add := func(media string, value any) ocispec.Descriptor {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		d := content.NewDescriptorFromBytes(media, data)
		blobs["blobs/sha256/"+d.Digest.Encoded()] = data
		descriptors = append(descriptors, d)
		return d
	}
	layer := add(ocispec.MediaTypeImageLayer, "fixture layer")
	var subject *ocispec.Descriptor
	if opt.subject != "" {
		otherConfig := add(ocispec.MediaTypeImageConfig, ocispec.Image{Platform: ocispec.Platform{OS: "linux", Architecture: "arm64"}})
		otherLayer := add(ocispec.MediaTypeImageLayer, "uninspected subject layer")
		d := add(ocispec.MediaTypeImageManifest, ocispec.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, Config: otherConfig, Layers: []ocispec.Descriptor{otherLayer}})
		subject = &d
		if opt.missingSubject {
			delete(blobs, "blobs/sha256/"+d.Digest.Encoded())
		}
	}
	labels := map[string]string{}
	if deploymentLabel != "" {
		labels[kmxsuite.ImageDeploymentLabel] = deploymentLabel
	}
	config := add(ocispec.MediaTypeImageConfig, ocispec.Image{Platform: ocispec.Platform{OS: "linux", Architecture: "amd64"}, Config: ocispec.ImageConfig{User: "1000", Entrypoint: []string{"/agent"}, Labels: labels}, RootFS: ocispec.RootFS{Type: "layers", DiffIDs: []digest.Digest{layer.Digest}}})
	manifest := ocispec.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, Config: config, Layers: []ocispec.Descriptor{layer}, Annotations: map[string]string{"fixture": marker}}
	if opt.subject == "image" {
		manifest.Subject = subject
	}
	image := add(ocispec.MediaTypeImageManifest, manifest)
	if !omitPlatform {
		image.Platform = &ocispec.Platform{OS: "linux", Architecture: "amd64"}
	}
	image.Annotations = map[string]string{ocispec.AnnotationRefName: "local:latest"}
	var layers []ocispec.Descriptor
	for _, predicate := range []string{"https://spdx.dev/Document", "https://slsa.dev/provenance/v1"} {
		layers = append(layers, add("application/vnd.in-toto+json", map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": predicate, "subject": []any{map[string]any{"digest": map[string]string{"sha256": image.Digest.Encoded()}}}, "predicate": map[string]any{"fixture": true}}))
	}
	attestationManifest := ocispec.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, Config: config, Layers: layers}
	if opt.subject == "attestation" {
		attestationManifest.Subject = subject
	}
	attestation := add(ocispec.MediaTypeImageManifest, attestationManifest)
	if !omitPlatform {
		attestation.Platform = &ocispec.Platform{OS: "unknown", Architecture: "unknown"}
	}
	attestation.Annotations = map[string]string{"vnd.docker.reference.type": "attestation-manifest", "vnd.docker.reference.digest": image.Digest.String()}
	index := ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{image, attestation}}
	if opt.subject == "index" {
		index.Subject = subject
	}
	for range opt.indexDepth {
		inner := add(ocispec.MediaTypeImageIndex, index)
		index = ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{inner}}
	}
	if marker == "large-index" {
		index.Annotations = map[string]string{"padding": strings.Repeat("x", (1<<20)+1024)}
	}
	root := add(ocispec.MediaTypeImageIndex, index)
	if nested {
		root.Annotations = map[string]string{ocispec.AnnotationRefName: "local:latest"}
		index = ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{root}}
	} else {
		// A flat index is not itself required to exist under blobs/.
		delete(blobs, "blobs/sha256/"+root.Digest.Encoded())
	}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	blobs["index.json"] = data
	blobs["oci-layout"] = []byte(`{"imageLayoutVersion":"1.0.0"}`)
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for name, data := range blobs {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "image.tar")
	if err := os.WriteFile(path, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path, image.Digest.String(), root.Digest.String(), descriptors
}

func TestPushImageRefusesSubjectsBeforeRegistryRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected registry request", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	for _, subject := range []string{"index", "image", "attestation"} {
		for _, missing := range []bool{false, true} {
			for _, nested := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/missing=%t/nested=%t", subject, missing, nested), func(t *testing.T) {
					archive, _, _, _ := publicationArchive(t, nested, "subject", "", false, publicationArchiveOptions{subject: subject, missingSubject: missing})
					ref := strings.TrimPrefix(server.URL, "http://") + "/team/image:v1"
					result, err := PushImage(t.Context(), archive, ref, suitecore.Platform{OS: "linux", Architecture: "amd64"}, true, false)
					if err == nil || !strings.Contains(err.Error(), "subjects are unsupported") || result != "" || requests.Load() != 0 {
						t.Fatalf("subject did not refuse before registry access: result=%q err=%v requests=%d", result, err, requests.Load())
					}
				})
			}
		}
	}
}

func TestImagePublicationMetadataBudget(t *testing.T) {
	server, _ := newTestRegistryServer("", "")
	defer server.Close()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	for _, nested := range []bool{false, true} {
		archive, _, _, _ := publicationArchive(t, nested, "large-index", "", false)
		ref := strings.TrimPrefix(server.URL, "http://") + fmt.Sprintf("/large/image:v%t", nested)
		if _, err := PushImage(t.Context(), archive, ref, suitecore.Platform{OS: "linux", Architecture: "amd64"}, true, false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPushAndResolveImageWithoutDescriptorPlatform(t *testing.T) {
	server, _ := newTestRegistryServer("", "")
	defer server.Close()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	platform := suitecore.Platform{OS: "linux", Architecture: "amd64"}
	suiteDigest := "sha256:" + strings.Repeat("a", 64)
	record := kmxsuite.ImageDeployment{
		SchemaVersion:      suitecore.SpecVersion,
		MediaType:          kmxsuite.ImageDeploymentMediaType,
		SuiteDigest:        suiteDigest,
		Agent:              "hello",
		Platform:           platform,
		CompositionDigest:  suiteDigest,
		BuildProfile:       "default",
		BuildProfileDigest: suiteDigest,
		Execution: suitecore.ExecutionContract{
			Kind:       suitecore.ExecutionHTTPV1,
			Protocol:   "openai-chat-v1",
			Port:       8080,
			HealthPath: "/healthz",
			Inputs:     []suitecore.ExecutionInput{},
		},
	}
	label, err := kmxsuite.EncodeImageDeployment(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, depth := range []int{0, 1, 3} {
		for _, omitPlatform := range []bool{false, true} {
			t.Run(fmt.Sprintf("depth=%d/omit-platform=%t", depth, omitPlatform), func(t *testing.T) {
				archive, image, _, _ := publicationArchive(t, false, "omitted-platform", label, omitPlatform, publicationArchiveOptions{indexDepth: depth})
				ref := strings.TrimPrefix(server.URL, "http://") + fmt.Sprintf("/team/image:d%d-%t", depth, omitPlatform)
				if published, err := PushImage(t.Context(), archive, ref, platform, true, false); err != nil || published != strings.TrimPrefix(server.URL, "http://")+"/team/image@"+image {
					t.Fatalf("published=%q error=%v", published, err)
				}
				resolved, err := ResolveImageRegistryTransport(t.Context(), ref, platform, true)
				if err != nil || resolved.Descriptor.Digest.String() != image {
					t.Fatalf("resolved=%+v error=%v", resolved, err)
				}
			})
		}
	}
}

func TestImageBuildSourceBindsSupplementalContent(t *testing.T) {
	server, reg := newTestRegistryServer("", "")
	defer server.Close()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	source := filepath.Join(t.TempDir(), "source")
	if err := os.CopyFS(source, os.DirFS(filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"eval/case.yaml", "metadata/kmx/profile.json"} {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ref := strings.TrimPrefix(server.URL, "http://") + "/team/suite:v1"
	payloadRoot := addModeProvider(t, source, "image-source", [][]payloadEntry{{
		{InventoryEntry: suitecore.InventoryEntry{Path: "bin/tool", Type: "file", Mode: 0755}, data: "#!/bin/sh\nexit 0\n", host: 0644},
	}})
	executable := filepath.Join(source, payloadRoot, "bin", "tool")
	pushed, err := PushRegistry(t.Context(), source, ref, true, false, PushOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pinned := strings.TrimSuffix(ref, ":v1") + "@" + pushed.Descriptor.Digest.String()
	reg.mu.RLock()
	uploads, puts := reg.uploads, reg.manifestPuts
	reg.mu.RUnlock()
	output := filepath.Join(t.TempDir(), "build")
	if err := PrepareImageBuildSource(t.Context(), source, pinned, output, true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(output, payloadRoot, "bin", "tool"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("packaged executable mode was not retained: %v %v", info, err)
	}
	if err := os.Chmod(executable, 0755); err != nil {
		t.Fatal(err)
	}
	// Portable declarations, not incidental host execute bits, own packed modes.
	if err := PrepareImageBuildSource(t.Context(), source, pinned, filepath.Join(t.TempDir(), "host-mode-change"), true); err != nil {
		t.Fatal("host chmod changed declared packaged content", err)
	}
	for _, name := range []string{"eval/case.yaml", "metadata/kmx/profile.json"} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(source, name), []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
			defer os.WriteFile(filepath.Join(source, name), []byte("original"), 0o600)
			if _, err := suiteoci.ValidatePath(source); err != nil {
				t.Fatal("supplemental change must remain public-graph valid", err)
			}
			destination := filepath.Join(t.TempDir(), "refused")
			if err := PrepareImageBuildSource(t.Context(), source, pinned, destination, true); err == nil || !strings.Contains(err.Error(), "content differs") {
				t.Fatalf("supplemental drift accepted: %v", err)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("mismatch created build source", err)
			}
			data, err := os.ReadFile(filepath.Join(output, name))
			if err != nil || string(data) != "original" {
				t.Fatal("local edit changed retained build bytes", err)
			}
		})
	}
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	if uploads != reg.uploads || puts != reg.manifestPuts {
		t.Fatal("build-source preparation wrote to the registry")
	}
}

func TestResolveImageVerifiesContractAndPlatform(t *testing.T) {
	for _, scenario := range []string{"valid", "unmarked", "root", "wrong platform", "ambiguous index", "ambiguous omitted platform", "ambiguous nested index", "depth limit", "descriptor limit", "suite artifact"} {
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
			platform := suitecore.Platform{OS: "linux", Architecture: "amd64"}
			digest := "sha256:" + strings.Repeat("a", 64)
			record := kmxsuite.ImageDeployment{SchemaVersion: suitecore.SpecVersion, MediaType: kmxsuite.ImageDeploymentMediaType, Agent: "hello", Platform: platform, SuiteReference: "registry.example/suite@" + digest, SuiteDigest: digest, CompositionDigest: digest, BuildProfile: "default", Execution: suitecore.ExecutionContract{Kind: suitecore.ExecutionHTTPV1, Protocol: "openai-chat-v1", Port: 8080, HealthPath: "/healthz", Inputs: []suitecore.ExecutionInput{}}}
			record.BuildProfileDigest = digest
			label, err := kmxsuite.EncodeImageDeployment(record)
			if err != nil {
				t.Fatal(err)
			}
			layer := push(ocispec.MediaTypeImageLayerGzip, "payload")
			config := ocispec.Image{Platform: ocispec.Platform{OS: "linux", Architecture: "amd64"}, Config: ocispec.ImageConfig{User: "1000", Entrypoint: []string{"/agent"}, Labels: map[string]string{kmxsuite.ImageDeploymentLabel: label}}, RootFS: ocispec.RootFS{Type: "layers"}}
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
				manifest.ArtifactType = suitecore.MediaTypeArtifact
			}
			root := push(ocispec.MediaTypeImageManifest, manifest)
			root.Platform = &ocispec.Platform{OS: "linux", Architecture: "amd64"}
			entries := []ocispec.Descriptor{root}
			if scenario == "ambiguous index" || scenario == "ambiguous omitted platform" {
				entries = append(entries, root)
			}
			if scenario == "ambiguous omitted platform" {
				entries[0].Platform = nil
				entries[1].Platform = nil
			}
			if scenario == "ambiguous nested index" {
				inner := push(ocispec.MediaTypeImageIndex, ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, Manifests: entries})
				entries = []ocispec.Descriptor{inner, inner}
			}
			if scenario == "depth limit" {
				for range 102 {
					inner := push(ocispec.MediaTypeImageIndex, ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, Manifests: entries})
					entries = []ocispec.Descriptor{inner}
				}
			}
			if scenario == "descriptor limit" {
				inner := push(ocispec.MediaTypeImageIndex, ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, Manifests: []ocispec.Descriptor{}})
				entries = nil
				for range 1001 {
					entries = append(entries, inner)
				}
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
			if (scenario == "depth limit" || scenario == "descriptor limit") && (err == nil || !strings.Contains(err.Error(), "traversal limit")) {
				t.Fatalf("global traversal budget not enforced: %v", err)
			}
		})
	}
}
