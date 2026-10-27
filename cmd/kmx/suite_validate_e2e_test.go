package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gowebpki/jcs"

	suitecore "github.com/agent-primitives/suite/core"
)

func TestSuiteValidateRejectsMissingProviderExtensionCriticalityEndToEnd(t *testing.T) {
	root := repositoryRoot(t)
	fixture := filepath.Join(root, "internal", "kmx", "agentsuite", "testdata", "incident-analyst")
	afterSuite := copySuiteFixture(t, fixture)
	mutateIncidentAnalystSuite(t, afterSuite)

	binary := filepath.Join(t.TempDir(), "kmx")
	buildContext, cancelBuild := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancelBuild()
	command := exec.CommandContext(buildContext, "go", "build", "-o", binary, "./cmd/kmx")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build kmx: %v\n%s", err, output)
	}

	runKMX(t, binary, "package", "validate", afterSuite)

	// Rebind every ancestor so only the missing extension criticality is invalid.
	var provider map[string]any
	providerPath := "tool-providers/incident-evidence/1.0.0/tool-provider.json"
	readJSON(t, afterSuite, providerPath, &provider)
	provider["extensions"] = []any{map[string]any{"name": "future-feature"}}
	providerDigest, providerSize := writeJSON(t, afterSuite, providerPath, provider)
	var catalog suitecore.ToolProviderCatalog
	readJSON(t, afterSuite, "tool-providers/catalog.json", &catalog)
	catalog.ToolProviders[0].Descriptor = descriptorFor(suitecore.MediaTypeToolProvider, providerDigest, providerSize)
	catalogDigest, catalogSize := writeJSON(t, afterSuite, "tool-providers/catalog.json", catalog)
	var composition suitecore.Composition
	compositionPath := "compositions/incident-analyst-linux-amd64.json"
	readJSON(t, afterSuite, compositionPath, &composition)
	composition.ToolProviders[0].ManifestDigest = providerDigest
	compositionDigest, compositionSize := writeJSON(t, afterSuite, compositionPath, composition)
	var providerComposition suitecore.ToolProviderComposition
	providerCompositionPath := "tool-provider-compositions/incident-evidence-linux-amd64.json"
	readJSON(t, afterSuite, providerCompositionPath, &providerComposition)
	providerComposition.ManifestDigest = providerDigest
	providerCompositionDigest, providerCompositionSize := writeJSON(t, afterSuite, providerCompositionPath, providerComposition)
	var suite suitecore.Suite
	readJSON(t, afterSuite, "agentsuite.json", &suite)
	suite.ToolProviderCatalog.Descriptor = descriptorFor(suitecore.MediaTypeToolProviderCatalog, catalogDigest, catalogSize)
	suite.Compositions[0].Descriptor = descriptorFor(suitecore.MediaTypeComposition, compositionDigest, compositionSize)
	suite.ToolProviderCompositions[0].Descriptor = descriptorFor(suitecore.MediaTypeToolProviderComposition, providerCompositionDigest, providerCompositionSize)
	writeJSON(t, afterSuite, "agentsuite.json", suite)
	command = exec.CommandContext(t.Context(), binary, "package", "validate", afterSuite)
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "non-null critical") {
		t.Fatalf("missing provider extension criticality: error = %v, output = %s", err, output)
	}
}

