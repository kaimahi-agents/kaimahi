package agentsuite

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestValidatePathAcceptsMinimalExtractedSuite(t *testing.T) {
	root := writeMinimalSuite(t)
	report, err := ValidatePath(root)
	if err != nil {
		t.Fatalf("ValidatePath() error = %v", err)
	}
	if report.Name != "example" || report.Agents != 1 || report.Tools != 0 || report.ToolSets != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestMinimalLayoutFixtureIsConformant(t *testing.T) {
	report, err := ValidatePath(filepath.Join("testdata", "minimal"))
	if err != nil {
		t.Fatalf("ValidatePath(testdata/minimal) error = %v", err)
	}
	if report.Name != "minimal" || report.Agents != 1 || report.Tools != 0 || report.ToolSets != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestValidatePathAllowsOnePinnedToolVariantToBeReusedByTwoAgents(t *testing.T) {
	root := t.TempDir()
	schemaBytes := []byte(`{"type":"object","additionalProperties":false}`)
	mustWrite(t, root, "schemas/read.json", schemaBytes)
	payload := []byte("#!/bin/sh\nprintf tool\n")
	mustWriteMode(t, root, "tools/reader/1.2.3/linux-amd64/bin/reader", payload, 0o755)

	variant := ToolVariant{
		Platform:    Platform{OS: "linux", Architecture: "amd64"},
		InstallRoot: "/opt/agentsuite/tools/reader",
		PayloadRoot: "tools/reader/1.2.3/linux-amd64",
		Entrypoint:  "/opt/agentsuite/tools/reader/bin/reader",
		Runtime:     RuntimeRequirement{ABI: "static", CPUBaseline: "x86-64-v1"},
		Files: []InventoryEntry{{
			Path: "bin/reader", Type: "file", Mode: 0o755, UID: 0, GID: 0,
			Size: int64(len(payload)), Digest: digestBytes(payload), Component: "reader",
		}},
	}
	var err error
	variant.VariantDigest, err = marshaledVariantDigest(variant)
	if err != nil {
		t.Fatal(err)
	}
	tool := Tool{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeTool,
		ID:            "reader",
		Version:       "1.2.3",
		Provider: ToolProvider{
			Protocol: "mcp", Revision: "2025-06-18",
			Operations: []ToolOperation{{
				Name: "read", InputSchema: FileRef{Path: "schemas/read.json", Digest: digestBytes(schemaBytes)},
			}},
		},
		Variants:   []ToolVariant{variant},
		Extensions: []Extension{},
	}
	toolDigest := mustWriteJSON(t, root, "tools/reader/1.2.3/tool.json", tool)
	catalog := ToolCatalog{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeToolCatalog,
		Tools: []ManifestRef{{
			ID: "reader", Version: "1.2.3", Path: "tools/reader/1.2.3/tool.json", Digest: toolDigest,
		}},
	}
	catalogDigest := mustWriteJSON(t, root, "tools/catalog.json", catalog)

	image := Descriptor{
		MediaType: ociManifestMediaType,
		Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Size:      1,
	}
	profile := BuildProfile{
		SchemaVersion: SpecVersion, MediaType: MediaTypeBuildProfile, ID: "default",
		RuntimeBase: []PlatformImage{{Platform: Platform{OS: "linux", Architecture: "amd64"}, Image: image}},
		Harness:     []PlatformImage{{Platform: Platform{OS: "linux", Architecture: "amd64"}, Image: image}},
		SourceEpoch: 1,
	}
	profileDigest := mustWriteJSON(t, root, "build-profiles/default.json", profile)

	var agentRefs []ManifestRef
	var toolSetRefs []ToolSetRef
	for _, id := range []string{"writer", "reviewer"} {
		instructions := []byte("Use the reader tool.\n")
		instructionPath := "instructions/" + id + ".md"
		mustWrite(t, root, instructionPath, instructions)
		agent := Agent{
			SchemaVersion: SpecVersion, MediaType: MediaTypeAgent, ID: id,
			Instructions: FileRef{Path: instructionPath, Digest: digestBytes(instructions)},
			Model:        ModelRequirement{Protocol: "openai-compatible", Model: "example-model"},
			Tools: []ToolRequirement{{
				ID: "reader", Version: "1.2.3", ExecutionMode: ExecutionInAgentSandbox,
			}},
			Invokes: []AgentInvoke{}, Extensions: []Extension{},
		}
		agentPath := "agents/" + id + ".json"
		agentRefs = append(agentRefs, ManifestRef{ID: id, Path: agentPath, Digest: mustWriteJSON(t, root, agentPath, agent)})
		toolSet := ToolSet{
			SchemaVersion: SpecVersion, MediaType: MediaTypeToolSet, Agent: id,
			Platform: Platform{OS: "linux", Architecture: "amd64"}, BuildProfile: "default",
			Tools: []LockedTool{{
				ID: "reader", Version: "1.2.3", ManifestDigest: toolDigest,
				VariantDigest: variant.VariantDigest, ExecutionMode: ExecutionInAgentSandbox,
			}},
		}
		toolSetPath := "tool-sets/" + id + "-linux-amd64.json"
		toolSetRefs = append(toolSetRefs, ToolSetRef{
			Agent: id, Platform: toolSet.Platform, Path: toolSetPath,
			Digest: mustWriteJSON(t, root, toolSetPath, toolSet),
		})
	}
	suite := Suite{
		SchemaVersion: SpecVersion, MediaType: MediaTypeSuite, Name: "shared-tool",
		Agents: agentRefs, ToolCatalog: ManifestRef{ID: "catalog", Path: "tools/catalog.json", Digest: catalogDigest},
		ToolSets: toolSetRefs,
		BuildProfiles: []ManifestRef{{
			ID: "default", Path: "build-profiles/default.json", Digest: profileDigest,
		}},
		Capabilities: []string{"bundled-stdio-mcp"}, Extensions: []Extension{},
	}
	mustWriteJSON(t, root, "agentsuite.json", suite)

	report, err := ValidatePath(root)
	if err != nil {
		t.Fatalf("ValidatePath() error = %v", err)
	}
	if report.Agents != 2 || report.Tools != 1 || report.ToolSets != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestValidatePathAcceptsOCILayout(t *testing.T) {
	contentRoot := writeMinimalSuite(t)
	tarBytes := tarDirectory(t, contentRoot)
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	if _, err := gzipWriter.Write(tarBytes); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	configDescriptor := writeOCIBlob(t, root, MediaTypeEmptyConfig, emptyConfigBytes)
	layerDescriptor := writeOCIBlob(t, root, MediaTypeContent, compressed.Bytes())
	manifest := ociManifest{
		SchemaVersion: 2,
		MediaType:     ociManifestMediaType,
		ArtifactType:  MediaTypeArtifact,
		Config:        configDescriptor,
		Layers:        []ociDescriptor{layerDescriptor},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDescriptor := writeOCIBlob(t, root, ociManifestMediaType, manifestBytes)
	manifestDescriptor.ArtifactType = MediaTypeArtifact
	indexBytes, err := json.Marshal(ociIndex{
		SchemaVersion: 2,
		MediaType:     ociIndexMediaType,
		Manifests:     []ociDescriptor{manifestDescriptor},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, root, "oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	mustWrite(t, root, "index.json", indexBytes)

	report, err := ValidatePath(root)
	if err != nil {
		t.Fatalf("ValidatePath() error = %v", err)
	}
	if report.Name != "example" || report.Agents != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestEmptyOCIConfigIsExact(t *testing.T) {
	if err := validateEmptyConfig([]byte("{}")); err != nil {
		t.Fatalf("validateEmptyConfig({}) error = %v", err)
	}
	for _, value := range [][]byte{[]byte("null"), []byte("{ }"), []byte("{}\n")} {
		if err := validateEmptyConfig(value); err == nil {
			t.Errorf("validateEmptyConfig(%q) succeeded", value)
		}
	}
}

func TestValidatePathRejectsDuplicateKeys(t *testing.T) {
	root := writeMinimalSuite(t)
	path := filepath.Join(root, "agentsuite.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"name":"example"`, `"name":"example","name":"shadow"`, 1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePath(root); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("expected duplicate-key error, got %v", err)
	}
}

func TestValidatePathRejectsUnknownFields(t *testing.T) {
	root := writeMinimalSuite(t)
	path := filepath.Join(root, "agentsuite.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"name":"example"`, `"name":"example","unexpected":true`, 1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePath(root); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestDecodeStrictRejectsInvalidUTF8AndMismatchedFieldCase(t *testing.T) {
	for _, data := range [][]byte{
		{0xff},
		[]byte(`{"Name":"example"}`),
		[]byte(`{"name":"example","Name":"shadow"}`),
		[]byte(`{"name":"example","ſame":"shadow"}`),
	} {
		var value struct {
			Name string `json:"name"`
		}
		if err := decodeStrict(data, &value); err == nil {
			t.Errorf("decodeStrict(%q) succeeded", data)
		}
	}
}

func TestContentSetRejectsUnicodeCaseFoldCollision(t *testing.T) {
	set := newContentSet()
	if err := set.add(contentEntry{Path: "tools/s"}); err != nil {
		t.Fatal(err)
	}
	if err := set.add(contentEntry{Path: "tools/ſ"}); err == nil || !strings.Contains(err.Error(), "case-folding path collision") {
		t.Fatalf("expected Unicode case-folding collision, got %v", err)
	}
}

func TestLoadContentLayerAllowsSymlinkMode0777(t *testing.T) {
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, header := range []*tar.Header{
		{Name: "bin/tool", Typeflag: tar.TypeReg, Mode: 0o755, Size: 1},
		{Name: "bin/tool-link", Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: "tool"},
	} {
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tarWriter.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := loadContentLayer(bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatalf("loadContentLayer() error = %v", err)
	}
	if content.entries["bin/tool-link"].Type != "symlink" {
		t.Fatalf("unexpected symlink entry: %+v", content.entries["bin/tool-link"])
	}
}

func TestShouldRetainMetadataMatchesJSONLimit(t *testing.T) {
	if !shouldRetainMetadata("tool.json", int64(maxJSONBytes)) {
		t.Fatal("JSON document at maxJSONBytes was not retained")
	}
	if shouldRetainMetadata("tool.json", int64(maxJSONBytes)+1) {
		t.Fatal("JSON document larger than maxJSONBytes was retained")
	}
}

func TestRawVariantDigestPreservesExplicitZeroValues(t *testing.T) {
	raw := []byte(`{
	  "variants":[{
	    "platform":{"os":"linux","architecture":"amd64"},
	    "variantDigest":"ignored",
	    "installRoot":"/opt/tool",
	    "relocatable":false,
	    "payloadRoot":"tools/tool",
	    "entrypoint":"/opt/tool/bin/tool",
	    "arguments":[],
	    "runtime":{"abi":"static","cpuBaseline":"x86-64-v1"},
	    "files":[{"path":"empty","type":"file","mode":420,"uid":0,"gid":0,"size":0}]
	  }]
	}`)
	digests, err := rawVariantDigests(raw)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	var variants []map[string]json.RawMessage
	if err := json.Unmarshal(document["variants"], &variants); err != nil {
		t.Fatal(err)
	}
	variants[0]["variantDigest"] = json.RawMessage(`""`)
	expectedJSON, err := json.Marshal(variants[0])
	if err != nil {
		t.Fatal(err)
	}
	expected, err := canonicalDigest(expectedJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(digests) != 1 || digests[0] != expected {
		t.Fatalf("rawVariantDigests() = %v, want %s", digests, expected)
	}
}

func TestOCIEmbeddedDataAndURLsPolicy(t *testing.T) {
	root := t.TempDir()
	data := []byte("{}")
	descriptor := writeOCIBlob(t, root, MediaTypeEmptyConfig, data)
	descriptor.Data = append([]byte(nil), data...)
	file, err := openBlob(root, descriptor)
	if err != nil {
		t.Fatalf("openBlob() with matching data error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	descriptor.URLs = []string{"https://example.invalid/blob"}
	if _, err := openBlob(root, descriptor); err == nil || !strings.Contains(err.Error(), "urls are not allowed") {
		t.Fatalf("expected descriptor urls rejection, got %v", err)
	}
}

func TestValidateReferencesRejectsDuplicateCapabilities(t *testing.T) {
	v := &validator{
		suite:  Suite{Capabilities: []string{"bundled-stdio-mcp", "bundled-stdio-mcp"}},
		agents: map[string]Agent{},
		tools: map[string]Tool{
			"tool@1.0.0": {Variants: []ToolVariant{{}}},
		},
		toolDigests: map[string]string{},
		toolSets:    map[string]ToolSet{},
	}
	if err := v.validateReferences(); err == nil || !strings.Contains(err.Error(), "suite capabilities") {
		t.Fatalf("expected duplicate capabilities rejection, got %v", err)
	}
}

func TestValidateSandboxBindingRejectsUnpinnedIdentity(t *testing.T) {
	data := []byte(`{
	  "schemaVersion":"1.0.0-draft",
	  "mediaType":"application/vnd.agentsuite.sandbox-binding.v1+json",
	  "suiteDigest":"latest",
	  "agent":"writer",
	  "platform":{"os":"linux","architecture":"amd64"},
	  "buildProfile":"default",
	  "toolSet":{"mediaType":"application/json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1},
	  "inventory":{"mediaType":"application/json","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":1}
	}`)
	if _, err := ValidateSandboxBinding(data); err == nil {
		t.Fatal("expected invalid suite digest to be rejected")
	}
}

func TestValidateContentPathRejectsTraversalAndAbsolutePaths(t *testing.T) {
	for _, value := range []string{"../secret", "a/../../secret", "/etc/passwd", `a\b`} {
		if _, err := validateContentPath(value); err == nil {
			t.Errorf("validateContentPath(%q) succeeded", value)
		}
	}
}

func writeMinimalSuite(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, root, "instructions/writer.md", []byte("Write clearly.\n"))
	instructionDigest := digestBytes([]byte("Write clearly.\n"))

	agent := Agent{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeAgent,
		ID:            "writer",
		Instructions:  FileRef{Path: "instructions/writer.md", Digest: instructionDigest},
		Model:         ModelRequirement{Protocol: "openai-compatible", Model: "example-model"},
		Tools:         []ToolRequirement{},
		Invokes:       []AgentInvoke{},
		Extensions:    []Extension{},
	}
	agentDigest := mustWriteJSON(t, root, "agents/writer.json", agent)

	catalog := ToolCatalog{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeToolCatalog,
		Tools:         []ManifestRef{},
	}
	catalogDigest := mustWriteJSON(t, root, "tools/catalog.json", catalog)

	image := Descriptor{
		MediaType: ociManifestMediaType,
		Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Size:      1,
	}
	profile := BuildProfile{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeBuildProfile,
		ID:            "default",
		RuntimeBase:   []PlatformImage{{Platform: Platform{OS: "linux", Architecture: "amd64"}, Image: image}},
		Harness:       []PlatformImage{{Platform: Platform{OS: "linux", Architecture: "amd64"}, Image: image}},
		SourceEpoch:   1,
	}
	profileDigest := mustWriteJSON(t, root, "build-profiles/default.json", profile)

	toolSet := ToolSet{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeToolSet,
		Agent:         "writer",
		Platform:      Platform{OS: "linux", Architecture: "amd64"},
		BuildProfile:  "default",
		Tools:         []LockedTool{},
	}
	toolSetDigest := mustWriteJSON(t, root, "tool-sets/writer-linux-amd64.json", toolSet)

	suite := Suite{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeSuite,
		Name:          "example",
		Agents: []ManifestRef{{
			ID: "writer", Path: "agents/writer.json", Digest: agentDigest,
		}},
		ToolCatalog: ManifestRef{ID: "catalog", Path: "tools/catalog.json", Digest: catalogDigest},
		ToolSets: []ToolSetRef{{
			Agent: "writer", Platform: Platform{OS: "linux", Architecture: "amd64"},
			Path: "tool-sets/writer-linux-amd64.json", Digest: toolSetDigest,
		}},
		BuildProfiles: []ManifestRef{{
			ID: "default", Path: "build-profiles/default.json", Digest: profileDigest,
		}},
		Capabilities: []string{},
		Extensions:   []Extension{},
	}
	mustWriteJSON(t, root, "agentsuite.json", suite)
	return root
}

func mustWriteJSON(t *testing.T, root, name string, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, root, name, data)
	digest, err := canonicalDigest(data)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func marshaledVariantDigest(variant ToolVariant) (string, error) {
	variant.VariantDigest = ""
	data, err := json.Marshal(variant)
	if err != nil {
		return "", err
	}
	return canonicalDigest(data)
}

func mustWrite(t *testing.T, root, name string, data []byte) {
	mustWriteMode(t, root, name, data, 0o644)
}

func mustWriteMode(t *testing.T, root, name string, data []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func writeOCIBlob(t *testing.T, root, mediaType string, data []byte) ociDescriptor {
	t.Helper()
	digest := digestBytes(data)
	mustWrite(t, root, "blobs/sha256/"+strings.TrimPrefix(digest, "sha256:"), data)
	return ociDescriptor{MediaType: mediaType, Digest: digest, Size: int64(len(data))}
}

func tarDirectory(t *testing.T, root string) []byte {
	t.Helper()
	var names []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			names = append(names, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, name := range names {
		info, err := os.Lstat(name)
		if err != nil {
			t.Fatal(err)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			t.Fatal(err)
		}
		header.Name = filepath.ToSlash(relative)
		header.Uid = 0
		header.Gid = 0
		header.ModTime = time.Unix(0, 0)
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
