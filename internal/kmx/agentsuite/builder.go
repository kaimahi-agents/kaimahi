package agentsuite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	agentsuite "github.com/agent-primitives/suite"
)

const (
	maxBuildJSONBytes     = int64(4 << 20)
	maxBuildMetadataBytes = int64(256 << 20)
)

// BuildSelection identifies one runnable agent composition.
type BuildSelection struct {
	Agent    string
	Platform string
}

// SelectedToolProvider is one exact provider bundle selected by a composition.
// The closure order follows selection traversal; builders must not infer install
// ordering from this slice.
type SelectedToolProvider struct {
	ID             string
	Version        string
	ManifestDigest string
	Variant        agentsuite.ToolProviderVariant
	Root           bool
}

// SandboxPlan is the provider-neutral, validated metadata selected for a
// sandbox image builder. The current experimental contract does not yet expose
// verified image blobs or ToolProvider payload content.
type SandboxPlan struct {
	Suite             agentsuite.Suite
	SuiteManifestHash string
	Agent             agentsuite.Agent
	Instructions      []byte
	Composition       agentsuite.Composition
	CompositionDigest string
	BuildProfile      agentsuite.BuildProfile
	RuntimeBase       agentsuite.PlatformImage
	Harness           agentsuite.PlatformImage
	ToolProviders     []SelectedToolProvider
}

// BuildResult describes archive emission and its inspection policy. Streaming
// builders leave digest metadata unset; InspectImageArchive populates verified
// identities before BuildSuite publishes the archive.
type BuildResult struct {
	MediaType string
	// Digest is the runnable image manifest, never the attestation-inclusive index.
	Digest string
	// IndexDigest includes attestations when present and can vary between builds.
	IndexDigest           string
	SBOMDigest            string
	HasAttestations       bool
	AttestationsRequested bool
	RequireAttestations   bool
	Warnings              []string
}

// SandboxBuilder turns one resolved plan into an image archive.
type SandboxBuilder interface {
	Build(context.Context, SandboxPlan, io.Writer) (BuildResult, error)
}

