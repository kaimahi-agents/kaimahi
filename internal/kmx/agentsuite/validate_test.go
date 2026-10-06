package agentsuite

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
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
	if report.Name != "example" || report.Agents != 1 || report.Tools != 0 || report.Compositions != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestCoordinatorWorkersFixtureIsConformant(t *testing.T) {
	root := filepath.Join("testdata", "coordinator-workers")
	report, err := ValidatePath(root)
	if err != nil {
		t.Fatalf("ValidatePath(%s) error = %v", root, err)
	}
	if report.Name != "coordinator-workers" || report.Agents != 3 || report.Tools != 0 || report.Compositions != 3 {
		t.Fatalf("unexpected report: %+v", report)
	}

	readAgent := func(name string) Agent {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, "agents", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var agent Agent
		if err := decodeStrict(data, &agent); err != nil {
			t.Fatal(err)
		}
		return agent
	}
	coordinator := readAgent("coordinator")
	writer := readAgent("writer")
	reviewer := readAgent("reviewer")
	if len(coordinator.Invokes) != 2 ||
		coordinator.Invokes[0].Agent != "writer" ||
		coordinator.Invokes[1].Agent != "reviewer" {
		t.Fatalf("coordinator invocation edges = %+v, want coordinator -> writer and coordinator -> reviewer", coordinator.Invokes)
	}
	if len(writer.Invokes) != 0 {
		t.Fatalf("writer must not invoke another agent: %+v", writer.Invokes)
	}
	if len(reviewer.Invokes) != 0 {
		t.Fatalf("reviewer must not invoke another agent: %+v", reviewer.Invokes)
	}
}

