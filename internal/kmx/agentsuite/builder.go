package agentsuite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// BuildSelection identifies one runnable agent composition.
type BuildSelection struct {
	Agent    string
	Platform string
}

// SelectedToolProvider is one exact provider bundle selected by a composition.
// The closure order follows validator traversal; builders must not infer install
// ordering from this slice.
type SelectedToolProvider struct {
	ID             string
	Version        string
	ManifestDigest string
	Variant        ToolProviderVariant
	Root           bool
}

// SandboxPlan is the provider-neutral, validated metadata selected for a
// sandbox image builder. The current experimental contract does not yet expose
// verified image blobs or ToolProvider payload content.
type SandboxPlan struct {
	Suite             Suite
	SuiteManifestHash string
	Agent             Agent
	Instructions      []byte
	Composition       Composition
	CompositionDigest string
	BuildProfile      BuildProfile
	RuntimeBase       PlatformImage
	Harness           PlatformImage
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
	content, err := loadDirectory(root)
	if err != nil {
		return nil, err
	}
	v, _, err := validateContentGraph(content)
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

	var candidates []Composition
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

	instructions, err := readBuildInstructions(root, agent.Instructions, content)
	if err != nil {
		return nil, fmt.Errorf("agent %s instructions: %w", agentID, err)
	}
	suiteBytes, err := content.data("agentsuite.json")
	if err != nil {
		return nil, err
	}
	suiteHash, err := canonicalDigest(suiteBytes)
	if err != nil {
		return nil, err
	}

	compositionDigest := ""
	for _, ref := range v.suite.Compositions {
		if ref.Agent == composition.Agent && ref.Platform == composition.Platform {
			compositionDigest = ref.Digest
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
		Instructions:      append([]byte(nil), instructions...),
		Composition:       composition,
		CompositionDigest: compositionDigest,
		BuildProfile:      profile,
		RuntimeBase:       runtimeBase,
		Harness:           harness,
		ToolProviders:     selected,
	}, nil
}

func resolveSelectedToolProviders(v *validator, composition Composition) ([]SelectedToolProvider, error) {
	roots := map[string]bool{}
	for _, resolved := range composition.ToolProviders {
		roots[resolved.ID+"@"+resolved.Version] = true
	}
	var selected []SelectedToolProvider
	seen := map[string]bool{}
	for _, resolved := range composition.ToolProviders {
		key := resolved.ID + "@" + resolved.Version
		provider := v.toolProviders[key]
		variant, _ := exactVariant(provider.Variants, composition.Platform)
		closure, err := bundleClosure(key, variant, v.toolProviders)
		if err != nil {
			return nil, err
		}
		for _, bundle := range closure {
			identity := bundle.providerKey + "#" + bundle.variant.VariantDigest
			if seen[identity] {
				continue
			}
			seen[identity] = true
			id, version, _ := strings.Cut(bundle.providerKey, "@")
			selected = append(selected, SelectedToolProvider{
				ID:             id,
				Version:        version,
				ManifestDigest: v.toolProviderDigests[bundle.providerKey],
				Variant:        bundle.variant,
				Root:           roots[bundle.providerKey],
			})
		}
	}
	return selected, nil
}

func exactPlatformImage(name string, images []PlatformImage, platform Platform) (PlatformImage, error) {
	var selected PlatformImage
	count := 0
	for _, image := range images {
		if image.Platform == platform {
			selected = image
			count++
		}
	}
	if count != 1 {
		return PlatformImage{}, fmt.Errorf("build profile must contain exactly one %s image for %s", name, platform)
	}
	return selected, nil
}

func readBuildInstructions(root string, ref FileRef, content *contentSet) ([]byte, error) {
	entry, ok := content.entries[ref.Path]
	if !ok || entry.Type != "file" || entry.Digest != ref.Digest {
		return nil, errors.New("instruction content was not validated")
	}
	if entry.Size < 0 || entry.Size > maxRetainedMetadataBytes {
		return nil, fmt.Errorf("instruction content exceeds %d bytes", maxRetainedMetadataBytes)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	rootHandle, err := os.OpenRoot(canonicalRoot)
	if err != nil {
		return nil, err
	}
	defer rootHandle.Close()
	file, err := rootHandle.Open(filepath.FromSlash(ref.Path))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != entry.Size {
		return nil, errors.New("instruction content changed after validation")
	}
	data, err := io.ReadAll(io.LimitReader(file, entry.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != entry.Size || digestBytes(data) != ref.Digest {
		return nil, errors.New("instruction content changed after validation")
	}
	return data, nil
}
