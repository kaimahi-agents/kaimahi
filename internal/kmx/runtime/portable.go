// Portable agent schema: a closed, platform-neutral authoring document with
// optional Orka and kagent-v1 extensions. It is decoded strictly — unknown
// fields, duplicate YAML keys, extra documents and inline credential values
// are all refused — because this document is later hashed for identity and
// handed to adapters that must render exactly what it says, nothing more.
package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

// PortableAPIVersion and PortableKind are the only accepted document identity.
const (
	PortableAPIVersion = "kmx.kaimahi.dev/v1alpha1"
	PortableKind       = "PortableAgent"

	orkaExtensionAPIVersion   = "core.orka.ai/v1alpha1"
	kagentExtensionAPIVersion = "kagent.dev/v1alpha3"
)

// portableIdentifierRE matches an explicit name: no server:tool syntax, no
// embedded YAML or whitespace that could hide a second value.
var portableIdentifierRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// PortableAgent is the closed kmx.kaimahi.dev/v1alpha1 document. It carries
// its own exact source bytes so identity digests and adapter output can be
// framed over precisely what was authored, not a reserialized approximation.
type PortableAgent struct {
	APIVersion string             `yaml:"apiVersion"`
	Kind       string             `yaml:"kind"`
	Metadata   PortableMetadata   `yaml:"metadata"`
	Spec       PortableSpec       `yaml:"spec"`
	Extensions PortableExtensions `yaml:"extensions"`

	source []byte
}

type PortableMetadata struct {
	Name string `yaml:"name"`
}

type PortableSpec struct {
	Instructions string        `yaml:"instructions"`
	Model        PortableModel `yaml:"model"`
}

type PortableModel struct {
	Name string `yaml:"name"`
}

// PortableExtensions holds the two runtime-specific blocks this document may
// carry. Both are optional; a document with neither has no lifecycle target.
type PortableExtensions struct {
	Orka   *OrkaExtension   `yaml:"orka,omitempty"`
	Kagent *KagentExtension `yaml:"kagent,omitempty"`
}

// OrkaExtension preserves Orka's current Provider, Agent, Secret-reference,
// limits, tools and skills inputs under an explicit target apiVersion and
// namespace.
type OrkaExtension struct {
	APIVersion     string                  `yaml:"apiVersion"`
	Namespace      string                  `yaml:"namespace"`
	Provider       OrkaProviderExtension   `yaml:"provider"`
	SecretRef      OrkaSecretRefExtension  `yaml:"secretRef"`
	Tools          []string                `yaml:"tools,omitempty"`
	Skills         []string                `yaml:"skills,omitempty"`
	AgentRateLimit *OrkaRateLimitExtension `yaml:"agentRateLimit,omitempty"`
}

type OrkaProviderExtension struct {
	Type      string                  `yaml:"type"`
	Model     string                  `yaml:"model"`
	BaseURL   string                  `yaml:"baseURL,omitempty"`
	RateLimit *OrkaRateLimitExtension `yaml:"rateLimit,omitempty"`
}

type OrkaSecretRefExtension struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key,omitempty"`
}

type OrkaRateLimitExtension struct {
	RequestsPerMinute *int32 `yaml:"requestsPerMinute,omitempty"`
	TokensPerMinute   *int64 `yaml:"tokensPerMinute,omitempty"`
}

// KagentExtension represents a kagent-v1 v1alpha3 Harness reference, a
// ModelConfig reference, MCP tools, immutable skill/plugin identities, a
// prompt template and an optional output schema, under an explicit target
// apiVersion and namespace.
type KagentExtension struct {
	APIVersion     string               `yaml:"apiVersion"`
	Namespace      string               `yaml:"namespace"`
	Harness        KagentHarnessRef     `yaml:"harness"`
	ModelConfig    KagentModelConfigRef `yaml:"modelConfig"`
	Tools          []KagentToolRef      `yaml:"tools,omitempty"`
	Skills         []KagentIdentityRef  `yaml:"skills,omitempty"`
	Plugins        []KagentIdentityRef  `yaml:"plugins,omitempty"`
	PromptTemplate string               `yaml:"promptTemplate,omitempty"`
	OutputSchema   map[string]any       `yaml:"outputSchema,omitempty"`
}

type KagentHarnessRef struct {
	Name string `yaml:"name"`
}

type KagentModelConfigRef struct {
	Name string `yaml:"name"`
}

// KagentToolRef is one MCP tool reference: an explicit server and tool name,
// never symbolic server:tool syntax.
type KagentToolRef struct {
	Server string `yaml:"server"`
	Name   string `yaml:"name"`
}

// KagentIdentityRef is an immutable skill or plugin identity: a single
// explicit name, never an inline definition.
type KagentIdentityRef struct {
	Name string `yaml:"name"`
}

