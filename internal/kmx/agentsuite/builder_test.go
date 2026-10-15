package agentsuite

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
)

func specFixture(name string) string {
	return filepath.Join("..", "..", "..", "agentsuite", "testdata", name)
}

func TestResolveSandboxPlanSelectsMinimalComposition(t *testing.T) {
	plan, err := ResolveSandboxPlan(specFixture("minimal"), BuildSelection{})
	if err != nil {
		t.Fatalf("ResolveSandboxPlan() error = %v", err)
	}
	if plan.Agent.ID != "writer" || plan.Composition.Platform.String() != "linux/amd64" {
		t.Fatalf("unexpected plan selection: agent=%q platform=%s", plan.Agent.ID, plan.Composition.Platform)
	}
	if plan.RuntimeBase.ImageRef == "" || plan.Harness.ImageRef == "" {
		t.Fatalf("plan did not resolve build images: runtime=%q harness=%q", plan.RuntimeBase.ImageRef, plan.Harness.ImageRef)
	}
	if got := string(plan.Instructions); got != "Write clearly.\n" {
		t.Fatalf("instructions = %q", got)
	}
	if got := plan.CompositionDigest; got != "sha256:c19a6173a3bad6723b5f4c36f49c8ee21afbd65317d9c9c4275ebd130b2223d5" {
		t.Fatalf("composition digest = %s", got)
	}
	if got := plan.SuiteManifestHash; got != "sha256:59b19197bc5a053ec68968dfd9f990d50c24829c5761f251559f26cff6ba0711" {
		t.Fatalf("suite manifest hash = %s", got)
	}
}

func TestResolveSandboxPlanSelectsBuildableIncidentAnalystExample(t *testing.T) {
	plan, err := ResolveSandboxPlan(filepath.Join("testdata", "incident-analyst"), BuildSelection{})
	if err != nil {
		t.Fatalf("ResolveSandboxPlan() error = %v", err)
	}
	if plan.Agent.ID != "incident-analyst" || plan.Agent.Model.Model != "gpt-5-mini" {
		t.Fatalf("unexpected agent: %+v", plan.Agent)
	}
	if got := plan.RuntimeBase.ImageRef; !strings.HasPrefix(got, "docker.io/library/python@sha256:") {
		t.Fatalf("runtime base = %q", got)
	}
	if got := plan.Harness.ImageRef; !strings.HasPrefix(got, "ghcr.io/orka-agents/agentkit/serve-pydantic-ai@sha256:") {
		t.Fatalf("harness = %q", got)
	}
	if !strings.Contains(string(plan.Instructions), "incident response analyst") {
		t.Fatalf("unexpected instructions: %q", plan.Instructions)
	}
}

func TestResolveSandboxPlanRequiresAgentForMultiAgentSuite(t *testing.T) {
	_, err := ResolveSandboxPlan(specFixture("coordinator-workers"), BuildSelection{})
	if err == nil || !strings.Contains(err.Error(), "--agent is required") {
		t.Fatalf("ResolveSandboxPlan() error = %v", err)
	}
}

func TestResolveSandboxPlanRejectsUnknownPlatform(t *testing.T) {
	_, err := ResolveSandboxPlan(specFixture("minimal"), BuildSelection{Agent: "writer", Platform: "linux/arm64"})
	if err == nil || !strings.Contains(err.Error(), "no composition") {
		t.Fatalf("ResolveSandboxPlan() error = %v", err)
	}
}

func TestResolveSandboxPlanRequiresPlatformAndSelectsExactImages(t *testing.T) {
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(specFixture("minimal"))); err != nil {
		t.Fatal(err)
	}
	var suite spec.Suite
	if err := spec.Unmarshal(readBuilderTestFile(t, root, "agentsuite.json"), &suite); err != nil {
		t.Fatal(err)
	}
	var profile spec.BuildProfile
	if err := spec.Unmarshal(readBuilderTestFile(t, root, suite.BuildProfiles[0].Path), &profile); err != nil {
		t.Fatal(err)
	}
	arm64 := spec.Platform{OS: "linux", Architecture: "arm64"}
	base, harness := profile.RuntimeBase[0], profile.Harness[0]
	base.Platform, harness.Platform = arm64, arm64
	profile.RuntimeBase = append(profile.RuntimeBase, base)
	profile.Harness = append(profile.Harness, harness)
	suite.BuildProfiles[0].Descriptor = writeBuilderTestDescriptor(t, root, suite.BuildProfiles[0].Path, spec.MediaTypeBuildProfile, profile)
	var composition spec.Composition
	if err := spec.Unmarshal(readBuilderTestFile(t, root, suite.Compositions[0].Path), &composition); err != nil {
		t.Fatal(err)
	}
	composition.Platform = arm64
	compositionPath := "compositions/writer-linux-arm64.json"
	suite.Compositions = append(suite.Compositions, spec.CompositionRef{
		Agent: "writer", Platform: arm64, Path: compositionPath,
		Descriptor: writeBuilderTestDescriptor(t, root, compositionPath, spec.MediaTypeComposition, composition),
	})
	writeBuilderTestJSON(t, root, "agentsuite.json", suite)
	if _, err := ResolveSandboxPlan(root, BuildSelection{}); err == nil || !strings.Contains(err.Error(), "--platform is required (available: linux/amd64, linux/arm64)") {
		t.Fatalf("ambiguous platform error = %v", err)
	}
	plan, err := ResolveSandboxPlan(root, BuildSelection{Platform: "linux/arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Composition.Platform != arm64 || plan.RuntimeBase.Platform != arm64 || plan.Harness.Platform != arm64 {
		t.Fatalf("wrong exact platform selected: %+v", plan)
	}
}

