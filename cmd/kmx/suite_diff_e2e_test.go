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

	"github.com/kaimahi-agents/kaimahi/agentsuite"
)

func TestSuiteDiffArtifactsEndToEnd(t *testing.T) {
	root := repositoryRoot(t)
	fixture := filepath.Join(root, "internal", "kmx", "agentsuite", "testdata", "incident-analyst")
	beforeSuite := copySuiteFixture(t, fixture)
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

	runKMX(t, binary, "suite", "validate", beforeSuite)
	runKMX(t, binary, "suite", "validate", afterSuite)

	beforeLayout := filepath.Join(t.TempDir(), "before.oci")
	afterLayout := filepath.Join(t.TempDir(), "after.oci")
	runKMX(t, binary, "suite", "push", beforeSuite,
		"--to-layout", beforeLayout, "agentsuites/incident-response:v1")
	runKMX(t, binary, "suite", "push", afterSuite,
		"--to-layout", afterLayout, "agentsuites/incident-response:v2")

	unified := runKMX(t, binary, "suite", "diff", beforeLayout, afterLayout)
	for _, expected := range []string{
		"@@ ~ agent incident-analyst @@",
		"@@ ~ buildProfile agentkit-v0.1.0 @@",
		"@@ ~ composition incident-analyst@linux/amd64 @@",
		"@@ + toolProvider incident-evidence@1.0.0 @@",
		"@@ + toolProviderComposition incident-evidence@1.0.0@linux/amd64 @@",
		"@@ ~ toolProviderCatalog catalog @@",
		`- model.model: "gpt-5-mini"`,
		`+ model.model: "gpt-5"`,
		"- sourceEpoch: 1790388400",
		"+ sourceEpoch: 1790388460",
		"+ toolProviders[incident-evidence@1.0.0]: {",
		"Affected agents",
		"build profile agentkit-v0.1.0 modified",
		"composition incident-analyst@linux/amd64 modified",
		"tool provider incident-evidence@1.0.0 added",
	} {
		if !strings.Contains(unified, expected) {
			t.Errorf("unified artifact diff does not contain %q:\n%s", expected, unified)
		}
	}

	split := runKMX(t, binary, "suite", "diff", beforeLayout, afterLayout, "--view", "split")
	for _, expected := range []string{"--- BEFORE", "+++ AFTER", `- model.model: "gpt-5-mini"`, `+ model.model: "gpt-5"`} {
		if !strings.Contains(split, expected) {
			t.Errorf("split artifact diff does not contain %q:\n%s", expected, split)
		}
	}

	rawReport := runKMX(t, binary, "suite", "diff", beforeLayout, afterLayout, "--output", "json")
	var report agentsuite.DiffReport
	if err := json.Unmarshal([]byte(rawReport), &report); err != nil {
		t.Fatalf("decode JSON artifact diff: %v\n%s", err, rawReport)
	}
	if report.SchemaVersion != agentsuite.DiffReportSchemaVersion {
		t.Fatalf("schema version = %q", report.SchemaVersion)
	}
	if report.Before.Suite != "incident-response" || report.After.Suite != "incident-response" {
		t.Fatalf("suite identities = %q -> %q", report.Before.Suite, report.After.Suite)
	}
	if report.Before.ArtifactDigest == "" || report.After.ArtifactDigest == "" ||
		report.Before.ContentDigest == report.After.ContentDigest {
		t.Fatalf("artifact identities = %+v -> %+v", report.Before, report.After)
	}
	if report.Summary.Added != 4 || report.Summary.Removed != 0 || report.Summary.Modified != 6 ||
		report.Summary.AgentsAffected != 1 {
		t.Fatalf("unexpected artifact diff summary: %+v", report.Summary)
	}
	for _, expected := range []struct {
		changeType string
		kind       string
		id         string
	}{
		{agentsuite.DiffModified, "agent", "incident-analyst"},
		{agentsuite.DiffModified, "buildProfile", "agentkit-v0.1.0"},
		{agentsuite.DiffModified, "composition", "incident-analyst@linux/amd64"},
		{agentsuite.DiffAdded, "toolProvider", "incident-evidence@1.0.0"},
		{agentsuite.DiffAdded, "toolProviderComposition", "incident-evidence@1.0.0@linux/amd64"},
		{agentsuite.DiffAdded, "content", "schemas/incident-evidence/query-input.json"},
		{agentsuite.DiffAdded, "content", "tool-providers/incident-evidence/1.0.0/linux-amd64/bin/incident-evidence"},
		{agentsuite.DiffModified, "content", "instructions/incident-analyst.md"},
		{agentsuite.DiffModified, "toolProviderCatalog", "catalog"},
		{agentsuite.DiffModified, "suite", "metadata"},
	} {
		if !reportHasRecordChange(report, expected.changeType, expected.kind, expected.id) {
			t.Errorf("JSON artifact diff is missing %s %s %s", expected.changeType, expected.kind, expected.id)
		}
	}
}

