// Portable authoring document: a closed `kmx.kaimahi.dev/v1alpha1` document
// carrying neutral agent behavior plus exactly one runtime extension, which
// today is Orka. It is decoded strictly — unknown fields, duplicate keys,
// merge keys, aliases, extra documents and credential-shaped values are all
// refused — because these exact bytes become the portable identity a
// LifecycleAdapter renders from, and an adapter must render what the
// document literally says, nothing more.
//
// The Orka extension states only what internal/kmx/scaffold can render:
// namespace, Provider type/baseURL/secretRef/rateLimit, and the optional
// Agent tools/skills/rateLimit. It deliberately does not restate the model —
// spec.model.name is the document's one statement of which model to use, and
// the adapter renders it into the Orka Provider's defaultModel — so a
// document cannot say two different things about the same model.
package runtime

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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

	orkaExtensionAPIVersion = "core.orka.ai/v1alpha1"
	// portableMergeKey is YAML's merge key, refused rather than resolved.
	portableMergeKey = "<<"
)

// PortableAgent is the closed document. It carries its own exact source bytes
// so identity digests are framed over precisely what was authored, never over
// a reserialized approximation.
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

// PortableSpec is the runtime-neutral behavior: what the agent is told to do
// and which model it uses. Anything platform-specific belongs in an extension.
type PortableSpec struct {
	Instructions string        `yaml:"instructions"`
	Model        PortableModel `yaml:"model"`
}

type PortableModel struct {
	Name string `yaml:"name"`
}

// PortableExtensions holds the runtime-specific blocks. Orka is the shipped
// runtime and the only extension modeled, so it is required: a document with
// no extension has no lifecycle target. A future runtime is added here as a
// separate optional field, never by loosening this one.
type PortableExtensions struct {
	Orka *OrkaExtension `yaml:"orka"`
}

// OrkaExtension is everything the Orka renderer needs beyond spec: the
// namespace its objects live in, the Provider to create, and the optional
// Agent-level tool, skill and rate-limit selections.
type OrkaExtension struct {
	APIVersion string                `yaml:"apiVersion"`
	Namespace  string                `yaml:"namespace"`
	Provider   OrkaProviderExtension `yaml:"provider"`
	Agent      *OrkaAgentExtension   `yaml:"agent,omitempty"`
}

// OrkaProviderExtension names the model provider and the separately
// provisioned Secret its credential lives in. It carries a reference, never
// a value.
type OrkaProviderExtension struct {
	Type      string                 `yaml:"type"`
	BaseURL   string                 `yaml:"baseURL,omitempty"`
	SecretRef OrkaSecretRefExtension `yaml:"secretRef"`
	RateLimit *OrkaRateLimit         `yaml:"rateLimit,omitempty"`
}

// OrkaSecretRefExtension names a Secret and, optionally, the key within it.
// An omitted key means the renderer's default, not an empty key.
type OrkaSecretRefExtension struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key,omitempty"`
}

// OrkaAgentExtension is optional, and so is each of its fields: omitting one
// means no corresponding field on the rendered Agent, never a default.
type OrkaAgentExtension struct {
	Tools     []OrkaNamedRef `yaml:"tools,omitempty"`
	Skills    []OrkaNamedRef `yaml:"skills,omitempty"`
	RateLimit *OrkaRateLimit `yaml:"rateLimit,omitempty"`
}

// OrkaNamedRef is one Orka tool or skill reference: an explicit name, never
// server:tool syntax or an inline definition.
type OrkaNamedRef struct {
	Name string `yaml:"name"`
}

// OrkaRateLimit carries only the limits an author stated. A nil field is an
// unstated limit; a stated one must be positive, because zero is not a limit.
type OrkaRateLimit struct {
	RequestsPerMinute *int32 `yaml:"requestsPerMinute,omitempty"`
	TokensPerMinute   *int64 `yaml:"tokensPerMinute,omitempty"`
}