// ResolveSandboxPlan validates an extracted AgentSuite directory and resolves
// exactly one agent/platform composition.
func ResolveSandboxPlan(root string, selection BuildSelection) (*SandboxPlan, error) {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	rootHandle, err := os.OpenRoot(canonicalRoot)
	if err != nil {
		return nil, err
	}
	defer rootHandle.Close()
	suiteBytes, err := readBuildFile(rootHandle, "agentsuite.json", maxBuildJSONBytes)
	if err != nil {
		return nil, err
	}
	// Public suite validation is the conformance authority. The byte comparison
	// detects ordinary root-manifest mutation, not a transactional snapshot.
	if _, err := agentsuite.ValidateDirectory(canonicalRoot); err != nil {
		return nil, err
	}
	after, err := readBuildFile(rootHandle, "agentsuite.json", maxBuildJSONBytes)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(suiteBytes, after) {
		return nil, errors.New("agentsuite.json changed during validation")
	}
	v, err := loadBuildMetadata(rootHandle, suiteBytes)
	if err != nil {
		return nil, err
	}

	agentID := selection.Agent
	if agentID == "" {
		if len(v.agents) != 1 {
			return nil, fmt.Errorf("AgentSuite contains %d agents; --agent is required", len(v.agents))
		}
		for id := range v.agents {
			agentID = id
		}
	}
	agent, ok := v.agents[agentID]
	if !ok {
		return nil, fmt.Errorf("AgentSuite has no agent %q", agentID)
	}

	var candidates []agentsuite.Composition
	for _, composition := range v.compositions {
		if composition.Agent == agentID {
			candidates = append(candidates, composition)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("agent %q has no platform composition", agentID)
	}
	if selection.Platform == "" {
		if len(candidates) != 1 {
			var platforms []string
			for _, composition := range candidates {
				platforms = append(platforms, composition.Platform.String())
			}
			slices.Sort(platforms)
			return nil, fmt.Errorf("agent %q has %d compositions; --platform is required (available: %s)",
				agentID, len(candidates), strings.Join(platforms, ", "))
		}
	} else {
		filtered := candidates[:0]
		for _, composition := range candidates {
			if composition.Platform.String() == selection.Platform {
				filtered = append(filtered, composition)
			}
		}
		candidates = filtered
	}
	if len(candidates) != 1 {
		return nil, fmt.Errorf("agent %q has no composition for platform %q", agentID, selection.Platform)
	}
	composition := candidates[0]
	profile := v.builds[composition.BuildProfile]

	runtimeBase, err := exactPlatformImage("runtimeBase", profile.RuntimeBase, composition.Platform)
	if err != nil {
		return nil, err
	}
	harness, err := exactPlatformImage("harness", profile.Harness, composition.Platform)
	if err != nil {
		return nil, err
	}

	instructions, err := readBuildInstructions(rootHandle, agent.Instructions)
	if err != nil {
		return nil, fmt.Errorf("agent %s instructions: %w", agentID, err)
	}
	suiteHash, err := agentsuite.Digest(suiteBytes)
	if err != nil {
		return nil, err
	}
	compositionDigest := ""
	for _, ref := range v.suite.Compositions {
		if ref.Agent == composition.Agent && ref.Platform == composition.Platform {
			compositionDigest = ref.Descriptor.Digest
			break
		}
	}
	selected, err := resolveSelectedToolProviders(v, composition)
	if err != nil {
		return nil, err
	}
	return &SandboxPlan{
		Suite:             v.suite,
		SuiteManifestHash: suiteHash,
		Agent:             agent,
		Instructions:      instructions,
		Composition:       composition,
		CompositionDigest: compositionDigest,
		BuildProfile:      profile,
		RuntimeBase:       runtimeBase,
		Harness:           harness,
		ToolProviders:     selected,
	}, nil
}

// buildMetadata retains only the documents needed by the builder after public
// validation, without exposing or replicating the spec validator's graph.
type buildMetadata struct {
	suite               agentsuite.Suite
	agents              map[string]agentsuite.Agent
	builds              map[string]agentsuite.BuildProfile
	compositions        []agentsuite.Composition
	toolProviders       map[string]agentsuite.ToolProvider
	toolProviderDigests map[string]string
}

func loadBuildMetadata(root *os.Root, suiteBytes []byte) (*buildMetadata, error) {
	v := &buildMetadata{
		agents:              map[string]agentsuite.Agent{},
		builds:              map[string]agentsuite.BuildProfile{},
		toolProviders:       map[string]agentsuite.ToolProvider{},
		toolProviderDigests: map[string]string{},
	}
	if err := agentsuite.Unmarshal(suiteBytes, &v.suite); err != nil {
		return nil, fmt.Errorf("agentsuite.json: %w", err)
	}
	retained := int64(len(suiteBytes))
	readJSON := func(name string, descriptor agentsuite.Descriptor, out any) error {
		data, err := readBuildFile(root, name, maxBuildJSONBytes)
		if err != nil {
			return err
		}
		if int64(len(data)) > maxBuildMetadataBytes-retained {
			return fmt.Errorf("build metadata exceeds %d bytes", maxBuildMetadataBytes)
		}
		retained += int64(len(data))
		// Reject a size/digest-mismatched child before parsing it: a file
		// replaced after ValidateDirectory ran should not receive full
		// strict-JSON decoding merely to discover the mismatch afterward.
		if !agentsuite.DescriptorSizeAndDigestMatch(data, descriptor) {
			return fmt.Errorf("%s manifest digest mismatch after validation", name)
		}
		if err := agentsuite.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		// Recheck the full descriptor (digest, size, and stored-canonical-byte
		// identity) against the freshly reread bytes, not merely the digest:
		// a file could otherwise gain or lose whitespace after
		// ValidateDirectory ran and still satisfy a digest-only comparison.
		if !agentsuite.MatchesCanonicalDescriptor(data, descriptor) {
			return fmt.Errorf("%s manifest digest mismatch after validation", name)
		}
		return nil
	}
	for _, ref := range v.suite.Agents {
		var agent agentsuite.Agent
		if err := readJSON(ref.Path, ref.Descriptor, &agent); err != nil {
			return nil, err
		}
		v.agents[ref.ID] = agent
	}
	for _, ref := range v.suite.BuildProfiles {
		var profile agentsuite.BuildProfile
		if err := readJSON(ref.Path, ref.Descriptor, &profile); err != nil {
			return nil, err
		}
		v.builds[ref.ID] = profile
	}
	for _, ref := range v.suite.Compositions {
		var composition agentsuite.Composition
		if err := readJSON(ref.Path, ref.Descriptor, &composition); err != nil {
			return nil, err
		}
		v.compositions = append(v.compositions, composition)
	}
	var catalog agentsuite.ToolProviderCatalog
	if err := readJSON(v.suite.ToolProviderCatalog.Path, v.suite.ToolProviderCatalog.Descriptor, &catalog); err != nil {
		return nil, err
	}
	for _, ref := range catalog.ToolProviders {
		var provider agentsuite.ToolProvider
		if err := readJSON(ref.Path, ref.Descriptor, &provider); err != nil {
			return nil, err
		}
		key := ref.ID + "@" + ref.Version
		v.toolProviders[key] = provider
		v.toolProviderDigests[key] = ref.Descriptor.Digest
	}
	for _, ref := range v.suite.ToolProviderCompositions {
		var composition agentsuite.ToolProviderComposition
		if err := readJSON(ref.Path, ref.Descriptor, &composition); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func resolveSelectedToolProviders(v *buildMetadata, composition agentsuite.Composition) ([]SelectedToolProvider, error) {
	roots := map[string]bool{}
	for _, resolved := range composition.ToolProviders {
		roots[resolved.ID+"@"+resolved.Version] = true
	}
	var selected []SelectedToolProvider
	seen := map[string]bool{}
	active := map[string]bool{}
	var visit func(string, agentsuite.ToolProviderVariant) error
	visit = func(key string, variant agentsuite.ToolProviderVariant) error {
		identity := key + "#" + variant.VariantDigest
		if active[identity] {
			return fmt.Errorf("tool provider %s has a dependency cycle", key)
		}
		if seen[identity] {
			return nil
		}
		active[identity] = true
		id, version, _ := strings.Cut(key, "@")
		selected = append(selected, SelectedToolProvider{
			ID:             id,
			Version:        version,
			ManifestDigest: v.toolProviderDigests[key],
			Variant:        variant,
			Root:           roots[key],
		})
		for _, dep := range variant.Dependencies {
			depKey := dep.ID + "@" + dep.Version
			provider, ok := v.toolProviders[depKey]
			if !ok {
				return fmt.Errorf("missing tool provider %s", depKey)
			}
			if !agentsuite.IsValidDigest(v.toolProviderDigests[depKey]) {
				return fmt.Errorf("dependency %s has no pinned manifest digest", depKey)
			}
			dependency, matches := exactBuildVariant(provider.Variants, variant.Platform)
			if matches != 1 || !agentsuite.IsValidDigest(dep.VariantDigest) || dependency.VariantDigest != dep.VariantDigest {
				return fmt.Errorf("dependency %s must select exactly one matching variant", depKey)
			}
			if err := visit(depKey, dependency); err != nil {
				return err
			}
		}
		delete(active, identity)
		seen[identity] = true
		return nil
	}
	for _, resolved := range composition.ToolProviders {
		key := resolved.ID + "@" + resolved.Version
		provider, ok := v.toolProviders[key]
		if !ok {
			return nil, fmt.Errorf("composition contains missing tool provider %s", key)
		}
		if !agentsuite.IsValidDigest(resolved.ManifestDigest) || resolved.ManifestDigest != v.toolProviderDigests[key] {
			return nil, fmt.Errorf("tool provider %s manifest digest mismatch", key)
		}
		variant, matches := exactBuildVariant(provider.Variants, composition.Platform)
		if matches != 1 || !agentsuite.IsValidDigest(resolved.VariantDigest) || variant.VariantDigest != resolved.VariantDigest {
			return nil, fmt.Errorf("tool provider %s must select exactly one matching variant for %s", key, composition.Platform)
		}
		if err := visit(key, variant); err != nil {
			return nil, err
		}
	}
	return selected, nil
}

func exactBuildVariant(variants []agentsuite.ToolProviderVariant, platform agentsuite.Platform) (agentsuite.ToolProviderVariant, int) {
	var selected agentsuite.ToolProviderVariant
	count := 0
	for _, variant := range variants {
		if variant.Platform == platform {
			selected = variant
			count++
		}
	}
	return selected, count
}

func exactPlatformImage(name string, images []agentsuite.PlatformImage, platform agentsuite.Platform) (agentsuite.PlatformImage, error) {
	var selected agentsuite.PlatformImage
	count := 0
	for _, image := range images {
		if image.Platform == platform {
			selected = image
			count++
		}
	}
	if count != 1 {
		return agentsuite.PlatformImage{}, fmt.Errorf("build profile must contain exactly one %s image for %s", name, platform)
	}
	return selected, nil
}

func readBuildInstructions(root *os.Root, ref agentsuite.FileRef) ([]byte, error) {
	data, err := readBuildFile(root, ref.Path, maxBuildMetadataBytes)
	if err != nil {
		return nil, err
	}
	if digestBytes(data) != ref.Descriptor.Digest {
		return nil, errors.New("instruction content changed after validation")
	}
	return data, nil
}

func readBuildFile(root *os.Root, name string, limit int64) ([]byte, error) {
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) || strings.ContainsAny(name, "\\\x00") ||
		path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return nil, fmt.Errorf("content path is not normalized below the root: %q", name)
	}
	info, err := root.Lstat(filepath.FromSlash(name))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, fmt.Errorf("%s must be regular and at most %d bytes", name, limit)
	}
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || openedInfo.Size() != info.Size() {
		return nil, fmt.Errorf("%s changed while opening", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, info.Size()+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != info.Size() {
		return nil, fmt.Errorf("%s size changed while reading", name)
	}
	return data, nil
}

// digestBytes is the raw SHA-256 identity shared by instruction and OCI archive
// checks. JSON manifest identities use spec.Digest instead.
func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
