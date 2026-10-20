package oras

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	kmxsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/content/oci"
)

// payloadEntry is one authored inventory entry plus its loose host state.
type payloadEntry struct {
	agentsuite.InventoryEntry
	data string
	host os.FileMode
}

func TestPushDirectoryIgnoresHostExecuteBits(t *testing.T) {
	source := minimalSuiteCopy(t)
	mustWrite(t, source, "notes/run.sh", []byte("#!/bin/sh\n"), 0o644)
	before := pushDirectoryForTest(t, source, memory.New())
	if err := os.Chmod(filepath.Join(source, "notes", "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	headers := archiveHeaders(t, source)
	if mode := headers["notes/run.sh"].Mode; mode != 0o644 {
		t.Fatalf("undeclared host-executable file mode = %#o, want 0644", mode)
	}
	if after := pushDirectoryForTest(t, source, memory.New()); !content.Equal(before, after) {
		t.Fatalf("host chmod changed archive: %s != %s", before.Digest, after.Digest)
	}
}

func TestPushDirectoryUsesDeclaredPayloadModes(t *testing.T) {
	source := minimalSuiteCopy(t)
	payloadRoot := addModeProvider(t, source, "modes", [][]payloadEntry{{
		{InventoryEntry: agentsuite.InventoryEntry{Path: "bin/tool", Type: "file", Mode: 0o755}, data: "#!/bin/sh\n", host: 0o644},
		{InventoryEntry: agentsuite.InventoryEntry{Path: "bin/alias", Type: "symlink", Mode: 0o700, LinkTarget: "tool"}},
		{InventoryEntry: agentsuite.InventoryEntry{Path: "lib", Type: "directory", Mode: 0o700}, host: 0o755},
		{InventoryEntry: agentsuite.InventoryEntry{Path: "lib/private", Type: "file", Mode: 0o600}, data: "private", host: 0o755},
		{InventoryEntry: agentsuite.InventoryEntry{Path: "lib/zero", Type: "file", Mode: 0}, data: "zero", host: 0o644},
	}})
	modes := directoryModesForTest(t, source)
	for name, want := range map[string]uint32{"bin/tool": 0o755, "bin/alias": 0o700, "lib": 0o700, "lib/private": 0o600, "lib/zero": 0} {
		if got, ok := modes[path.Join(payloadRoot, name)]; !ok || got != want {
			t.Errorf("declared %s = %#o (present %t), want %#o", name, got, ok, want)
		}
	}
	headers := archiveHeaders(t, source)
	for name, want := range map[string]int64{
		"bin/tool": 0o755, "bin/alias": 0o700, "lib": 0o700, "lib/private": 0o600, "lib/zero": 0, "bin": 0o755,
	} {
		header, ok := headers[path.Join(payloadRoot, name)]
		if !ok || header.Mode != want {
			t.Errorf("archived %s mode = %+v, want %#o", name, header, want)
		}
	}
	if mode := headers["agentsuite.json"].Mode; mode != 0o644 {
		t.Errorf("default file mode = %#o, want 0644", mode)
	}
	// The packed artifact must satisfy the public archive validator.
	packedRoot, _, _, err := packDirectory(context.Background(), source)
	if err != nil {
		t.Fatalf("declared-mode suite did not pack: %v", err)
	}
	os.RemoveAll(packedRoot)
}

func TestDirectoryModesRefuseConflictsAndChangedDescriptors(t *testing.T) {
	t.Run("conflict", func(t *testing.T) {
		source := minimalSuiteCopy(t)
		tool := payloadEntry{InventoryEntry: agentsuite.InventoryEntry{Path: "bin/tool", Type: "file", Mode: 0o755}, data: "#!/bin/sh\n", host: 0o644}
		data := func(mode uint32) payloadEntry {
			return payloadEntry{InventoryEntry: agentsuite.InventoryEntry{Path: "data", Type: "file", Mode: mode}, data: "data", host: 0o644}
		}
		addModeProvider(t, source, "shared", [][]payloadEntry{{tool, data(0o644)}, {tool, data(0o600)}})
		handle := openRootForTest(t, source)
		if _, err := kmxsuite.DirectoryModes(handle); err == nil || !strings.Contains(err.Error(), "conflicting") {
			t.Fatalf("conflicting declarations error = %v", err)
		}
		// The production entry point must also refuse via public loose validation.
		if _, _, _, err := packDirectory(context.Background(), source); err == nil || !strings.Contains(err.Error(), "conflicting declared mode") {
			t.Fatalf("conflicting declarations did not get the specific refusal: %v", err)
		}
	})
	t.Run("changed descriptor", func(t *testing.T) {
		source := minimalSuiteCopy(t)
		addModeProvider(t, source, "changed", [][]payloadEntry{{
			{InventoryEntry: agentsuite.InventoryEntry{Path: "bin/tool", Type: "file", Mode: 0o755}, data: "#!/bin/sh\n", host: 0o644},
		}})
		providerPath := filepath.Join(source, "tool-providers", "changed", "1.0.0", "tool-provider.json")
		data, err := os.ReadFile(providerPath)
		if err != nil {
			t.Fatal(err)
		}
		mutated := strings.Replace(string(data), `"mode":493`, `"mode":448`, 1)
		if mutated == string(data) {
			t.Fatal("fixture lacks the expected mode marker")
		}
		if err := os.WriteFile(providerPath, []byte(mutated), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := kmxsuite.DirectoryModes(openRootForTest(t, source)); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
			t.Fatalf("changed provider descriptor error = %v", err)
		}
		if _, err := pushDirectory(context.Background(), source, memory.New()); err == nil {
			t.Fatal("changed provider descriptor archived")
		}
	})
}

func TestPackDirectoryValidatesLooseSourceFirst(t *testing.T) {
	source := minimalSuiteCopy(t)
	// Group-writable loose content is refused by the public loose reader but
	// would be invisible after archiving under default modes.
	mustWrite(t, source, "notes/shared.txt", []byte("shared"), 0o644)
	if err := os.Chmod(filepath.Join(source, "notes", "shared.txt"), 0o664); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := packDirectory(context.Background(), source); err == nil || !strings.Contains(err.Error(), "unsafe file mode") {
		t.Fatalf("group-writable loose source error = %v", err)
	}
}

func TestPublishedArchiveKeepsIdentityWhileRepackageUsesDefaults(t *testing.T) {
	ctx := context.Background()
	source := minimalSuiteCopy(t)
	mustWrite(t, source, "bin/legacy", []byte("#!/bin/sh\n"), 0o755)
	current := archiveBytes(t, source)
	// Reproduce an artifact produced by the old host-execute-bit packager.
	legacy := retarWithModes(t, current, map[string]int64{"bin/legacy": 0o755})
	legacyDescriptor := content.NewDescriptorFromBytes(agentsuite.MediaTypeContent, legacy)
	sourceStore := memory.New()
	if err := sourceStore.Push(ctx, legacyDescriptor, bytes.NewReader(legacy)); err != nil {
		t.Fatal(err)
	}
	published, err := oci.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	packed, err := New(LayoutValidator{}).Pack(ctx, sourceStore, published, legacyDescriptor)
	if err != nil {
		t.Fatalf("legacy archive no longer validates: %v", err)
	}
	if _, err := (LayoutValidator{}).Validate(ctx, published, packed.Descriptor); err != nil {
		t.Fatal(err)
	}
	stored, err := content.FetchAll(ctx, published, legacyDescriptor)
	if err != nil || !bytes.Equal(stored, legacy) {
		t.Fatalf("published legacy layer changed: err=%v", err)
	}
	if headers := tarHeaders(t, stored); headers["bin/legacy"].Mode != 0o755 {
		t.Fatalf("published legacy mode = %#o, want 0755", headers["bin/legacy"].Mode)
	}
	repacked := pushDirectoryForTest(t, source, memory.New())
	if repacked.Digest == legacyDescriptor.Digest {
		t.Fatal("default repackage reused the legacy archive identity")
	}
}

func minimalSuiteCopy(t *testing.T) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "suite")
	copyDirectory(t, filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), source)
	return source
}