func TestResolveSandboxPlanRejectsUnknownAgent(t *testing.T) {
	_, err := ResolveSandboxPlan(specFixture("minimal"), BuildSelection{Agent: "missing"})
	if err == nil || !strings.Contains(err.Error(), `has no agent "missing"`) {
		t.Fatalf("ResolveSandboxPlan() error = %v", err)
	}
}

// Dropping the public conformance check would incorrectly report the caller's
// selection error instead of refusing these invalid suites.
func TestResolveSandboxPlanRefusesInvalidSuiteBeforeSelection(t *testing.T) {
	for _, test := range []struct{ name, old, replacement, want string }{
		{"unsupported version", `"schemaVersion": "1.0.0-draft"`, `"schemaVersion": "unsupported"`, "schemaVersion"},
		{"critical extension", `"name": "minimal",`, `"name": "minimal",` + "\n  " + `"extensions": [{"name":"future-feature","critical":true}],`, "critical extension"},
		{"missing child", `"path": "agents/writer.json"`, `"path": "agents/missing.json"`, "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.CopyFS(root, os.DirFS(specFixture("minimal"))); err != nil {
				t.Fatal(err)
			}
			data := readBuilderTestFile(t, root, "agentsuite.json")
			changed := strings.Replace(string(data), test.old, test.replacement, 1)
			if changed == string(data) {
				t.Fatal("fixture mutation did not apply")
			}
			writeBuilderTestFile(t, root, "agentsuite.json", []byte(changed))
			plan, err := ResolveSandboxPlan(root, BuildSelection{Agent: "not-declared"})
			if plan != nil || err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "has no agent") {
				t.Fatalf("invalid suite plan = %+v, error = %v", plan, err)
			}
		})
	}
}

