package scaffold

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

const (
	// KagentVersion is the exact upstream contract this scaffolder emits.
	KagentVersion = "v0.10.2"

	KagentAPIVersion = "kagent.dev/v1alpha2"

	// DefaultKagentSecretKey is applied only by generation/encoding when a
	// caller omits a key. Parsed bindings must state their effective key.
	DefaultKagentSecretKey = "api-key"

	// KagentRemoteMCPServerKind is the only MCP server kind accepted by this
	// explicit same-namespace authoring path.
	KagentRemoteMCPServerKind = "RemoteMCPServer"
)

// KagentMCPToolBinding is an explicit same-namespace RemoteMCPServer grant.
type KagentMCPToolBinding struct {
	ServerKind, ServerName string
	ToolNames              []string
}

// KagentSpec is the fully separated behavior and creation target needed to
// render an exact Kagent v0.10.2 review bundle. It never carries a credential.
type KagentSpec struct {
	Name, Namespace, Description, Runtime, Instructions string
	ProviderType, Model, BaseURL, SecretName, SecretKey string
	Tools                                               []KagentMCPToolBinding
}

// KagentBundle is ordered as a value-free Secret prerequisite, ModelConfig,
// then Agent. The Secret is review-only and must never be applied.
type KagentBundle struct {
	Secret, ModelConfig, Agent map[string]any
}

// ValidateKagentToolName accepts only a single explicit MCP tool name, never
// server:tool shorthand or text that could encode another YAML value.
func ValidateKagentToolName(name string) error {
	if !identifierRE.MatchString(name) || strings.Contains(name, ":") {
		return fmt.Errorf("Kagent tool name must be an explicit name, not server:tool syntax or YAML")
	}
	return nil
}

// ValidateKagentProvider validates the target provider and optional endpoint.
// It never includes baseURL in an error because URLs can contain credentials.
func ValidateKagentProvider(provider, model, baseURL string) error {
	if provider != "openai" && provider != "anthropic" {
		return fmt.Errorf("an explicit Kagent model provider is required: openai or anthropic")
	}
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("an explicit Kagent model identifier is required")
	}
	if err := ValidateSingleLineText(model); err != nil {
		return fmt.Errorf("Kagent model %w", err)
	}
	if baseURL == "" {
		return nil
	}
	if err := ValidateSingleLineText(baseURL); err != nil {
		return fmt.Errorf("Kagent model baseURL must be an absolute HTTP(S) URL with a host and no credentials, query, or fragment")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(baseURL, "#") {
		return fmt.Errorf("Kagent model baseURL must be an absolute HTTP(S) URL with a host and no credentials, query, or fragment")
	}
	return nil
}

// ValidateKagentSecretKey applies Kubernetes' Secret data-key shape to a
// reference. It validates a key name only; no API here accepts a value.
func ValidateKagentSecretKey(key string) error {
	return ValidateOrkaSecretKey(key)
}

// GenerateKagent builds a review-only Secret/ModelConfig/Agent bundle for the
// exact v0.10.2 API without cluster or schema I/O.
func GenerateKagent(spec KagentSpec) (*KagentBundle, error) {
	inputs := []string{spec.Name, spec.Namespace, spec.Description, spec.Runtime, spec.Instructions,
		spec.ProviderType, spec.Model, spec.BaseURL, spec.SecretName, spec.SecretKey}
	for _, binding := range spec.Tools {
		inputs = append(inputs, binding.ServerKind, binding.ServerName)
		inputs = append(inputs, binding.ToolNames...)
	}
	for _, input := range inputs {
		if err := refuseKagentSecretShape(input); err != nil {
			return nil, err
		}
	}
	if err := ValidateName(spec.Name); err != nil {
		return nil, err
	}
	if err := ValidateNamespace(spec.Namespace); err != nil {
		return nil, fmt.Errorf("an explicit Kagent namespace is required: %w", err)
	}
	if strings.TrimSpace(spec.Description) == "" {
		return nil, fmt.Errorf("Kagent description is required")
	}
	if err := ValidateSingleLineText(spec.Description); err != nil {
		return nil, fmt.Errorf("Kagent description %w", err)
	}
	if spec.Runtime != "go" && spec.Runtime != "python" {
		return nil, fmt.Errorf("Kagent declarative runtime must be explicitly go or python")
	}
	if strings.TrimSpace(spec.Instructions) == "" {
		return nil, fmt.Errorf("Kagent system message is required")
	}
	if err := ValidateBlockText(spec.Instructions); err != nil {
		return nil, fmt.Errorf("Kagent system message %w", err)
	}
	if err := ValidateKagentProvider(spec.ProviderType, spec.Model, spec.BaseURL); err != nil {
		return nil, err
	}
	if err := ValidateObjectName(spec.SecretName); err != nil {
		return nil, fmt.Errorf("Kagent Secret name: %w", err)
	}
	if spec.SecretName == spec.Name {
		return nil, fmt.Errorf("Kagent model Secret name must differ from Agent name %s because the controller generates a same-name Secret", spec.Name)
	}
	if spec.SecretKey == "" {
		spec.SecretKey = DefaultKagentSecretKey
	}
	if err := ValidateKagentSecretKey(spec.SecretKey); err != nil {
		return nil, fmt.Errorf("Kagent Secret key: %w", err)
	}
	if err := validateKagentTools(spec.Tools); err != nil {
		return nil, err
	}

	bundle := buildKagentBundle(spec)
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	return bundle, nil
}