func mutateIncidentAnalystSuite(t *testing.T, root string) {
	t.Helper()
	const (
		providerID      = "incident-evidence"
		providerVersion = "1.0.0"
	)
	platform := suitecore.Platform{OS: "linux", Architecture: "amd64"}

	instructionsPath := "instructions/incident-analyst.md"
	instructions := readFile(t, root, instructionsPath)
	instructions = append(instructions, []byte("\n9. **Escalation criteria** — identify the exact condition that requires escalation.\n")...)
	writeFile(t, root, instructionsPath, instructions, 0o644)

	schemaPath := "schemas/incident-evidence/query-input.json"
	schema := []byte(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)
	writeFile(t, root, schemaPath, schema, 0o644)

	payloadPath := "tool-providers/incident-evidence/1.0.0/linux-amd64/bin/incident-evidence"
	payload := []byte("#!/bin/sh\nprintf '%s\\n' incident-evidence\n")
	writeFile(t, root, payloadPath, payload, 0o755)

	variant := suitecore.ToolProviderVariant{
		Platform:    platform,
		InstallRoot: "/opt/agentsuite/tool-providers/incident-evidence/1.0.0",
		PayloadRoot: "tool-providers/incident-evidence/1.0.0/linux-amd64",
		Entrypoint:  "/opt/agentsuite/tool-providers/incident-evidence/1.0.0/bin/incident-evidence",
		Runtime:     suitecore.RuntimeRequirement{ABI: "static", CPUBaseline: "x86-64-v1"},
		Files: []suitecore.InventoryEntry{{
			Path: "bin/incident-evidence", Type: "file", Mode: 0o755, UID: 0, GID: 0,
			Size: int64(len(payload)), Digest: rawDigest(payload), Component: providerID,
		}},
	}
	variant.VariantDigest = variantDigest(t, variant)
	provider := suitecore.ToolProvider{
		SchemaVersion: suitecore.SpecVersion,
		MediaType:     suitecore.MediaTypeToolProvider,
		ID:            providerID,
		Version:       providerVersion,
		Protocol:      "mcp",
		Revision:      "2025-06-18",
		Tools: []suitecore.Tool{{
			Name:        "query_incident_evidence",
			Description: "Query evidence supplied for the current incident.",
			InputSchema: suitecore.FileRef{
				Path:       schemaPath,
				Descriptor: descriptorFor(suitecore.MediaTypeJSONSchema, rawDigest(schema), int64(len(schema))),
			},
			Effects: []string{"incident-observation"},
		}},
		Variants:   []suitecore.ToolProviderVariant{variant},
		Extensions: []suitecore.Extension{},
	}
	providerPath := "tool-providers/incident-evidence/1.0.0/tool-provider.json"
	providerDigest, providerSize := writeJSON(t, root, providerPath, provider)

	catalog := suitecore.ToolProviderCatalog{
		SchemaVersion: suitecore.SpecVersion,
		MediaType:     suitecore.MediaTypeToolProviderCatalog,
		ToolProviders: []suitecore.ManifestRef{{
			ID: providerID, Version: providerVersion, Path: providerPath,
			Descriptor: descriptorFor(suitecore.MediaTypeToolProvider, providerDigest, providerSize),
		}},
	}
	catalogDigest, catalogSize := writeJSON(t, root, "tool-providers/catalog.json", catalog)

	var profile suitecore.BuildProfile
	readJSON(t, root, "build-profiles/agentkit-v0.1.0.json", &profile)
	profile.SourceEpoch += 60
	profileDigest, profileSize := writeJSON(t, root, "build-profiles/agentkit-v0.1.0.json", profile)

	var agent suitecore.Agent
	readJSON(t, root, "agents/incident-analyst.json", &agent)
	agent.Description = "Turns incident evidence into an actionable triage and escalation report."
	agent.Instructions.Descriptor = descriptorFor(suitecore.MediaTypeInstructions, rawDigest(instructions), int64(len(instructions)))
	agent.Model.Model = "gpt-5"
	agent.ToolProviders = []suitecore.ToolProviderRequirement{{
		ID: providerID, Version: providerVersion, ExecutionMode: suitecore.ExecutionSharedSandbox,
	}}
	agentDigest, agentSize := writeJSON(t, root, "agents/incident-analyst.json", agent)

	var composition suitecore.Composition
	readJSON(t, root, "compositions/incident-analyst-linux-amd64.json", &composition)
	composition.ToolProviders = []suitecore.ResolvedToolProvider{{
		ID: providerID, Version: providerVersion, ManifestDigest: providerDigest,
		VariantDigest: variant.VariantDigest, ExecutionMode: suitecore.ExecutionSharedSandbox,
	}}
	compositionDigest, compositionSize := writeJSON(t, root, "compositions/incident-analyst-linux-amd64.json", composition)

	providerComposition := suitecore.ToolProviderComposition{
		SchemaVersion:  suitecore.SpecVersion,
		MediaType:      suitecore.MediaTypeToolProviderComposition,
		ID:             providerID,
		Version:        providerVersion,
		ManifestDigest: providerDigest,
		Platform:       platform,
		VariantDigest:  variant.VariantDigest,
		BuildProfile:   profile.ID,
	}
	providerCompositionPath := "tool-provider-compositions/incident-evidence-linux-amd64.json"
	providerCompositionDigest, providerCompositionSize := writeJSON(t, root, providerCompositionPath, providerComposition)

	var suite suitecore.Suite
	readJSON(t, root, "agentsuite.json", &suite)
	suite.Agents[0].Descriptor = descriptorFor(suitecore.MediaTypeAgent, agentDigest, agentSize)
	suite.ToolProviderCatalog.Descriptor = descriptorFor(suitecore.MediaTypeToolProviderCatalog, catalogDigest, catalogSize)
	suite.Compositions[0].Descriptor = descriptorFor(suitecore.MediaTypeComposition, compositionDigest, compositionSize)
	suite.BuildProfiles[0].Descriptor = descriptorFor(suitecore.MediaTypeBuildProfile, profileDigest, profileSize)
	suite.ToolProviderCompositions = []suitecore.ToolProviderCompositionRef{{
		ID: providerID, Version: providerVersion, Platform: platform,
		Path:       providerCompositionPath,
		Descriptor: descriptorFor(suitecore.MediaTypeToolProviderComposition, providerCompositionDigest, providerCompositionSize),
	}}
	suite.Capabilities = []string{"bundled-stdio-mcp"}
	writeJSON(t, root, "agentsuite.json", suite)
}