func selectionMetadata() (*buildMetadata, spec.Composition) {
	platform := spec.Platform{OS: "linux", Architecture: "amd64"}
	dependencyVariant := spec.ToolProviderVariant{
		Platform: platform, VariantDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	rootVariant := spec.ToolProviderVariant{
		Platform: platform, VariantDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Dependencies: []spec.ToolProviderDependency{{ID: "shared", Version: "1.0.0", VariantDigest: dependencyVariant.VariantDigest}},
	}
	v := &buildMetadata{
		toolProviders: map[string]spec.ToolProvider{
			"root@1.0.0":   {ID: "root", Version: "1.0.0", Variants: []spec.ToolProviderVariant{rootVariant}},
			"shared@1.0.0": {ID: "shared", Version: "1.0.0", Variants: []spec.ToolProviderVariant{dependencyVariant}},
		},
		toolProviderDigests: map[string]string{
			"root@1.0.0":   "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			"shared@1.0.0": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		},
	}
	return v, spec.Composition{
		Platform: platform,
		ToolProviders: []spec.ResolvedToolProvider{{
			ID: "root", Version: "1.0.0", ManifestDigest: v.toolProviderDigests["root@1.0.0"], VariantDigest: rootVariant.VariantDigest,
		}},
	}
}

func TestResolveSelectedToolProvidersMarksEveryDeclaredRoot(t *testing.T) {
	v, composition := selectionMetadata()
	composition.ToolProviders = append(composition.ToolProviders, spec.ResolvedToolProvider{
		ID: "shared", Version: "1.0.0", ManifestDigest: v.toolProviderDigests["shared@1.0.0"],
		VariantDigest: v.toolProviders["shared@1.0.0"].Variants[0].VariantDigest,
	})
	selected, err := resolveSelectedToolProviders(v, composition)
	if err != nil {
		t.Fatalf("resolveSelectedToolProviders() error = %v", err)
	}
	if len(selected) != 2 || selected[0].ID != "root" || selected[1].ID != "shared" {
		t.Fatalf("selected providers = %+v", selected)
	}
	for _, provider := range selected {
		if !provider.Root || provider.ManifestDigest != v.toolProviderDigests[provider.ID+"@"+provider.Version] {
			t.Fatalf("declared provider %s@%s is not a digest-bound root: %+v", provider.ID, provider.Version, provider)
		}
	}
}

func TestResolveSelectedToolProvidersIncludesDependencyWithoutAuthorizingIt(t *testing.T) {
	v, composition := selectionMetadata()
	selected, err := resolveSelectedToolProviders(v, composition)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || !selected[0].Root || selected[1].Root || selected[1].ID != "shared" {
		t.Fatalf("selected providers = %+v", selected)
	}
}

func TestResolveSelectedToolProvidersDeduplicatesTransitiveDependencies(t *testing.T) {
	v, composition := selectionMetadata()
	bridge := v.toolProviders["root@1.0.0"]
	bridge.ID = "bridge"
	bridge.Variants = append([]spec.ToolProviderVariant(nil), bridge.Variants...)
	bridge.Variants[0].VariantDigest = "sha256:" + strings.Repeat("e", 64)
	v.toolProviders["bridge@1.0.0"] = bridge
	v.toolProviderDigests["bridge@1.0.0"] = "sha256:" + strings.Repeat("f", 64)
	root := v.toolProviders["root@1.0.0"]
	root.Variants[0].Dependencies = append(root.Variants[0].Dependencies, spec.ToolProviderDependency{
		ID: "bridge", Version: "1.0.0", VariantDigest: bridge.Variants[0].VariantDigest,
	})
	v.toolProviders["root@1.0.0"] = root
	selected, err := resolveSelectedToolProviders(v, composition)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 3 || selected[0].ID != "root" || selected[1].ID != "shared" || selected[2].ID != "bridge" {
		t.Fatalf("transitive selection = %+v", selected)
	}
}

func TestResolveSelectedToolProvidersRefusesMissingMismatchedAndCyclicSelections(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*buildMetadata, *spec.Composition)
		want   string
	}{
		{"missing root", func(v *buildMetadata, _ *spec.Composition) { delete(v.toolProviders, "root@1.0.0") }, "missing tool provider"},
		{"stale root manifest", func(_ *buildMetadata, c *spec.Composition) {
			c.ToolProviders[0].ManifestDigest = "sha256:" + strings.Repeat("e", 64)
		}, "manifest digest"},
		{"unpinned root manifest", func(_ *buildMetadata, c *spec.Composition) { c.ToolProviders[0].ManifestDigest = "" }, "manifest digest"},
		{"stale root variant", func(_ *buildMetadata, c *spec.Composition) {
			c.ToolProviders[0].VariantDigest = "sha256:" + strings.Repeat("e", 64)
		}, "matching variant"},
		{"wrong root platform", func(_ *buildMetadata, c *spec.Composition) { c.Platform.Architecture = "arm64" }, "matching variant"},
		{"OCI variant is not interchangeable", func(_ *buildMetadata, c *spec.Composition) { c.Platform.Variant = "v8" }, "matching variant"},
		{"ambiguous root variant", func(v *buildMetadata, _ *spec.Composition) {
			p := v.toolProviders["root@1.0.0"]
			p.Variants = append(p.Variants, p.Variants[0])
			v.toolProviders["root@1.0.0"] = p
		}, "matching variant"},
		{"missing dependency", func(v *buildMetadata, _ *spec.Composition) { delete(v.toolProviders, "shared@1.0.0") }, "missing tool provider"},
		{"unpinned dependency manifest", func(v *buildMetadata, _ *spec.Composition) { delete(v.toolProviderDigests, "shared@1.0.0") }, "pinned manifest digest"},
		{"stale dependency variant", func(v *buildMetadata, _ *spec.Composition) {
			p := v.toolProviders["shared@1.0.0"]
			p.Variants[0].VariantDigest = "sha256:" + strings.Repeat("e", 64)
			v.toolProviders["shared@1.0.0"] = p
		}, "matching variant"},
		{"wrong dependency platform", func(v *buildMetadata, _ *spec.Composition) {
			p := v.toolProviders["shared@1.0.0"]
			p.Variants[0].Platform.Architecture = "arm64"
			v.toolProviders["shared@1.0.0"] = p
		}, "matching variant"},
		{"dependency cycle", func(v *buildMetadata, _ *spec.Composition) {
			p := v.toolProviders["shared@1.0.0"]
			p.Variants[0].Dependencies = []spec.ToolProviderDependency{{ID: "root", Version: "1.0.0", VariantDigest: v.toolProviders["root@1.0.0"].Variants[0].VariantDigest}}
			v.toolProviders["shared@1.0.0"] = p
		}, "dependency cycle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			v, composition := selectionMetadata()
			test.mutate(v, &composition)
			selected, err := resolveSelectedToolProviders(v, composition)
			if selected != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("selection = %+v, error = %v, want %q", selected, err, test.want)
			}
		})
	}
}

