package localartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"reflect"
	"strings"
	"unicode/utf8"

	agentsuite "github.com/agent-primitives/suite"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var (
	ErrArtifact          = errors.New("bounded suite retrieval refused")
	ErrArtifactLimit     = errors.New("suite artifact exceeds loader limits")
	ErrArtifactAmbiguous = errors.New("suite artifact selection is ambiguous")
	ErrArtifactNotFound  = errors.New("selected suite artifact was not found")
)

const (
	manifestBytes   = 1 << 20
	compressedBytes = 9 << 20
	tarBytes        = 10 << 20
	fileBytes       = 1 << 20
	contentBytes    = 8 << 20
	entriesLimit    = 256
)

// MaxFiles fits 4 shared files, 5 files per member (including its eval index
// and optional policy), and 128 total cases.
const MaxFiles = 4 + 5*16 + 128

type Fetcher interface {
	Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error)
}

// Artifact retains verified original packaged bytes, not source or a re-archive.
type Artifact struct {
	descriptor ocispec.Descriptor
	files      map[string][]byte
	graph      *agentsuite.FlowV1Alpha1Graph
}

func (a *Artifact) Descriptor() ocispec.Descriptor { return cloneOCIDescriptor(a.descriptor) }

func (a *Artifact) Files() map[string][]byte {
	files := make(map[string][]byte, len(a.files))
	for name, data := range a.files {
		files[name] = bytes.Clone(data)
	}
	return files
}

type source interface {
	Fetcher
	read(context.Context, string) ([]byte, error)
}

// ReadLayout selects the sole root when digest is empty, or one exact root.
func ReadLayout(ctx context.Context, layout, digest string) (*Artifact, error) {
	return readLayout(ctx, layout, digest, false)
}

