package agentsuite

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	OCIArchiveMediaType       = "application/vnd.oci.image.layout.v1.tar"
	maxArchiveJSONBytes       = int64(64 << 20)
	maxArchiveObjectMembers   = 100_000
	maxArchiveDocumentMembers = 4_000_000
	inTotoMediaType           = "application/vnd.in-toto+json"
	spdxPredicate             = "https://spdx.dev/Document"
	provenancePredicatePrefix = "https://slsa.dev/provenance/"
)

type archiveEntry struct {
	offset, size int64
	digest       string
}
type imageArchive struct {
	ctx     context.Context
	source  io.ReaderAt
	entries map[string]archiveEntry
}

// InspectImageArchive verifies a self-contained OCI tar without extracting paths
// or retaining image layers. Only bounded JSON metadata is read into memory.
// Digest identifies the single runnable image; IndexDigest identifies its
// enclosing index (or index.json for an unwrapped layout).
func InspectImageArchive(ctx context.Context, source io.ReaderAt, size int64, platform Platform, requested, required bool) (BuildResult, error) {
	result := BuildResult{MediaType: OCIArchiveMediaType, AttestationsRequested: requested, RequireAttestations: required}
	archive, err := scanImageArchive(ctx, source, size)
	if err != nil {
		return BuildResult{}, err
	}
	layoutBytes, err := archive.readJSON("oci-layout")
	if err != nil {
		return BuildResult{}, err
	}
	var layout ociLayout
	if err := decodeArchiveJSON(layoutBytes, &layout); err != nil {
		return BuildResult{}, fmt.Errorf("oci-layout: %w", err)
	}
	if layout.ImageLayoutVersion != LayoutVersion {
		return BuildResult{}, errors.New("unsupported OCI layout version")
	}
	data, err := archive.readJSON("index.json")
	if err != nil {
		return BuildResult{}, err
	}
	var index ociIndex
	if err := decodeArchiveJSON(data, &index); err != nil {
		return BuildResult{}, fmt.Errorf("index.json: %w", err)
	}
	result.IndexDigest = digestBytes(data)
	if len(index.Manifests) == 1 && index.Manifests[0].MediaType == ociIndexMediaType {
		result.IndexDigest = index.Manifests[0].Digest
	}
	var images, attestations []ociDescriptor
	var visit func(ociIndex, int) error
	count := 0
	visit = func(index ociIndex, depth int) error {
		if depth > maxJSONDepth {
			return errors.New("OCI index nesting limit exceeded")
		}
		if index.SchemaVersion != 2 || index.MediaType != "" && index.MediaType != ociIndexMediaType {
			return errors.New("invalid OCI image index")
		}
		for _, d := range index.Manifests {
			count++
			if count > maxOCIIndexEntries {
				return errors.New("OCI index descriptor limit exceeded")
			}
			if err := archive.verify(d); err != nil {
				return err
			}
			switch d.MediaType {
			case ociIndexMediaType:
				body, err := archive.blobJSON(d)
				if err != nil {
					return err
				}
				var nested ociIndex
				if err := decodeArchiveJSON(body, &nested); err != nil {
					return fmt.Errorf("OCI index: %w", err)
				}
				if err := visit(nested, depth+1); err != nil {
					return err
				}
			case ociManifestMediaType:
				if d.Annotations["vnd.docker.reference.type"] == "attestation-manifest" {
					attestations = append(attestations, d)
				} else {
					images = append(images, d)
				}
			default:
				return fmt.Errorf("unsupported image descriptor media type %q", d.MediaType)
			}
		}
		return nil
	}
	if err := visit(index, 0); err != nil {
		return BuildResult{}, err
	}
	if len(images) != 1 {
		return BuildResult{}, fmt.Errorf("OCI archive must contain exactly one runnable image manifest, found %d", len(images))
	}
	image := images[0]
	manifest, err := archive.manifest(image)
	if err != nil {
		return BuildResult{}, err
	}
	configBytes, err := archive.blobJSON(manifest.Config)
	if err != nil {
		return BuildResult{}, err
	}
	var config struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	}
	if err := decodeArchiveJSON(configBytes, &config); err != nil {
		return BuildResult{}, fmt.Errorf("image config: %w", err)
	}
	configPlatform := Platform{OS: config.OS, Architecture: config.Architecture, Variant: config.Variant}
	if configPlatform != platform || image.Platform != nil && *image.Platform != platform {
		return BuildResult{}, fmt.Errorf("runnable image platform does not match selected platform %s", platform)
	}
	result.Digest = image.Digest
	result.HasAttestations = len(attestations) != 0
	hasProvenance := false
	for _, d := range attestations {
		if d.Annotations["vnd.docker.reference.digest"] != image.Digest {
			return BuildResult{}, errors.New("attestation reference does not bind to runnable image digest")
		}
		m, err := archive.manifest(d)
		if err != nil {
			return BuildResult{}, err
		}
		for _, layer := range m.Layers {
			if layer.MediaType != inTotoMediaType {
				continue
			}
			body, err := archive.blobJSON(layer)
			if err != nil {
				return BuildResult{}, err
			}
			var statement struct {
				Type          string `json:"_type"`
				PredicateType string `json:"predicateType"`
				Subject       []struct {
					Digest map[string]string `json:"digest"`
				} `json:"subject"`
				Predicate json.RawMessage `json:"predicate"`
			}
			if err := decodeArchiveJSONWithLimits(body, &statement, maxArchiveJSONBytes, maxArchiveObjectMembers, maxArchiveDocumentMembers); err != nil {
				return BuildResult{}, fmt.Errorf("attestation statement: %w", err)
			}
			if statement.Type != "https://in-toto.io/Statement/v0.1" && statement.Type != "https://in-toto.io/Statement/v1" {
				return BuildResult{}, errors.New("unsupported in-toto statement type")
			}
			if annotation := layer.Annotations["in-toto.io/predicate-type"]; annotation != "" && annotation != statement.PredicateType {
				return BuildResult{}, errors.New("attestation predicate does not match layer annotation")
			}
			if len(statement.Subject) == 0 {
				return BuildResult{}, errors.New("attestation statement has no subject")
			}
			for _, subject := range statement.Subject {
				if subject.Digest["sha256"] != strings.TrimPrefix(image.Digest, "sha256:") {
					return BuildResult{}, errors.New("attestation subject does not bind to runnable image digest")
				}
			}
			if len(statement.Predicate) == 0 || string(statement.Predicate) == "null" {
				return BuildResult{}, errors.New("attestation predicate is missing")
			}
			switch {
			case statement.PredicateType == spdxPredicate:
				result.SBOMDigest = layer.Digest
			case strings.HasPrefix(statement.PredicateType, provenancePredicatePrefix) && len(statement.PredicateType) > len(provenancePredicatePrefix):
				hasProvenance = true
			}
		}
	}
	if requested || required {
		var missing []string
		if result.SBOMDigest == "" {
			missing = append(missing, "SBOM")
		}
		if !hasProvenance {
			missing = append(missing, "provenance")
		}
		if len(missing) > 0 {
			detail := strings.Join(missing, " and ")
			if required {
				return BuildResult{}, fmt.Errorf("required attestations missing from OCI archive: %s", detail)
			}
			result.Warnings = append(result.Warnings, "requested attestations missing from OCI archive: "+detail)
		}
	}
	return result, nil
}