// ParsePortableAgent strictly decodes exactly one YAML document into a
// PortableAgent, rejecting unknown fields, duplicate keys at every level,
// additional documents, missing required fields, extension/apiVersion
// mismatches and inline credential-shaped values. On success it holds a
// defensive copy of data; mutating data afterward never changes the result.
func ParsePortableAgent(data []byte) (*PortableAgent, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("portable agent document must be valid UTF-8")
	}

	var root yaml.Node
	firstDecoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := firstDecoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("portable agent document is empty or not valid YAML: %w", err)
	}
	var extra yaml.Node
	switch err := firstDecoder.Decode(&extra); {
	case errors.Is(err, io.EOF):
		// Exactly one document, as required.
	case err == nil:
		return nil, fmt.Errorf("portable agent document must contain exactly one YAML document")
	default:
		return nil, fmt.Errorf("portable agent document must contain exactly one YAML document: %w", err)
	}

	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("portable agent document must be a single YAML mapping")
	}
	if err := rejectDuplicatePortableKeys(root.Content[0], ""); err != nil {
		return nil, fmt.Errorf("portable agent document: %w", err)
	}

	strictDecoder := yaml.NewDecoder(bytes.NewReader(data))
	strictDecoder.KnownFields(true)
	var agent PortableAgent
	if err := strictDecoder.Decode(&agent); err != nil {
		return nil, fmt.Errorf("portable agent document: %w", err)
	}
	if err := agent.validate(); err != nil {
		return nil, fmt.Errorf("portable agent document: %w", err)
	}

	source := make([]byte, len(data))
	copy(source, data)
	agent.source = source
	return &agent, nil
}

// Source returns a defensive copy of the exact bytes this document was
// decoded from. Callers may freely mutate the result without affecting the
// PortableAgent or any other caller's copy.
func (p *PortableAgent) Source() []byte {
	if p == nil || p.source == nil {
		return nil
	}
	out := make([]byte, len(p.source))
	copy(out, p.source)
	return out
}

// rejectDuplicatePortableKeys recurses through every mapping node in the
// document — at every nesting level, including inside sequences — and
// refuses a mapping that repeats a key. yaml.v3 otherwise silently keeps
// the last occurrence, which would let an authored document quietly say
// two different things about the same field.
func rejectDuplicatePortableKeys(node *yaml.Node, path string) error {
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := rejectDuplicatePortableKeys(child, path); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode, valueNode := node.Content[i], node.Content[i+1]
			key := keyNode.Value
			location := key
			if path != "" {
				location = path + "." + key
			}
			if seen[key] {
				return fmt.Errorf("duplicate key %q at %s (line %d)", key, location, keyNode.Line)
			}
			seen[key] = true
			if err := rejectDuplicatePortableKeys(valueNode, location); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			if err := rejectDuplicatePortableKeys(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// validate applies every requirement this closed schema states beyond what
// strict field decoding already enforces: exact API/kind, required
// name/instructions/model, extension apiVersion/namespace and
// runtime/extension consistency, immutable kagent skill/plugin identities,
// and the absence of any inline credential-shaped value anywhere in the
// document.
func (p *PortableAgent) validate() error {
	if p.APIVersion != PortableAPIVersion {
		return fmt.Errorf("apiVersion must be %q (found %q)", PortableAPIVersion, p.APIVersion)
	}
	if p.Kind != PortableKind {
		return fmt.Errorf("kind must be %q (found %q)", PortableKind, p.Kind)
	}
	if err := scaffold.ValidateName(p.Metadata.Name); err != nil {
		return fmt.Errorf("metadata.name: %w", err)
	}
	if strings.TrimSpace(p.Spec.Instructions) == "" {
		return fmt.Errorf("spec.instructions is required")
	}
	if strings.TrimSpace(p.Spec.Model.Name) == "" {
		return fmt.Errorf("spec.model.name is required")
	}
	if p.Extensions.Orka != nil {
		if err := p.Extensions.Orka.validate(); err != nil {
			return fmt.Errorf("extensions.orka.%w", err)
		}
	}
	if p.Extensions.Kagent != nil {
		if err := p.Extensions.Kagent.validate(); err != nil {
			return fmt.Errorf("extensions.kagent.%w", err)
		}
	}
	return refusePortableSecretShapes(p)
}

func (e *OrkaExtension) validate() error {
	if e.APIVersion != orkaExtensionAPIVersion {
		return fmt.Errorf("apiVersion must be %q for the %q runtime (found %q)", orkaExtensionAPIVersion, Orka, e.APIVersion)
	}
	if err := scaffold.ValidateNamespace(e.Namespace); err != nil {
		return fmt.Errorf("namespace: %w", err)
	}
	if strings.TrimSpace(e.Provider.Type) == "" {
		return fmt.Errorf("provider.type is required")
	}
	if strings.TrimSpace(e.Provider.Model) == "" {
		return fmt.Errorf("provider.model is required")
	}
	if err := scaffold.ValidateObjectName(e.SecretRef.Name); err != nil {
		return fmt.Errorf("secretRef.name: %w", err)
	}
	for _, list := range []struct {
		field string
		refs  []string
	}{{"tools", e.Tools}, {"skills", e.Skills}} {
		for _, ref := range list.refs {
			if !portableIdentifierRE.MatchString(ref) {
				return fmt.Errorf("%s: %q must be an explicit name, not server:tool syntax or YAML", list.field, ref)
			}
		}
	}
	return nil
}

func (e *KagentExtension) validate() error {
	if e.APIVersion != kagentExtensionAPIVersion {
		return fmt.Errorf("apiVersion must be %q for the kagent-v1 runtime (found %q)", kagentExtensionAPIVersion, e.APIVersion)
	}
	if err := scaffold.ValidateNamespace(e.Namespace); err != nil {
		return fmt.Errorf("namespace: %w", err)
	}
	if err := scaffold.ValidateObjectName(e.Harness.Name); err != nil {
		return fmt.Errorf("harness.name: %w", err)
	}
	if err := scaffold.ValidateObjectName(e.ModelConfig.Name); err != nil {
		return fmt.Errorf("modelConfig.name: %w", err)
	}
	for i, tool := range e.Tools {
		if strings.TrimSpace(tool.Server) == "" || strings.TrimSpace(tool.Name) == "" {
			return fmt.Errorf("tools[%d]: server and name are both required", i)
		}
	}
	if err := validatePortableIdentityRefs("skills", e.Skills); err != nil {
		return err
	}
	return validatePortableIdentityRefs("plugins", e.Plugins)
}

// validatePortableIdentityRefs enforces that every kagent skill or plugin
// reference names a single, present, unique identity. Two entries claiming
// the same name would make that identity ambiguous, which is exactly what
// "immutable" rules out.
func validatePortableIdentityRefs(field string, refs []KagentIdentityRef) error {
	seen := make(map[string]bool, len(refs))
	for i, ref := range refs {
		if strings.TrimSpace(ref.Name) == "" {
			return fmt.Errorf("%s[%d].name is required", field, i)
		}
		if seen[ref.Name] {
			return fmt.Errorf("%s: %q is not a unique, immutable identity (duplicate)", field, ref.Name)
		}
		seen[ref.Name] = true
	}
	return nil
}

// refusePortableSecretShapes scans every string this document carries —
// after strict decoding, so only modeled fields are in scope — for anything
// shaped like a credential. A portable agent references a Secret by name;
// it never carries the value.
func refusePortableSecretShapes(p *PortableAgent) error {
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode portable agent for credential scan: %w", err)
	}
	var values any
	if err := json.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("decode portable agent for credential scan: %w", err)
	}
	return scanPortableValues(values)
}