// addModeProvider authors one conformant bundled provider whose variants share
// payloadRoot. The first entry of each variant is its executable entrypoint.
func addModeProvider(t *testing.T, root, id string, variants [][]payloadEntry) string {
	t.Helper()
	payloadRoot := "tool-providers/" + id + "/1.0.0/payload"
	schema := []byte(`{"type":"object","additionalProperties":false}`)
	schemaPath := "schemas/" + id + ".json"
	mustWrite(t, root, schemaPath, schema, 0o644)
	architectures := []string{"amd64", "arm64"}
	retained := true
	provider := agentsuite.ToolProvider{
		Retained:      &retained,
		SchemaVersion: agentsuite.SpecVersion,
		MediaType:     agentsuite.MediaTypeToolProvider,
		ID:            id,
		Version:       "1.0.0",
		Protocol:      "mcp",
		Revision:      "2025-06-18",
		Tools: []agentsuite.Tool{{Name: id, InputSchema: agentsuite.FileRef{Path: schemaPath, Descriptor: agentsuite.Descriptor{
			MediaType: agentsuite.MediaTypeJSONSchema, Digest: sha256Digest(schema), Size: int64(len(schema)),
		}}}},
		Extensions: []agentsuite.Extension{},
	}
	installRoot := "/opt/agentsuite/tool-providers/" + id
	for i, entries := range variants {
		var files []agentsuite.InventoryEntry
		for _, entry := range entries {
			name := filepath.Join(root, filepath.FromSlash(path.Join(payloadRoot, entry.Path)))
			switch entry.Type {
			case "file":
				mustWrite(t, root, path.Join(payloadRoot, entry.Path), []byte(entry.data), entry.host)
				if err := os.Chmod(name, entry.host); err != nil {
					t.Fatal(err)
				}
				entry.Size, entry.Digest = int64(len(entry.data)), sha256Digest([]byte(entry.data))
			case "directory":
				if err := os.MkdirAll(name, entry.host); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(name, entry.host); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if _, err := os.Lstat(name); os.IsNotExist(err) {
					if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
						t.Fatal(err)
					}
					mustSymlink(t, entry.LinkTarget, name)
				}
			}
			files = append(files, entry.InventoryEntry)
		}
		variant := agentsuite.ToolProviderVariant{
			Platform:    agentsuite.Platform{OS: "linux", Architecture: architectures[i]},
			InstallRoot: installRoot,
			PayloadRoot: payloadRoot,
			Entrypoint:  installRoot + "/" + entries[0].Path,
			Runtime:     agentsuite.RuntimeRequirement{ABI: "static", CPUBaseline: "baseline"},
			Files:       files,
		}
		unsigned, err := agentsuite.Marshal(variant)
		if err != nil {
			t.Fatal(err)
		}
		if variant.VariantDigest, err = agentsuite.Digest(unsigned); err != nil {
			t.Fatal(err)
		}
		provider.Variants = append(provider.Variants, variant)
	}
	providerPath := "tool-providers/" + id + "/1.0.0/tool-provider.json"
	providerDescriptor := writeSuiteJSON(t, root, providerPath, agentsuite.MediaTypeToolProvider, provider)

	var suite agentsuite.Suite
	readSuiteJSON(t, root, "agentsuite.json", &suite)
	var catalog agentsuite.ToolProviderCatalog
	readSuiteJSON(t, root, suite.ToolProviderCatalog.Path, &catalog)
	catalog.ToolProviders = append(catalog.ToolProviders, agentsuite.ManifestRef{ID: id, Version: "1.0.0", Path: providerPath, Descriptor: providerDescriptor})
	suite.ToolProviderCatalog.Descriptor = writeSuiteJSON(t, root, suite.ToolProviderCatalog.Path, agentsuite.MediaTypeToolProviderCatalog, catalog)
	if !slices.Contains(suite.Capabilities, "bundled-stdio-mcp") {
		suite.Capabilities = append(suite.Capabilities, "bundled-stdio-mcp")
	}
	writeSuiteJSON(t, root, "agentsuite.json", agentsuite.MediaTypeSuite, suite)
	return payloadRoot
}

