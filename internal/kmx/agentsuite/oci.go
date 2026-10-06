package agentsuite

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	ociManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	ociIndexMediaType    = "application/vnd.oci.image.index.v1+json"
	maxOCIIndexEntries   = 1000
	maxIndexedBlobBytes  = int64(1 << 30)
)

var emptyConfigBytes = []byte("{}")

type ociLayout struct {
	ImageLayoutVersion string `json:"imageLayoutVersion"`
}

type ociDescriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	Data         json.RawMessage   `json:"data,omitempty"`
	URLs         json.RawMessage   `json:"urls,omitempty"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Platform     *Platform         `json:"platform,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

type ociIndex struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	Manifests     []ociDescriptor   `json:"manifests"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

type ociManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	ArtifactType  string            `json:"artifactType"`
	Config        ociDescriptor     `json:"config"`
	Layers        []ociDescriptor   `json:"layers"`
	Subject       *ociDescriptor    `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// ValidatePath validates either an extracted AgentSuite content directory or
// an OCI image-layout directory containing one AgentSuite artifact.
func ValidatePath(value string) (*Report, error) {
	info, err := os.Stat(value)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("AgentSuite validation currently requires an extracted content directory or OCI image-layout directory")
	}
	if _, err := os.Stat(filepath.Join(value, "oci-layout")); err == nil {
		return validateOCILayout(value)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	content, err := loadDirectory(value)
	if err != nil {
		return nil, err
	}
	return validateContent(content)
}