func scanPortableValues(value any) error {
	switch value := value.(type) {
	case string:
		return refusePortableSecretShape(value)
	case map[string]any:
		for key, item := range value {
			if err := refusePortableSecretShape(key); err != nil {
				return err
			}
			if err := scanPortableValues(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range value {
			if err := scanPortableValues(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func refusePortableSecretShape(value string) error {
	if shape := secretshapes.Match(value); shape != nil {
		return fmt.Errorf("refusing portable agent document containing something shaped like %s; reference a separately provisioned Secret, never a credential value", shape.What)
	}
	return nil
}

// OrkaShorthand mirrors the existing flag-based Orka creation inputs
// (OrkaSpec), for callers that author a portable document from CLI flags
// rather than an explicit --file.
type OrkaShorthand struct {
	Name, Namespace, Instructions                       string
	ProviderType, Model, BaseURL, SecretName, SecretKey string
	Tools, Skills                                       []string
	AgentRateLimit, ProviderRateLimit                   *OrkaRateLimitExtension
}

// EncodeOrkaShorthand deterministically encodes flag-based Orka creation
// inputs into a closed PortableAgent document carrying only the Orka
// extension. The result validates the same way any decoded document does.
func EncodeOrkaShorthand(s OrkaShorthand) (*PortableAgent, error) {
	agent := &PortableAgent{
		APIVersion: PortableAPIVersion,
		Kind:       PortableKind,
		Metadata:   PortableMetadata{Name: s.Name},
		Spec: PortableSpec{
			Instructions: s.Instructions,
			Model:        PortableModel{Name: s.Model},
		},
		Extensions: PortableExtensions{
			Orka: &OrkaExtension{
				APIVersion: orkaExtensionAPIVersion,
				Namespace:  s.Namespace,
				Provider: OrkaProviderExtension{
					Type:      s.ProviderType,
					Model:     s.Model,
					BaseURL:   s.BaseURL,
					RateLimit: s.ProviderRateLimit,
				},
				SecretRef: OrkaSecretRefExtension{
					Name: s.SecretName,
					Key:  s.SecretKey,
				},
				Tools:          append([]string(nil), s.Tools...),
				Skills:         append([]string(nil), s.Skills...),
				AgentRateLimit: s.AgentRateLimit,
			},
		},
	}
	if err := agent.validate(); err != nil {
		return nil, err
	}
	return agent, nil
}

// YAML deterministically renders the document: struct field order fixes the
// key order, so two equal documents always render identical bytes.
func (p *PortableAgent) YAML() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(p)
}