func writeSuiteJSON(t *testing.T, root, name, mediaType string, value any) agentsuite.Descriptor {
	t.Helper()
	data, err := agentsuite.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, root, name, data, 0o644)
	digest, err := agentsuite.Digest(data)
	if err != nil {
		t.Fatal(err)
	}
	return agentsuite.Descriptor{MediaType: mediaType, Digest: digest, Size: int64(len(data))}
}

func readSuiteJSON(t *testing.T, root, name string, out any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	if err := agentsuite.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

func sha256Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func openRootForTest(t *testing.T, root string) *os.Root {
	t.Helper()
	handle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { handle.Close() })
	return handle
}

func directoryModesForTest(t *testing.T, root string) map[string]uint32 {
	t.Helper()
	modes, err := kmxsuite.DirectoryModes(openRootForTest(t, root))
	if err != nil {
		t.Fatal(err)
	}
	return modes
}

func archiveBytes(t *testing.T, root string) []byte {
	t.Helper()
	store := memory.New()
	descriptor := pushDirectoryForTest(t, root, store)
	data, err := content.FetchAll(context.Background(), store, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func archiveHeaders(t *testing.T, root string) map[string]*tar.Header {
	t.Helper()
	return tarHeaders(t, archiveBytes(t, root))
}

func tarHeaders(t *testing.T, archive []byte) map[string]*tar.Header {
	t.Helper()
	headers := map[string]*tar.Header{}
	readTar(t, archive, func(header *tar.Header, _ []byte) {
		headers[strings.TrimSuffix(header.Name, "/")] = header
	})
	return headers
}

// retarWithModes rewrites selected header modes while preserving order and bytes.
func retarWithModes(t *testing.T, archive []byte, modes map[string]int64) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	gzipWriter.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	readTar(t, archive, func(header *tar.Header, body []byte) {
		if mode, ok := modes[strings.TrimSuffix(header.Name, "/")]; ok {
			header.Mode = mode
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(body); err != nil {
			t.Fatal(err)
		}
	})
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func readTar(t *testing.T, archive []byte, visit func(*tar.Header, []byte)) {
	t.Helper()
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		visit(header, body)
	}
}