func validateKagentTools(tools []KagentMCPToolBinding) error {
	seenServers := make(map[string]bool, len(tools))
	for i, binding := range tools {
		if binding.ServerKind != KagentRemoteMCPServerKind {
			return fmt.Errorf("Kagent tools[%d].server kind must be exactly %s", i, KagentRemoteMCPServerKind)
		}
		if strings.Contains(binding.ServerName, ":") {
			return fmt.Errorf("Kagent tools[%d].server name must be an explicit name, not server:tool syntax", i)
		}
		if err := ValidateObjectName(binding.ServerName); err != nil {
			return fmt.Errorf("Kagent tools[%d].server name: %w", i, err)
		}
		if seenServers[binding.ServerName] {
			return fmt.Errorf("Kagent tools[%d].server name duplicates another MCP binding", i)
		}
		seenServers[binding.ServerName] = true
		if len(binding.ToolNames) == 0 {
			return fmt.Errorf("Kagent tools[%d].toolNames must be nonempty", i)
		}
		seenToolNames := make(map[string]bool, len(binding.ToolNames))
		for j, name := range binding.ToolNames {
			if err := ValidateKagentToolName(name); err != nil {
				return fmt.Errorf("Kagent tools[%d].toolNames[%d]: %w", i, j, err)
			}
			if seenToolNames[name] {
				return fmt.Errorf("Kagent tools[%d].toolNames[%d] duplicates another tool name", i, j)
			}
			seenToolNames[name] = true
		}
	}
	if len(tools) > 1 {
		return fmt.Errorf("Kagent create supports at most one RemoteMCPServer binding")
	}
	return nil
}

func buildKagentBundle(spec KagentSpec) *KagentBundle {
	providerName := "OpenAI"
	providerField := "openAI"
	if spec.ProviderType == "anthropic" {
		providerName = "Anthropic"
		providerField = "anthropic"
	}
	providerConfig := map[string]any{}
	if spec.BaseURL != "" {
		providerConfig["baseUrl"] = spec.BaseURL
	}
	modelSpec := map[string]any{
		"provider":        providerName,
		"model":           spec.Model,
		"apiKeySecret":    spec.SecretName,
		"apiKeySecretKey": spec.SecretKey,
		providerField:     providerConfig,
	}
	declarative := map[string]any{
		"runtime":       spec.Runtime,
		"modelConfig":   spec.Name,
		"systemMessage": spec.Instructions,
		"deployment": map[string]any{
			"podSecurityContext": map[string]any{
				"runAsNonRoot":   true,
				"seccompProfile": map[string]any{"type": "RuntimeDefault"},
			},
			"securityContext": map[string]any{
				"allowPrivilegeEscalation": false,
				"capabilities":             map[string]any{"drop": []any{"ALL"}},
				"readOnlyRootFilesystem":   true,
			},
			"volumes": []any{
				map[string]any{"name": "tmp", "emptyDir": map[string]any{}},
			},
			"volumeMounts": []any{
				map[string]any{"name": "tmp", "mountPath": "/tmp"},
			},
		},
	}
	if len(spec.Tools) > 0 {
		tools := make([]any, 0, len(spec.Tools))
		for _, binding := range spec.Tools {
			names := make([]any, len(binding.ToolNames))
			for i, name := range binding.ToolNames {
				names[i] = name
			}
			tools = append(tools, map[string]any{
				"type": "McpServer",
				"mcpServer": map[string]any{
					"apiGroup":  "kagent.dev",
					"kind":      binding.ServerKind,
					"name":      binding.ServerName,
					"toolNames": names,
				},
			})
		}
		declarative["tools"] = tools
	}
	bundle := &KagentBundle{
		Secret:      kagentResource("v1", "Secret", spec.SecretName, spec.Namespace),
		ModelConfig: kagentResource(KagentAPIVersion, "ModelConfig", spec.Name, spec.Namespace),
		Agent:       kagentResource(KagentAPIVersion, "Agent", spec.Name, spec.Namespace),
	}
	bundle.ModelConfig["spec"] = modelSpec
	bundle.Agent["spec"] = map[string]any{
		"description": spec.Description,
		"type":        "Declarative",
		"declarative": declarative,
	}
	return bundle
}