func scanImageArchive(ctx context.Context, source io.ReaderAt, size int64) (*imageArchive, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if size < 0 {
		return nil, errors.New("invalid OCI archive size")
	}
	reader := &archiveReader{ctx: ctx, reader: io.NewSectionReader(source, 0, size)}
	tr := tar.NewReader(reader)
	archive := &imageArchive{ctx: ctx, source: source, entries: map[string]archiveEntry{}}
	seen := map[string]bool{}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read OCI archive: %w", err)
		}
		name := header.Name
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if _, err := validateContentPath(name); err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate OCI archive path %s", name)
		}
		seen[name] = true
		if len(seen) > maxContentEntries {
			return nil, errors.New("OCI archive entry limit exceeded")
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("OCI archive path %s is not a regular file", name)
		}
		entry := archiveEntry{offset: reader.offset, size: header.Size}
		digest, written, err := digestReader(tr)
		if err != nil {
			return nil, err
		}
		if written != header.Size {
			return nil, errors.New("OCI archive entry size mismatch")
		}
		entry.digest = digest
		if strings.HasPrefix(name, "blobs/") {
			expected := "sha256:" + strings.TrimPrefix(name, "blobs/sha256/")
			if !strings.HasPrefix(name, "blobs/sha256/") || !validDigest(expected) || expected != digest {
				return nil, fmt.Errorf("OCI blob path or digest mismatch: %s", name)
			}
		}
		archive.entries[name] = entry
	}
	return archive, nil
}

