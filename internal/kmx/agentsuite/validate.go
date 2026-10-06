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
)

type Report struct {
	Name           string          `json:"name"`
	Agents         int             `json:"agents"`
	Tools          int             `json:"tools"`
	ToolSets       int             `json:"toolSets"`
	Capabilities   []string        `json:"capabilities"`
	AgentPlatforms []AgentPlatform `json:"agentPlatforms"`
}

type validator struct {
	content     *contentSet
	suite       Suite
	agents      map[string]Agent
	tools       map[string]Tool
	toolDigests map[string]string
	toolSets    map[string]ToolSet
	builds      map[string]BuildProfile
}

func validateContent(content *contentSet) (*Report, error) {
	rawSuite, err := content.data("agentsuite.json")
	if err != nil {
		return nil, err
	}
	var suite Suite
	if err := decodeStrict(rawSuite, &suite); err != nil {
		return nil, fmt.Errorf("agentsuite.json: %w", err)
	}
	v := &validator{
		content:     content,
		suite:       suite,
		agents:      map[string]Agent{},
		tools:       map[string]Tool{},
		toolDigests: map[string]string{},
		toolSets:    map[string]ToolSet{},
		builds:      map[string]BuildProfile{},
	}
	var errs []error
	errs = append(errs, v.validateSuite())
	errs = append(errs, v.loadAgents())
	errs = append(errs, v.loadTools())
	errs = append(errs, v.loadBuildProfiles())
	errs = append(errs, v.loadToolSets())
	errs = append(errs, v.validateReferences())
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	capabilities := derivedCapabilities(v.tools)
	agentPlatforms := make([]AgentPlatform, 0, len(v.agents))
	for agentID := range v.agents {
		var platforms []Platform
		for _, toolSet := range v.toolSets {
			if toolSet.Agent == agentID {
				platforms = append(platforms, toolSet.Platform)
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
	return &Report{
		Name:           suite.Name,
		Agents:         len(v.agents),
		Tools:          len(v.tools),
		ToolSets:       len(v.toolSets),
		Capabilities:   capabilities,
		AgentPlatforms: agentPlatforms,
	}, nil
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

func (v *validator) loadTools() error {
	data, err := v.content.data(v.suite.ToolCatalog.Path)
	if err != nil {
		return fmt.Errorf("tool catalog: %w", err)
	}
	digest, err := canonicalDigest(data)
	if err != nil || digest != v.suite.ToolCatalog.Digest {
		return errors.New("tool catalog digest mismatch")
	}
	var catalog ToolCatalog
	if err := decodeStrict(data, &catalog); err != nil {
		return fmt.Errorf("tool catalog: %w", err)
	}
	if catalog.SchemaVersion != SpecVersion || catalog.MediaType != MediaTypeToolCatalog {
		return errors.New("tool catalog has unsupported schemaVersion or mediaType")
	}
	var errs []error
	for _, ref := range catalog.Tools {
		key := ref.ID + "@" + ref.Version
		if !identifierPattern.MatchString(ref.ID) || !versionPattern.MatchString(ref.Version) {
			errs = append(errs, fmt.Errorf("tool reference %q has invalid identity or exact version", key))
			continue
		}
		if _, exists := v.tools[key]; exists {
			errs = append(errs, fmt.Errorf("duplicate tool %s", key))
			continue
		}
		raw, err := v.content.data(ref.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("tool %s: %w", key, err))
			continue
		}
		actualDigest, err := canonicalDigest(raw)
		if err != nil || actualDigest != ref.Digest {
			errs = append(errs, fmt.Errorf("tool %s manifest digest mismatch", key))
			continue
		}
		var tool Tool
		if err := decodeStrict(raw, &tool); err != nil {
			errs = append(errs, fmt.Errorf("tool %s: %w", key, err))
			continue
		}
		if err := validateTool(tool, raw, v.content); err != nil {
			errs = append(errs, fmt.Errorf("tool %s: %w", key, err))
			continue
		}
		if tool.ID != ref.ID || tool.Version != ref.Version {
			errs = append(errs, fmt.Errorf("tool reference %s points to %s@%s", key, tool.ID, tool.Version))
			continue
		}
		v.tools[key] = tool
		v.toolDigests[key] = actualDigest
	}
	return errors.Join(errs...)
}

func (v *validator) loadToolSets() error {
	var errs []error
	for _, ref := range v.suite.ToolSets {
		key := ref.Agent + "@" + ref.Platform.String()
		if _, exists := v.toolSets[key]; exists {
			errs = append(errs, fmt.Errorf("duplicate tool set %s", key))
			continue
		}
		data, err := v.content.data(ref.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("tool set %s: %w", key, err))
			continue
		}
		digest, err := canonicalDigest(data)
		if err != nil || digest != ref.Digest {
			errs = append(errs, fmt.Errorf("tool set %s digest mismatch", key))
			continue
		}
		var toolSet ToolSet
		if err := decodeStrict(data, &toolSet); err != nil {
			errs = append(errs, fmt.Errorf("tool set %s: %w", key, err))
			continue
		}
		if err := validateToolSet(toolSet); err != nil {
			errs = append(errs, fmt.Errorf("tool set %s: %w", key, err))
			continue
		}
		if toolSet.Agent != ref.Agent || toolSet.Platform != ref.Platform {
			errs = append(errs, fmt.Errorf("tool set %s identity does not match its reference", key))
			continue
		}
		profile, ok := v.builds[toolSet.BuildProfile]
		if !ok || !profileSupportsPlatform(profile, toolSet.Platform) {
			errs = append(errs, fmt.Errorf("tool set %s build profile %q does not support %s", key, toolSet.BuildProfile, toolSet.Platform))
			continue
		}
		v.toolSets[key] = toolSet
	}
	return errors.Join(errs...)
}

func (v *validator) validateReferences() error {
	var errs []error
	for toolKey, tool := range v.tools {
		for _, variant := range tool.Variants {
			errs = append(errs, validateBundleDependencies(toolKey, variant, v.tools))
		}
	}
	for agentID, agent := range v.agents {
		for _, invoke := range agent.Invokes {
			if _, ok := v.agents[invoke.Agent]; !ok {
				errs = append(errs, fmt.Errorf("agent %s invokes unknown agent %s", agentID, invoke.Agent))
			}
		}
		for key, toolSet := range v.toolSets {
			if toolSet.Agent != agentID {
				continue
			}
			destinations := map[string]InventoryEntry{}
			requirements := map[string]ToolRequirement{}
			for _, requirement := range agent.Tools {
				requirements[requirement.ID+"@"+requirement.Version] = requirement
			}
			if len(requirements) != len(toolSet.Tools) {
				errs = append(errs, fmt.Errorf("tool set %s does not lock every agent requirement exactly once", key))
				continue
			}
			for _, locked := range toolSet.Tools {
				toolKey := locked.ID + "@" + locked.Version
				requirement, ok := requirements[toolKey]
				if !ok {
					errs = append(errs, fmt.Errorf("tool set %s locks undeclared tool %s", key, toolKey))
					continue
				}
				tool, exists := v.tools[toolKey]
				if !exists {
					errs = append(errs, fmt.Errorf("tool set %s locks missing tool %s", key, toolKey))
					continue
				}
				if locked.ManifestDigest != v.toolDigests[toolKey] || locked.ExecutionMode != requirement.ExecutionMode {
					errs = append(errs, fmt.Errorf("tool set %s has stale binding for %s", key, toolKey))
				}
				switch locked.ExecutionMode {
				case ExecutionInAgentSandbox:
					variant, matches := exactVariant(tool.Variants, toolSet.Platform)
					if matches != 1 || locked.VariantDigest != variant.VariantDigest {
						errs = append(errs, fmt.Errorf("tool set %s must select exactly one matching variant for %s", key, toolKey))
						continue
					}
					for _, entry := range variant.Files {
						destination := path.Join(variant.InstallRoot, entry.Path)
						if existing, ok := destinations[destination]; ok && !sameInstallEntry(existing, entry) {
							errs = append(errs, fmt.Errorf("tool set %s has non-identical destination collision at %s", key, destination))
						} else {
							destinations[destination] = entry
						}
					}
				case ExecutionIsolatedSandbox:
					errs = append(errs, fmt.Errorf("tool set %s requests reserved isolated-tool-sandbox capability", key))
				default:
					if tool.Remote == nil || locked.VariantDigest != "" {
						errs = append(errs, fmt.Errorf("tool set %s has invalid remote binding for %s", key, toolKey))
					}
				}
			}
		}
		found := false
		for _, toolSet := range v.toolSets {
			if toolSet.Agent == agentID {
				found = true
			}
		}
		if !found {
			errs = append(errs, fmt.Errorf("agent %s has no platform tool set", agentID))
		}
	}
	for key, toolSet := range v.toolSets {
		if _, ok := v.agents[toolSet.Agent]; !ok {
			errs = append(errs, fmt.Errorf("tool set %s references unknown agent %s", key, toolSet.Agent))
		}
	}
	if !slices.Equal(v.suite.Capabilities, derivedCapabilities(v.tools)) {
		errs = append(errs, errors.New("suite capabilities must exactly equal capabilities derived from its tool declarations"))
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
	for _, tool := range agent.Tools {
		key := tool.ID + "@" + tool.Version
		if !identifierPattern.MatchString(tool.ID) || !versionPattern.MatchString(tool.Version) {
			errs = append(errs, fmt.Errorf("tool requirement %s is not an exact identity", key))
		}
		if seen[key] {
			errs = append(errs, fmt.Errorf("duplicate tool requirement %s", key))
		}
		seen[key] = true
		switch tool.ExecutionMode {
		case ExecutionInAgentSandbox, "remote-mcp", ExecutionIsolatedSandbox:
		default:
			errs = append(errs, fmt.Errorf("tool requirement %s has unknown executionMode", key))
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

func validateTool(tool Tool, raw []byte, content *contentSet) error {
	var errs []error
	if tool.SchemaVersion != SpecVersion || tool.MediaType != MediaTypeTool {
		errs = append(errs, errors.New("unsupported schemaVersion or mediaType"))
	}
	if !identifierPattern.MatchString(tool.ID) || !versionPattern.MatchString(tool.Version) {
		errs = append(errs, errors.New("tool id or exact version is invalid"))
	}
	if tool.Provider.Protocol != "mcp" || tool.Provider.Revision == "" || len(tool.Provider.Operations) == 0 {
		errs = append(errs, errors.New("v1 tool provider must declare an MCP revision and at least one operation"))
	}
	operations := map[string]bool{}
	for _, operation := range tool.Provider.Operations {
		if !identifierPattern.MatchString(operation.Name) || operations[operation.Name] {
			errs = append(errs, fmt.Errorf("operation %q is invalid or duplicated", operation.Name))
		}
		operations[operation.Name] = true
		if err := validateFileRef(operation.InputSchema, content); err != nil {
			errs = append(errs, fmt.Errorf("operation %q input schema: %w", operation.Name, err))
		}
		if operation.OutputSchema != nil {
			if err := validateFileRef(*operation.OutputSchema, content); err != nil {
				errs = append(errs, fmt.Errorf("operation %q output schema: %w", operation.Name, err))
			}
		}
	}
	arms := 0
	if len(tool.Variants) > 0 {
		arms++
	}
	if tool.Remote != nil {
		arms++
		errs = append(errs, validateRemote(*tool.Remote))
	}
	if tool.Isolated != nil {
		arms++
		errs = append(errs, errors.New("isolated-tool-sandbox is reserved in v1 and must be rejected"))
	}
	if arms != 1 {
		errs = append(errs, errors.New("tool must select exactly one bundled, remote, or isolated connector"))
	}
	platforms := map[string]bool{}
	variantDigests, err := rawVariantDigests(raw)
	if err != nil {
		errs = append(errs, fmt.Errorf("variant identities: %w", err))
	}
	if len(variantDigests) != len(tool.Variants) {
		errs = append(errs, errors.New("raw and decoded variant counts differ"))
	}
	for i, variant := range tool.Variants {
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
	errs = append(errs, validateExtensions(tool.Extensions))
	return errors.Join(errs...)
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

func validateVariant(variant ToolVariant, expectedDigest string, content *contentSet) error {
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

func rawVariantDigests(rawTool []byte) ([]string, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(rawTool, &document); err != nil {
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

func validateRemote(remote RemoteMCP) error {
	var errs []error
	if remote.Transport != "streamable-http" || !envPattern.MatchString(remote.URLRef) || unsafeInjectionEnv(remote.URLRef) {
		errs = append(errs, errors.New("remote MCP requires streamable-http and an environment URL reference"))
	}
	for _, header := range remote.Headers {
		if header.Name == "" || (header.Value == "") == (header.ValueEnv == "") {
			errs = append(errs, errors.New("remote header must select exactly one literal or environment value"))
		}
		if header.ValueEnv != "" && (!envPattern.MatchString(header.ValueEnv) || unsafeInjectionEnv(header.ValueEnv)) {
			errs = append(errs, fmt.Errorf("remote header %s has invalid environment reference", header.Name))
		}
		if strings.ContainsAny(header.Name+header.Value, "\r\n") {
			errs = append(errs, fmt.Errorf("remote header %s contains a newline", header.Name))
		}
		lower := strings.ToLower(header.Name)
		if header.Value != "" && (lower == "authorization" || lower == "cookie" || lower == "x-api-key") {
			errs = append(errs, fmt.Errorf("credential header %s must not contain a literal value", header.Name))
		}
	}
	errs = append(errs, validateIdentifierList("remote secretRefs", remote.SecretRefs))
	return errors.Join(errs...)
}

func validateBundleDependencies(toolKey string, variant ToolVariant, tools map[string]Tool) error {
	var errs []error
	seen := map[string]bool{}
	for _, dependency := range variant.Dependencies {
		key := dependency.ID + "@" + dependency.Version
		if key == toolKey || seen[key] || !identifierPattern.MatchString(dependency.ID) ||
			!versionPattern.MatchString(dependency.Version) || !validDigest(dependency.VariantDigest) {
			errs = append(errs, fmt.Errorf("tool %s has invalid or duplicate dependency %s", toolKey, key))
			continue
		}
		seen[key] = true
		dependencyTool, ok := tools[key]
		if !ok {
			errs = append(errs, fmt.Errorf("tool %s depends on missing tool %s", toolKey, key))
			continue
		}
		selected, matches := exactVariant(dependencyTool.Variants, variant.Platform)
		if matches != 1 || selected.VariantDigest != dependency.VariantDigest {
			errs = append(errs, fmt.Errorf("tool %s dependency %s does not bind one %s variant", toolKey, key, variant.Platform))
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

func validateToolSet(toolSet ToolSet) error {
	var errs []error
	if toolSet.SchemaVersion != SpecVersion || toolSet.MediaType != MediaTypeToolSet {
		errs = append(errs, errors.New("unsupported schemaVersion or mediaType"))
	}
	if !identifierPattern.MatchString(toolSet.Agent) {
		errs = append(errs, errors.New("agent id is invalid"))
	}
	if !identifierPattern.MatchString(toolSet.BuildProfile) {
		errs = append(errs, errors.New("buildProfile is invalid"))
	}
	errs = append(errs, validatePlatform(toolSet.Platform))
	seen := map[string]bool{}
	for _, tool := range toolSet.Tools {
		key := tool.ID + "@" + tool.Version
		if seen[key] || !identifierPattern.MatchString(tool.ID) || !versionPattern.MatchString(tool.Version) ||
			!validDigest(tool.ManifestDigest) {
			errs = append(errs, fmt.Errorf("locked tool %s is invalid or duplicated", key))
		}
		seen[key] = true
	}
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

func validatePlatform(platform Platform) error {
	if platform.OS != "linux" {
		return fmt.Errorf("v1 supports exact Linux tool platforms only, got %s", platform.String())
	}
	if platform.Architecture != "amd64" && platform.Architecture != "arm64" {
		return fmt.Errorf("v1 supports linux/amd64 and linux/arm64 tools, got %s", platform.String())
	}
	if platform.Variant != "" {
		return fmt.Errorf("v1 requires an exact platform without OCI variant, got %s", platform.String())
	}
	return nil
}

func exactVariant(variants []ToolVariant, platform Platform) (ToolVariant, int) {
	var selected ToolVariant
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

func derivedCapabilities(tools map[string]Tool) []string {
	set := map[string]bool{}
	for _, tool := range tools {
		switch {
		case len(tool.Variants) > 0:
			set["bundled-stdio-mcp"] = true
		case tool.Remote != nil:
			set["remote-streamable-http-mcp"] = true
		case tool.Isolated != nil:
			set["isolated-tool-sandbox"] = true
		}
	}
	out := make([]string, 0, len(set))
	for capability := range set {
		out = append(out, capability)
	}
	slices.Sort(out)
	return out
}