func readLayout(ctx context.Context, layout, digest string, flowMode bool) (a *Artifact, err error) {
	defer func() { err = artifactError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if digest != "" && !agentsuite.IsValidDigest(digest) {
		return nil, ErrArtifact
	}
	root, err := os.OpenRoot(layout)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return readSource(ctx, layoutSource{root: root}, digest, flowMode)
}

func readSource(ctx context.Context, source source, digest string, flowMode bool) (*Artifact, error) {
	layoutBytes, err := source.read(ctx, "oci-layout")
	if err != nil {
		return nil, err
	}
	var layoutRecord ocispec.ImageLayout
	if agentsuite.Unmarshal(layoutBytes, &layoutRecord) != nil || layoutRecord.Version != ocispec.ImageLayoutVersion {
		return nil, ErrArtifact
	}
	indexBytes, err := source.read(ctx, "index.json")
	if err != nil {
		return nil, err
	}
	var index indexEnvelope
	if agentsuite.Unmarshal(indexBytes, &index) != nil || index.SchemaVersion != 2 || index.MediaType != "" && index.MediaType != ocispec.MediaTypeImageIndex {
		return nil, ErrArtifact
	}
	if len(index.Manifests) > 1000 {
		return nil, ErrArtifactLimit
	}
	selected, err := selectRoot(index.Manifests, digest)
	if err != nil {
		return nil, err
	}
	rootDescriptor, err := selected.descriptor(manifestBytes)
	if err != nil {
		return nil, err
	}
	return read(ctx, source, rootDescriptor, rootDescriptor.Digest.String(), flowMode)
}

// Read verifies one selected AgentSuite graph from descriptor-addressed bytes.
func Read(ctx context.Context, source Fetcher, root ocispec.Descriptor, expected string) (*Artifact, error) {
	return read(ctx, source, root, expected, false)
}

func read(ctx context.Context, source Fetcher, root ocispec.Descriptor, expected string, flowMode bool) (a *Artifact, err error) {
	defer func() { err = artifactError(ctx, err) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil || !agentsuite.IsValidDigest(expected) || root.Digest.String() != expected || root.Size > manifestBytes {
		if root.Size > manifestBytes {
			return nil, ErrArtifactLimit
		}
		return nil, ErrArtifact
	}
	if root.ArtifactType != "" && root.ArtifactType != agentsuite.MediaTypeArtifact || !validDescriptor(root, ocispec.MediaTypeImageManifest, manifestBytes) {
		return nil, ErrArtifact
	}
	manifestBytes, err := readDescriptor(ctx, source, root)
	if err != nil {
		return nil, err
	}
	var manifest manifestEnvelope
	if agentsuite.Unmarshal(manifestBytes, &manifest) != nil || manifest.SchemaVersion != 2 || manifest.MediaType != "" && manifest.MediaType != ocispec.MediaTypeImageManifest || manifest.ArtifactType != agentsuite.MediaTypeArtifact || manifest.Subject != nil || len(manifest.Layers) != 1 {
		return nil, ErrArtifact
	}
	config, err := manifest.Config.descriptor(2)
	if err != nil {
		return nil, err
	}
	layer, err := manifest.Layers[0].descriptor(compressedBytes)
	if err != nil {
		return nil, err
	}
	if config.MediaType != agentsuite.MediaTypeEmptyConfig || config.Size != 2 || config.Digest != godigest.FromString("{}") || layer.MediaType != agentsuite.MediaTypeContent {
		return nil, ErrArtifact
	}
	configBytes, err := readDescriptor(ctx, source, config)
	if err != nil {
		return nil, err
	}
	layerBytes, err := readDescriptor(ctx, source, layer)
	if err != nil {
		return nil, err
	}
	files, err := contentFiles(ctx, layerBytes, flowMode)
	if err != nil {
		return nil, err
	}
	graph, err := validateSelectedGraph(ctx, root, manifestBytes, config, configBytes, layer, layerBytes, flowMode)
	if err != nil {
		return nil, err
	}
	return &Artifact{descriptor: cloneOCIDescriptor(root), files: files, graph: graph}, ctx.Err()
}

func validateSelectedGraph(ctx context.Context, root ocispec.Descriptor, manifestBytes []byte, config ocispec.Descriptor, configBytes []byte, layer ocispec.Descriptor, layerBytes []byte, flowMode bool) (graph *agentsuite.FlowV1Alpha1Graph, err error) {
	stage, err := os.MkdirTemp("", "kmx-artifact-*")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	if err := os.MkdirAll(pathOnDisk(stage, "blobs/sha256"), 0o700); err != nil {
		return nil, err
	}
	for _, item := range []struct {
		descriptor ocispec.Descriptor
		body       []byte
	}{{root, manifestBytes}, {config, configBytes}, {layer, layerBytes}} {
		if err := os.WriteFile(pathOnDisk(stage, "blobs/sha256/"+item.descriptor.Digest.Encoded()), item.body, 0o600); err != nil {
			return nil, err
		}
	}
	layoutBytes, err := agentsuite.Marshal(ocispec.ImageLayout{Version: ocispec.ImageLayoutVersion})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(pathOnDisk(stage, "oci-layout"), layoutBytes, 0o600); err != nil {
		return nil, err
	}
	selected := cloneOCIDescriptor(root)
	index := ocispec.Index{Manifests: []ocispec.Descriptor{selected}}
	index.SchemaVersion = 2
	index.MediaType = ocispec.MediaTypeImageIndex
	indexBytes, err := json.Marshal(index)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(pathOnDisk(stage, "index.json"), indexBytes, 0o600); err != nil {
		return nil, err
	}
	if flowMode {
		graph, err = agentsuite.ValidatePathFlowV1Alpha1(stage)
	} else {
		_, err = agentsuite.ValidatePath(stage)
	}
	if err != nil {
		return nil, err
	}
	return graph, ctx.Err()
}

func pathOnDisk(root, slashPath string) string {
	return root + string(os.PathSeparator) + strings.ReplaceAll(slashPath, "/", string(os.PathSeparator))
}

// Files reads the bounded AgentSuite content layer and retains regular files.
// Confined links are validated but do not create aliases in the returned map.
func Files(ctx context.Context, compressed []byte) (map[string][]byte, error) {
	return contentFiles(ctx, compressed, false)
}

func contentFiles(ctx context.Context, compressed []byte, flowMode bool) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(compressed) > compressedBytes {
		return nil, ErrArtifactLimit
	}
	input := bytes.NewReader(compressed)
	gzipReader, err := gzip.NewReader(input)
	if err != nil {
		return nil, ErrArtifact
	}
	gzipReader.Multistream(false)
	raw, readErr := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: gzipReader}, tarBytes+1))
	if err := errors.Join(readErr, gzipReader.Close()); err != nil {
		return nil, err
	}
	if len(raw) > tarBytes {
		return nil, ErrArtifactLimit
	}
	if input.Len() != 0 {
		return nil, ErrArtifact
	}
	if len(raw) < 1024 || len(raw)%512 != 0 || len(bytes.Trim(raw[len(raw)-1024:], "\x00")) != 0 {
		return nil, ErrArtifact
	}
	stream := bytes.NewReader(raw[:len(raw)-1024])
	reader := tar.NewReader(stream)
	files := map[string][]byte{}
	types := map[string]byte{}
	regular := map[string]bool{}
	total := int64(0)
	for entries := 0; ; entries++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		offset := (len(raw) - 1024 - stream.Len() + 511) / 512 * 512
		if offset+512 <= len(raw)-1024 {
			kind := raw[offset+156]
			if kind != tar.TypeReg && kind != 0 && kind != tar.TypeDir && kind != tar.TypeSymlink && kind != tar.TypeLink {
				return nil, ErrArtifact
			}
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrArtifact
		}
		if entries >= entriesLimit || len(header.Name) > 1024 {
			return nil, ErrArtifactLimit
		}
		name, err := contentPath(header)
		_, duplicate := types[name]
		if err != nil || len(header.PAXRecords) != 0 || duplicate || contentPathConflict(name, header.Typeflag, types) {
			return nil, ErrArtifact
		}
		types[name] = header.Typeflag
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return nil, ErrArtifact
			}
		case tar.TypeReg, 0:
			limit := int64(fileBytes)
			if flowMode && name == agentsuite.FlowPathV1Alpha1 {
				limit = 4 << 20
			}
			if len(files) >= MaxFiles || header.Size > limit || header.Size > contentBytes-total {
				return nil, ErrArtifactLimit
			}
			if header.Size < 0 {
				return nil, ErrArtifact
			}
			data, err := io.ReadAll(&contextReader{ctx: ctx, reader: reader})
			if err != nil || int64(len(data)) != header.Size {
				return nil, ErrArtifact
			}
			files[name] = data
			regular[name] = true
			total += header.Size
		case tar.TypeSymlink:
			if header.Size != 0 || !validLinkTarget(name, header.Linkname) {
				return nil, ErrArtifact
			}
		case tar.TypeLink:
			target, err := normalizedContentPath(header.Linkname)
			if err != nil || header.Size != 0 || !regular[target] {
				return nil, ErrArtifact
			}
		default:
			return nil, ErrArtifact
		}
	}
	if stream.Len() != 0 && len(bytes.Trim(raw[len(raw)-1024-stream.Len():len(raw)-1024], "\x00")) != 0 {
		return nil, ErrArtifact
	}
	return files, nil
}

