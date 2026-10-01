// Portable authoring document: a closed `kmx.kaimahi.dev/v1alpha1` document
// carrying neutral agent behavior plus exactly one Orka or Kagent runtime
// extension. It is decoded strictly — unknown fields, duplicate keys,
// merge keys, aliases, extra documents and credential-shaped values are all
// refused — because these exact bytes become the portable identity a
// LifecycleAdapter renders from, and an adapter must render what the
// document literally says, nothing more.
//
// Runtime extensions state only portable behavior. Namespace, provider
// endpoint and Secret references belong to separately validated bindings
// documents.
// The common spec.description remains optional for Orka and is required for
// Kagent, whose scaffold explicitly states it. An extension does not restate the model:
// spec.model.name is the document's one model, so a document cannot say two
// different things about the same model.
package runtime

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
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
	kagentExtensionAPIVersion = "kagent.dev/v1alpha2"
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
	Description  string        `yaml:"description,omitempty"`
	Model        PortableModel `yaml:"model"`
}

type PortableModel struct {
	Name string `yaml:"name"`
}

// PortableExtensions is a strict union. Exactly one pointer must be non-nil;
// a document cannot have no lifecycle target or two conflicting targets.
type PortableExtensions struct {
	Orka   *OrkaExtension   `yaml:"orka,omitempty"`
	Kagent *KagentExtension `yaml:"kagent,omitempty"`
}

// OrkaExtension holds runtime-specific behavior, never creation-target data.
type OrkaExtension struct {
	APIVersion string                `yaml:"apiVersion"`
	Provider   OrkaProviderExtension `yaml:"provider,omitempty"`
	Agent      *OrkaAgentExtension   `yaml:"agent,omitempty"`
}

// OrkaProviderExtension carries only behavior-level rate limits.
type OrkaProviderExtension struct {
	RateLimit *OrkaRateLimit `yaml:"rateLimit,omitempty"`
}

// OrkaAgentExtension is optional, and so is each of its fields: omitting one
// means no corresponding field on the rendered Agent, never a default.
type OrkaAgentExtension struct {
	Tools        []OrkaNamedRef    `yaml:"tools,omitempty"`
	Skills       []OrkaNamedRef    `yaml:"skills,omitempty"`
	RateLimit    *OrkaRateLimit    `yaml:"rateLimit,omitempty"`
	Coordination *OrkaCoordination `yaml:"coordination,omitempty"`
}

// OrkaCoordination contains only explicitly authored delegation behavior.
// Allowed agents are resolved in the Agent's target namespace.
type OrkaCoordination struct {
	Enabled               *bool          `yaml:"enabled,omitempty"`
	AllowedAgents         []OrkaNamedRef `yaml:"allowedAgents,omitempty"`
	MaxConcurrentChildren *int32         `yaml:"maxConcurrentChildren,omitempty"`
	MaxDepth              *int32         `yaml:"maxDepth,omitempty"`
}

// OrkaNamedRef carries an explicit name for a tool, skill, or allowed Agent.
// Its containing field determines which resource kind that name identifies.
type OrkaNamedRef struct {
	Name string `yaml:"name"`
}

// OrkaRateLimit carries only the limits an author stated. A nil field is an
// unstated limit; a stated one must be positive, because zero is not a limit.
type OrkaRateLimit struct {
	RequestsPerMinute *int32 `yaml:"requestsPerMinute,omitempty"`
	TokensPerMinute   *int64 `yaml:"tokensPerMinute,omitempty"`
}

// KagentExtension carries only behavior rendered into a Kagent v0.10.2 declarative
// Agent. Runtime is explicit rather than relying on Kagent's default. Tool
// bindings omit namespace by design: Kagent resolves them in the Agent's own
// namespace, and cross-namespace references are not portable authoring input.
type KagentExtension struct {
	APIVersion string             `yaml:"apiVersion"`
	Runtime    string             `yaml:"runtime"`
	Tools      []KagentMCPBinding `yaml:"tools,omitempty"`
}