// The metadata reread must bind to the validated root's child digests, not to
// whichever bytes are present after the public validator returns.
func TestLoadBuildMetadataRefusesChangedPinnedChildren(t *testing.T) {
	for _, child := range []string{"agents/writer.json", "tool-providers/catalog.json", "build-profiles/default.json", "compositions/writer-linux-amd64.json"} {
		t.Run(child, func(t *testing.T) {
			root := t.TempDir()
			if err := os.CopyFS(root, os.DirFS(specFixture("minimal"))); err != nil {
				t.Fatal(err)
			}
			suiteBytes := readBuilderTestFile(t, root, "agentsuite.json")
			if _, err := spec.ValidatePath(root); err != nil {
				t.Fatal(err)
			}
			data := readBuilderTestFile(t, root, child)
			mutated := strings.Replace(string(data), `"schemaVersion":"1.0.0-draft"`, `"schemaVersion":"changed"`, 1)
			if mutated == string(data) {
				t.Fatalf("fixture %s does not contain the expected schemaVersion marker", child)
			}
			writeBuilderTestFile(t, root, child, []byte(mutated))
			handle, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer handle.Close()
			if _, err := loadBuildMetadata(handle, suiteBytes); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
				t.Fatalf("changed %s error = %v", child, err)
			}
		})
	}
}

// A child file that regains JSON whitespace after ValidateDirectory ran must
// be refused even though trailing whitespace does not change its JCS digest:
// the reread bytes are no longer the descriptor's own canonical
// representation, so a digest-only comparison would wrongly accept them.
func TestLoadBuildMetadataRefusesNonCanonicalPinnedChildBytes(t *testing.T) {
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(specFixture("minimal"))); err != nil {
		t.Fatal(err)
	}
	suiteBytes := readBuilderTestFile(t, root, "agentsuite.json")
	if _, err := spec.ValidatePath(root); err != nil {
		t.Fatal(err)
	}
	child := "agents/writer.json"
	data := readBuilderTestFile(t, root, child)
	padded := append(append([]byte(nil), data...), ' ')
	writeBuilderTestFile(t, root, child, padded)
	handle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if _, err := loadBuildMetadata(handle, suiteBytes); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("non-canonical %s error = %v", child, err)
	}
}

func TestLoadBuildMetadataVerifiesProviderAndProviderCompositionPins(t *testing.T) {
	for _, child := range []string{"tool-providers/provider.json", "tool-provider-compositions/provider.json"} {
		t.Run(child, func(t *testing.T) {
			root := t.TempDir()
			if err := os.CopyFS(root, os.DirFS(specFixture("minimal"))); err != nil {
				t.Fatal(err)
			}
			var suite spec.Suite
			if err := spec.Unmarshal(readBuilderTestFile(t, root, "agentsuite.json"), &suite); err != nil {
				t.Fatal(err)
			}
			providerPath := "tool-providers/provider.json"
			provider := spec.ToolProvider{ID: "provider", Version: "1.0.0"}
			providerDescriptor := writeBuilderTestDescriptor(t, root, providerPath, spec.MediaTypeToolProvider, provider)
			catalog := spec.ToolProviderCatalog{ToolProviders: []spec.ManifestRef{{ID: provider.ID, Version: provider.Version, Path: providerPath, Descriptor: providerDescriptor}}}
			suite.ToolProviderCatalog.Descriptor = writeBuilderTestDescriptor(t, root, suite.ToolProviderCatalog.Path, spec.MediaTypeToolProviderCatalog, catalog)
			compositionPath := "tool-provider-compositions/provider.json"
			composition := spec.ToolProviderComposition{ID: "provider", Version: "1.0.0"}
			suite.ToolProviderCompositions = []spec.ToolProviderCompositionRef{{ID: composition.ID, Version: composition.Version, Path: compositionPath,
				Descriptor: writeBuilderTestDescriptor(t, root, compositionPath, spec.MediaTypeToolProviderComposition, composition)}}
			writeBuilderTestJSON(t, root, "agentsuite.json", suite)
			suiteBytes := readBuilderTestFile(t, root, "agentsuite.json")
			handle, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer handle.Close()
			// The private loader checks immutable metadata binding, not conformance.
			metadata, err := loadBuildMetadata(handle, suiteBytes)
			if err != nil || metadata.toolProviders["provider@1.0.0"].ID != "provider" {
				t.Fatalf("metadata = %+v, error = %v", metadata, err)
			}
			data := readBuilderTestFile(t, root, child)
			writeBuilderTestFile(t, root, child, []byte(strings.Replace(string(data), `"id":"provider"`, `"id":"changed"`, 1)))
			if _, err := loadBuildMetadata(handle, suiteBytes); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
				t.Fatalf("changed %s error = %v", child, err)
			}
		})
	}
}

