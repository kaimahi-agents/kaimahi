package app

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func buildSuiteArchive(t *testing.T, attested bool) ([]byte, string, string, string) {
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

func TestBuildSuiteInspectsArchiveBeforePublish(t *testing.T) {
	for _, tc := range []struct {
		name                                           string
		attested, requested, required, invalid, cancel bool
		wantErr, wantWarning                           string
	}{
		{name: "verified attestations", attested: true, requested: true, required: true},
		{name: "requested missing warns", requested: true, wantWarning: "SBOM and provenance"},
		{name: "required missing fails", requested: true, required: true, wantErr: "required attestations"},
		{name: "invalid archive fails", invalid: true, wantErr: "OCI archive"},
		{name: "canceled success fails", cancel: true, wantErr: "canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive, image, index, sbom := buildSuiteArchive(t, tc.attested)
			if tc.invalid {
				archive = []byte("not an OCI archive")
			}
			parent := t.TempDir()
			output := filepath.Join(parent, "image.oci.tar")
			if err := os.WriteFile(output, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			builder := sandboxBuilderFunc(func(_ context.Context, _ agentsuite.SandboxPlan, dst io.Writer) (agentsuite.BuildResult, error) {
				_, err := dst.Write(archive)
				if tc.cancel {
					cancel()
				}
				return agentsuite.BuildResult{MediaType: agentsuite.OCIArchiveMediaType, Digest: "untrusted", IndexDigest: "untrusted", SBOMDigest: "untrusted", AttestationsRequested: tc.requested, RequireAttestations: tc.required}, err
			})
			result, err := (&App{}).BuildSuite(ctx, minimalSuitePath(), output, agentsuite.BuildSelection{}, builder)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error=%v, want %q", err, tc.wantErr)
				}
				data, readErr := os.ReadFile(output)
				if readErr != nil || string(data) != "existing" {
					t.Fatalf("existing output changed: %q %v", data, readErr)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if result.Digest != image || result.IndexDigest != index || result.SBOMDigest != sbom {
					t.Fatalf("result=%+v", result)
				}
				if tc.wantWarning != "" && (len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], tc.wantWarning)) {
					t.Fatalf("warnings=%v", result.Warnings)
				}
			}
			staged, err := filepath.Glob(filepath.Join(parent, ".image.oci.tar-*"))
			if err != nil || len(staged) != 0 {
				t.Fatalf("staged=%v error=%v", staged, err)
			}
		})
	}
}
func TestBuildSuiteAtomicRenameFailureCleansStage(t *testing.T) {
	archive, _, _, _ := buildSuiteArchive(t, false)
	parent := t.TempDir()
	output := filepath.Join(parent, "image.oci.tar")
	builder := sandboxBuilderFunc(func(_ context.Context, _ agentsuite.SandboxPlan, dst io.Writer) (agentsuite.BuildResult, error) {
		_, err := dst.Write(archive)
		if err != nil {
			return agentsuite.BuildResult{}, err
		}
		if err := os.Mkdir(output, 0700); err != nil {
			return agentsuite.BuildResult{}, err
		}
		return agentsuite.BuildResult{MediaType: agentsuite.OCIArchiveMediaType}, nil
	})
	_, err := (&App{}).BuildSuite(t.Context(), minimalSuitePath(), output, agentsuite.BuildSelection{}, builder)
	if err == nil || !strings.Contains(err.Error(), "publish AgentSuite build output") {
		t.Fatalf("error=%v", err)
	}
	staged, err := filepath.Glob(filepath.Join(parent, ".image.oci.tar-*"))
	if err != nil || len(staged) != 0 {
		t.Fatalf("staged=%v error=%v", staged, err)
	}
}
func TestBuildSuiteCancellationIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	builder := sandboxBuilderFunc(func(context.Context, agentsuite.SandboxPlan, io.Writer) (agentsuite.BuildResult, error) {
		return agentsuite.BuildResult{}, context.Canceled
	})
	_, err := (&App{}).BuildSuite(ctx, minimalSuitePath(), filepath.Join(t.TempDir(), "image.tar"), agentsuite.BuildSelection{}, builder)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