func contentPath(header *tar.Header) (string, error) {
	name := header.Name
	if header.Typeflag == tar.TypeDir {
		name = strings.TrimSuffix(name, "/")
	}
	return normalizedContentPath(name)
}

func normalizedContentPath(name string) (string, error) {
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") || path.IsAbs(name) || path.Clean(name) != name || !fs.ValidPath(name) || name == "." {
		return "", ErrArtifact
	}
	return name, nil
}

func contentPathConflict(name string, kind byte, types map[string]byte) bool {
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if parentKind, ok := types[parent]; ok && parentKind != tar.TypeDir {
			return true
		}
	}
	if kind == tar.TypeDir {
		return false
	}
	prefix := name + "/"
	for existing := range types {
		if strings.HasPrefix(existing, prefix) {
			return true
		}
	}
	return false
}

func validLinkTarget(name, target string) bool {
	if target == "" || len(target) > 1024 || !utf8.ValidString(target) || strings.ContainsRune(target, '\x00') || strings.Contains(target, "\\") || path.IsAbs(target) {
		return false
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	return resolved != "." && resolved != ".." && !strings.HasPrefix(resolved, "../")
}

func readDescriptor(ctx context.Context, source Fetcher, descriptor ocispec.Descriptor) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reader, err := source.Fetch(ctx, descriptor)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: reader}, descriptor.Size+1))
	if err := errors.Join(readErr, reader.Close()); err != nil {
		return nil, err
	}
	if int64(len(raw)) != descriptor.Size || godigest.FromBytes(raw) != descriptor.Digest || descriptor.Data != nil && !bytes.Equal(raw, descriptor.Data) {
		return nil, ErrArtifact
	}
	return raw, ctx.Err()
}