func kagentResource(apiVersion, kind, name, namespace string) map[string]any {
	return map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": namespace},
	}
}

// Validate enforces the exact emitted structure, same-namespace references,
// explicit runtime and MCP allowlists, hardened pod shape, and value-free
// Secret independently of the upstream CRD's weaker optional defaults.
func (b *KagentBundle) Validate() error {
	if b == nil || b.Secret == nil || b.ModelConfig == nil || b.Agent == nil {
		return fmt.Errorf("a Kagent bundle requires a metadata-only Secret, ModelConfig and Agent")
	}
	for _, doc := range b.Documents() {
		if err := scanKagentValues(doc); err != nil {
			return err
		}
	}
	secretName, namespace, err := kagentIdentity(b.Secret, "v1", "Secret")
	if err != nil {
		return err
	}
	modelName, modelNamespace, err := kagentIdentity(b.ModelConfig, KagentAPIVersion, "ModelConfig")
	if err != nil {
		return err
	}
	agentName, agentNamespace, err := kagentIdentity(b.Agent, KagentAPIVersion, "Agent")
	if err != nil {
		return err
	}
	if namespace != modelNamespace || namespace != agentNamespace {
		return fmt.Errorf("Kagent bundle resources must share one explicit namespace")
	}
	if modelName != agentName {
		return fmt.Errorf("ModelConfig.metadata.name must match Agent.metadata.name")
	}
	if secretName == agentName {
		return fmt.Errorf("Kagent model Secret name must differ from Agent name %s because the controller generates a same-name Secret", agentName)
	}
	if err := ValidateName(agentName); err != nil {
		return fmt.Errorf("Agent.metadata.name: %w", err)
	}

	modelSpec, ok := b.ModelConfig["spec"].(map[string]any)
	if !ok {
		return fmt.Errorf("ModelConfig.spec must be a mapping")
	}
	provider, _ := modelSpec["provider"].(string)
	model, _ := modelSpec["model"].(string)
	secretRef, _ := modelSpec["apiKeySecret"].(string)
	secretKey, _ := modelSpec["apiKeySecretKey"].(string)
	if secretKey == "" {
		return fmt.Errorf("ModelConfig.spec.apiKeySecretKey is required")
	}
	providerInput := ""
	providerField := ""
	switch provider {
	case "OpenAI":
		providerInput, providerField = "openai", "openAI"
	case "Anthropic":
		providerInput, providerField = "anthropic", "anthropic"
	default:
		return fmt.Errorf("ModelConfig.spec.provider must be OpenAI or Anthropic")
	}
	providerConfig, ok := modelSpec[providerField].(map[string]any)
	if !ok {
		return fmt.Errorf("ModelConfig.spec.%s must be an explicit mapping", providerField)
	}
	baseURL, _ := providerConfig["baseUrl"].(string)
	if err := ValidateKagentProvider(providerInput, model, baseURL); err != nil {
		return err
	}
	if secretRef != secretName {
		return fmt.Errorf("ModelConfig.spec.apiKeySecret must reference the bundle Secret")
	}
	if err := ValidateKagentSecretKey(secretKey); err != nil {
		return fmt.Errorf("ModelConfig.spec.apiKeySecretKey: %w", err)
	}

	agentSpec, ok := b.Agent["spec"].(map[string]any)
	if !ok || agentSpec["type"] != "Declarative" {
		return fmt.Errorf("Agent.spec must explicitly describe a Declarative agent")
	}
	description, ok := agentSpec["description"].(string)
	if !ok || strings.TrimSpace(description) == "" {
		return fmt.Errorf("Agent.spec.description must be explicitly stated and nonempty")
	}
	declarative, ok := agentSpec["declarative"].(map[string]any)
	if !ok {
		return fmt.Errorf("Agent.spec.declarative must be a mapping")
	}
	runtimeName, _ := declarative["runtime"].(string)
	instructions, _ := declarative["systemMessage"].(string)
	if declarative["modelConfig"] != modelName {
		return fmt.Errorf("Agent.spec.declarative.modelConfig must reference the bundle ModelConfig")
	}
	tools, err := decodeKagentTools(declarative["tools"])
	if err != nil {
		return err
	}
	spec := KagentSpec{
		Name: agentName, Namespace: namespace, Description: description,
		Runtime: runtimeName, Instructions: instructions,
		ProviderType: providerInput, Model: model, BaseURL: baseURL,
		SecretName: secretName, SecretKey: secretKey, Tools: tools,
	}
	if spec.Runtime != "go" && spec.Runtime != "python" {
		return fmt.Errorf("Agent.spec.declarative.runtime must be explicitly go or python")
	}
	if strings.TrimSpace(spec.Instructions) == "" {
		return fmt.Errorf("Agent.spec.declarative.systemMessage is required")
	}
	if err := ValidateBlockText(spec.Instructions); err != nil {
		return fmt.Errorf("Agent.spec.declarative.systemMessage %w", err)
	}
	if err := ValidateSingleLineText(spec.Description); err != nil {
		return fmt.Errorf("Agent.spec.description %w", err)
	}
	if err := validateKagentTools(spec.Tools); err != nil {
		return err
	}
	expected := buildKagentBundle(spec)
	if !reflect.DeepEqual(b.Secret, expected.Secret) {
		return fmt.Errorf("Secret must be a metadata-only review prerequisite with no data or stringData")
	}
	if !reflect.DeepEqual(b.ModelConfig, expected.ModelConfig) {
		return fmt.Errorf("ModelConfig does not match the closed Kagent v0.10.2 scaffold shape")
	}
	if !reflect.DeepEqual(b.Agent, expected.Agent) {
		return fmt.Errorf("Agent does not match the closed, hardened Kagent v0.10.2 scaffold shape")
	}
	return nil
}