// KagentMCPBinding grants a nonempty, explicit subset of one RemoteMCPServer.
type KagentMCPBinding struct {
	Server    KagentMCPServerRef `yaml:"server"`
	ToolNames []string           `yaml:"toolNames"`
}

type KagentMCPServerRef struct {
	Kind string `yaml:"kind"`
	Name string `yaml:"name"`
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
	// Now that the document has parsed, scan what it DECODES to, before the
	// two gates below can quote a decoded key back: the key walk names the
	// key it refused and the path it sits at, and yaml.v3's KnownFields
	// error names the unknown field it found. Neither value need exist in
	// the authored bytes the scan above saw, because escapes and a
	// "!!binary" payload are resolved at decode time.
	if err := refusePortableDecodedSecretShapes(root.Content[0]); err != nil {
		return nil, err
	}
	if err := rejectPortableKeyHazards(root.Content[0], ""); err != nil {
		return nil, fmt.Errorf("portable agent document: %w", err)
	}
	if hasBothPortableExtensions(root.Content[0]) {
		return nil, fmt.Errorf("portable agent document: extensions must contain exactly one of %q or %q", Orka, Kagent)
	}
	if block := portableValueAt(root.Content[0], "extensions", "orka", "agent", "coordination"); block != nil && block.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("portable agent document: extensions.orka.agent.coordination must be a mapping")
	}
	if portableValueAt(root.Content[0], "extensions", "orka", "agent", "coordination", "autonomous") != nil {
		return nil, fmt.Errorf("portable agent document: extensions.orka.agent.coordination.autonomous is not supported yet")
	}
	if refs := portableValueAt(root.Content[0], "extensions", "orka", "agent", "coordination", "allowedAgents"); refs != nil && refs.Kind == yaml.SequenceNode {
		for i, entry := range refs.Content {
			if portableValueAt(entry, "namespace") != nil {
				return nil, fmt.Errorf("portable agent document: allowedAgents namespace is a creation-target choice; remove namespace from entry %d", i)
			}
		}
	}
	// yaml.v3 otherwise truncates a fractional scalar decoded into *int32.
	for _, field := range []string{"maxConcurrentChildren", "maxDepth"} {
		if value := portableValueAt(root.Content[0], "extensions", "orka", "agent", "coordination", field); value != nil && value.Tag != "!!int" && value.Tag != "!!null" {
			return nil, fmt.Errorf("portable agent document: extensions.orka.agent.coordination.%s must be an integer", field)
		}
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
//  4. A key that is not a plain string name, or contains control characters.
//     Such a key is unsafe to name in an error or use as a path component.
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
			if keyNode.Kind != yaml.ScalarNode || keyNode.Tag != "!!str" ||
				strings.IndexFunc(keyNode.Value, unicode.IsControl) >= 0 {
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

func portableValueAt(node *yaml.Node, path ...string) *yaml.Node {
	if node.Kind != yaml.MappingNode || len(path) == 0 {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == path[0] {
			if len(path) == 1 {
				return node.Content[i+1]
			}
			return portableValueAt(node.Content[i+1], path[1:]...)
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

// hasBothPortableExtensions checks authored keys, not decoded pointers. A
// null arm is still an authored union arm and cannot accompany the other one.
func hasBothPortableExtensions(root *yaml.Node) bool {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "extensions" || root.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		var orka, kagent bool
		for j := 0; j+1 < len(root.Content[i+1].Content); j += 2 {
			switch root.Content[i+1].Content[j].Value {
			case "orka":
				orka = true
			case "kagent":
				kagent = true
			}
		}
		return orka && kagent
	}
	return false
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
	// Instructions are rendered as a literal block scalar and the model name
	// as a single-line one, so each is held to exactly the control-character
	// policy that renderer applies — shared from scaffold rather than
	// restated, because a portable document that validated here and then
	// failed to render would be a rejection with no authoring gate behind
	// it. Neither refusal quotes the value: the value is what was refused.
	if err := scaffold.ValidateBlockText(p.Spec.Instructions); err != nil {
		return fmt.Errorf("spec.instructions %w", err)
	}
	if err := scaffold.ValidateSingleLineText(p.Spec.Description); err != nil {
		return fmt.Errorf("spec.description %w", err)
	}
	if strings.TrimSpace(p.Spec.Model.Name) == "" {
		return fmt.Errorf("spec.model.name is required")
	}
	if err := scaffold.ValidateSingleLineText(p.Spec.Model.Name); err != nil {
		return fmt.Errorf("spec.model.name %w", err)
	}
	extensions := 0
	if p.Extensions.Orka != nil {
		extensions++
	}
	if p.Extensions.Kagent != nil {
		extensions++
	}
	if extensions != 1 {
		return fmt.Errorf("extensions must contain exactly one of %q or %q", Orka, Kagent)
	}
	if p.Extensions.Kagent != nil && strings.TrimSpace(p.Spec.Description) == "" {
		return fmt.Errorf("spec.description is required for the %q runtime", Kagent)
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
	return nil
}

// validate checks behavior independently of any target bindings.
func (e *OrkaExtension) validate() error {
	if e.APIVersion != orkaExtensionAPIVersion {
		return fmt.Errorf("apiVersion must be %q for the %q runtime (found %q)", orkaExtensionAPIVersion, Orka, e.APIVersion)
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
	if c := e.Agent.Coordination; c != nil {
		if c.Enabled == nil {
			return fmt.Errorf("agent.coordination.enabled is required when coordination is stated")
		}
		if *c.Enabled && len(c.AllowedAgents) == 0 {
			return fmt.Errorf("agent.coordination: enabled coordination requires at least one allowed agent; Orka treats an empty allowedAgents list as any Agent")
		}
		for i, ref := range c.AllowedAgents {
			if err := scaffold.ValidateName(ref.Name); err != nil {
				return fmt.Errorf("agent.coordination.allowedAgents[%d].name: %w", i, err)
			}
		}
		if c.MaxConcurrentChildren != nil && *c.MaxConcurrentChildren <= 0 {
			return fmt.Errorf("agent.coordination.maxConcurrentChildren must be positive")
		}
		if c.MaxDepth != nil && (*c.MaxDepth < 1 || *c.MaxDepth > 10) {
			return fmt.Errorf("agent.coordination.maxDepth must be between 1 and 10")
		}
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

func (e *KagentExtension) validate() error {
	if e.APIVersion != kagentExtensionAPIVersion {
		return fmt.Errorf("apiVersion must be %q for the %q runtime (found %q)", kagentExtensionAPIVersion, Kagent, e.APIVersion)
	}
	if e.Runtime != "go" && e.Runtime != "python" {
		return fmt.Errorf("runtime must be explicitly go or python")
	}
	seenServers := make(map[string]bool, len(e.Tools))
	for i, binding := range e.Tools {
		if binding.Server.Kind != scaffold.KagentRemoteMCPServerKind {
			return fmt.Errorf("tools[%d].server.kind must be exactly %s", i, scaffold.KagentRemoteMCPServerKind)
		}
		if strings.Contains(binding.Server.Name, ":") {
			return fmt.Errorf("tools[%d].server.name must be an explicit name, not server:tool syntax", i)
		}
		if err := scaffold.ValidateObjectName(binding.Server.Name); err != nil {
			return fmt.Errorf("tools[%d].server.name: %w", i, err)
		}
		if seenServers[binding.Server.Name] {
			return fmt.Errorf("tools[%d].server.name duplicates another MCP binding", i)
		}
		seenServers[binding.Server.Name] = true
		if len(binding.ToolNames) == 0 {
			return fmt.Errorf("tools[%d].toolNames must be nonempty", i)
		}
		seenToolNames := make(map[string]bool, len(binding.ToolNames))
		for j, name := range binding.ToolNames {
			if err := scaffold.ValidateKagentToolName(name); err != nil {
				return fmt.Errorf("tools[%d].toolNames[%d]: %w", i, j, err)
			}
			if seenToolNames[name] {
				return fmt.Errorf("tools[%d].toolNames[%d] duplicates another tool name", i, j)
			}
			seenToolNames[name] = true
		}
	}
	if len(e.Tools) > 1 {
		return fmt.Errorf("tools supports at most one RemoteMCPServer binding")
	}
	return nil
}

// refusePortableInvalidUTF8 rejects a decoded string that is not valid
// UTF-8. It walks every string field this closed schema models, including
// tools, skills and allowed Agents, so nothing decoded can carry
// binary content past this point. The error names the field, never the
// value: the value is exactly what is refused for not being displayable
// text, so quoting it would defeat the refusal.
func refusePortableInvalidUTF8(p *PortableAgent) error {
	fields := []struct{ path, value string }{
		{"apiVersion", p.APIVersion},
		{"kind", p.Kind},
		{"metadata.name", p.Metadata.Name},
		{"spec.instructions", p.Spec.Instructions},
		{"spec.description", p.Spec.Description},
		{"spec.model.name", p.Spec.Model.Name},
	}
	if orka := p.Extensions.Orka; orka != nil {
		fields = append(fields, struct{ path, value string }{"extensions.orka.apiVersion", orka.APIVersion})
		if orka.Agent != nil {
			for i, ref := range orka.Agent.Tools {
				fields = append(fields, struct{ path, value string }{fmt.Sprintf("extensions.orka.agent.tools[%d].name", i), ref.Name})
			}
			for i, ref := range orka.Agent.Skills {
				fields = append(fields, struct{ path, value string }{fmt.Sprintf("extensions.orka.agent.skills[%d].name", i), ref.Name})
			}
			if orka.Agent.Coordination != nil {
				for i, ref := range orka.Agent.Coordination.AllowedAgents {
					fields = append(fields, struct{ path, value string }{fmt.Sprintf("extensions.orka.agent.coordination.allowedAgents[%d].name", i), ref.Name})
				}
			}
		}
	}
	if kagent := p.Extensions.Kagent; kagent != nil {
		fields = append(fields,
			struct{ path, value string }{"extensions.kagent.apiVersion", kagent.APIVersion},
			struct{ path, value string }{"extensions.kagent.runtime", kagent.Runtime},
		)
		for i, binding := range kagent.Tools {
			fields = append(fields,
				struct{ path, value string }{fmt.Sprintf("extensions.kagent.tools[%d].server.kind", i), binding.Server.Kind},
				struct{ path, value string }{fmt.Sprintf("extensions.kagent.tools[%d].server.name", i), binding.Server.Name},
			)
			for j, name := range binding.ToolNames {
				fields = append(fields, struct{ path, value string }{fmt.Sprintf("extensions.kagent.tools[%d].toolNames[%d]", i, j), name})
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
	values := []string{p.APIVersion, p.Kind, p.Metadata.Name, p.Spec.Instructions, p.Spec.Description, p.Spec.Model.Name}
	if orka := p.Extensions.Orka; orka != nil {
		values = append(values, orka.APIVersion)
		if orka.Agent != nil {
			for _, ref := range append(append([]OrkaNamedRef(nil), orka.Agent.Tools...), orka.Agent.Skills...) {
				values = append(values, ref.Name)
			}
			if orka.Agent.Coordination != nil {
				for _, ref := range orka.Agent.Coordination.AllowedAgents {
					values = append(values, ref.Name)
				}
			}
		}
	}
	if kagent := p.Extensions.Kagent; kagent != nil {
		values = append(values, kagent.APIVersion, kagent.Runtime)
		for _, binding := range kagent.Tools {
			values = append(values, binding.Server.Kind, binding.Server.Name)
			values = append(values, binding.ToolNames...)
		}
	}
	for _, value := range values {
		if err := refusePortableSecretShape(value); err != nil {
			return err
		}
	}
	return nil
}

// refusePortableDecodedSecretShapes scans every scalar the parsed document
// carries — mapping keys as well as values, at every nesting level, modeled
// by this schema or not — and refuses a credential shape before any later
// gate can quote one back.
//
// The raw-byte scan in ParsePortableAgent sees what was authored; this sees
// what that text decodes to, and they are not the same string. A key
// written "\x67hp_..." carries no shape in the file and a credential once
// decoded, and the gates that name keys — the duplicate/merge/alias walk,
// and the strict decoder's "field X not found" — print the decoded form. A
// key is never a value this schema models, so validate()'s scan of the
// decoded struct cannot cover one.
func refusePortableDecodedSecretShapes(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if err := refusePortableSecretShape(node.Value); err != nil {
			return err
		}
		// A "!!binary" scalar's Value is still the base64 text; yaml.v3
		// resolves it into the bytes it encodes, and those bytes are what a
		// later error would print. A payload that does not decode is left
		// to the decoder to refuse — it carries no assembled value to echo.
		if node.Tag == "!!binary" {
			if decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(node.Value), "")); err == nil {
				if err := refusePortableSecretShape(string(decoded)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, child := range node.Content {
		if err := refusePortableDecodedSecretShapes(child); err != nil {
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

// OrkaShorthand is the flag-shaped creation input to EncodeOrkaShorthand.
// Target references are checked here but omitted from the portable source.
// It is distinct from scaffold.OrkaSpec so adding scaffold fields cannot
// silently change the document's shape.
type OrkaShorthand struct {
	Name, Namespace, Instructions, Description                                            string
	ProviderType, Model, BaseURL, AzureDeployment, AzureAPIVersion, SecretName, SecretKey string
	Tools, Skills, AllowedAgents                                                          []string
	Coordination                                                                          bool
	ProviderRateLimit, AgentRateLimit                                                     *OrkaRateLimit
}

// cloneOrkaRateLimit returns a deep copy of limit — a new struct with its
// own copies of RequestsPerMinute and TokensPerMinute, not the caller's
// pointers — so that encoding never aliases a caller-owned value. A nil
// limit stays nil, because an unstated limit is not a stated empty one.
func cloneOrkaRateLimit(limit *OrkaRateLimit) *OrkaRateLimit {
	if limit == nil {
		return nil
	}
	clone := &OrkaRateLimit{}
	if limit.RequestsPerMinute != nil {
		rpm := *limit.RequestsPerMinute
		clone.RequestsPerMinute = &rpm
	}
	if limit.TokensPerMinute != nil {
		tpm := *limit.TokensPerMinute
		clone.TokensPerMinute = &tpm
	}
	return clone
}

// EncodeOrkaShorthand deterministically encodes flag-shaped input into a
// closed portable document and retains those exact encoded bytes as its
// source. Struct field order fixes key order, so the same shorthand always
// encodes to the same bytes and therefore to the same portable identity —
// an encoded document without source bytes would have no identity at all.
//
// The revision is validated by the authored-document rules; target fields
// are validated separately before either can be quoted in an error.
func EncodeOrkaShorthand(s OrkaShorthand) (*PortableAgent, error) {
	if !s.Coordination && len(s.AllowedAgents) > 0 {
		return nil, fmt.Errorf("allowed agents require coordination")
	}
	agent := &PortableAgent{
		APIVersion: PortableAPIVersion,
		Kind:       PortableKind,
		Metadata:   PortableMetadata{Name: s.Name},
		Spec: PortableSpec{
			Instructions: s.Instructions,
			Description:  s.Description,
			Model:        PortableModel{Name: s.Model},
		},
		Extensions: PortableExtensions{
			Orka: &OrkaExtension{
				APIVersion: orkaExtensionAPIVersion,
				Provider:   OrkaProviderExtension{RateLimit: cloneOrkaRateLimit(s.ProviderRateLimit)},
			},
		},
	}
	// Only state an agent block the caller actually asked for: an empty one
	// would be a field a renderer then has to decide what to do with.
	if len(s.Tools) > 0 || len(s.Skills) > 0 || s.AgentRateLimit != nil || s.Coordination {
		block := &OrkaAgentExtension{RateLimit: cloneOrkaRateLimit(s.AgentRateLimit)}
		if s.Coordination {
			enabled := true
			block.Coordination = &OrkaCoordination{Enabled: &enabled}
			for _, name := range s.AllowedAgents {
				block.Coordination.AllowedAgents = append(block.Coordination.AllowedAgents, OrkaNamedRef{Name: name})
			}
		}
		for _, name := range s.Tools {
			block.Tools = append(block.Tools, OrkaNamedRef{Name: name})
		}
		for _, name := range s.Skills {
			block.Skills = append(block.Skills, OrkaNamedRef{Name: name})
		}
		agent.Extensions.Orka.Agent = block
	}
	// Flag-shaped target fields are validated separately, never encoded
	// into agent.yaml or its exact-source digest.
	if err := (OrkaBindings{Namespace: s.Namespace, Provider: OrkaProviderBindings{
		Type: s.ProviderType, BaseURL: s.BaseURL,
		Azure:     OrkaAzureBindings{DeploymentName: s.AzureDeployment, APIVersion: s.AzureAPIVersion},
		SecretRef: OrkaSecretRefBindings{Name: s.SecretName, Key: s.SecretKey},
	}}).validate(); err != nil {
		return nil, fmt.Errorf("Orka creation bindings: %w", err)
	}
	if s.ProviderType == "azure-openai" {
		if err := scaffold.ValidateOrkaProvider(s.ProviderType, s.Model, s.BaseURL, s.AzureDeployment, s.AzureAPIVersion); err != nil {
			return nil, fmt.Errorf("Orka creation bindings: %w", err)
		}
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

// KagentShorthand is flag-shaped Kagent creation input. Only behavior fields
// enter the PortableAgent source; namespace and model credentials are checked
// independently through KagentBindings.
type KagentShorthand struct {
	Name, Namespace, Instructions, Description string
	Runtime, ProviderType, Model, BaseURL      string
	SecretName, SecretKey                      string
	Tools                                      []KagentMCPBinding
}

// EncodeKagentShorthand deterministically encodes behavior and validates the
// creation target separately so target changes never alter portable identity.
// An omitted SecretKey is validated using DefaultKagentSecretKey;
// callers encoding the companion bindings get the same documented default.
func EncodeKagentShorthand(s KagentShorthand) (*PortableAgent, error) {
	values := []string{s.Name, s.Namespace, s.Instructions, s.Description, s.Runtime,
		s.ProviderType, s.Model, s.BaseURL, s.SecretName, s.SecretKey}
	for _, binding := range s.Tools {
		values = append(values, binding.Server.Kind, binding.Server.Name)
		values = append(values, binding.ToolNames...)
	}
	for _, value := range values {
		if err := refusePortableSecretShape(value); err != nil {
			return nil, err
		}
	}
	agent := &PortableAgent{
		APIVersion: PortableAPIVersion,
		Kind:       PortableKind,
		Metadata:   PortableMetadata{Name: s.Name},
		Spec: PortableSpec{
			Instructions: s.Instructions,
			Description:  s.Description,
			Model:        PortableModel{Name: s.Model},
		},
		Extensions: PortableExtensions{Kagent: &KagentExtension{
			APIVersion: kagentExtensionAPIVersion,
			Runtime:    s.Runtime,
		}},
	}
	for _, binding := range s.Tools {
		copyBinding := binding
		copyBinding.ToolNames = append([]string(nil), binding.ToolNames...)
		agent.Extensions.Kagent.Tools = append(agent.Extensions.Kagent.Tools, copyBinding)
	}
	secretKey := s.SecretKey
	if secretKey == "" {
		secretKey = DefaultKagentSecretKey
	}
	if err := (KagentBindings{
		APIVersion: KagentBindingsAPIVersion,
		Kind:       KagentBindingsKind,
		Namespace:  s.Namespace,
		ModelConfig: KagentModelConfigBindings{
			Provider: s.ProviderType,
			BaseURL:  s.BaseURL,
			SecretRef: KagentSecretRefBindings{
				Name: s.SecretName,
				Key:  secretKey,
			},
		},
	}).validate(); err != nil {
		return nil, fmt.Errorf("Kagent creation bindings: %w", err)
	}
	if err := agent.validate(); err != nil {
		return nil, fmt.Errorf("portable agent document: %w", err)
	}
	source, err := yaml.Marshal(agent)
	if err != nil {
		return nil, fmt.Errorf("encode Kagent shorthand: %w", err)
	}
	agent.source = source
	return agent, nil
}
