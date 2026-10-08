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

	if report.Name != "example" || report.Agents != 1 || report.ToolProviders != 0 || report.Compositions != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestValidateBuildProfileRequiresDigestMatchedImageReferences(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	profile := BuildProfile{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeBuildProfile,
		ID:            "default",
		RuntimeBase: []PlatformImage{{
			Platform: Platform{OS: "linux", Architecture: "amd64"},
			ImageRef: "registry.example/agentsuite/runtime-base@" + digest,
			Image:    Descriptor{MediaType: ociManifestMediaType, Digest: digest, Size: 1},
		}},
		Harness: []PlatformImage{{
			Platform: Platform{OS: "linux", Architecture: "amd64"},
			ImageRef: "registry.example/agentsuite/harness@" + digest,
			Image:    Descriptor{MediaType: ociManifestMediaType, Digest: digest, Size: 1},
		}},
		SourceEpoch: 1,
	}
	if err := validateBuildProfile(profile); err != nil {
		t.Fatalf("valid build profile rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*BuildProfile)
		want   string
	}{
		{
			name: "tag reference",
			mutate: func(profile *BuildProfile) {
				profile.RuntimeBase[0].ImageRef = "registry.example/agentsuite/runtime-base:latest"
			},
			want: "imageRef must be a registry-qualified",
		},
		{
			name: "reference descriptor digest mismatch",
			mutate: func(profile *BuildProfile) {
				profile.Harness[0].ImageRef = "registry.example/agentsuite/harness@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			},
			want: "imageRef digest must match image descriptor",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := profile
			candidate.RuntimeBase = append([]PlatformImage(nil), profile.RuntimeBase...)
			candidate.Harness = append([]PlatformImage(nil), profile.Harness...)
			test.mutate(&candidate)
			err := validateBuildProfile(candidate)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateBuildProfile() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCoordinatorWorkersFixtureIsConformant(t *testing.T) {
	root := filepath.Join("testdata", "coordinator-workers")
	report, err := ValidatePath(root)
	if err != nil {
		t.Fatalf("ValidatePath(%s) error = %v", root, err)
	}
	if report.Name != "coordinator-workers" || report.Agents != 3 || report.ToolProviders != 0 || report.Compositions != 3 {
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

func TestValidatePathAllowsOnePinnedToolProviderVariantToBeReusedByTwoAgents(t *testing.T) {
	root := t.TempDir()
	schemaBytes := []byte(`{"type":"object","additionalProperties":false}`)
	mustWrite(t, root, "schemas/read.json", schemaBytes)
	payload := []byte("#!/bin/sh\nprintf tool\n")
	mustWriteMode(t, root, "tool-providers/reader/1.2.3/linux-amd64/bin/reader", payload, 0o755)

	variant := ToolProviderVariant{
		Platform:    Platform{OS: "linux", Architecture: "amd64"},
		InstallRoot: "/opt/agentsuite/tool-providers/reader",
		PayloadRoot: "tool-providers/reader/1.2.3/linux-amd64",
		Entrypoint:  "/opt/agentsuite/tool-providers/reader/bin/reader",
		Runtime:     RuntimeRequirement{ABI: "static", CPUBaseline: "x86-64-v1"},
		Files: []InventoryEntry{{
			Path: "bin/reader", Type: "file", Mode: 0o755, UID: 0, GID: 0,
			Size: int64(len(payload)), Digest: digestBytes(payload), Component: "reader",
		}},
	}
	variant.VariantDigest = mustVariantDigest(t, variant)
	provider := ToolProvider{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeToolProvider,
		ID:            "reader",
		Version:       "1.2.3",
		Protocol:      "mcp", Revision: "2025-06-18",
		Tools: []Tool{{
			Name: "read", InputSchema: FileRef{Path: "schemas/read.json", Digest: digestBytes(schemaBytes)},
		}},
		Variants: []ToolProviderVariant{variant},
		Remote: &RemoteToolProvider{
			Transport:    "streamable-http",
			EndpointRef:  "reader-mcp-endpoint",
			Network:      []RemoteNetworkRequirement{{DestinationRef: "reader-mcp-egress"}},
			Timeouts:     RemoteTimeouts{ConnectMilliseconds: 5_000, RequestMilliseconds: 60_000},
			Cancellation: "propagate",
			Connection:   "session-aware",
		},
		Extensions: []Extension{},
	}
	providerDigest := mustWriteJSON(t, root, "tool-providers/reader/1.2.3/tool-provider.json", provider)
	catalog := ToolProviderCatalog{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeToolProviderCatalog,
		ToolProviders: []ManifestRef{{
			ID: "reader", Version: "1.2.3", Path: "tool-providers/reader/1.2.3/tool-provider.json", Digest: providerDigest,
		}},
	}
	catalogDigest := mustWriteJSON(t, root, "tool-providers/catalog.json", catalog)

	image := Descriptor{
		MediaType: ociManifestMediaType,
		Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Size:      1,
	}
	profile := BuildProfile{
		SchemaVersion: SpecVersion, MediaType: MediaTypeBuildProfile, ID: "default",
		RuntimeBase: []PlatformImage{{Platform: Platform{OS: "linux", Architecture: "amd64"}, ImageRef: "registry.example/agentsuite/runtime-base@" + image.Digest, Image: image}},
		Harness:     []PlatformImage{{Platform: Platform{OS: "linux", Architecture: "amd64"}, ImageRef: "registry.example/agentsuite/harness@" + image.Digest, Image: image}},
		SourceEpoch: 1,
	}
	profileDigest := mustWriteJSON(t, root, "build-profiles/default.json", profile)
	providerComposition := ToolProviderComposition{
		SchemaVersion:  SpecVersion,
		MediaType:      MediaTypeToolProviderComposition,
		ID:             "reader",
		Version:        "1.2.3",
		ManifestDigest: providerDigest,
		Platform:       Platform{OS: "linux", Architecture: "amd64"},
		VariantDigest:  variant.VariantDigest,
		BuildProfile:   "default",
	}
	providerCompositionPath := "tool-provider-compositions/reader-linux-amd64.json"
	providerCompositionDigest := mustWriteJSON(t, root, providerCompositionPath, providerComposition)

	var agentRefs []ManifestRef
	var compositionRefs []CompositionRef
	for _, id := range []string{"writer", "reviewer"} {
		instructions := []byte("Use the reader provider's read tool.\n")
		instructionPath := "instructions/" + id + ".md"
		mustWrite(t, root, instructionPath, instructions)
		agent := Agent{
			SchemaVersion: SpecVersion, MediaType: MediaTypeAgent, ID: id,
			Instructions: FileRef{Path: instructionPath, Digest: digestBytes(instructions)},
			Model:        ModelRequirement{Protocol: "openai-compatible", Model: "example-model"},
			ToolProviders: []ToolProviderRequirement{{
				ID: "reader", Version: "1.2.3", ExecutionMode: ExecutionSharedSandbox,
			}},
			Invokes: []AgentInvoke{}, Extensions: []Extension{},
		}
		agentPath := "agents/" + id + ".json"
		agentRefs = append(agentRefs, ManifestRef{ID: id, Path: agentPath, Digest: mustWriteJSON(t, root, agentPath, agent)})
		composition := Composition{
			SchemaVersion: SpecVersion, MediaType: MediaTypeComposition, Agent: id,
			Platform: Platform{OS: "linux", Architecture: "amd64"}, BuildProfile: "default",
			ToolProviders: []ResolvedToolProvider{{
				ID: "reader", Version: "1.2.3", ManifestDigest: providerDigest,
				VariantDigest: variant.VariantDigest, ExecutionMode: ExecutionSharedSandbox,
			}},
		}
		compositionPath := "compositions/" + id + "-linux-amd64.json"
		compositionRefs = append(compositionRefs, CompositionRef{
			Agent: id, Platform: composition.Platform, Path: compositionPath,
			Digest: mustWriteJSON(t, root, compositionPath, composition),
		})
	}
	suite := Suite{
		SchemaVersion: SpecVersion, MediaType: MediaTypeSuite, Name: "shared-tool-provider",
		Agents: agentRefs, ToolProviderCatalog: ManifestRef{ID: "catalog", Path: "tool-providers/catalog.json", Digest: catalogDigest},
		ToolProviderCompositions: []ToolProviderCompositionRef{{
			ID: "reader", Version: "1.2.3", Platform: providerComposition.Platform,
			Path: providerCompositionPath, Digest: providerCompositionDigest,
		}},
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
	if report.Agents != 2 || report.ToolProviders != 1 || report.ToolProviderCompositions != 1 || report.Compositions != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(report.CompositionSelections) != 2 {
		t.Fatalf("composition selections = %+v, want two", report.CompositionSelections)
	}
	for _, selection := range report.CompositionSelections {
		if selection.BuildProfile != "default" || selection.Digest == "" {
			t.Fatalf("incomplete composition selection: %+v", selection)
		}
	}
}

func TestValidatePathRejectsTwoCompositionsForOneAgentPlatform(t *testing.T) {
	root := writeMinimalSuite(t)
	raw, err := os.ReadFile(filepath.Join(root, "agentsuite.json"))
	if err != nil {
		t.Fatal(err)
	}
	var suite Suite
	if err := json.Unmarshal(raw, &suite); err != nil {
		t.Fatal(err)
	}
	duplicate := suite.Compositions[0]
	duplicate.Path = "compositions/writer-linux-amd64-copy.json"
	body, err := os.ReadFile(filepath.Join(root, suite.Compositions[0].Path))
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, root, duplicate.Path, body)
	suite.Compositions = append(suite.Compositions, duplicate)
	mustWriteJSON(t, root, "agentsuite.json", suite)
	if _, err := ValidatePath(root); err == nil || !strings.Contains(err.Error(), "duplicate composition") {
		t.Fatalf("duplicate composition error = %v", err)
	}
}

func TestLoadToolProviderCompositionsRejectsUnresolvedBuildInputs(t *testing.T) {
	platform := Platform{OS: "linux", Architecture: "amd64"}
	providerDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	variantDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	image := Descriptor{
		MediaType: ociManifestMediaType,
		Digest:    "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Size:      1,
	}
	baseProvider := ToolProvider{
		ID: "reader", Version: "1.2.3",
		Variants: []ToolProviderVariant{{
			Platform: platform, VariantDigest: variantDigest,
			InstallRoot: "/opt/reader",
		}},
	}
	baseComposition := ToolProviderComposition{
		SchemaVersion: SpecVersion, MediaType: MediaTypeToolProviderComposition,
		ID: "reader", Version: "1.2.3", ManifestDigest: providerDigest,
		Platform: platform, VariantDigest: variantDigest, BuildProfile: "default",
	}
	tests := []struct {
		name   string
		mutate func(*validator, *ToolProviderComposition)
		want   string
	}{
		{
			name: "missing tool provider",
			mutate: func(v *validator, _ *ToolProviderComposition) {
				delete(v.toolProviders, "reader@1.2.3")
			},
			want: "missing or stale ToolProvider manifest",
		},
		{
			name: "stale manifest digest",
			mutate: func(v *validator, _ *ToolProviderComposition) {
				v.toolProviderDigests["reader@1.2.3"] = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
			},
			want: "missing or stale ToolProvider manifest",
		},
		{
			name: "wrong platform",
			mutate: func(v *validator, _ *ToolProviderComposition) {
				provider := v.toolProviders["reader@1.2.3"]
				provider.Variants[0].Platform.Architecture = "arm64"
				v.toolProviders["reader@1.2.3"] = provider
			},
			want: "exactly one matching variant",
		},
		{
			name: "stale variant digest",
			mutate: func(_ *validator, composition *ToolProviderComposition) {
				composition.VariantDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
			},
			want: "exactly one matching variant",
		},
		{
			name: "remote-only tool provider",
			mutate: func(v *validator, _ *ToolProviderComposition) {
				provider := v.toolProviders["reader@1.2.3"]
				provider.Variants = nil
				v.toolProviders["reader@1.2.3"] = provider
			},
			want: "exactly one matching variant",
		},
		{
			name: "missing runtime base",
			mutate: func(v *validator, _ *ToolProviderComposition) {
				v.builds["default"] = BuildProfile{ID: "default"}
			},
			want: "does not support linux/amd64",
		},
		{
			name: "dependency collision",
			mutate: func(v *validator, _ *ToolProviderComposition) {
				provider := v.toolProviders["reader@1.2.3"]
				provider.Variants[0].InstallRoot = "/opt/shared"
				provider.Variants[0].Files = []InventoryEntry{{
					Path: "bin/tool", Type: "file", Digest: providerDigest,
				}}
				provider.Variants[0].Dependencies = []ToolProviderDependency{{
					ID: "runtime", Version: "1.0.0",
					VariantDigest: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
				}}
				v.toolProviders["reader@1.2.3"] = provider
				v.toolProviders["runtime@1.0.0"] = ToolProvider{
					ID: "runtime", Version: "1.0.0",
					Variants: []ToolProviderVariant{{
						Platform:      platform,
						VariantDigest: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
						InstallRoot:   "/opt/shared",
						Files: []InventoryEntry{{
							Path: "bin/tool", Type: "file",
							Digest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
						}},
					}},
				}
			},
			want: "non-identical destination collision",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			composition := baseComposition
			provider := baseProvider
			provider.Variants = append([]ToolProviderVariant(nil), baseProvider.Variants...)
			v := &validator{
				suite: Suite{ToolProviderCompositions: []ToolProviderCompositionRef{{
					ID: composition.ID, Version: composition.Version, Platform: composition.Platform,
					Path: "tool-provider-compositions/reader.json",
				}}},
				content: &contentSet{entries: map[string]contentEntry{}},
				toolProviders: map[string]ToolProvider{
					"reader@1.2.3": provider,
				},
				toolProviderDigests:      map[string]string{"reader@1.2.3": providerDigest},
				toolProviderCompositions: map[string]ToolProviderComposition{},
				builds: map[string]BuildProfile{
					"default": {
						ID: "default",
						RuntimeBase: []PlatformImage{{
							Platform: platform, ImageRef: "registry.example/agentsuite/runtime-base@" + image.Digest, Image: image,
						}},
					},
				},
			}
			test.mutate(v, &composition)
			data, err := json.Marshal(composition)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := canonicalDigest(data)
			if err != nil {
				t.Fatal(err)
			}
			v.suite.ToolProviderCompositions[0].Digest = digest
			v.content.entries["tool-provider-compositions/reader.json"] = contentEntry{
				Path: "tool-provider-compositions/reader.json", Type: "file",
				Size: int64(len(data)), Digest: digestBytes(data), Data: data,
			}
			if err := v.loadToolProviderCompositions(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadToolProviderCompositions() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRemoteMCPFixtureIsConformantAndCatalogable(t *testing.T) {
	fixture := filepath.Join("testdata", "remote-mcp")
	content, err := loadDirectory(fixture)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := content.data("tool-provider.json")
	if err != nil {
		t.Fatal(err)
	}
	var provider ToolProvider
	if err := decodeStrict(raw, &provider); err != nil {
		t.Fatal(err)
	}
	if err := validateToolProvider(provider, raw, content); err != nil {
		t.Fatalf("validateToolProvider() error = %v", err)
	}

	root := writeMinimalSuite(t)
	for _, name := range []string{"search-input.json", "search-output.json"} {
		data, err := os.ReadFile(filepath.Join(fixture, "schemas", name))
		if err != nil {
			t.Fatal(err)
		}
		mustWrite(t, root, filepath.Join("schemas", name), data)
	}
	providerPath := "tool-providers/search/1.0.0/tool-provider.json"
	mustWrite(t, root, providerPath, raw)
	providerDigest, err := canonicalDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	catalog := ToolProviderCatalog{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeToolProviderCatalog,
		ToolProviders: []ManifestRef{{
			ID: "search", Version: "1.0.0", Path: providerPath, Digest: providerDigest,
		}},
	}
	catalogDigest := mustWriteJSON(t, root, "tool-providers/catalog.json", catalog)
	suiteData, err := os.ReadFile(filepath.Join(root, "agentsuite.json"))
	if err != nil {
		t.Fatal(err)
	}
	var suite Suite
	if err := decodeStrict(suiteData, &suite); err != nil {
		t.Fatal(err)
	}
	suite.ToolProviderCatalog.Digest = catalogDigest
	mustWriteJSON(t, root, "agentsuite.json", suite)

	report, err := ValidatePath(root)
	if err != nil {
		t.Fatalf("ValidatePath() error = %v", err)
	}
	if report.ToolProviders != 1 || len(report.Capabilities) != 0 {
		t.Fatalf("remote catalog report = %+v", report)
	}

	unretained := []byte(strings.Replace(string(raw), "  \"retained\": true,\n", "", 1))
	mustWrite(t, root, providerPath, unretained)
	unretainedDigest, err := canonicalDigest(unretained)
	if err != nil {
		t.Fatal(err)
	}
	catalog.ToolProviders[0].Digest = unretainedDigest
	suite.ToolProviderCatalog.Digest = mustWriteJSON(t, root, "tool-providers/catalog.json", catalog)
	mustWriteJSON(t, root, "agentsuite.json", suite)

	if _, err := ValidatePath(root); err == nil || !strings.Contains(err.Error(), "has no inward reference") {
		t.Fatalf("unreferenced catalog ToolProvider error = %v", err)
	}
}

func TestRemoteMCPMetadataContributesToToolProviderIdentity(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "remote-mcp", "tool-provider.json"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := canonicalDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	for name, changedRaw := range map[string]string{
		"remote metadata": strings.Replace(string(raw), `"requestMilliseconds": 60000`, `"requestMilliseconds": 60001`, 1),
		"tool schema": strings.Replace(
			string(raw),
			"sha256:25f52652219a4e1e8581e76db1955df62b26a1174a2612eb12538b3efa9d8c84",
			"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			1,
		),
	} {
		t.Run(name, func(t *testing.T) {
			changed, err := canonicalDigest([]byte(changedRaw))
			if err != nil {
				t.Fatal(err)
			}
			if original == changed {
				t.Fatalf("%s did not affect tool provider identity", name)
			}
		})
	}
}

func TestToolProviderKeepsCallableToolsScopedToItsContract(t *testing.T) {
	content, err := loadDirectory(filepath.Join("testdata", "remote-mcp"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := content.data("tool-provider.json")
	if err != nil {
		t.Fatal(err)
	}
	var provider ToolProvider
	if err := decodeStrict(raw, &provider); err != nil {
		t.Fatal(err)
	}
	// Different providers can expose the same tool name.
	for _, id := range []string{"search-primary", "search-secondary"} {
		provider.ID = id
		if err := validateToolProvider(provider, raw, content); err != nil {
			t.Fatalf("provider %s: %v", id, err)
		}
	}
	provider.Tools = append(provider.Tools, provider.Tools[0])
	if err := validateToolProvider(provider, raw, content); err == nil || !strings.Contains(err.Error(), `tool "search" is invalid or duplicated`) {
		t.Fatalf("duplicate callable tool error = %v", err)
	}
	provider.Tools = nil
	if err := validateToolProvider(provider, raw, content); err == nil || !strings.Contains(err.Error(), "at least one tool") {
		t.Fatalf("empty callable tool contract error = %v", err)
	}
}

func TestEarlierDraftProviderMediaTypesAreRejected(t *testing.T) {
	content, err := loadDirectory(filepath.Join("testdata", "remote-mcp"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := content.data("tool-provider.json")
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(string(raw), MediaTypeToolProvider, "application/vnd.agentsuite.tool.v1+json", 1)
	validateSchemaJSON(t, compileReferenceSchema(t, "tool-provider.schema.json"), legacy, false)
	var provider ToolProvider
	if err := decodeStrict([]byte(legacy), &provider); err != nil {
		t.Fatal(err)
	}
	if err := validateToolProvider(provider, []byte(legacy), content); err == nil || !strings.Contains(err.Error(), "mediaType") {
		t.Fatalf("legacy provider media type error = %v", err)
	}

	raw, err = os.ReadFile(filepath.Join("testdata", "tool-providers", "opa-tool-provider-composition.json"))
	if err != nil {
		t.Fatal(err)
	}
	legacy = strings.Replace(string(raw), MediaTypeToolProviderComposition, "application/vnd.agentsuite.tool.composition.v1+json", 1)
	validateSchemaJSON(t, compileReferenceSchema(t, "tool-provider-composition.schema.json"), legacy, false)
	var composition ToolProviderComposition
	if err := decodeStrict([]byte(legacy), &composition); err != nil {
		t.Fatal(err)
	}
	if err := validateToolProviderComposition(composition); err == nil || !strings.Contains(err.Error(), "mediaType") {
		t.Fatalf("legacy provider composition media type error = %v", err)
	}
}

func TestValidateToolProviderInboundReferencesRequiresExplicitRetention(t *testing.T) {
	unreferenced := ToolProvider{ID: "opa", Version: "1.0.0"}
	providers := map[string]ToolProvider{"opa@1.0.0": unreferenced}
	if err := validateToolProviderInboundReferences(providers, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "has no inward reference") {
		t.Fatalf("unreferenced ToolProvider error = %v", err)
	}

	retained := true
	unreferenced.Retained = &retained
	providers["opa@1.0.0"] = unreferenced
	if err := validateToolProviderInboundReferences(providers, nil, nil); err != nil {
		t.Fatalf("retained ToolProvider error = %v", err)
	}
}

func TestValidateToolProviderInboundReferencesAcceptsEveryInwardEdge(t *testing.T) {
	dependencyDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name         string
		providers    map[string]ToolProvider
		agents       map[string]Agent
		compositions map[string]ToolProviderComposition
	}{
		{
			name:      "agent requirement",
			providers: map[string]ToolProvider{"opa@1.0.0": {ID: "opa", Version: "1.0.0"}},
			agents: map[string]Agent{"writer": {
				ToolProviders: []ToolProviderRequirement{{ID: "opa", Version: "1.0.0"}},
			}},
		},
		{
			name:      "ToolProvider composition",
			providers: map[string]ToolProvider{"opa@1.0.0": {ID: "opa", Version: "1.0.0"}},
			compositions: map[string]ToolProviderComposition{"opa@1.0.0@linux/amd64": {
				ID: "opa", Version: "1.0.0",
			}},
		},
		{
			name: "bundle dependency",
			providers: map[string]ToolProvider{
				"root@1.0.0": {
					ID: "root", Version: "1.0.0", Retained: boolPointer(true),
					Variants: []ToolProviderVariant{{Dependencies: []ToolProviderDependency{{
						ID: "opa", Version: "1.0.0", VariantDigest: dependencyDigest,
					}}}},
				},
				"opa@1.0.0": {ID: "opa", Version: "1.0.0"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateToolProviderInboundReferences(test.providers, test.agents, test.compositions); err != nil {
				t.Fatalf("validateToolProviderInboundReferences() error = %v", err)
			}
		})
	}
}

func TestValidateToolProviderRequiresAnImplementation(t *testing.T) {
	provider := ToolProvider{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeToolProvider,
		ID:            "search",
		Version:       "1.0.0",
		Protocol:      "mcp",
		Revision:      "2025-06-18",
		Tools: []Tool{{
			Name:        "search",
			InputSchema: FileRef{Path: "schemas/search-input.json", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		}},
	}
	content := &contentSet{entries: map[string]contentEntry{
		"schemas/search-input.json": {
			Path: "schemas/search-input.json", Type: "file",
			Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}}
	err := validateToolProvider(provider, []byte(`{}`), content)
	if err == nil || !strings.Contains(err.Error(), "or both") {
		t.Fatalf("expected missing implementation error, got %v", err)
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func TestValidateRemoteToolProviderRejectsTransportManagedAndDuplicateHeaders(t *testing.T) {
	remote := RemoteToolProvider{
		Transport:   "streamable-http",
		EndpointRef: "search-mcp-endpoint",
		Headers: []RemoteHeader{
			{Name: "Content-Type", SecretRef: SecretKeyRef{Name: "auth", Key: "token"}},
			{Name: "X-API-Key", SecretRef: SecretKeyRef{Name: "auth", Key: "api-key"}},
			{Name: "x-api-key", SecretRef: SecretKeyRef{Name: "auth", Key: "api-key-2"}},
		},
		Network:      []RemoteNetworkRequirement{{DestinationRef: "search-mcp-egress"}},
		Timeouts:     RemoteTimeouts{ConnectMilliseconds: 5_000, RequestMilliseconds: 60_000},
		Cancellation: "propagate",
		Connection:   "session-aware",
	}
	err := validateRemoteToolProvider(remote)
	if err == nil || !strings.Contains(err.Error(), "transport-managed") || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("expected header validation errors, got %v", err)
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

func TestCompositionToolProvidersMustBeSortedByIdentity(t *testing.T) {
	composition := Composition{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeComposition,
		Agent:         "writer",
		Platform:      Platform{OS: "linux", Architecture: "amd64"},
		BuildProfile:  "default",
		ToolProviders: []ResolvedToolProvider{
			{
				ID:             "source-control",
				Version:        "1.0.0",
				ManifestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				VariantDigest:  "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				ExecutionMode:  ExecutionSharedSandbox,
			},
			{
				ID:             "datetime",
				Version:        "1.0.0",
				ManifestDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				VariantDigest:  "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
				ExecutionMode:  ExecutionSharedSandbox,
			},
		},
	}
	if err := validateComposition(composition); err == nil || !strings.Contains(err.Error(), "sorted by id and version") {
		t.Fatalf("expected tool provider ordering error, got %v", err)
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
	variant := ToolProviderVariant{
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

func TestVariantRejectsSchemaConstrainedFields(t *testing.T) {
	payload := []byte("x")
	digest := digestBytes(payload)
	base := func() ToolProviderVariant {
		return ToolProviderVariant{
			Platform:      Platform{OS: "linux", Architecture: "amd64"},
			VariantDigest: digest,
			InstallRoot:   "/opt/tool",
			PayloadRoot:   "payload",
			Entrypoint:    "/opt/tool/bin/tool",
			Runtime:       RuntimeRequirement{ABI: "static", CPUBaseline: "x86-64-v1"},
			Files: []InventoryEntry{{
				Path: "bin/tool", Type: "file", Mode: 0o755, Size: 1, Digest: digest,
			}},
		}
	}
	content := &contentSet{entries: map[string]contentEntry{
		"payload/bin/tool": {Path: "payload/bin/tool", Type: "file", Mode: 0o755, Size: 1, Digest: digest},
	}}
	tests := []struct {
		name    string
		mutate  func(*ToolProviderVariant)
		message string
	}{
		{
			name: "relative search path",
			mutate: func(variant *ToolProviderVariant) {
				variant.SearchPath = []string{"bin"}
			},
			message: "searchPath",
		},
		{
			name: "invalid network port",
			mutate: func(variant *ToolProviderVariant) {
				variant.Network = []NetworkAccess{{Scheme: "https", Host: "example.com", Port: 0}}
			},
			message: "network access",
		},
		{
			name: "invalid sbom descriptor",
			mutate: func(variant *ToolProviderVariant) {
				variant.SBOM = &Descriptor{MediaType: "application/spdx+json", Digest: "invalid", Size: 1}
			},
			message: "sbom descriptor",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			variant := base()
			test.mutate(&variant)
			if err := validateVariant(variant, digest, content); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("validateVariant() error = %v, want %q", err, test.message)
			}
		})
	}
}

func TestVariantRejectsPayloadOwnerMismatch(t *testing.T) {
	payload := []byte("x")
	digest := digestBytes(payload)
	variant := ToolProviderVariant{
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

func TestBundleClosureTraversesDependenciesAndChecksTheirCollisions(t *testing.T) {
	platform := Platform{OS: "linux", Architecture: "amd64"}
	variant := func(digest, installRoot, fileDigest string, dependencies ...ToolProviderDependency) ToolProviderVariant {
		return ToolProviderVariant{
			Platform:      platform,
			VariantDigest: digest,
			InstallRoot:   installRoot,
			Files: []InventoryEntry{{
				Path: "bin/tool", Type: "file", Mode: 0o755, UID: 0, GID: 0, Size: 1, Digest: fileDigest,
			}},
			Dependencies: dependencies,
		}
	}
	runtimeVariant := variant(
		"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"/opt/shared",
		"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	)
	bridgeVariant := variant(
		"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"/opt/bridge",
		"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		ToolProviderDependency{ID: "runtime", Version: "1.0.0", VariantDigest: runtimeVariant.VariantDigest},
	)
	rootVariant := variant(
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"/opt/root",
		"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		ToolProviderDependency{ID: "bridge", Version: "1.0.0", VariantDigest: bridgeVariant.VariantDigest},
	)
	providers := map[string]ToolProvider{
		"bridge@1.0.0":  {ID: "bridge", Version: "1.0.0", Variants: []ToolProviderVariant{bridgeVariant}},
		"runtime@1.0.0": {ID: "runtime", Version: "1.0.0", Variants: []ToolProviderVariant{runtimeVariant}},
	}

	closure, err := bundleClosure("root@1.0.0", rootVariant, providers)
	if err != nil {
		t.Fatalf("bundleClosure() error = %v", err)
	}
	if len(closure) != 3 || closure[1].providerKey != "bridge@1.0.0" || closure[2].providerKey != "runtime@1.0.0" {
		t.Fatalf("bundleClosure() = %+v, want root and transitive dependencies", closure)
	}

	destinations := map[string]InventoryEntry{}
	for _, bundle := range closure {
		if err := addVariantDestinations(destinations, bundle.variant); err != nil {
			t.Fatalf("addVariantDestinations() error = %v", err)
		}
	}
	conflicting := variant(
		"sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"/opt/shared",
		"sha256:2222222222222222222222222222222222222222222222222222222222222222",
	)
	if err := addVariantDestinations(destinations, conflicting); err == nil ||
		!strings.Contains(err.Error(), "/opt/shared/bin/tool") {
		t.Fatalf("dependency collision error = %v", err)
	}
}

func TestBundleClosureRejectsTransitiveCycle(t *testing.T) {
	platform := Platform{OS: "linux", Architecture: "amd64"}
	aDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	a := ToolProviderVariant{
		Platform: platform, VariantDigest: aDigest,
		Dependencies: []ToolProviderDependency{{ID: "b", Version: "1.0.0", VariantDigest: bDigest}},
	}
	b := ToolProviderVariant{
		Platform: platform, VariantDigest: bDigest,
		Dependencies: []ToolProviderDependency{{ID: "a", Version: "1.0.0", VariantDigest: aDigest}},
	}
	providers := map[string]ToolProvider{
		"a@1.0.0": {ID: "a", Version: "1.0.0", Variants: []ToolProviderVariant{a}},
		"b@1.0.0": {ID: "b", Version: "1.0.0", Variants: []ToolProviderVariant{b}},
	}
	if _, err := bundleClosure("a@1.0.0", a, providers); err == nil ||
		!strings.Contains(err.Error(), "dependency cycle") {
		t.Fatalf("cycle error = %v", err)
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

func TestOCILayoutRejectsAnExtraIndexDescriptor(t *testing.T) {
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
	if _, err := ValidatePath(root); err == nil || !strings.Contains(err.Error(), "exactly one manifest descriptor") {
		t.Fatalf("extra index descriptor error = %v", err)
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
		suite:         Suite{Capabilities: []string{"bundled-stdio-mcp", "bundled-stdio-mcp"}},
		toolProviders: map[string]ToolProvider{"reader@1.0.0": {Variants: []ToolProviderVariant{{}}}},
		agents:        map[string]Agent{},
		compositions:  map[string]Composition{},
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
	  "mediaType":"application/vnd.agentsuite.sandbox.binding.v1+json",
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
	  "mediaType":"application/vnd.agentsuite.sandbox.binding.v1+json",
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
		ToolProviders: []ToolProviderRequirement{},
		Invokes:       []AgentInvoke{},
		Extensions:    []Extension{},
	}
	agentDigest := mustWriteJSON(t, root, "agents/writer.json", agent)

	catalog := ToolProviderCatalog{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeToolProviderCatalog,
		ToolProviders: []ManifestRef{},
	}
	catalogDigest := mustWriteJSON(t, root, "tool-providers/catalog.json", catalog)

	image := Descriptor{
		MediaType: ociManifestMediaType,
		Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Size:      1,
	}
	profile := BuildProfile{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeBuildProfile,
		ID:            "default",
		RuntimeBase:   []PlatformImage{{Platform: Platform{OS: "linux", Architecture: "amd64"}, ImageRef: "registry.example/agentsuite/runtime-base@" + image.Digest, Image: image}},
		Harness:       []PlatformImage{{Platform: Platform{OS: "linux", Architecture: "amd64"}, ImageRef: "registry.example/agentsuite/harness@" + image.Digest, Image: image}},
		SourceEpoch:   1,
	}
	profileDigest := mustWriteJSON(t, root, "build-profiles/default.json", profile)

	composition := Composition{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeComposition,
		Agent:         "writer",
		Platform:      Platform{OS: "linux", Architecture: "amd64"},
		BuildProfile:  "default",
		ToolProviders: []ResolvedToolProvider{},
	}
	compositionDigest := mustWriteJSON(t, root, "compositions/writer-linux-amd64.json", composition)

	suite := Suite{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeSuite,
		Name:          "example",
		Agents: []ManifestRef{{
			ID: "writer", Path: "agents/writer.json", Digest: agentDigest,
		}},
		ToolProviderCatalog: ManifestRef{ID: "catalog", Path: "tool-providers/catalog.json", Digest: catalogDigest},
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

func mustVariantDigest(t *testing.T, variant ToolProviderVariant) string {
	t.Helper()
	data, err := json.Marshal(struct {
		Variants []ToolProviderVariant `json:"variants"`
	}{Variants: []ToolProviderVariant{variant}})
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