// ParsePortableAgent strictly decodes exactly one YAML document into a
// PortableAgent. On success it retains a defensive copy of data; mutating
// data afterward never changes the result.
func ParsePortableAgent(data []byte) (*PortableAgent, error) {
	// The raw bytes are scanned before anything is allowed to quote them.
	// Every gate below names what it refused — yaml.v3 quotes the token it
	// choked on, the key walk names the key at its path, and field
	// validation prints the offending value — so a credential pasted into a
	// document that is ALSO malformed would be echoed by whichever gate
	// happened to fail first. Scanning first means the only thing kmx ever
	// says about such a document is which shape it carries.
	if err := refusePortableSecretShape(string(data)); err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("portable agent document must be valid UTF-8")
	}

	var root yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("portable agent document is empty or not valid YAML: %w", err)
	}
	var extra yaml.Node
	switch err := decoder.Decode(&extra); {
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
	if err := rejectPortableKeyHazards(root.Content[0], ""); err != nil {
		return nil, fmt.Errorf("portable agent document: %w", err)
	}

	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	var agent PortableAgent
	if err := strict.Decode(&agent); err != nil {
		return nil, fmt.Errorf("portable agent document: %w", err)
	}
	if err := agent.validate(); err != nil {
		return nil, fmt.Errorf("portable agent document: %w", err)
	}
	agent.source = append([]byte(nil), data...)
	return &agent, nil
}

// Source returns a defensive copy of the exact bytes this document was
// authored as. Callers may freely mutate the result.
func (p *PortableAgent) Source() []byte {
	if p == nil || p.source == nil {
		return nil
	}
	return append([]byte(nil), p.source...)
}