func validDescriptor(descriptor ocispec.Descriptor, mediaType string, limit int64) bool {
	return descriptor.MediaType == mediaType && agentsuite.IsValidDigest(descriptor.Digest.String()) && descriptor.Size >= 0 && descriptor.Size <= limit && descriptor.URLs == nil && (descriptor.Data == nil || int64(len(descriptor.Data)) == descriptor.Size && godigest.FromBytes(descriptor.Data) == descriptor.Digest)
}

func selectRoot(roots []descriptorEnvelope, digest string) (descriptorEnvelope, error) {
	var selected descriptorEnvelope
	found := false
	for _, root := range roots {
		if digest != "" && root.Digest != digest {
			continue
		}
		if !found {
			selected, found = root, true
			continue
		}
		if descriptorEnvelopeEqual(selected, root) {
			continue
		}
		if digest == "" {
			return descriptorEnvelope{}, ErrArtifactAmbiguous
		}
		return descriptorEnvelope{}, ErrArtifact
	}
	if !found {
		return descriptorEnvelope{}, ErrArtifactNotFound
	}
	return selected, nil
}

func descriptorEnvelopeEqual(a, b descriptorEnvelope) bool {
	return a.MediaType == b.MediaType && a.Digest == b.Digest && a.Size == b.Size && a.ArtifactType == b.ArtifactType && bytes.Equal(a.Data, b.Data) && reflect.DeepEqual(a.URLs, b.URLs) && reflect.DeepEqual(a.Platform, b.Platform)
}

type indexEnvelope struct {
	SchemaVersion int                  `json:"schemaVersion"`
	MediaType     string               `json:"mediaType,omitempty"`
	Manifests     []descriptorEnvelope `json:"manifests"`
	Annotations   map[string]string    `json:"annotations,omitempty"`
}

type manifestEnvelope struct {
	SchemaVersion int                  `json:"schemaVersion"`
	MediaType     string               `json:"mediaType,omitempty"`
	ArtifactType  string               `json:"artifactType"`
	Config        descriptorEnvelope   `json:"config"`
	Layers        []descriptorEnvelope `json:"layers"`
	Subject       json.RawMessage      `json:"subject,omitempty"`
	Annotations   map[string]string    `json:"annotations,omitempty"`
}

type descriptorEnvelope struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	Data         json.RawMessage   `json:"data,omitempty"`
	URLs         json.RawMessage   `json:"urls,omitempty"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Platform     *ocispec.Platform `json:"platform,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

func (d *descriptorEnvelope) UnmarshalJSON(data []byte) error {
	type plain descriptorEnvelope
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"mediaType", "digest", "size"} {
		raw, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrArtifact
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded plain
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*d = descriptorEnvelope(decoded)
	return nil
}