func mutateIncidentAnalystSuite(t *testing.T, root string) {
	t.Helper()
	const (
		providerID      = "incident-evidence"
		providerVersion = "1.0.0"
	)
	platform := agentsuite.Platform{OS: "linux", Architecture: "amd64"}

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

	variant := agentsuite.ToolProviderVariant{
		Platform:    platform,
		InstallRoot: "/opt/agentsuite/tool-providers/incident-evidence/1.0.0",
		PayloadRoot: "tool-providers/incident-evidence/1.0.0/linux-amd64",
		Entrypoint:  "/opt/agentsuite/tool-providers/incident-evidence/1.0.0/bin/incident-evidence",
		Runtime:     agentsuite.RuntimeRequirement{ABI: "static", CPUBaseline: "x86-64-v1"},
		Files: []agentsuite.InventoryEntry{{
			Path: "bin/incident-evidence", Type: "file", Mode: 0o755, UID: 0, GID: 0,
			Size: int64(len(payload)), Digest: rawDigest(payload), Component: providerID,
		}},
	}
	variant.VariantDigest = variantDigest(t, variant)
	provider := agentsuite.ToolProvider{
		SchemaVersion: agentsuite.SpecVersion,
		MediaType:     agentsuite.MediaTypeToolProvider,
		ID:            providerID,
		Version:       providerVersion,
		Protocol:      "mcp",
		Revision:      "2025-06-18",
		Tools: []agentsuite.Tool{{
			Name:        "query_incident_evidence",
			Description: "Query evidence supplied for the current incident.",
			InputSchema: agentsuite.FileRef{Path: schemaPath, Digest: rawDigest(schema)},
			Effects:     []string{"incident-observation"},
		}},
		Variants:   []agentsuite.ToolProviderVariant{variant},
		Extensions: []agentsuite.Extension{},
	}
	providerPath := "tool-providers/incident-evidence/1.0.0/tool-provider.json"
	providerDigest := writeJSON(t, root, providerPath, provider)

	catalog := agentsuite.ToolProviderCatalog{
		SchemaVersion: agentsuite.SpecVersion,
		MediaType:     agentsuite.MediaTypeToolProviderCatalog,
		ToolProviders: []agentsuite.ManifestRef{{
			ID: providerID, Version: providerVersion, Path: providerPath, Digest: providerDigest,
		}},
	}
	catalogDigest := writeJSON(t, root, "tool-providers/catalog.json", catalog)

	var profile agentsuite.BuildProfile
	readJSON(t, root, "build-profiles/agentkit-v0.1.0.json", &profile)
	profile.SourceEpoch += 60
	profileDigest := writeJSON(t, root, "build-profiles/agentkit-v0.1.0.json", profile)

	var agent agentsuite.Agent
	readJSON(t, root, "agents/incident-analyst.json", &agent)
	agent.Description = "Turns incident evidence into an actionable triage and escalation report."
	agent.Instructions.Digest = rawDigest(instructions)
	agent.Model.Model = "gpt-5"
	agent.ToolProviders = []agentsuite.ToolProviderRequirement{{
		ID: providerID, Version: providerVersion, ExecutionMode: agentsuite.ExecutionSharedSandbox,
	}}
	agentDigest := writeJSON(t, root, "agents/incident-analyst.json", agent)

	var composition agentsuite.Composition
	readJSON(t, root, "compositions/incident-analyst-linux-amd64.json", &composition)
	composition.ToolProviders = []agentsuite.ResolvedToolProvider{{
		ID: providerID, Version: providerVersion, ManifestDigest: providerDigest,
		VariantDigest: variant.VariantDigest, ExecutionMode: agentsuite.ExecutionSharedSandbox,
	}}
	compositionDigest := writeJSON(t, root, "compositions/incident-analyst-linux-amd64.json", composition)

	providerComposition := agentsuite.ToolProviderComposition{
		SchemaVersion:  agentsuite.SpecVersion,
		MediaType:      agentsuite.MediaTypeToolProviderComposition,
		ID:             providerID,
		Version:        providerVersion,
		ManifestDigest: providerDigest,
		Platform:       platform,
		VariantDigest:  variant.VariantDigest,
		BuildProfile:   profile.ID,
	}
	providerCompositionPath := "tool-provider-compositions/incident-evidence-linux-amd64.json"
	providerCompositionDigest := writeJSON(t, root, providerCompositionPath, providerComposition)

	var suite agentsuite.Suite
	readJSON(t, root, "agentsuite.json", &suite)
	suite.Agents[0].Digest = agentDigest
	suite.ToolProviderCatalog.Digest = catalogDigest
	suite.Compositions[0].Digest = compositionDigest
	suite.BuildProfiles[0].Digest = profileDigest
	suite.ToolProviderCompositions = []agentsuite.ToolProviderCompositionRef{{
		ID: providerID, Version: providerVersion, Platform: platform,
		Path: providerCompositionPath, Digest: providerCompositionDigest,
	}}
	suite.Capabilities = []string{"bundled-stdio-mcp"}
	writeJSON(t, root, "agentsuite.json", suite)
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

func writeJSON(t *testing.T, root, name string, value any) string {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	data = append(data, '\n')
	writeFile(t, root, name, data, 0o644)
	return canonicalDigest(t, data)
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
}

func canonicalDigest(t *testing.T, data []byte) string {
	t.Helper()
	canonical, err := jcs.Transform(data)
	if err != nil {
		t.Fatalf("canonicalize JSON: %v", err)
	}
	return rawDigest(canonical)
}

func rawDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func variantDigest(t *testing.T, variant agentsuite.ToolProviderVariant) string {
	t.Helper()
	variant.VariantDigest = ""
	data, err := json.Marshal(variant)
	if err != nil {
		t.Fatalf("encode ToolProvider variant: %v", err)
	}
	return canonicalDigest(t, data)
}

func reportHasRecordChange(report agentsuite.DiffReport, changeType, kind, id string) bool {
	for _, change := range report.Changes {
		if change.Type == changeType && change.Kind == kind && change.ID == id {
			return true
		}
	}
	return false
}