func kagentIdentity(doc map[string]any, apiVersion, kind string) (string, string, error) {
	if doc["apiVersion"] != apiVersion || doc["kind"] != kind {
		return "", "", fmt.Errorf("%s kind or apiVersion does not match exact Kagent %s", kind, KagentVersion)
	}
	metadata, ok := doc["metadata"].(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("%s.metadata must be a mapping", kind)
	}
	name, _ := metadata["name"].(string)
	namespace, _ := metadata["namespace"].(string)
	if err := ValidateObjectName(name); err != nil {
		return "", "", fmt.Errorf("%s.metadata.name: %w", kind, err)
	}
	if err := ValidateNamespace(namespace); err != nil {
		return "", "", fmt.Errorf("%s.metadata.namespace: %w", kind, err)
	}
	return name, namespace, nil
}

func decodeKagentTools(raw any) ([]KagentMCPToolBinding, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("Agent.spec.declarative.tools must be a nonempty sequence when stated")
	}
	out := make([]KagentMCPToolBinding, 0, len(items))
	for i, item := range items {
		tool, ok := item.(map[string]any)
		if !ok || tool["type"] != "McpServer" {
			return nil, fmt.Errorf("Agent.spec.declarative.tools[%d] must be an MCP server mapping", i)
		}
		server, ok := tool["mcpServer"].(map[string]any)
		if !ok || server["apiGroup"] != "kagent.dev" {
			return nil, fmt.Errorf("Agent.spec.declarative.tools[%d].mcpServer must explicitly use apiGroup kagent.dev", i)
		}
		binding := KagentMCPToolBinding{}
		binding.ServerKind, _ = server["kind"].(string)
		binding.ServerName, _ = server["name"].(string)
		names, ok := server["toolNames"].([]any)
		if !ok {
			return nil, fmt.Errorf("Agent.spec.declarative.tools[%d].mcpServer.toolNames must be a sequence", i)
		}
		for j, rawName := range names {
			name, ok := rawName.(string)
			if !ok {
				return nil, fmt.Errorf("Agent.spec.declarative.tools[%d].mcpServer.toolNames[%d] must be a string", i, j)
			}
			binding.ToolNames = append(binding.ToolNames, name)
		}
		out = append(out, binding)
	}
	return out, nil
}