func (d descriptorEnvelope) descriptor(limit int64) (ocispec.Descriptor, error) {
	if d.Size > limit {
		return ocispec.Descriptor{}, ErrArtifactLimit
	}
	if !agentsuite.IsValidDigest(d.Digest) || d.Size < 0 || d.URLs != nil {
		return ocispec.Descriptor{}, ErrArtifact
	}
	var embedded []byte
	if d.Data != nil {
		if bytes.Equal(bytes.TrimSpace(d.Data), []byte("null")) || json.Unmarshal(d.Data, &embedded) != nil || int64(len(embedded)) != d.Size || godigest.FromBytes(embedded).String() != d.Digest {
			return ocispec.Descriptor{}, ErrArtifact
		}
	}
	descriptor := ocispec.Descriptor{MediaType: d.MediaType, Digest: godigest.Digest(d.Digest), Size: d.Size, Data: embedded, ArtifactType: d.ArtifactType, Platform: clonePlatform(d.Platform)}
	if d.Annotations != nil {
		descriptor.Annotations = make(map[string]string, len(d.Annotations))
		for key, value := range d.Annotations {
			descriptor.Annotations[key] = value
		}
	}
	return descriptor, nil
}

func clonePlatform(platform *ocispec.Platform) *ocispec.Platform {
	if platform == nil {
		return nil
	}
	clone := *platform
	clone.OSFeatures = append([]string(nil), platform.OSFeatures...)
	return &clone
}

func cloneOCIDescriptor(descriptor ocispec.Descriptor) ocispec.Descriptor {
	descriptor.Data = bytes.Clone(descriptor.Data)
	descriptor.URLs = append([]string(nil), descriptor.URLs...)
	descriptor.Platform = clonePlatform(descriptor.Platform)
	if descriptor.Annotations != nil {
		annotations := make(map[string]string, len(descriptor.Annotations))
		for key, value := range descriptor.Annotations {
			annotations[key] = value
		}
		descriptor.Annotations = annotations
	}
	return descriptor
}

func artifactError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if cause := ctx.Err(); cause != nil {
		return errors.Join(ErrArtifact, cause)
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, ErrArtifactLimit, ErrArtifactAmbiguous, ErrArtifactNotFound} {
		if errors.Is(err, cause) {
			return errors.Join(ErrArtifact, cause)
		}
	}
	return ErrArtifact
}

type layoutSource struct{ root *os.Root }

func (s layoutSource) read(ctx context.Context, name string) ([]byte, error) {
	file, err := s.open(name)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(&contextReader{ctx: ctx, reader: file})
	return data, errors.Join(readErr, file.Close())
}

func (s layoutSource) Fetch(ctx context.Context, descriptor ocispec.Descriptor) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !agentsuite.IsValidDigest(descriptor.Digest.String()) {
		return nil, ErrArtifact
	}
	return s.open("blobs/sha256/" + descriptor.Digest.Encoded())
}

func (s layoutSource) open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, ErrArtifact
	}
	parts := strings.Split(name, "/")
	var before fs.FileInfo
	for i := range parts {
		info, err := s.root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrArtifact
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, ErrArtifact
		}
		before = info
	}
	limit := int64(compressedBytes)
	if name == "index.json" {
		limit = manifestBytes
	}
	if name == "oci-layout" {
		limit = 1024
	}
	if before == nil || !before.Mode().IsRegular() || before.Size() < 0 {
		return nil, ErrArtifact
	}
	if before.Size() > limit {
		return nil, ErrArtifactLimit
	}
	file, err := s.root.OpenFile(name, os.O_RDONLY|readFlags, 0)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() != before.Size() {
		file.Close()
		return nil, ErrArtifact
	}
	return &boundedFile{File: file, remaining: limit + 1}, nil
}

type boundedFile struct {
	*os.File
	remaining int64
}

func (file *boundedFile) Read(buffer []byte) (int, error) {
	if file.remaining <= 0 {
		return 0, ErrArtifactLimit
	}
	if int64(len(buffer)) > file.remaining {
		buffer = buffer[:file.remaining]
	}
	read, err := file.File.Read(buffer)
	file.remaining -= int64(read)
	return read, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