func validateOCILayout(root string) (*Report, error) {
	layoutBytes, err := readRegularFile(filepath.Join(root, "oci-layout"), maxJSONBytes)
	if err != nil {
		return nil, fmt.Errorf("read oci-layout: %w", err)
	}
	var layout ociLayout
	if err := decodeStrict(layoutBytes, &layout); err != nil {
		return nil, fmt.Errorf("oci-layout: %w", err)
	}
	if layout.ImageLayoutVersion != LayoutVersion {
		return nil, fmt.Errorf("oci-layout imageLayoutVersion must be %s", LayoutVersion)
	}
	indexBytes, err := readRegularFile(filepath.Join(root, "index.json"), maxJSONBytes)
	if err != nil {
		return nil, fmt.Errorf("read index.json: %w", err)
	}
	var index ociIndex
	if err := decodeStrict(indexBytes, &index); err != nil {
		return nil, fmt.Errorf("index.json: %w", err)
	}
	if index.SchemaVersion != 2 || index.MediaType != "" && index.MediaType != ociIndexMediaType {
		return nil, errors.New("index.json is not an OCI image index")
	}
	if len(index.Manifests) > maxOCIIndexEntries {
		return nil, fmt.Errorf("index.json has more than %d manifest descriptors", maxOCIIndexEntries)
	}
	var candidates []ociDescriptor
	var indexedBytes int64
	for i, descriptor := range index.Manifests {
		if err := validateDescriptorFields(descriptor); err != nil {
			return nil, fmt.Errorf("index.json manifest descriptor %d: %w", i, err)
		}
		if descriptor.Size > maxIndexedBlobBytes-indexedBytes {
			return nil, fmt.Errorf("index.json references more than %d bytes", maxIndexedBlobBytes)
		}
		indexedBytes += descriptor.Size
		blob, err := openBlob(root, descriptor)
		if err != nil {
			return nil, fmt.Errorf("index.json manifest descriptor %d: %w", i, err)
		}
		if err := blob.Close(); err != nil {
			return nil, fmt.Errorf("index.json manifest descriptor %d: %w", i, err)
		}
		if descriptor.MediaType == ociManifestMediaType &&
			(descriptor.ArtifactType == "" || descriptor.ArtifactType == MediaTypeArtifact) {
			candidates = append(candidates, descriptor)
		}
	}
	if len(candidates) != 1 {
		return nil, fmt.Errorf("index.json must contain exactly one AgentSuite manifest descriptor, found %d", len(candidates))
	}
	manifestBytes, err := readBlob(root, candidates[0])
	if err != nil {
		return nil, fmt.Errorf("read AgentSuite manifest: %w", err)
	}
	var manifest ociManifest
	if err := decodeStrict(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("AgentSuite OCI manifest: %w", err)
	}
	if manifest.SchemaVersion != 2 || manifest.MediaType != "" && manifest.MediaType != ociManifestMediaType ||
		manifest.ArtifactType != MediaTypeArtifact {
		return nil, errors.New("OCI manifest is not an AgentSuite artifact")
	}
	if manifest.Subject != nil {
		return nil, errors.New("AgentSuite manifest must not use subject to bind a derived sandbox image")
	}
	if manifest.Config.MediaType != MediaTypeEmptyConfig {
		return nil, fmt.Errorf("AgentSuite config mediaType must be %s", MediaTypeEmptyConfig)
	}
	if len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != MediaTypeContent {
		return nil, fmt.Errorf("AgentSuite manifest must contain exactly one %s layer", MediaTypeContent)
	}
	if err := validateDescriptorFields(manifest.Config); err != nil {
		return nil, fmt.Errorf("AgentSuite config descriptor: %w", err)
	}
	if err := validateDescriptorFields(manifest.Layers[0]); err != nil {
		return nil, fmt.Errorf("AgentSuite content descriptor: %w", err)
	}
	configBytes, err := readBlob(root, manifest.Config)
	if err != nil {
		return nil, fmt.Errorf("read AgentSuite config: %w", err)
	}
	if err := validateEmptyConfig(configBytes); err != nil {
		return nil, err
	}
	layer, err := openBlob(root, manifest.Layers[0])
	if err != nil {
		return nil, fmt.Errorf("open AgentSuite content: %w", err)
	}
	content, layerErr := loadContentLayer(layer)
	closeErr := layer.Close()
	if layerErr != nil {
		return nil, layerErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	report, err := validateContent(content)
	if err != nil {
		return nil, err
	}
	return report, nil
}

func readRegularFile(name string, limit int64) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, fmt.Errorf("file must be regular and at most %d bytes", limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != info.Size() {
		return nil, errors.New("file size changed while reading")
	}
	return data, nil
}

func validateEmptyConfig(data []byte) error {
	if !bytes.Equal(data, emptyConfigBytes) {
		return errors.New("AgentSuite config blob must be the exact bytes {}")
	}
	return nil
}

func readBlob(root string, descriptor ociDescriptor) ([]byte, error) {
	if descriptor.Size > maxJSONBytes {
		return nil, fmt.Errorf("JSON blob exceeds %d bytes", maxJSONBytes)
	}
	file, err := openBlob(root, descriptor)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, descriptor.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != descriptor.Size {
		return nil, errors.New("blob size does not match descriptor")
	}
	if digestBytes(data) != descriptor.Digest {
		return nil, errors.New("blob digest does not match descriptor")
	}
	return data, nil
}

func openBlob(root string, descriptor ociDescriptor) (*os.File, error) {
	if err := validateDescriptorFields(descriptor); err != nil {
		return nil, err
	}
	embedded, hasEmbedded, err := descriptor.embeddedData()
	if err != nil {
		return nil, err
	}
	if hasEmbedded && (int64(len(embedded)) != descriptor.Size || digestBytes(embedded) != descriptor.Digest) {
		return nil, errors.New("descriptor data does not match digest and size")
	}
	encoded := strings.TrimPrefix(descriptor.Digest, "sha256:")
	blobPath := filepath.Join(root, "blobs", "sha256", encoded)
	file, err := os.Open(blobPath)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != descriptor.Size {
		file.Close()
		return nil, errors.New("blob is not a regular file of the descriptor size")
	}
	digest, _, err := digestReader(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	if digest != descriptor.Digest {
		file.Close()
		return nil, errors.New("blob digest does not match descriptor")
	}
	if hasEmbedded {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			file.Close()
			return nil, err
		}
		local, err := io.ReadAll(io.LimitReader(file, descriptor.Size+1))
		if err != nil {
			file.Close()
			return nil, err
		}
		if !bytes.Equal(embedded, local) {
			file.Close()
			return nil, errors.New("descriptor data does not match blob")
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func validateDescriptorFields(descriptor ociDescriptor) error {
	if !validDigest(descriptor.Digest) || descriptor.Size < 0 {
		return errors.New("descriptor digest or size is invalid")
	}
	if descriptor.URLs != nil {
		return errors.New("descriptor urls are not allowed in a self-contained AgentSuite layout")
	}
	embedded, hasEmbedded, err := descriptor.embeddedData()
	if err != nil {
		return err
	}
	if hasEmbedded && (int64(len(embedded)) != descriptor.Size || digestBytes(embedded) != descriptor.Digest) {
		return errors.New("descriptor data does not match digest and size")
	}
	return nil
}

func (d ociDescriptor) embeddedData() ([]byte, bool, error) {
	if d.Data == nil {
		return nil, false, nil
	}
	if bytes.Equal(bytes.TrimSpace(d.Data), []byte("null")) {
		return nil, false, errors.New("descriptor data must be a base64 string")
	}
	var data []byte
	if err := json.Unmarshal(d.Data, &data); err != nil {
		return nil, false, fmt.Errorf("descriptor data must be a base64 string: %w", err)
	}
	return data, true, nil
}

// ValidateSandboxBinding validates the fixed binding record embedded in a
// derived Agent Sandbox Image. Image filesystem and runtime probes are separate
// conformance operations.
func ValidateSandboxBinding(data []byte) (*SandboxBinding, error) {
	var binding SandboxBinding
	if err := decodeStrict(data, &binding); err != nil {
		return nil, err
	}
	if binding.SchemaVersion != SpecVersion || binding.MediaType != MediaTypeSandboxBinding {
		return nil, errors.New("sandbox binding has unsupported schemaVersion or mediaType")
	}
	if !validDigest(binding.SuiteDigest) || !identifierPattern.MatchString(binding.Agent) ||
		binding.BuildProfile == "" || !validDescriptor(binding.ToolSet) || !validDescriptor(binding.Inventory) {
		return nil, errors.New("sandbox binding identity or descriptors are invalid")
	}
	if err := validatePlatform(binding.Platform); err != nil {
		return nil, err
	}
	return &binding, nil
}

func validDescriptor(descriptor Descriptor) bool {
	return descriptor.MediaType != "" && validDigest(descriptor.Digest) && descriptor.Size >= 0
}

func digestReader(reader io.Reader) (string, int64, error) {
	hash := sha256.New()
	size, err := io.Copy(hash, reader)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, nil
}