func TestValidateAgentRejectsSelfInvocation(t *testing.T) {
	agent := Agent{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeAgent,
		ID:            "coordinator",
		Instructions: FileRef{
			Path:   "instructions/coordinator.md",
			Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		Model:   ModelRequirement{Protocol: "openai-compatible", Model: "example-model"},
		Invokes: []AgentInvoke{{Agent: "coordinator", MaxConcurrent: 1, MaxDepth: 1}},
	}
	if err := validateAgent(agent); err == nil || !strings.Contains(err.Error(), "invoked agent") {
		t.Fatalf("expected self-invocation rejection, got %v", err)
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
	variant.VariantDigest = mustVariantDigest(t, variant)
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
	var compositionRefs []CompositionRef
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
		composition := Composition{
			SchemaVersion: SpecVersion, MediaType: MediaTypeComposition, Agent: id,
			Platform: Platform{OS: "linux", Architecture: "amd64"}, BuildProfile: "default",
			Tools: []ResolvedTool{{
				ID: "reader", Version: "1.2.3", ManifestDigest: toolDigest,
				VariantDigest: variant.VariantDigest, ExecutionMode: ExecutionInAgentSandbox,
			}},
		}
		compositionPath := "compositions/" + id + "-linux-amd64.json"
		compositionRefs = append(compositionRefs, CompositionRef{
			Agent: id, Platform: composition.Platform, Path: compositionPath,
			Digest: mustWriteJSON(t, root, compositionPath, composition),
		})
	}
	suite := Suite{
		SchemaVersion: SpecVersion, MediaType: MediaTypeSuite, Name: "shared-tool",
		Agents: agentRefs, ToolCatalog: ManifestRef{ID: "catalog", Path: "tools/catalog.json", Digest: catalogDigest},
		Compositions: compositionRefs,
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
	if report.Agents != 2 || report.Tools != 1 || report.Compositions != 2 {
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
	configDescriptor.Data = json.RawMessage(`"e30="`)
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

func TestDecodeStrictRejectsInvalidUnicodeAndLossyNumbers(t *testing.T) {
	for _, data := range [][]byte{
		{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'},
		[]byte(`{"value":"\ud800"}`),
		[]byte(`{"value":"\ud800\u0061"}`),
	} {
		var value map[string]any
		if err := decodeStrict(data, &value); err == nil {
			t.Errorf("decodeStrict(%q) succeeded", data)
		}
	}
	var value map[string]any
	if err := decodeStrict([]byte(`{"value":"�"}`), &value); err != nil {
		t.Fatalf("literal replacement character was rejected: %v", err)
	}
	if err := decodeStrict([]byte(`{"value":333333333.33333329}`), &value); err != nil {
		t.Fatalf("valid JCS number was rejected: %v", err)
	}
	if err := decodeStrict([]byte(`{"value":9007199254740993}`), &value); err == nil {
		t.Fatal("lossy integral JCS value succeeded")
	}
}

func TestDecodeStrictRequiresExactFieldNamesButAllowsMapKeyCase(t *testing.T) {
	target := struct {
		Name        string            `json:"name"`
		Annotations map[string]string `json:"annotations"`
	}{}
	if err := decodeStrict([]byte(`{"Name":"example"}`), &target); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("mis-cased field error = %v", err)
	}
	if err := decodeStrict([]byte(`{"name":"example","Name":"shadow"}`), &target); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("case-shadowed field error = %v", err)
	}
	if err := decodeStrict([]byte(`{"name":"example","annotations":{"Foo":"one","foo":"two"}}`), &target); err != nil {
		t.Fatalf("case-distinct map keys were rejected: %v", err)
	}
	if err := decodeStrict([]byte(`{"name":null}`), &target); err == nil || !strings.Contains(err.Error(), "null") {
		t.Fatalf("null scalar field error = %v", err)
	}
}

func TestDecodeStrictEnforcesDocumentLimits(t *testing.T) {
	if maxJSONBytes != 4<<20 || maxJSONDepth != 100 {
		t.Fatalf("JSON limits = %d bytes and %d levels", maxJSONBytes, maxJSONDepth)
	}
	for depth, wantError := range map[int]bool{100: false, 101: true} {
		data := []byte(strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth))
		var value any
		err := decodeStrict(data, &value)
		if (err != nil) != wantError {
			t.Errorf("depth %d error = %v, wantError %t", depth, err, wantError)
		}
	}
	var members strings.Builder
	members.WriteByte('{')
	for i := 0; i <= maxJSONObjectMembers; i++ {
		if i != 0 {
			members.WriteByte(',')
		}
		fmt.Fprintf(&members, "%q:0", fmt.Sprintf("k%d", i))
	}
	members.WriteByte('}')
	var value any
	if err := decodeStrict([]byte(members.String()), &value); err == nil || !strings.Contains(err.Error(), "member limit") {
		t.Fatalf("object member limit error = %v", err)
	}
}

func TestContentSetRejectsUnicodeCaseFoldCollision(t *testing.T) {
	set := &contentSet{entries: map[string]contentEntry{}, folded: map[string]string{}}
	if err := set.add(contentEntry{Path: "tools/s"}); err != nil {
		t.Fatal(err)
	}
	if err := set.add(contentEntry{Path: "tools/ſ"}); err == nil || !strings.Contains(err.Error(), "case-folding") {
		t.Fatalf("case-fold collision error = %v", err)
	}
	if err := set.add(contentEntry{Path: "tools/ı"}); err != nil {
		t.Fatalf("distinct dotless-i path collided: %v", err)
	}
}

func TestValidateLinkTargetRejectsOversizedTarget(t *testing.T) {
	if err := validateLinkTarget("bin/tool", strings.Repeat("a", maxPathBytes+1)); err == nil {
		t.Fatal("oversized link target succeeded")
	}
}

func TestRetainedMetadataLimits(t *testing.T) {
	if !shouldRetainMetadata("metadata/value.json", maxJSONBytes) || shouldRetainMetadata("metadata/value.json", maxJSONBytes+1) {
		t.Fatal("metadata retention is not bounded by the JSON document limit")
	}
	if got, err := addRetainedMetadata(maxRetainedMetadataBytes-1, 1); err != nil || got != maxRetainedMetadataBytes {
		t.Fatalf("exact retained metadata limit = %d, %v", got, err)
	}
	if _, err := addRetainedMetadata(maxRetainedMetadataBytes, 1); err == nil {
		t.Fatal("retained metadata above the cumulative limit succeeded")
	}
}

func TestContentLayerAllowsWritableSymlinkMode(t *testing.T) {
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	entries := []tar.Header{
		{Name: "bin/tool", Typeflag: tar.TypeReg, Mode: 0o755, Size: 1},
		{Name: "bin/tool-link", Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: "tool"},
	}
	for i := range entries {
		if err := tarWriter.WriteHeader(&entries[i]); err != nil {
			t.Fatal(err)
		}
		if entries[i].Typeflag == tar.TypeReg {
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
	if got := content.entries["bin/tool-link"]; got.Type != "symlink" || got.Mode != 0o777 {
		t.Fatalf("symlink entry = %+v", got)
	}
}

func TestContentLayerAllowsDirectoryTrailingSlash(t *testing.T) {
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "agents/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
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
	if got := content.entries["agents"]; got.Type != "directory" {
		t.Fatalf("directory entry = %+v", got)
	}
}

func TestContentLayerRejectsInvalidGzipTrailer(t *testing.T) {
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "metadata/value.json", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	data := compressed.Bytes()
	data[len(data)-1] ^= 0xff
	if _, err := loadContentLayer(bytes.NewReader(data)); err == nil || !strings.Contains(err.Error(), "gzip") {
		t.Fatalf("invalid gzip trailer error = %v", err)
	}
}

func TestVariantAllowsWritableSymlinkMode(t *testing.T) {
	payload := []byte("x")
	digest := digestBytes(payload)
	variant := ToolVariant{
		Platform:      Platform{OS: "linux", Architecture: "amd64"},
		VariantDigest: digest,
		InstallRoot:   "/opt/tool",
		PayloadRoot:   "payload",
		Entrypoint:    "/opt/tool/bin/tool",
		Runtime:       RuntimeRequirement{ABI: "static", CPUBaseline: "x86-64-v1"},
		Files: []InventoryEntry{
			{Path: "bin/tool", Type: "file", Mode: 0o755, Size: 1, Digest: digest},
			{Path: "bin/tool-link", Type: "symlink", Mode: 0o777, LinkTarget: "tool"},
		},
	}
	content := &contentSet{entries: map[string]contentEntry{
		"payload/bin/tool":      {Path: "payload/bin/tool", Type: "file", Mode: 0o755, Size: 1, Digest: digest},
		"payload/bin/tool-link": {Path: "payload/bin/tool-link", Type: "symlink", Mode: 0o777, LinkTarget: "tool"},
	}}
	if err := validateVariant(variant, digest, content); err != nil {
		t.Fatalf("validateVariant() error = %v", err)
	}
}

func TestVariantRejectsPayloadOwnerMismatch(t *testing.T) {
	payload := []byte("x")
	digest := digestBytes(payload)
	variant := ToolVariant{
		Platform:      Platform{OS: "linux", Architecture: "amd64"},
		VariantDigest: digest,
		InstallRoot:   "/opt/tool",
		PayloadRoot:   "payload",
		Entrypoint:    "/opt/tool/bin/tool",
		Runtime:       RuntimeRequirement{ABI: "static", CPUBaseline: "x86-64-v1"},
		Files: []InventoryEntry{{
			Path: "bin/tool", Type: "file", Mode: 0o755, UID: 0, GID: 0, Size: 1, Digest: digest,
		}},
	}
	content := &contentSet{entries: map[string]contentEntry{
		"payload/bin/tool": {Path: "payload/bin/tool", Type: "file", Mode: 0o755, UID: 1000, GID: 0, Size: 1, Digest: digest},
	}}
	if err := validateVariant(variant, digest, content); err == nil || !strings.Contains(err.Error(), "payload metadata") {
		t.Fatalf("owner mismatch error = %v", err)
	}
}

func TestOCIContentRejectsDescriptorURLsAndMismatchedData(t *testing.T) {
	root := t.TempDir()
	descriptor := writeOCIBlob(t, root, MediaTypeEmptyConfig, emptyConfigBytes)
	descriptor.URLs = json.RawMessage(`[]`)
	if _, err := readBlob(root, descriptor); err == nil || !strings.Contains(err.Error(), "urls") {
		t.Fatalf("descriptor URL error = %v", err)
	}
	descriptor.URLs = nil
	descriptor.Data = json.RawMessage(`"W10="`)
	if _, err := readBlob(root, descriptor); err == nil || !strings.Contains(err.Error(), "descriptor data") {
		t.Fatalf("descriptor data error = %v", err)
	}
}

func TestOCILayoutRejectsURLOnIgnoredIndexDescriptor(t *testing.T) {
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
	manifestBytes, err := json.Marshal(ociManifest{
		SchemaVersion: 2, MediaType: ociManifestMediaType, ArtifactType: MediaTypeArtifact,
		Config: configDescriptor, Layers: []ociDescriptor{layerDescriptor},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestDescriptor := writeOCIBlob(t, root, ociManifestMediaType, manifestBytes)
	manifestDescriptor.ArtifactType = MediaTypeArtifact
	ignored := writeOCIBlob(t, root, "application/vnd.example.other", []byte("other"))
	ignored.URLs = json.RawMessage(`["https://example.invalid/blob"]`)
	indexBytes, err := json.Marshal(ociIndex{SchemaVersion: 2, MediaType: ociIndexMediaType, Manifests: []ociDescriptor{manifestDescriptor, ignored}})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, root, "oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	mustWrite(t, root, "index.json", indexBytes)
	if _, err := ValidatePath(root); err == nil || !strings.Contains(err.Error(), "urls") {
		t.Fatalf("ignored descriptor URL error = %v", err)
	}
}

func TestRawVariantDigestsPreserveExplicitZeroValues(t *testing.T) {
	declared := "sha256:" + strings.Repeat("a", 64)
	raw := []byte(`{"variants":[{"variantDigest":"` + declared + `","relocatable":false,"arguments":[],"files":[{"size":0,"component":""}]}]}`)
	digests, err := rawVariantDigests(raw)
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonicalDigest([]byte(`{"variantDigest":"","relocatable":false,"arguments":[],"files":[{"size":0,"component":""}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(digests) != 1 || digests[0] != want {
		t.Fatalf("raw variant digests = %v, want %s", digests, want)
	}
	absent, err := rawVariantDigests([]byte(`{"variants":[{"variantDigest":"` + declared + `","files":[{}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if absent[0] == digests[0] {
		t.Fatal("explicit zero-valued members did not affect variant identity")
	}
}

func TestDeclaredCapabilitiesMustAlreadyBeSortedAndUnique(t *testing.T) {
	v := &validator{
		suite:        Suite{Capabilities: []string{"remote-streamable-http-mcp", "remote-streamable-http-mcp"}},
		tools:        map[string]Tool{"remote@1.0.0": {Remote: &RemoteMCP{}}},
		agents:       map[string]Agent{},
		compositions: map[string]Composition{},
	}
	if err := v.validateReferences(); err == nil || !strings.Contains(err.Error(), "capabilities") {
		t.Fatalf("duplicate capabilities error = %v", err)
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

func TestValidateSandboxBindingRejectsUnpinnedIdentity(t *testing.T) {
	data := []byte(`{
	  "schemaVersion":"1.0.0-draft",
	  "mediaType":"application/vnd.agentsuite.sandbox-binding.v1+json",
	  "suiteDigest":"latest",
	  "agent":"writer",
	  "platform":{"os":"linux","architecture":"amd64"},
	  "buildProfile":"default",
	  "composition":{"mediaType":"application/vnd.agentsuite.composition.v1+json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1},
	  "inventory":{"mediaType":"application/json","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":1}
	}`)
	if _, err := ValidateSandboxBinding(data); err == nil {
		t.Fatal("expected invalid suite digest to be rejected")
	}
}

func TestValidateSandboxBindingRejectsWrongCompositionMediaType(t *testing.T) {
	data := []byte(`{
	  "schemaVersion":"1.0.0-draft",
	  "mediaType":"application/vnd.agentsuite.sandbox-binding.v1+json",
	  "suiteDigest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	  "agent":"writer",
	  "platform":{"os":"linux","architecture":"amd64"},
	  "buildProfile":"default",
	  "composition":{"mediaType":"application/json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1},
	  "inventory":{"mediaType":"application/json","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":1}
	}`)
	if _, err := ValidateSandboxBinding(data); err == nil {
		t.Fatal("expected composition media type to be rejected")
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

	composition := Composition{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeComposition,
		Agent:         "writer",
		Platform:      Platform{OS: "linux", Architecture: "amd64"},
		BuildProfile:  "default",
		Tools:         []ResolvedTool{},
	}
	compositionDigest := mustWriteJSON(t, root, "compositions/writer-linux-amd64.json", composition)

	suite := Suite{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeSuite,
		Name:          "example",
		Agents: []ManifestRef{{
			ID: "writer", Path: "agents/writer.json", Digest: agentDigest,
		}},
		ToolCatalog: ManifestRef{ID: "catalog", Path: "tools/catalog.json", Digest: catalogDigest},
		Compositions: []CompositionRef{{
			Agent: "writer", Platform: Platform{OS: "linux", Architecture: "amd64"},
			Path: "compositions/writer-linux-amd64.json", Digest: compositionDigest,
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

func mustVariantDigest(t *testing.T, variant ToolVariant) string {
	t.Helper()
	data, err := json.Marshal(struct {
		Variants []ToolVariant `json:"variants"`
	}{Variants: []ToolVariant{variant}})
	if err != nil {
		t.Fatal(err)
	}
	digests, err := rawVariantDigests(data)
	if err != nil {
		t.Fatal(err)
	}
	return digests[0]
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