// Documents returns deterministic review order. The Secret is shown only so
// an operator can provision the named prerequisite; it must not be applied.
func (b *KagentBundle) Documents() []map[string]any {
	if b == nil {
		return nil
	}
	return []map[string]any{b.Secret, b.ModelConfig, b.Agent}
}

// YAML validates and deterministically serializes the three review documents.
func (b *KagentBundle) YAML() (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	documents := make([][]byte, 0, 3)
	for _, doc := range b.Documents() {
		encoded, err := yaml.Marshal(doc)
		if err != nil {
			return "", fmt.Errorf("render Kagent bundle: %w", err)
		}
		documents = append(documents, encoded)
	}
	return KagentArtifact(documents)
}

// KagentArtifact validates document bytes, canonicalizes their YAML framing,
// and adds the non-applicability warning. The header pins the upstream contract.
func KagentArtifact(documents [][]byte) (string, error) {
	if len(documents) != 3 {
		return "", fmt.Errorf("a Kagent review artifact requires Secret, ModelConfig and Agent documents")
	}
	for _, document := range documents {
		if !utf8.Valid(document) {
			return "", fmt.Errorf("Kagent artifact documents must be valid UTF-8")
		}
		if err := refuseKagentSecretShape(string(document)); err != nil {
			return "", err
		}
	}
	decoded := make([]map[string]any, 0, len(documents))
	var out strings.Builder
	out.WriteString("# Kagent review bundle for exact Kagent v0.10.2, scaffolded by kmx.\n")
	out.WriteString("# The metadata-only Secret skeleton must not be applied. Provision its named key separately.\n")
	out.WriteString("# Apply ModelConfig, wait for acceptance, then apply Agent and wait for readiness.\n")
	for i, document := range documents {
		doc, err := decodeKagentArtifactDocument(document)
		if err != nil {
			return "", fmt.Errorf("Kagent artifact document %d: %w", i+1, err)
		}
		decoded = append(decoded, doc)
		encoded, err := yaml.Marshal(doc)
		if err != nil {
			return "", fmt.Errorf("Kagent artifact document %d: cannot encode canonical YAML", i+1)
		}
		out.WriteString("---\n")
		out.Write(encoded)
	}
	if err := (&KagentBundle{Secret: decoded[0], ModelConfig: decoded[1], Agent: decoded[2]}).Validate(); err != nil {
		return "", fmt.Errorf("Kagent artifact: %w", err)
	}
	result := out.String()
	if err := refuseKagentSecretShape(result); err != nil {
		return "", err
	}
	return result, nil
}

func decodeKagentArtifactDocument(document []byte) (map[string]any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(document))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("must be one valid YAML mapping")
	}
	var extra any
	switch err := decoder.Decode(&extra); {
	case errors.Is(err, io.EOF):
	case err == nil:
		return nil, fmt.Errorf("must contain exactly one YAML document")
	default:
		return nil, fmt.Errorf("must contain exactly one valid YAML document")
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("must be a YAML mapping")
	}
	if err := rejectKagentYAMLHazards(root.Content[0]); err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := root.Content[0].Decode(&decoded); err != nil {
		return nil, fmt.Errorf("must be one valid YAML mapping")
	}
	return decoded, nil
}

func rejectKagentYAMLHazards(node *yaml.Node) error {
	switch node.Kind {
	case yaml.AliasNode:
		return fmt.Errorf("YAML aliases are not accepted")
	case yaml.MappingNode:
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || key.Tag == "!!merge" ||
				strings.IndexFunc(key.Value, unicode.IsControl) >= 0 {
				return fmt.Errorf("YAML mapping keys must be explicit strings, never merges")
			}
			if seen[key.Value] {
				return fmt.Errorf("duplicate YAML mapping key")
			}
			seen[key.Value] = true
			if err := rejectKagentYAMLHazards(value); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := rejectKagentYAMLHazards(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func refuseKagentSecretShape(value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("Kagent input must be valid UTF-8")
	}
	if shape := secretshapes.Match(value); shape != nil {
		return fmt.Errorf("refusing Kagent manifest containing something shaped like %s; reference a separately provisioned Secret, never a credential value", shape.What)
	}
	return nil
}

func scanKagentValues(value any) error {
	switch value := value.(type) {
	case string:
		return refuseKagentSecretShape(value)
	case map[string]any:
		for key, item := range value {
			if err := refuseKagentSecretShape(key); err != nil {
				return err
			}
			if err := scanKagentValues(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range value {
			if err := scanKagentValues(item); err != nil {
				return err
			}
		}
	}
	return nil
}