func TestLoadBuildMetadataRejectsStrictJSONAndOversizedChildren(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
		want string
	}{
		{"unknown field", []byte(`{"unexpected":true}`), "unknown field"},
		{"duplicate field", []byte(`{"id":"writer","id":"writer"}`), "duplicate JSON key"},
		{"oversized", []byte(strings.Repeat(" ", 4<<20+1)), "at most"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.CopyFS(root, os.DirFS(specFixture("minimal"))); err != nil {
				t.Fatal(err)
			}
			var suite spec.Suite
			if err := spec.Unmarshal(readBuilderTestFile(t, root, "agentsuite.json"), &suite); err != nil {
				t.Fatal(err)
			}
			// Pin the descriptor to these exact bytes' own raw digest, so the
			// cheap pre-parse size/digest gate passes and spec.Unmarshal's
			// own strict-decode diagnostics (the actual target of this test)
			// still run and surface first. The oversized case never reaches
			// that gate: readBuildFile's size limit rejects it first.
			sum := sha256.Sum256(test.data)
			suite.Agents[0].Descriptor.Digest = "sha256:" + hex.EncodeToString(sum[:])
			suite.Agents[0].Descriptor.Size = int64(len(test.data))
			suiteBytes, err := spec.Marshal(suite)
			if err != nil {
				t.Fatal(err)
			}
			writeBuilderTestFile(t, root, "agents/writer.json", test.data)
			handle, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer handle.Close()
			if _, err := loadBuildMetadata(handle, suiteBytes); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("metadata error = %v, want %s", err, test.want)
			}
		})
	}
}

func TestReadBuildInstructionsRechecksRawDigestAndSize(t *testing.T) {
	root := t.TempDir()
	data := []byte("Write clearly.\n")
	writeBuilderTestFile(t, root, "instructions.md", data)
	sum := sha256.Sum256(data)
	ref := spec.FileRef{Path: "instructions.md", Descriptor: spec.Descriptor{
		MediaType: spec.MediaTypeInstructions, Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(data)),
	}}
	handle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	got, err := readBuildInstructions(handle, ref)
	if err != nil || string(got) != string(data) {
		t.Fatalf("instructions = %q, %v", got, err)
	}
	writeBuilderTestFile(t, root, ref.Path, []byte("Wrong clearly.\n"))
	if _, err := readBuildInstructions(handle, ref); err == nil || !strings.Contains(err.Error(), "changed after validation") {
		t.Fatalf("changed instructions error = %v", err)
	}
	file, err := os.OpenFile(filepath.Join(root, ref.Path), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(256<<20 + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	if _, err := readBuildInstructions(handle, ref); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("oversized instructions error = %v", err)
	}
}

func TestReadBuildFileRejectsUnsafeAndNonRegularPaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeBuilderTestFile(t, outside, "secret", []byte("outside"))
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	for _, name := range []string{"../secret", "/etc/passwd", "a/../secret", `a\b`, "escape", "directory"} {
		t.Run(name, func(t *testing.T) {
			if _, err := readBuildFile(handle, name, 4<<20); err == nil {
				t.Fatalf("unsafe path %q accepted", name)
			}
		})
	}
}

func writeBuilderTestJSON(t *testing.T, root, name string, value any) string {
	t.Helper()
	data, err := spec.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeBuilderTestFile(t, root, name, data)
	digest, err := spec.Digest(data)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func writeBuilderTestDescriptor(t *testing.T, root, name, mediaType string, value any) spec.Descriptor {
	t.Helper()
	data, err := spec.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeBuilderTestFile(t, root, name, data)
	digest, err := spec.Digest(data)
	if err != nil {
		t.Fatal(err)
	}
	return spec.Descriptor{MediaType: mediaType, Digest: digest, Size: int64(len(data))}
}

func readBuilderTestFile(t *testing.T, root, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeBuilderTestFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	filePath := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
