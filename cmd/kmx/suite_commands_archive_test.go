package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	agentkitbuilder "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/agentkit"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func suiteCommandArchive(t *testing.T, attested bool) ([]byte, string, string, string) {
	t.Helper()
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	write := func(name string, data []byte) {
		t.Helper()
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	add := func(media string, body any) ocispec.Descriptor {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		hashString := "sha256:" + hex.EncodeToString(hash[:])
		write("blobs/sha256/"+strings.TrimPrefix(hashString, "sha256:"), data)
		return ocispec.Descriptor{MediaType: media, Digest: digest.Digest(hashString), Size: int64(len(data))}
	}
	config := add(ocispec.MediaTypeImageConfig, map[string]string{"os": "linux", "architecture": "amd64"})
	image := add(ocispec.MediaTypeImageManifest, map[string]any{"schemaVersion": 2, "config": config, "layers": []any{}})
	image.Platform = &ocispec.Platform{OS: "linux", Architecture: "amd64"}
	manifests := []ocispec.Descriptor{image}
	sbom := ""
	if attested {
		var layers []ocispec.Descriptor
		for _, predicate := range []string{"https://spdx.dev/Document", "https://slsa.dev/provenance/v1"} {
			layer := add("application/vnd.in-toto+json", map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": predicate, "predicate": map[string]any{"fixture": true}, "subject": []any{map[string]any{"digest": map[string]string{"sha256": image.Digest.Encoded()}}}})
			layers = append(layers, layer)
			if predicate == "https://spdx.dev/Document" {
				sbom = layer.Digest.String()
			}
		}
		att := add(ocispec.MediaTypeImageManifest, map[string]any{"schemaVersion": 2, "config": config, "layers": layers})
		att.Platform = &ocispec.Platform{OS: "unknown", Architecture: "unknown"}
		att.Annotations = map[string]string{"vnd.docker.reference.type": "attestation-manifest", "vnd.docker.reference.digest": image.Digest.String()}
		manifests = append(manifests, att)
	}
	index := add(ocispec.MediaTypeImageIndex, map[string]any{"schemaVersion": 2, "manifests": manifests})
	data, err := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []ocispec.Descriptor{index}})
	if err != nil {
		t.Fatal(err)
	}
	write("index.json", data)
	write("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), image.Digest.String(), index.Digest.String(), sbom
}

func TestSuiteBuildAttestationFlagsAndOutput(t *testing.T) {
	for _, tc := range []struct {
		name               string
		args               []string
		builder            string
		disabled, required bool
	}{
		{name: "default"},
		{name: "selected builder", args: []string{"--builder", "remote-builder"}, builder: "remote-builder"},
		{name: "opt out", args: []string{"--attestations=false"}, disabled: true},
		{name: "required", args: []string{"--require-attestations"}, required: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive, image, index, sbom := suiteCommandArchive(t, !tc.disabled)
			output := filepath.Join(t.TempDir(), "writer's image.oci.tar")
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			deps.newAgentKitBuilder = func(options agentkitbuilder.Options) agentsuite.SandboxBuilder {
				if options.Builder != tc.builder || options.DisableAttestations != tc.disabled || options.RequireAttestations != tc.required {
					t.Fatalf("options=%+v", options)
				}
				return sandboxBuilderFunc(func(_ context.Context, _ agentsuite.SandboxPlan, dst io.Writer) (agentsuite.BuildResult, error) {
					_, err := dst.Write(archive)
					return agentsuite.BuildResult{MediaType: agentsuite.OCIArchiveMediaType, AttestationsRequested: !tc.disabled, RequireAttestations: tc.required}, err
				})
			}
			args := append([]string{"suite", "build", filepath.Join("..", "..", "internal", "kmx", "agentsuite", "testdata", "minimal"), "--model-base-url", "https://models.example/v1", "--output", output}, tc.args...)
			if err := execute(args, deps); err != nil {
				t.Fatal(err)
			}
			if *loads != 0 {
				t.Fatalf("config loads=%d", *loads)
			}
			indexLabel := "Index digest (includes attestations; may vary per build): "
			if tc.disabled {
				indexLabel = "Index digest (no attestations): "
			}
			for _, want := range []string{"Image digest: " + image, indexLabel + index} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("stdout=%q missing %q", out.String(), want)
				}
			}
			if sbom != "" {
				command := "tar -xOf " + quoteShell(output) + " blobs/sha256/" + strings.TrimPrefix(sbom, "sha256:")
				if !strings.Contains(out.String(), command) {
					t.Fatalf("stdout=%q missing command=%q", out.String(), command)
				}
			} else if strings.Contains(out.String(), "tar -xOf") {
				t.Fatalf("unattested output claimed SBOM: %q", out.String())
			}
		})
	}
}
func TestSuiteBuildRejectsAttestationFlagConflicts(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"--attestations=false", "--require-attestations"}, want: "--require-attestations cannot be used with --attestations=false"},
		{args: []string{"--builder="}, want: "--builder cannot be empty"},
		{args: []string{"--builder", "  "}, want: "--builder cannot be empty"},
	} {
		var out, diagnostics bytes.Buffer
		deps, _ := testDependencies(&out, &diagnostics)
		created := false
		deps.newAgentKitBuilder = func(agentkitbuilder.Options) agentsuite.SandboxBuilder { created = true; return nil }
		args := append([]string{"suite", "build", "missing-suite", "--model-base-url", "https://models.example/v1", "--output", filepath.Join(t.TempDir(), "missing", "image.tar")}, tc.args...)
		err := execute(args, deps)
		if err == nil || !strings.Contains(err.Error(), tc.want) || created {
			t.Fatalf("error=%v builder created=%v", err, created)
		}
	}
}
func TestSuiteBuildHelpExplainsBuilderAndAttestations(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	if err := execute([]string{"suite", "build", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--builder", "--attestations", "--require-attestations", "SBOM", "provenance", "classic Docker image store", "OCI export", "does not enable", "image manifest", "index digest"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "--push") {
		t.Fatalf("build unexpectedly advertises push: %s", out.String())
	}
}