func (a *imageArchive) verify(d ociDescriptor) error {
	if err := validateDescriptorFields(d); err != nil {
		return err
	}
	name := "blobs/sha256/" + strings.TrimPrefix(d.Digest, "sha256:")
	entry, ok := a.entries[name]
	if !ok || entry.size != d.Size || entry.digest != d.Digest {
		return fmt.Errorf("OCI blob missing or descriptor size/digest mismatch: %s", d.Digest)
	}
	return nil
}
func (a *imageArchive) readJSON(name string) ([]byte, error) {
	entry, ok := a.entries[name]
	if !ok {
		return nil, fmt.Errorf("OCI archive missing %s", name)
	}
	if entry.size > maxArchiveJSONBytes {
		return nil, fmt.Errorf("OCI JSON metadata exceeds %d bytes", maxArchiveJSONBytes)
	}
	reader := &archiveReader{ctx: a.ctx, reader: io.NewSectionReader(a.source, entry.offset, entry.size)}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != entry.size || digestBytes(data) != entry.digest {
		return nil, errors.New("OCI archive changed during inspection")
	}
	return data, nil
}
func (a *imageArchive) blobJSON(d ociDescriptor) ([]byte, error) {
	if err := a.verify(d); err != nil {
		return nil, err
	}
	return a.readJSON("blobs/sha256/" + strings.TrimPrefix(d.Digest, "sha256:"))
}
func (a *imageArchive) manifest(d ociDescriptor) (ociManifest, error) {
	body, err := a.blobJSON(d)
	if err != nil {
		return ociManifest{}, err
	}
	var m ociManifest
	if err := decodeArchiveJSON(body, &m); err != nil {
		return m, fmt.Errorf("OCI manifest: %w", err)
	}
	if m.SchemaVersion != 2 || m.MediaType != "" && m.MediaType != ociManifestMediaType {
		return m, errors.New("invalid OCI image manifest")
	}
	if err := a.verify(m.Config); err != nil {
		return m, err
	}
	for _, layer := range m.Layers {
		if err := a.verify(layer); err != nil {
			return m, err
		}
	}
	return m, nil
}
func decodeArchiveJSON(data []byte, out any) error {
	return decodeArchiveJSONWithLimits(data, out, maxJSONBytes, maxJSONObjectMembers, maxJSONDocumentMembers)
}

func decodeArchiveJSONWithLimits(data []byte, out any, byteLimit int64, objectLimit, documentLimit int) error {
	// OCI and in-toto may add fields independently of the AgentSuite schema.
	// Statements need larger inventories, but root arrays (manifests, layers,
	// subjects) must be bounded before typed allocation. Nested predicate arrays
	// remain governed by the byte, member, nesting, and duplicate-key budgets.
	if int64(len(data)) > byteLimit {
		return fmt.Errorf("OCI JSON metadata exceeds %d bytes", byteLimit)
	}
	if err := validateJSONTokensWithLimits(data, objectLimit, documentLimit, maxOCIIndexEntries); err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

type archiveReader struct {
	ctx    context.Context
	reader io.Reader
	offset int64
}

func (r *archiveReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	r.offset += int64(n)
	return n, err
}
