package agentsuite

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

var (
	identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	versionPattern    = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	envPattern        = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	capabilityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,127}$`)
	schemePattern     = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)
	headerNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	secretKeyPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)
)

type Report struct {
	Name                     string                 `json:"name"`
	Agents                   int                    `json:"agents"`
	ToolProviders            int                    `json:"toolProviders"`
	ToolProviderCompositions int                    `json:"toolProviderCompositions"`
	Compositions             int                    `json:"compositions"`
	Capabilities             []string               `json:"capabilities"`
	AgentPlatforms           []AgentPlatform        `json:"agentPlatforms"`
	CompositionSelections    []CompositionSelection `json:"compositionSelections"`
}

type CompositionSelection struct {
	Agent        string   `json:"agent"`
	Platform     Platform `json:"platform"`
	BuildProfile string   `json:"buildProfile"`
	Digest       string   `json:"digest"`
}

type validator struct {
	content                  *contentSet
	suite                    Suite
	agents                   map[string]Agent
	toolProviders            map[string]ToolProvider
	toolProviderDigests      map[string]string
	toolProviderCompositions map[string]ToolProviderComposition
	compositions             map[string]Composition
	builds                   map[string]BuildProfile
}

func validateContent(content *contentSet) (*Report, error) {
	_, report, err := validateContentGraph(content)
	return report, err
}

func validateContentGraph(content *contentSet) (*validator, *Report, error) {
	rawSuite, err := content.data("agentsuite.json")
	if err != nil {
		return nil, nil, err
	}
	var suite Suite
	if err := decodeStrict(rawSuite, &suite); err != nil {
		return nil, nil, fmt.Errorf("agentsuite.json: %w", err)
	}
	v := &validator{
		content:                  content,
		suite:                    suite,
		agents:                   map[string]Agent{},
		toolProviders:            map[string]ToolProvider{},
		toolProviderDigests:      map[string]string{},
		toolProviderCompositions: map[string]ToolProviderComposition{},
		compositions:             map[string]Composition{},
		builds:                   map[string]BuildProfile{},
	}
	var errs []error
	errs = append(errs, v.validateSuite())
	errs = append(errs, v.loadAgents())
	errs = append(errs, v.loadToolProviders())
	errs = append(errs, v.loadBuildProfiles())
	errs = append(errs, v.loadToolProviderCompositions())
	errs = append(errs, v.loadCompositions())
	errs = append(errs, v.validateReferences())
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}
	capabilities := derivedCapabilities(v.toolProviders)
	agentPlatforms := make([]AgentPlatform, 0, len(v.agents))
	for agentID := range v.agents {
		var platforms []Platform
		for _, composition := range v.compositions {
			if composition.Agent == agentID {
				platforms = append(platforms, composition.Platform)
			}
		}
		slices.SortFunc(platforms, func(a, b Platform) int {
			return strings.Compare(a.String(), b.String())
		})
		agentPlatforms = append(agentPlatforms, AgentPlatform{ID: agentID, Platforms: platforms})
	}
	slices.SortFunc(agentPlatforms, func(a, b AgentPlatform) int {
		return strings.Compare(a.ID, b.ID)
	})
	selections := make([]CompositionSelection, 0, len(v.suite.Compositions))
	for _, ref := range v.suite.Compositions {
		key := ref.Agent + "@" + ref.Platform.String()
		composition, ok := v.compositions[key]
		if !ok {
			continue
		}
		selections = append(selections, CompositionSelection{
			Agent: ref.Agent, Platform: ref.Platform,
			BuildProfile: composition.BuildProfile, Digest: ref.Digest,
		})
	}
	slices.SortFunc(selections, func(a, b CompositionSelection) int {
		return strings.Compare(
			a.Agent+"@"+a.Platform.String()+"@"+a.BuildProfile,
			b.Agent+"@"+b.Platform.String()+"@"+b.BuildProfile,
		)
	})
	report := &Report{
		Name:                     suite.Name,
		Agents:                   len(v.agents),
		ToolProviders:            len(v.toolProviders),
		ToolProviderCompositions: len(v.toolProviderCompositions),
		Compositions:             len(v.compositions),
		Capabilities:             capabilities,
		AgentPlatforms:           agentPlatforms,
		CompositionSelections:    selections,
	}
	return v, report, nil
}

func (v *validator) validateSuite() error {
	var errs []error
	if v.suite.SchemaVersion != SpecVersion {
		errs = append(errs, fmt.Errorf("agentsuite.json schemaVersion must be %s", SpecVersion))
	}
	if v.suite.MediaType != MediaTypeSuite {
		errs = append(errs, fmt.Errorf("agentsuite.json mediaType must be %s", MediaTypeSuite))
	}
	if !identifierPattern.MatchString(v.suite.Name) {
		errs = append(errs, errors.New("agentsuite.json name is invalid"))
	}
	if len(v.suite.Agents) == 0 {
		errs = append(errs, errors.New("agentsuite.json must contain at least one agent"))
	}
	if len(v.suite.BuildProfiles) == 0 {
		errs = append(errs, errors.New("agentsuite.json must contain at least one build profile"))
	}
	errs = append(errs, validateExtensions(v.suite.Extensions))
	return errors.Join(errs...)
}

func (v *validator) loadBuildProfiles() error {
	var errs []error
	for _, ref := range v.suite.BuildProfiles {
		if !identifierPattern.MatchString(ref.ID) {
			errs = append(errs, fmt.Errorf("build profile reference %q has invalid id", ref.ID))
			continue
		}
		if _, exists := v.builds[ref.ID]; exists {
			errs = append(errs, fmt.Errorf("duplicate build profile %q", ref.ID))
			continue
		}
		data, err := v.content.data(ref.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("build profile %s: %w", ref.ID, err))
			continue
		}
		digest, err := canonicalDigest(data)
		if err != nil || digest != ref.Digest {
			errs = append(errs, fmt.Errorf("build profile %s digest mismatch", ref.ID))
			continue
		}
		var profile BuildProfile
		if err := decodeStrict(data, &profile); err != nil {
			errs = append(errs, fmt.Errorf("build profile %s: %w", ref.ID, err))
			continue
		}
		if err := validateBuildProfile(profile); err != nil {
			errs = append(errs, fmt.Errorf("build profile %s: %w", ref.ID, err))
			continue
		}
		if profile.ID != ref.ID {
			errs = append(errs, fmt.Errorf("build profile reference %s points to %s", ref.ID, profile.ID))
			continue
		}
		v.builds[ref.ID] = profile
	}
	return errors.Join(errs...)
}

func (v *validator) loadAgents() error {
	seenPaths := map[string]bool{}
	var errs []error
	for _, ref := range v.suite.Agents {
		if !identifierPattern.MatchString(ref.ID) {
			errs = append(errs, fmt.Errorf("agent reference %q has invalid id", ref.ID))
			continue
		}
		if _, exists := v.agents[ref.ID]; exists {
			errs = append(errs, fmt.Errorf("duplicate agent reference %q", ref.ID))
			continue
		}
		if seenPaths[ref.Path] {
			errs = append(errs, fmt.Errorf("agent path %q is referenced more than once", ref.Path))
			continue
		}
		seenPaths[ref.Path] = true
		data, err := v.content.data(ref.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("agent %s: %w", ref.ID, err))
			continue
		}
		digest, err := canonicalDigest(data)
		if err != nil || digest != ref.Digest {
			errs = append(errs, fmt.Errorf("agent %s manifest digest mismatch", ref.ID))
			continue
		}
		var agent Agent
		if err := decodeStrict(data, &agent); err != nil {
			errs = append(errs, fmt.Errorf("agent %s: %w", ref.ID, err))
			continue
		}
		if err := validateAgent(agent); err != nil {
			errs = append(errs, fmt.Errorf("agent %s: %w", ref.ID, err))
			continue
		}
		if agent.ID != ref.ID {
			errs = append(errs, fmt.Errorf("agent reference %s points to manifest for %s", ref.ID, agent.ID))
			continue
		}
		instructions, ok := v.content.entries[agent.Instructions.Path]
		if !ok || instructions.Type != "file" || instructions.Digest != agent.Instructions.Digest {
			errs = append(errs, fmt.Errorf("agent %s instructions do not match %s", ref.ID, agent.Instructions.Path))
			continue
		}
		v.agents[ref.ID] = agent
	}
	return errors.Join(errs...)
}

func (v *validator) loadToolProviders() error {
	data, err := v.content.data(v.suite.ToolProviderCatalog.Path)
	if err != nil {
		return fmt.Errorf("tool provider catalog: %w", err)
	}
	digest, err := canonicalDigest(data)
	if err != nil || digest != v.suite.ToolProviderCatalog.Digest {
		return errors.New("tool provider catalog digest mismatch")
	}
	var catalog ToolProviderCatalog
	if err := decodeStrict(data, &catalog); err != nil {
		return fmt.Errorf("tool provider catalog: %w", err)
	}
	if catalog.SchemaVersion != SpecVersion || catalog.MediaType != MediaTypeToolProviderCatalog {
		return errors.New("tool provider catalog has unsupported schemaVersion or mediaType")
	}
	var errs []error
	for _, ref := range catalog.ToolProviders {
		key := ref.ID + "@" + ref.Version
		if !identifierPattern.MatchString(ref.ID) || !versionPattern.MatchString(ref.Version) {
			errs = append(errs, fmt.Errorf("tool provider reference %q has invalid identity or exact version", key))
			continue
		}
		if _, exists := v.toolProviders[key]; exists {
			errs = append(errs, fmt.Errorf("duplicate tool provider %s", key))
			continue
		}
		raw, err := v.content.data(ref.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("tool provider %s: %w", key, err))
			continue
		}
		actualDigest, err := canonicalDigest(raw)
		if err != nil || actualDigest != ref.Digest {
			errs = append(errs, fmt.Errorf("tool provider %s manifest digest mismatch", key))
			continue
		}
		var provider ToolProvider
		if err := decodeStrict(raw, &provider); err != nil {
			errs = append(errs, fmt.Errorf("tool provider %s: %w", key, err))
			continue
		}
		if err := validateToolProvider(provider, raw, v.content); err != nil {
			errs = append(errs, fmt.Errorf("tool provider %s: %w", key, err))
			continue
		}
		if provider.ID != ref.ID || provider.Version != ref.Version {
			errs = append(errs, fmt.Errorf("tool provider reference %s points to %s@%s", key, provider.ID, provider.Version))
			continue
		}
		v.toolProviders[key] = provider
		v.toolProviderDigests[key] = actualDigest
	}
	return errors.Join(errs...)
}

func (v *validator) loadToolProviderCompositions() error {
	var errs []error
	for _, ref := range v.suite.ToolProviderCompositions {
		key := ref.ID + "@" + ref.Version + "@" + ref.Platform.String()
		if _, exists := v.toolProviderCompositions[key]; exists {
			errs = append(errs, fmt.Errorf("duplicate tool provider composition %s", key))
			continue
		}
		data, err := v.content.data(ref.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("tool provider composition %s: %w", key, err))
			continue
		}
		digest, err := canonicalDigest(data)
		if err != nil || digest != ref.Digest {
			errs = append(errs, fmt.Errorf("tool provider composition %s digest mismatch", key))
			continue
		}
		var composition ToolProviderComposition
		if err := decodeStrict(data, &composition); err != nil {
			errs = append(errs, fmt.Errorf("tool provider composition %s: %w", key, err))
			continue
		}
		if err := validateToolProviderComposition(composition); err != nil {
			errs = append(errs, fmt.Errorf("tool provider composition %s: %w", key, err))
			continue
		}
		if composition.ID != ref.ID || composition.Version != ref.Version || composition.Platform != ref.Platform {
			errs = append(errs, fmt.Errorf("tool provider composition %s identity does not match its reference", key))
			continue
		}
		providerKey := composition.ID + "@" + composition.Version
		provider, ok := v.toolProviders[providerKey]
		if !ok || composition.ManifestDigest != v.toolProviderDigests[providerKey] {
			errs = append(errs, fmt.Errorf("tool provider composition %s references a missing or stale ToolProvider manifest", key))
			continue
		}
		variant, matches := exactVariant(provider.Variants, composition.Platform)
		if matches != 1 || composition.VariantDigest != variant.VariantDigest {
			errs = append(errs, fmt.Errorf("tool provider composition %s must select exactly one matching variant", key))
			continue
		}
		profile, ok := v.builds[composition.BuildProfile]
		if !ok || !profileSupportsToolProviderPlatform(profile, composition.Platform) {
			errs = append(errs, fmt.Errorf("tool provider composition %s build profile %q does not support %s", key, composition.BuildProfile, composition.Platform))
			continue
		}
		destinations := map[string]InventoryEntry{}
		closure, err := bundleClosure(providerKey, variant, v.toolProviders)
		if err != nil {
			errs = append(errs, fmt.Errorf("tool provider composition %s: %w", key, err))
			continue
		}
		for _, bundle := range closure {
			if err := addVariantDestinations(destinations, bundle.variant); err != nil {
				errs = append(errs, fmt.Errorf("tool provider composition %s: %w", key, err))
			}
		}
		v.toolProviderCompositions[key] = composition
	}
	return errors.Join(errs...)
}

func (v *validator) loadCompositions() error {
	var errs []error
	for _, ref := range v.suite.Compositions {
		key := ref.Agent + "@" + ref.Platform.String()
		if _, exists := v.compositions[key]; exists {
			errs = append(errs, fmt.Errorf("duplicate composition %s", key))
			continue
		}
		data, err := v.content.data(ref.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("composition %s: %w", key, err))
			continue
		}
		digest, err := canonicalDigest(data)
		if err != nil || digest != ref.Digest {
			errs = append(errs, fmt.Errorf("composition %s digest mismatch", key))
			continue
		}
		var composition Composition
		if err := decodeStrict(data, &composition); err != nil {
			errs = append(errs, fmt.Errorf("composition %s: %w", key, err))
			continue
		}
		if err := validateComposition(composition); err != nil {
			errs = append(errs, fmt.Errorf("composition %s: %w", key, err))
			continue
		}
		if composition.Agent != ref.Agent || composition.Platform != ref.Platform {
			errs = append(errs, fmt.Errorf("composition %s identity does not match its reference", key))
			continue
		}
		profile, ok := v.builds[composition.BuildProfile]
		if !ok || !profileSupportsPlatform(profile, composition.Platform) {
			errs = append(errs, fmt.Errorf("composition %s build profile %q does not support %s", key, composition.BuildProfile, composition.Platform))
			continue
		}
		v.compositions[key] = composition
	}
	return errors.Join(errs...)
}

func (v *validator) validateReferences() error {
	var errs []error
	for providerKey, provider := range v.toolProviders {
		for _, variant := range provider.Variants {
			errs = append(errs, validateBundleDependencies(providerKey, variant, v.toolProviders))
		}
	}
	for agentID, agent := range v.agents {
		for _, invoke := range agent.Invokes {
			if _, ok := v.agents[invoke.Agent]; !ok {
				errs = append(errs, fmt.Errorf("agent %s invokes unknown agent %s", agentID, invoke.Agent))
			}
		}
		for key, composition := range v.compositions {
			if composition.Agent != agentID {
				continue
			}
			destinations := map[string]InventoryEntry{}
			requirements := map[string]ToolProviderRequirement{}
			for _, requirement := range agent.ToolProviders {
				requirements[requirement.ID+"@"+requirement.Version] = requirement
			}
			if len(requirements) != len(composition.ToolProviders) {
				errs = append(errs, fmt.Errorf("composition %s does not resolve every agent requirement exactly once", key))
				continue
			}
			for _, resolved := range composition.ToolProviders {
				providerKey := resolved.ID + "@" + resolved.Version
				requirement, ok := requirements[providerKey]
				if !ok {
					errs = append(errs, fmt.Errorf("composition %s contains undeclared tool provider %s", key, providerKey))
					continue
				}
				provider, exists := v.toolProviders[providerKey]
				if !exists {
					errs = append(errs, fmt.Errorf("composition %s contains missing tool provider %s", key, providerKey))
					continue
				}
				if resolved.ManifestDigest != v.toolProviderDigests[providerKey] || resolved.ExecutionMode != requirement.ExecutionMode {
					errs = append(errs, fmt.Errorf("composition %s has stale resolution for %s", key, providerKey))
				}
				variant, matches := exactVariant(provider.Variants, composition.Platform)
				if matches != 1 || resolved.VariantDigest != variant.VariantDigest {
					errs = append(errs, fmt.Errorf("composition %s must select exactly one matching variant for %s", key, providerKey))
					continue
				}
				closure, err := bundleClosure(providerKey, variant, v.toolProviders)
				if err != nil {
					errs = append(errs, fmt.Errorf("composition %s: %w", key, err))
					continue
				}
				for _, bundle := range closure {
					if err := addVariantDestinations(destinations, bundle.variant); err != nil {
						errs = append(errs, fmt.Errorf("composition %s: %w", key, err))
					}
				}
			}
		}
		found := false
		for _, composition := range v.compositions {
			if composition.Agent == agentID {
				found = true
			}
		}
		if !found {
			errs = append(errs, fmt.Errorf("agent %s has no platform composition", agentID))
		}
	}
	for key, composition := range v.compositions {
		if _, ok := v.agents[composition.Agent]; !ok {
			errs = append(errs, fmt.Errorf("composition %s references unknown agent %s", key, composition.Agent))
		}
	}
	errs = append(errs, validateToolProviderInboundReferences(v.toolProviders, v.agents, v.toolProviderCompositions))
	if !slices.Equal(v.suite.Capabilities, derivedCapabilities(v.toolProviders)) {
		errs = append(errs, errors.New("suite capabilities must exactly equal capabilities derived from its tool provider declarations"))
	}
	return errors.Join(errs...)
}

func validateToolProviderInboundReferences(providers map[string]ToolProvider, agents map[string]Agent, compositions map[string]ToolProviderComposition) error {
	referenced := map[string]bool{}
	for _, agent := range agents {
		for _, requirement := range agent.ToolProviders {
			referenced[requirement.ID+"@"+requirement.Version] = true
		}
	}
	for _, composition := range compositions {
		referenced[composition.ID+"@"+composition.Version] = true
	}
	for _, provider := range providers {
		for _, variant := range provider.Variants {
			for _, dependency := range variant.Dependencies {
				referenced[dependency.ID+"@"+dependency.Version] = true
			}
		}
	}

	var errs []error
	for key, provider := range providers {
		if referenced[key] {
			continue
		}
		if provider.Retained == nil || !*provider.Retained {
			errs = append(errs, fmt.Errorf("tool provider %s has no inward reference and must declare retained true to remain in the catalog", key))
		}
	}
	return errors.Join(errs...)
}

func validateAgent(agent Agent) error {
	var errs []error
	if agent.SchemaVersion != SpecVersion || agent.MediaType != MediaTypeAgent {
		errs = append(errs, errors.New("unsupported schemaVersion or mediaType"))
	}
	if !identifierPattern.MatchString(agent.ID) {
		errs = append(errs, errors.New("id is invalid"))
	}
	if _, err := validateContentPath(agent.Instructions.Path); err != nil || !validDigest(agent.Instructions.Digest) {
		errs = append(errs, errors.New("instructions reference is invalid"))
	}
	if agent.Model.Protocol == "" || agent.Model.Model == "" {
		errs = append(errs, errors.New("model protocol and model are required"))
	}
	if agent.Model.EndpointEnv != "" && (!envPattern.MatchString(agent.Model.EndpointEnv) || unsafeInjectionEnv(agent.Model.EndpointEnv)) {
		errs = append(errs, errors.New("model endpointEnv is invalid"))
	}
	errs = append(errs, validateIdentifierList("model secretRefs", agent.Model.SecretRefs))
	seen := map[string]bool{}
	for _, provider := range agent.ToolProviders {
		key := provider.ID + "@" + provider.Version
		if !identifierPattern.MatchString(provider.ID) || !versionPattern.MatchString(provider.Version) {
			errs = append(errs, fmt.Errorf("tool provider requirement %s is not an exact identity", key))
		}
		if seen[key] {
			errs = append(errs, fmt.Errorf("duplicate tool provider requirement %s", key))
		}
		seen[key] = true
		if provider.ExecutionMode != ExecutionSharedSandbox {
			errs = append(errs, fmt.Errorf("tool provider requirement %s executionMode must be %q", key, ExecutionSharedSandbox))
		}
	}
	invokes := map[string]bool{}
	for _, invoke := range agent.Invokes {
		if !identifierPattern.MatchString(invoke.Agent) || invoke.Agent == agent.ID || invokes[invoke.Agent] {
			errs = append(errs, fmt.Errorf("invoked agent %q is invalid or duplicated", invoke.Agent))
		}
		invokes[invoke.Agent] = true
		if invoke.MaxConcurrent <= 0 || invoke.MaxDepth <= 0 {
			errs = append(errs, fmt.Errorf("invoked agent %q requires positive maxConcurrent and maxDepth", invoke.Agent))
		}
	}
	errs = append(errs, validateExtensions(agent.Extensions))
	return errors.Join(errs...)
}

func validateIdentifierList(name string, values []string) error {
	var errs []error
	seen := map[string]bool{}
	for _, value := range values {
		if !identifierPattern.MatchString(value) || seen[value] {
			errs = append(errs, fmt.Errorf("%s contains invalid or duplicate identifier %q", name, value))
		}
		seen[value] = true
	}
	return errors.Join(errs...)
}

func validateToolProvider(provider ToolProvider, raw []byte, content *contentSet) error {
	var errs []error
	if provider.SchemaVersion != SpecVersion || provider.MediaType != MediaTypeToolProvider {
		errs = append(errs, errors.New("unsupported schemaVersion or mediaType"))
	}
	if !identifierPattern.MatchString(provider.ID) || !versionPattern.MatchString(provider.Version) {
		errs = append(errs, errors.New("tool provider id or exact version is invalid"))
	}
	if provider.Retained != nil && !*provider.Retained {
		errs = append(errs, errors.New("retained, when present, must be true"))
	}
	if provider.Protocol != "mcp" || provider.Revision == "" || len(provider.Tools) == 0 {
		errs = append(errs, errors.New("the draft tool provider must declare an MCP revision and at least one tool"))
	}
	tools := map[string]bool{}
	for _, tool := range provider.Tools {
		if !identifierPattern.MatchString(tool.Name) || tools[tool.Name] {
			errs = append(errs, fmt.Errorf("tool %q is invalid or duplicated", tool.Name))
		}
		tools[tool.Name] = true
		if err := validateFileRef(tool.InputSchema, content); err != nil {
			errs = append(errs, fmt.Errorf("tool %q input schema: %w", tool.Name, err))
		}
		if tool.OutputSchema != nil {
			if err := validateFileRef(*tool.OutputSchema, content); err != nil {
				errs = append(errs, fmt.Errorf("tool %q output schema: %w", tool.Name, err))
			}
		}
		effects := map[string]bool{}
		for _, effect := range tool.Effects {
			if effect == "" || effects[effect] {
				errs = append(errs, fmt.Errorf("tool %q has an empty or duplicate effect", tool.Name))
			}
			effects[effect] = true
		}
	}
	if len(provider.Variants) == 0 && provider.Remote == nil {
		errs = append(errs, errors.New("tool provider must declare bundled variants, remote MCP, or both"))
	}
	platforms := map[string]bool{}
	variantDigests, err := rawVariantDigests(raw)
	if err != nil {
		errs = append(errs, fmt.Errorf("variant identities: %w", err))
	}
	if len(variantDigests) != len(provider.Variants) {
		errs = append(errs, errors.New("raw and decoded variant counts differ"))
	}
	for i, variant := range provider.Variants {
		key := variant.Platform.String()
		if platforms[key] {
			errs = append(errs, fmt.Errorf("duplicate platform variant %s", key))
		}
		platforms[key] = true
		expectedDigest := ""
		if i < len(variantDigests) {
			expectedDigest = variantDigests[i]
		}
		errs = append(errs, validateVariant(variant, expectedDigest, content))
	}
	if provider.Remote != nil {
		errs = append(errs, validateRemoteToolProvider(*provider.Remote))
	}
	errs = append(errs, validateExtensions(provider.Extensions))
	return errors.Join(errs...)
}

func validateRemoteToolProvider(remote RemoteToolProvider) error {
	var errs []error
	if remote.Transport != "streamable-http" {
		errs = append(errs, errors.New("remote transport must be streamable-http"))
	}
	if !identifierPattern.MatchString(remote.EndpointRef) {
		errs = append(errs, errors.New("remote endpointRef is invalid"))
	}
	if remote.Cancellation != "propagate" {
		errs = append(errs, errors.New("remote cancellation must be propagate"))
	}
	if remote.Connection != "session-aware" {
		errs = append(errs, errors.New("remote connection must be session-aware"))
	}
	if remote.Timeouts.ConnectMilliseconds <= 0 || remote.Timeouts.ConnectMilliseconds > 3_600_000 {
		errs = append(errs, errors.New("remote connect timeout is invalid"))
	}
	if remote.Timeouts.RequestMilliseconds <= 0 || remote.Timeouts.RequestMilliseconds > 86_400_000 {
		errs = append(errs, errors.New("remote request timeout is invalid"))
	}

	headers := map[string]bool{}
	for _, header := range remote.Headers {
		name := strings.ToLower(header.Name)
		if !headerNamePattern.MatchString(header.Name) || headers[name] || reservedRemoteHeader(name) {
			errs = append(errs, fmt.Errorf("remote header %q is invalid, duplicated, or transport-managed", header.Name))
		}
		headers[name] = true
		if !identifierPattern.MatchString(header.SecretRef.Name) || !secretKeyPattern.MatchString(header.SecretRef.Key) {
			errs = append(errs, fmt.Errorf("remote header %q secret reference is invalid", header.Name))
		}
	}

	if len(remote.Network) == 0 {
		errs = append(errs, errors.New("remote network must contain at least one destination reference"))
	}
	destinations := map[string]bool{}
	for _, network := range remote.Network {
		if !identifierPattern.MatchString(network.DestinationRef) || destinations[network.DestinationRef] {
			errs = append(errs, fmt.Errorf("remote network destination %q is invalid or duplicated", network.DestinationRef))
		}
		destinations[network.DestinationRef] = true
	}
	return errors.Join(errs...)
}

func reservedRemoteHeader(name string) bool {
	switch name {
	case "accept", "connection", "content-length", "content-type", "host",
		"last-event-id", "mcp-protocol-version", "mcp-session-id", "transfer-encoding":
		return true
	default:
		return false
	}
}

func validateFileRef(ref FileRef, content *contentSet) error {
	if _, err := validateContentPath(ref.Path); err != nil || !validDigest(ref.Digest) {
		return errors.New("reference is invalid")
	}
	entry, ok := content.entries[ref.Path]
	if !ok || entry.Type != "file" || entry.Digest != ref.Digest {
		return errors.New("referenced file is absent or has a different digest")
	}
	return nil
}

func validateVariant(variant ToolProviderVariant, expectedDigest string, content *contentSet) error {
	var errs []error
	if err := validatePlatform(variant.Platform); err != nil {
		errs = append(errs, err)
	}
	if variant.InstallRoot == "" || !strings.HasPrefix(variant.InstallRoot, "/") || path.Clean(variant.InstallRoot) != variant.InstallRoot {
		errs = append(errs, errors.New("installRoot must be a normalized absolute path"))
	}
	if _, err := validateContentPath(variant.PayloadRoot); err != nil {
		errs = append(errs, errors.New("payloadRoot is invalid"))
	}
	if variant.Entrypoint == "" || !strings.HasPrefix(variant.Entrypoint, variant.InstallRoot+"/") {
		errs = append(errs, errors.New("entrypoint must be below installRoot"))
	}
	if variant.Runtime.ABI != "static" && variant.Runtime.ABI != "gnu" && variant.Runtime.ABI != "musl" {
		errs = append(errs, errors.New("runtime.abi must be static, gnu, or musl"))
	}
	if variant.Runtime.CPUBaseline == "" {
		errs = append(errs, errors.New("runtime.cpuBaseline is required"))
	}
	if len(variant.Files) == 0 {
		errs = append(errs, errors.New("files must contain a complete payload inventory"))
	}
	errs = append(errs, validateAbsolutePathList("searchPath", variant.SearchPath))
	errs = append(errs, validateAbsolutePathList("writablePaths", variant.WritablePaths))
	errs = append(errs, validateAbsolutePathList("runtime.requiredBasePaths", variant.Runtime.RequiredPaths))
	for _, argument := range variant.Arguments {
		if strings.Contains(argument, "${") || strings.Contains(argument, "$(") {
			errs = append(errs, errors.New("arguments must not interpolate environment or secret values"))
		}
	}
	envSeen := map[string]bool{}
	for _, env := range variant.Environment {
		if !envPattern.MatchString(env.Name) || envSeen[env.Name] {
			errs = append(errs, fmt.Errorf("environment name %q is invalid or duplicated", env.Name))
		}
		envSeen[env.Name] = true
		if env.Secret && env.Delivery != "env" && env.Delivery != "file" {
			errs = append(errs, fmt.Errorf("secret environment %s must declare env or file delivery", env.Name))
		}
		if !env.Secret && env.Delivery != "" {
			errs = append(errs, fmt.Errorf("non-secret environment %s must not declare secret delivery", env.Name))
		}
		if unsafeInjectionEnv(env.Name) {
			errs = append(errs, fmt.Errorf("environment name %s can inject executable code", env.Name))
		}
	}
	networkSeen := map[string]bool{}
	for _, access := range variant.Network {
		key := fmt.Sprintf("%s://%s:%d", access.Scheme, access.Host, access.Port)
		if !schemePattern.MatchString(access.Scheme) || access.Host == "" || access.Port < 1 || access.Port > 65535 || networkSeen[key] {
			errs = append(errs, fmt.Errorf("network access %q is invalid or duplicated", key))
		}
		networkSeen[key] = true
	}
	if variant.SBOM != nil && !validDescriptor(*variant.SBOM) {
		errs = append(errs, errors.New("sbom descriptor is invalid"))
	}
	if variant.Provenance != nil && !validDescriptor(*variant.Provenance) {
		errs = append(errs, errors.New("provenance descriptor is invalid"))
	}
	inventory := map[string]InventoryEntry{}
	for _, entry := range variant.Files {
		if _, err := validateContentPath(entry.Path); err != nil {
			errs = append(errs, fmt.Errorf("inventory path %q is invalid", entry.Path))
			continue
		}
		if _, exists := inventory[entry.Path]; exists {
			errs = append(errs, fmt.Errorf("duplicate inventory path %s", entry.Path))
			continue
		}
		switch entry.Type {
		case "file":
			if entry.Size < 0 || !validDigest(entry.Digest) || entry.LinkTarget != "" {
				errs = append(errs, fmt.Errorf("inventory file %s has invalid size, digest, or link target", entry.Path))
			}
		case "directory":
			if entry.Size != 0 || entry.Digest != "" || entry.LinkTarget != "" {
				errs = append(errs, fmt.Errorf("inventory directory %s has file or link metadata", entry.Path))
			}
		case "symlink", "hardlink":
			if entry.Size != 0 || entry.Digest != "" || validateLinkTarget(entry.Path, entry.LinkTarget) != nil {
				errs = append(errs, fmt.Errorf("inventory link %s has invalid metadata or target", entry.Path))
			}
		default:
			errs = append(errs, fmt.Errorf("inventory path %s has unsupported type %q", entry.Path, entry.Type))
		}
		if entry.Mode > 0o777 {
			errs = append(errs, fmt.Errorf("inventory path %s has invalid mode %#o", entry.Path, entry.Mode))
		}
		inventory[entry.Path] = entry
		actualPath := path.Join(variant.PayloadRoot, entry.Path)
		actual, ok := content.entries[actualPath]
		if !ok {
			errs = append(errs, fmt.Errorf("inventory path %s is absent from payload", entry.Path))
			continue
		}
		if actual.Type != entry.Type || actual.Mode != entry.Mode || actual.Size != entry.Size ||
			actual.Digest != entry.Digest || actual.LinkTarget != entry.LinkTarget ||
			actual.UID != entry.UID || actual.GID != entry.GID {
			errs = append(errs, fmt.Errorf("inventory path %s does not match payload metadata", entry.Path))
		}
		if entry.UID != 0 || entry.GID != 0 {
			errs = append(errs, fmt.Errorf("inventory path %s must be root-owned", entry.Path))
		}
		if entry.Mode&0o022 != 0 && (entry.Type == "file" || entry.Type == "hardlink") {
			errs = append(errs, fmt.Errorf("inventory path %s is group or world writable", entry.Path))
		}
	}
	for name, entry := range content.entries {
		if name == variant.PayloadRoot || !strings.HasPrefix(name, variant.PayloadRoot+"/") || entry.Type == "directory" {
			continue
		}
		relative := strings.TrimPrefix(name, variant.PayloadRoot+"/")
		if _, ok := inventory[relative]; !ok {
			errs = append(errs, fmt.Errorf("payload contains untracked path %s", name))
		}
	}
	entryRelative := strings.TrimPrefix(variant.Entrypoint, variant.InstallRoot+"/")
	entry, ok := inventory[entryRelative]
	if !ok || entry.Type != "file" || entry.Mode&0o111 == 0 {
		errs = append(errs, errors.New("entrypoint must identify an executable regular inventory file"))
	}
	if expectedDigest == "" || expectedDigest != variant.VariantDigest {
		errs = append(errs, errors.New("variantDigest does not match canonical variant metadata"))
	}
	return errors.Join(errs...)
}

func validateAbsolutePathList(name string, values []string) error {
	var errs []error
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || !strings.HasPrefix(value, "/") || path.Clean(value) != value || seen[value] {
			errs = append(errs, fmt.Errorf("%s contains invalid or duplicate path %q", name, value))
		}
		seen[value] = true
	}
	return errors.Join(errs...)
}

func rawVariantDigests(rawProvider []byte) ([]string, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(rawProvider, &document); err != nil {
		return nil, err
	}
	rawVariants, ok := document["variants"]
	if !ok {
		return nil, nil
	}
	var variants []map[string]json.RawMessage
	if err := json.Unmarshal(rawVariants, &variants); err != nil {
		return nil, err
	}
	digests := make([]string, len(variants))
	for i, variant := range variants {
		if _, ok := variant["variantDigest"]; !ok {
			return nil, fmt.Errorf("variant %d has no variantDigest", i)
		}
		variant["variantDigest"] = json.RawMessage(`""`)
		data, err := json.Marshal(variant)
		if err != nil {
			return nil, err
		}
		digests[i], err = canonicalDigest(data)
		if err != nil {
			return nil, err
		}
	}
	return digests, nil
}

func validateBundleDependencies(providerKey string, variant ToolProviderVariant, providers map[string]ToolProvider) error {
	_, err := bundleClosure(providerKey, variant, providers)
	return err
}

type selectedBundle struct {
	providerKey string
	variant     ToolProviderVariant
}

func bundleClosure(rootKey string, root ToolProviderVariant, providers map[string]ToolProvider) ([]selectedBundle, error) {
	var (
		closure []selectedBundle
		errs    []error
	)
	visited := map[string]bool{}
	active := map[string]bool{}

	var visit func(string, ToolProviderVariant)
	visit = func(providerKey string, variant ToolProviderVariant) {
		identity := providerKey + "#" + variant.VariantDigest
		if active[identity] {
			errs = append(errs, fmt.Errorf("tool provider %s has a dependency cycle through %s", rootKey, providerKey))
			return
		}
		if visited[identity] {
			return
		}
		active[identity] = true
		closure = append(closure, selectedBundle{providerKey: providerKey, variant: variant})

		seen := map[string]bool{}
		for _, dependency := range variant.Dependencies {
			key := dependency.ID + "@" + dependency.Version
			if key == providerKey || seen[key] || !identifierPattern.MatchString(dependency.ID) ||
				!versionPattern.MatchString(dependency.Version) || !validDigest(dependency.VariantDigest) {
				errs = append(errs, fmt.Errorf("tool provider %s has invalid or duplicate dependency %s", providerKey, key))
				continue
			}
			seen[key] = true
			dependencyProvider, ok := providers[key]
			if !ok {
				errs = append(errs, fmt.Errorf("tool provider %s depends on missing tool provider %s", providerKey, key))
				continue
			}
			selected, matches := exactVariant(dependencyProvider.Variants, variant.Platform)
			if matches != 1 || selected.VariantDigest != dependency.VariantDigest {
				errs = append(errs, fmt.Errorf("tool provider %s dependency %s does not bind one %s variant", providerKey, key, variant.Platform))
				continue
			}
			visit(key, selected)
		}

		delete(active, identity)
		visited[identity] = true
	}

	visit(rootKey, root)
	return closure, errors.Join(errs...)
}

func addVariantDestinations(destinations map[string]InventoryEntry, variant ToolProviderVariant) error {
	var errs []error
	for _, entry := range variant.Files {
		destination := path.Join(variant.InstallRoot, entry.Path)
		if existing, ok := destinations[destination]; ok && !sameInstallEntry(existing, entry) {
			errs = append(errs, fmt.Errorf("non-identical destination collision at %s", destination))
		} else {
			destinations[destination] = entry
		}
	}
	return errors.Join(errs...)
}

func sameInstallEntry(a, b InventoryEntry) bool {
	return a.Type == b.Type && a.Mode == b.Mode && a.UID == b.UID && a.GID == b.GID &&
		a.Size == b.Size && a.Digest == b.Digest && a.LinkTarget == b.LinkTarget
}

func unsafeInjectionEnv(name string) bool {
	return strings.HasPrefix(name, "LD_") || name == "PYTHONPATH" || name == "NODE_OPTIONS" || name == "BASH_ENV"
}

func validateComposition(composition Composition) error {
	var errs []error
	if composition.SchemaVersion != SpecVersion || composition.MediaType != MediaTypeComposition {
		errs = append(errs, errors.New("unsupported schemaVersion or mediaType"))
	}
	if !identifierPattern.MatchString(composition.Agent) {
		errs = append(errs, errors.New("agent id is invalid"))
	}
	if !identifierPattern.MatchString(composition.BuildProfile) {
		errs = append(errs, errors.New("buildProfile is invalid"))
	}
	errs = append(errs, validatePlatform(composition.Platform))
	seen := map[string]bool{}
	previousID, previousVersion := "", ""
	for index, provider := range composition.ToolProviders {
		key := provider.ID + "@" + provider.Version
		if index > 0 && (provider.ID < previousID || provider.ID == previousID && provider.Version <= previousVersion) {
			errs = append(errs, errors.New("resolved tool providers must be sorted by id and version"))
		}
		if seen[key] || !identifierPattern.MatchString(provider.ID) || !versionPattern.MatchString(provider.Version) ||
			!validDigest(provider.ManifestDigest) || !validDigest(provider.VariantDigest) ||
			provider.ExecutionMode != ExecutionSharedSandbox {
			errs = append(errs, fmt.Errorf("resolved tool provider %s is invalid or duplicated", key))
		}
		seen[key] = true
		previousID, previousVersion = provider.ID, provider.Version
	}
	return errors.Join(errs...)
}

func validateToolProviderComposition(composition ToolProviderComposition) error {
	var errs []error
	if composition.SchemaVersion != SpecVersion || composition.MediaType != MediaTypeToolProviderComposition {
		errs = append(errs, errors.New("unsupported schemaVersion or mediaType"))
	}
	if !identifierPattern.MatchString(composition.ID) || !versionPattern.MatchString(composition.Version) {
		errs = append(errs, errors.New("tool provider identity is invalid"))
	}
	if !validDigest(composition.ManifestDigest) || !validDigest(composition.VariantDigest) {
		errs = append(errs, errors.New("tool provider manifest or variant digest is invalid"))
	}
	if !identifierPattern.MatchString(composition.BuildProfile) {
		errs = append(errs, errors.New("buildProfile is invalid"))
	}
	errs = append(errs, validatePlatform(composition.Platform))
	return errors.Join(errs...)
}

func validateBuildProfile(profile BuildProfile) error {
	var errs []error
	if profile.SchemaVersion != SpecVersion || profile.MediaType != MediaTypeBuildProfile {
		errs = append(errs, errors.New("unsupported schemaVersion or mediaType"))
	}
	if !identifierPattern.MatchString(profile.ID) {
		errs = append(errs, errors.New("id is invalid"))
	}
	if profile.SourceEpoch <= 0 {
		errs = append(errs, errors.New("sourceEpoch must be a positive Unix timestamp"))
	}
	errs = append(errs, validatePlatformImages("runtimeBase", profile.RuntimeBase))
	errs = append(errs, validatePlatformImages("harness", profile.Harness))
	return errors.Join(errs...)
}

func validatePlatformImages(name string, images []PlatformImage) error {
	if len(images) == 0 {
		return fmt.Errorf("%s must contain at least one pinned platform image", name)
	}
	var errs []error
	seen := map[string]bool{}
	for _, image := range images {
		key := image.Platform.String()
		if err := validatePlatform(image.Platform); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", name, key, err))
		}
		if seen[key] {
			errs = append(errs, fmt.Errorf("%s contains duplicate platform %s", name, key))
		}
		seen[key] = true
		if err := ValidateImageReference(image.ImageRef); err != nil {
			errs = append(errs, fmt.Errorf("%s %s imageRef must be a registry-qualified, digest-addressed OCI image reference", name, key))
		} else if !strings.HasSuffix(image.ImageRef, "@"+image.Image.Digest) {
			errs = append(errs, fmt.Errorf("%s %s imageRef digest must match image descriptor", name, key))
		}
		if image.Image.MediaType != ociManifestMediaType || !validDigest(image.Image.Digest) || image.Image.Size <= 0 {
			errs = append(errs, fmt.Errorf("%s %s image descriptor must pin an OCI image manifest", name, key))
		}
	}
	return errors.Join(errs...)
}

func profileSupportsPlatform(profile BuildProfile, platform Platform) bool {
	base := false
	harness := false
	for _, image := range profile.RuntimeBase {
		base = base || image.Platform == platform
	}
	for _, image := range profile.Harness {
		harness = harness || image.Platform == platform
	}
	return base && harness
}

func profileSupportsToolProviderPlatform(profile BuildProfile, platform Platform) bool {
	for _, image := range profile.RuntimeBase {
		if image.Platform == platform {
			return true
		}
	}
	return false
}

func validatePlatform(platform Platform) error {
	if platform.OS != "linux" {
		return fmt.Errorf("the draft supports exact Linux tool provider platforms only, got %s", platform.String())
	}
	if platform.Architecture != "amd64" && platform.Architecture != "arm64" {
		return fmt.Errorf("the draft supports linux/amd64 and linux/arm64 tool providers, got %s", platform.String())
	}
	if platform.Variant != "" {
		return fmt.Errorf("the draft requires an exact platform without OCI variant, got %s", platform.String())
	}
	return nil
}

func exactVariant(variants []ToolProviderVariant, platform Platform) (ToolProviderVariant, int) {
	var selected ToolProviderVariant
	count := 0
	for _, variant := range variants {
		if variant.Platform == platform {
			selected = variant
			count++
		}
	}
	return selected, count
}

func validateExtensions(extensions []Extension) error {
	var errs []error
	seen := map[string]bool{}
	for _, extension := range extensions {
		if !capabilityPattern.MatchString(extension.Name) || seen[extension.Name] {
			errs = append(errs, fmt.Errorf("extension %q is invalid or duplicated", extension.Name))
		}
		seen[extension.Name] = true
		if extension.Critical {
			errs = append(errs, fmt.Errorf("unknown critical extension %q is unsupported", extension.Name))
		}
		if extension.Digest != "" && !validDigest(extension.Digest) {
			errs = append(errs, fmt.Errorf("extension %q digest is invalid", extension.Name))
		}
	}
	return errors.Join(errs...)
}

func derivedCapabilities(providers map[string]ToolProvider) []string {
	set := map[string]bool{}
	for _, provider := range providers {
		if len(provider.Variants) > 0 {
			set["bundled-stdio-mcp"] = true
		}
	}
	out := make([]string, 0, len(set))
	for capability := range set {
		out = append(out, capability)
	}
	slices.Sort(out)
	return out
}