// descriptorFor builds the nested Descriptor carried by content-addressable
// suite references (ManifestRef, CompositionRef, ToolProviderCompositionRef,
// FileRef).
func descriptorFor(mediaType, digest string, size int64) suitecore.Descriptor {
	return suitecore.Descriptor{MediaType: mediaType, Digest: digest, Size: size}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}

func runKMX(t *testing.T, binary string, args ...string) string {
	t.Helper()
	commandContext, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(commandContext, binary, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", binary, strings.Join(args, " "), err, output)
	}
	return string(output)
}

func readJSON(t *testing.T, root, name string, value any) {
	t.Helper()
	if err := json.Unmarshal(readFile(t, root, name), value); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

// writeJSON writes value to disk as its exact RFC 8785 JCS canonical bytes,
// since suite content referenced by a nested descriptor (agent, build
// profile, composition, tool provider, tool provider composition, tool
// provider catalog, or suite manifest) must be stored byte-identical to its
// own canonical representation, not merely hashed and sized as if canonical.
func writeJSON(t *testing.T, root, name string, value any) (digest string, size int64) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	canonical, err := jcs.Transform(data)
	if err != nil {
		t.Fatalf("canonicalize %s: %v", name, err)
	}
	writeFile(t, root, name, canonical, 0o644)
	return canonicalDigestAndSize(t, canonical)
}

func readFile(t *testing.T, root, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

func writeFile(t *testing.T, root, name string, data []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", name, err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", name, err)
	}
}

func canonicalDigest(t *testing.T, data []byte) string {
	t.Helper()
	canonical, err := jcs.Transform(data)
	if err != nil {
		t.Fatalf("canonicalize JSON: %v", err)
	}
	return rawDigest(canonical)
}

// canonicalDigestAndSize returns the digest and size of data's JCS-canonical
// representation, matching the descriptor shape agentsuite validation checks
// against (agentsuite.matchesCanonicalDescriptor), not data's own raw length.
func canonicalDigestAndSize(t *testing.T, data []byte) (digest string, size int64) {
	t.Helper()
	canonical, err := jcs.Transform(data)
	if err != nil {
		t.Fatalf("canonicalize JSON: %v", err)
	}
	return rawDigest(canonical), int64(len(canonical))
}

func rawDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func variantDigest(t *testing.T, variant suitecore.ToolProviderVariant) string {
	t.Helper()
	variant.VariantDigest = ""
	data, err := json.Marshal(variant)
	if err != nil {
		t.Fatalf("encode ToolProvider variant: %v", err)
	}
	return canonicalDigest(t, data)
}