// rejectPortableKeyHazards recurses through every node in the document — at
// every nesting level, including inside sequences — and refuses the ways an
// authored mapping can mean something other than what it literally says.
//
//  1. A repeated key. yaml.v3 otherwise silently keeps the last occurrence,
//     which would let a document quietly say two different things about the
//     same field.
//  2. A merge key ("<<"). yaml.v3 resolves it before the strict decode ever
//     sees the mapping, so the merged-in keys are never written where they
//     take effect: a merge can supply a field this mapping also states — the
//     duplicate this walk exists to catch — or supply one the closed schema
//     does not model, with neither gate able to see it.
//  3. A plain alias. The same argument without the merge: the bytes where a
//     field takes effect are not what it means, and nothing here validates
//     an expanded alias, so none is accepted.
//  4. A non-scalar key. The closed schema has only plain-name keys, and a
//     complex key has no name for the duplicate check to compare.
func rejectPortableKeyHazards(node *yaml.Node, path string) error {
	switch node.Kind {
	case yaml.AliasNode:
		return fmt.Errorf("YAML alias at %s (line %d): every field must be stated where it applies, not copied in from an anchor", portablePathOrRoot(path), node.Line)
	case yaml.MappingNode:
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode, valueNode := node.Content[i], node.Content[i+1]
			if keyNode.Kind == yaml.AliasNode {
				return fmt.Errorf("YAML alias used as a key at %s (line %d): every field must be stated where it applies, not copied in from an anchor", portablePathOrRoot(path), keyNode.Line)
			}
			if keyNode.Kind != yaml.ScalarNode {
				return fmt.Errorf("key at %s (line %d) must be a plain name", portablePathOrRoot(path), keyNode.Line)
			}
			key := keyNode.Value
			location := key
			if path != "" {
				location = path + "." + key
			}
			if key == portableMergeKey || keyNode.Tag == "!!merge" {
				return fmt.Errorf("merge key %q at %s (line %d): every field must be stated where it applies, not merged in from an anchor", portableMergeKey, location, keyNode.Line)
			}
			if seen[key] {
				return fmt.Errorf("duplicate key %q at %s (line %d)", key, location, keyNode.Line)
			}
			seen[key] = true
			if err := rejectPortableKeyHazards(valueNode, location); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			if err := rejectPortableKeyHazards(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func portablePathOrRoot(path string) string {
	if path == "" {
		return "the document root"
	}
	return path
}

// validate applies every requirement this closed schema states beyond what
// strict field decoding already enforces. It is the single validation path:
// an authored document and a shorthand-encoded one are held to it equally.
func (p *PortableAgent) validate() error {
	// Every decoded string is checked for valid UTF-8 before anything else
	// can quote it, regex-match it, or otherwise treat it as text. A
	// "!!binary" scalar decodes to whatever bytes its base64 payload holds,
	// so a document whose raw bytes are valid UTF-8 (the base64 text itself
	// is plain ASCII) can still decode into a field that is not — the
	// raw-byte scan in ParsePortableAgent runs before decoding and cannot
	// see this. This is also the shorthand's only such scan, because
	// EncodeOrkaShorthand calls this same validate before ever marshaling.
	if err := refusePortableInvalidUTF8(p); err != nil {
		return err
	}
	// Before any check below can quote a field it refused.
	if err := refusePortableSecretShapes(p); err != nil {
		return err
	}
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
	if p.Extensions.Orka == nil {
		return fmt.Errorf("extensions.orka is required: %q is the only runtime this document targets", Orka)
	}
	if err := p.Extensions.Orka.validate(p.Spec.Model.Name); err != nil {
		return fmt.Errorf("extensions.orka.%w", err)
	}
	return nil
}

// validate checks the Orka extension against exactly what internal/kmx/
// scaffold can render, so a document that validates here cannot fail as an
// unrenderable surprise later. model comes from spec.model.name, which this
// extension deliberately does not restate.
func (e *OrkaExtension) validate(model string) error {
	if e.APIVersion != orkaExtensionAPIVersion {
		return fmt.Errorf("apiVersion must be %q for the %q runtime (found %q)", orkaExtensionAPIVersion, Orka, e.APIVersion)
	}
	if err := scaffold.ValidateNamespace(e.Namespace); err != nil {
		return fmt.Errorf("namespace: %w", err)
	}
	if err := scaffold.ValidateOrkaProvider(e.Provider.Type, model, e.Provider.BaseURL); err != nil {
		return fmt.Errorf("provider: %w", err)
	}
	if err := scaffold.ValidateObjectName(e.Provider.SecretRef.Name); err != nil {
		return fmt.Errorf("provider.secretRef.name: %w", err)
	}
	// An omitted key is the renderer's default; a stated one must be usable.
	if e.Provider.SecretRef.Key != "" {
		if err := scaffold.ValidateOrkaSecretKey(e.Provider.SecretRef.Key); err != nil {
			return fmt.Errorf("provider.secretRef.key: %w", err)
		}
	}
	if err := e.Provider.RateLimit.validate(); err != nil {
		return fmt.Errorf("provider.rateLimit.%w", err)
	}
	if e.Agent == nil {
		return nil
	}
	for _, list := range []struct {
		field string
		refs  []OrkaNamedRef
	}{{"tools", e.Agent.Tools}, {"skills", e.Agent.Skills}} {
		names := make([]string, len(list.refs))
		for i, ref := range list.refs {
			names[i] = ref.Name
		}
		if err := scaffold.ValidateOrkaRefNames(list.field, names); err != nil {
			return fmt.Errorf("agent.%s: %w", list.field, err)
		}
	}
	if err := e.Agent.RateLimit.validate(); err != nil {
		return fmt.Errorf("agent.rateLimit.%w", err)
	}
	return nil
}

// validate refuses a stated limit that does not limit anything. An absent
// rate-limit block states nothing and is always valid.
func (l *OrkaRateLimit) validate() error {
	if l == nil {
		return nil
	}
	if l.RequestsPerMinute != nil && *l.RequestsPerMinute <= 0 {
		return fmt.Errorf("requestsPerMinute must be positive")
	}
	if l.TokensPerMinute != nil && *l.TokensPerMinute <= 0 {
		return fmt.Errorf("tokensPerMinute must be positive")
	}
	return nil
}

// refusePortableInvalidUTF8 rejects a decoded string that is not valid
// UTF-8. It walks every string field this closed schema models, tools and
// skills slices included, so nothing decoded from a document can carry
// binary content past this point. The error names the field, never the
// value: the value is exactly what is refused for not being displayable
// text, so quoting it would defeat the refusal.
func refusePortableInvalidUTF8(p *PortableAgent) error {
	fields := []struct{ path, value string }{
		{"apiVersion", p.APIVersion},
		{"kind", p.Kind},
		{"metadata.name", p.Metadata.Name},
		{"spec.instructions", p.Spec.Instructions},
		{"spec.model.name", p.Spec.Model.Name},
	}
	if orka := p.Extensions.Orka; orka != nil {
		fields = append(fields,
			struct{ path, value string }{"extensions.orka.apiVersion", orka.APIVersion},
			struct{ path, value string }{"extensions.orka.namespace", orka.Namespace},
			struct{ path, value string }{"extensions.orka.provider.type", orka.Provider.Type},
			struct{ path, value string }{"extensions.orka.provider.baseURL", orka.Provider.BaseURL},
			struct{ path, value string }{"extensions.orka.provider.secretRef.name", orka.Provider.SecretRef.Name},
			struct{ path, value string }{"extensions.orka.provider.secretRef.key", orka.Provider.SecretRef.Key},
		)
		if orka.Agent != nil {
			for i, ref := range orka.Agent.Tools {
				fields = append(fields, struct{ path, value string }{fmt.Sprintf("extensions.orka.agent.tools[%d].name", i), ref.Name})
			}
			for i, ref := range orka.Agent.Skills {
				fields = append(fields, struct{ path, value string }{fmt.Sprintf("extensions.orka.agent.skills[%d].name", i), ref.Name})
			}
		}
	}
	for _, field := range fields {
		if !utf8.ValidString(field.value) {
			return fmt.Errorf("%s must be valid UTF-8", field.path)
		}
	}
	return nil
}

// refusePortableSecretShapes scans every string the decoded document carries.
// ParsePortableAgent already scanned the raw bytes, but a YAML folded or
// multi-line scalar can spell a credential across line breaks that the raw
// scan cannot see; this runs on the assembled values, and before any other
// check can quote one. It is also the shorthand's only scan, because every
// shorthand field reaches one of these strings.
func refusePortableSecretShapes(p *PortableAgent) error {
	values := []string{p.APIVersion, p.Kind, p.Metadata.Name, p.Spec.Instructions, p.Spec.Model.Name}
	if orka := p.Extensions.Orka; orka != nil {
		values = append(values, orka.APIVersion, orka.Namespace, orka.Provider.Type,
			orka.Provider.BaseURL, orka.Provider.SecretRef.Name, orka.Provider.SecretRef.Key)
		if orka.Agent != nil {
			for _, ref := range append(append([]OrkaNamedRef(nil), orka.Agent.Tools...), orka.Agent.Skills...) {
				values = append(values, ref.Name)
			}
		}
	}
	for _, value := range values {
		if err := refusePortableSecretShape(value); err != nil {
			return err
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

// OrkaShorthand is the flag-shaped input to EncodeOrkaShorthand: the same
// choices scaffold.OrkaSpec takes, narrowed to what a portable document can
// state. It is a distinct type so that a field added to OrkaSpec cannot be
// silently dropped from an encoded document.
type OrkaShorthand struct {
	Name, Namespace, Instructions                       string
	ProviderType, Model, BaseURL, SecretName, SecretKey string
	Tools, Skills                                       []string
	ProviderRateLimit, AgentRateLimit                   *OrkaRateLimit
}

// EncodeOrkaShorthand deterministically encodes flag-shaped input into a
// closed portable document and retains those exact encoded bytes as its
// source. Struct field order fixes key order, so the same shorthand always
// encodes to the same bytes and therefore to the same portable identity —
// an encoded document without source bytes would have no identity at all.
//
// The document is validated by exactly the rules an authored one faces,
// credential scan included and first, so shorthand is not a weaker way in.
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
					BaseURL:   s.BaseURL,
					SecretRef: OrkaSecretRefExtension{Name: s.SecretName, Key: s.SecretKey},
					RateLimit: s.ProviderRateLimit,
				},
			},
		},
	}
	// Only state an agent block the caller actually asked for: an empty one
	// would be a field a renderer then has to decide what to do with.
	if len(s.Tools) > 0 || len(s.Skills) > 0 || s.AgentRateLimit != nil {
		block := &OrkaAgentExtension{RateLimit: s.AgentRateLimit}
		for _, name := range s.Tools {
			block.Tools = append(block.Tools, OrkaNamedRef{Name: name})
		}
		for _, name := range s.Skills {
			block.Skills = append(block.Skills, OrkaNamedRef{Name: name})
		}
		agent.Extensions.Orka.Agent = block
	}
	if err := agent.validate(); err != nil {
		return nil, fmt.Errorf("portable agent document: %w", err)
	}
	source, err := yaml.Marshal(agent)
	if err != nil {
		return nil, fmt.Errorf("encode Orka shorthand: %w", err)
	}
	agent.source = source
	return agent, nil
}
