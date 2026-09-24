// Portable agent schema: a closed, platform-neutral authoring document with
// optional Orka and kagent-v1 extensions. It is decoded strictly — unknown
// fields, duplicate YAML keys, merge keys, extra documents and inline
// credential values are all refused — because this document is later hashed
// for identity and handed to adapters that must render exactly what it says,
// nothing more.
//
// Extension shapes follow TARGETS.md §7 exactly: Orka nests provider
// (defaultModel, secretRef, rateLimit) and a separate agent block (tools,
// skills, rateLimit); kagent nests harnessRef/modelConfigRef and a tools
// block with mcp/agents arms. Per-entry kagent shapes mirror the pinned
// kagent-v1 API verbatim: internal/kmx/runtime/portable.go's Kagent* types
// mirror agenttemplate_types.go's MCPToolBinding, AgentToolBinding,
// AgentTemplateSkill, PluginBundle and ArtifactSource — skills are
// {name, source}, plugins are {source, skills[]} with no name of their own,
// and a source is exactly one of an oci "ref@sha256:<64hex>" string, a git
// {url, commit}, or a bucket {s3: {endpoint, bucket, key, versionId,
// region?}}.
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

// ociDigestRE mirrors ArtifactSource.OCI's pinned pattern: a digest-pinned
// image reference, never a mutable tag.
var ociDigestRE = regexp.MustCompile(`^[^\s@]+@sha256:[0-9a-f]{64}$`)

// gitCommitRE mirrors GitArtifact.Commit's pinned pattern: a full SHA-1 (40
// hex) or SHA-256 (64 hex) commit ID, never a branch or tag.
var gitCommitRE = regexp.MustCompile(`^([0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

// httpURLRE mirrors GitArtifact.URL and S3Object.Endpoint's pinned pattern.
var httpURLRE = regexp.MustCompile(`^https?://\S+$`)

// kagentIsolationValues mirrors AgentToolIsolation's closed enum.
var kagentIsolationValues = map[string]bool{"Shared": true, "Dedicated": true}

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

// OrkaExtension follows TARGETS.md §7: `provider` is required for the
// current native-AI adapter (type, defaultModel, secretRef, and optional
// baseURL/rateLimit); the optional `agent` block carries tools, skills and
// agent-level rate limits, matching the current scaffold
// (internal/kmx/scaffold/orka.go).
type OrkaExtension struct {
	APIVersion string                `yaml:"apiVersion"`
	Namespace  string                `yaml:"namespace"`
	Provider   OrkaProviderExtension `yaml:"provider"`
	Agent      *OrkaAgentExtension   `yaml:"agent,omitempty"`
}

type OrkaProviderExtension struct {
	Type         string                  `yaml:"type"`
	BaseURL      string                  `yaml:"baseURL,omitempty"`
	DefaultModel string                  `yaml:"defaultModel"`
	SecretRef    OrkaSecretRefExtension  `yaml:"secretRef"`
	RateLimit    *OrkaRateLimitExtension `yaml:"rateLimit,omitempty"`
}

type OrkaSecretRefExtension struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key,omitempty"`
}

// OrkaAgentExtension is the optional `extensions.orka.agent` block: omitting
// it, or any of its fields, means no corresponding field, not a default.
type OrkaAgentExtension struct {
	Tools     []OrkaNamedRef          `yaml:"tools,omitempty"`
	Skills    []OrkaNamedRef          `yaml:"skills,omitempty"`
	RateLimit *OrkaRateLimitExtension `yaml:"rateLimit,omitempty"`
}

// OrkaNamedRef is one Orka tool or skill reference: an explicit name, never
// server:tool syntax or an inline definition.
type OrkaNamedRef struct {
	Name string `yaml:"name"`
}

type OrkaRateLimitExtension struct {
	RequestsPerMinute *int32 `yaml:"requestsPerMinute,omitempty"`
	TokensPerMinute   *int64 `yaml:"tokensPerMinute,omitempty"`
}

// KagentExtension follows TARGETS.md §7: `harnessRef` and `modelConfigRef`
// are required; `promptTemplate` falls back to spec.instructions when
// omitted; `tools` separates MCP tool bindings from delegated-AgentTemplate
// bindings, each mirroring the pinned kagent-v1 MCPToolBinding/
// AgentToolBinding exactly; `skills`/`plugins` mirror the pinned
// AgentTemplateSkill/PluginBundle exactly — a skill is {name, source} and a
// plugin bundle is {source, skills[]} with no name of its own — and every
// source pins a single immutable artifact, never a bare name.
type KagentExtension struct {
	APIVersion     string                `yaml:"apiVersion"`
	Namespace      string                `yaml:"namespace"`
	HarnessRef     KagentHarnessRef      `yaml:"harnessRef"`
	ModelConfigRef KagentModelConfigRef  `yaml:"modelConfigRef"`
	PromptTemplate string                `yaml:"promptTemplate,omitempty"`
	Tools          *KagentToolsExtension `yaml:"tools,omitempty"`
	Skills         []KagentSkillRef      `yaml:"skills,omitempty"`
	Plugins        []KagentPluginBundle  `yaml:"plugins,omitempty"`
	OutputSchema   map[string]any        `yaml:"outputSchema,omitempty"`
}

type KagentHarnessRef struct {
	Name string `yaml:"name"`
}

type KagentModelConfigRef struct {
	Name string `yaml:"name"`
}

// KagentToolsExtension separates the two arms TARGETS.md §7 names: `mcp`
// bindings to an MCP server's tools (mirroring the pinned MCPToolBinding),
// and `agents` delegation to another AgentTemplate (mirroring the pinned
// AgentToolBinding).
type KagentToolsExtension struct {
	MCP    []KagentMCPToolExtension `yaml:"mcp,omitempty"`
	Agents []KagentAgentToolBinding `yaml:"agents,omitempty"`
}

// KagentMCPToolExtension mirrors the pinned MCPToolBinding exactly: an
// explicit RemoteMCPServer, the tools allowed on it, and whether calling
// them requires approval.
type KagentMCPToolExtension struct {
	Server          KagentMCPServerRef `yaml:"server"`
	Tools           []string           `yaml:"tools,omitempty"`
	RequireApproval bool               `yaml:"requireApproval,omitempty"`
}

type KagentMCPServerRef struct {
	Kind string `yaml:"kind"`
	Name string `yaml:"name"`
}

// KagentAgentToolBinding mirrors the pinned AgentToolBinding exactly: name
// and description tell the parent when and why to delegate; templateRef
// names the same-namespace AgentTemplate; isolation is optional (Shared or
// Dedicated). A bare name carries none of this and is refused as lossy.
type KagentAgentToolBinding struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	TemplateRef KagentTemplateRef `yaml:"templateRef"`
	Isolation   string            `yaml:"isolation,omitempty"`
}

// KagentTemplateRef names a same-namespace AgentTemplate.
type KagentTemplateRef struct {
	Name string `yaml:"name"`
}

// KagentSkillRef mirrors the pinned AgentTemplateSkill exactly: one
// standalone skill's explicit name and its immutable source.
type KagentSkillRef struct {
	Name   string               `yaml:"name"`
	Source KagentArtifactSource `yaml:"source"`
}

// KagentPluginBundle mirrors the pinned PluginBundle exactly: a plugin
// package has no name of its own, only an immutable source and the skill
// names it selects from that package.
type KagentPluginBundle struct {
	Source KagentArtifactSource `yaml:"source"`
	Skills []string             `yaml:"skills,omitempty"`
}

// KagentArtifactSource mirrors the pinned ArtifactSource exactly: exactly
// one of oci (a single "<reference>@sha256:<64hex>" string), git or bucket,
// plus an optional shared relative path.
type KagentArtifactSource struct {
	OCI    string                `yaml:"oci,omitempty"`
	Git    *KagentGitArtifact    `yaml:"git,omitempty"`
	Bucket *KagentBucketArtifact `yaml:"bucket,omitempty"`
	Path   string                `yaml:"path,omitempty"`
}

// KagentGitArtifact mirrors the pinned GitArtifact exactly: a URL and a
// full commit ID, never a branch or tag.
type KagentGitArtifact struct {
	URL    string `yaml:"url"`
	Commit string `yaml:"commit"`
}

// KagentBucketArtifact mirrors the pinned BucketArtifact exactly: S3 is
// currently the only supported provider.
type KagentBucketArtifact struct {
	S3 KagentS3Object `yaml:"s3"`
}

// KagentS3Object mirrors the pinned S3Object exactly: an explicit object
// version, never a mutable "latest" read.
type KagentS3Object struct {
	Endpoint  string `yaml:"endpoint"`
	Bucket    string `yaml:"bucket"`
	Key       string `yaml:"key"`
	VersionID string `yaml:"versionId"`
	Region    string `yaml:"region,omitempty"`
}

// ParsePortableAgent strictly decodes exactly one YAML document into a
// PortableAgent, rejecting unknown fields, duplicate keys and merge keys at
// every level, additional documents, missing required fields,
// extension/apiVersion mismatches and inline credential-shaped values. On
// success it holds a defensive copy of data; mutating data afterward never
// changes the result.
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
	if err := rejectPortableKeyHazards(root.Content[0], ""); err != nil {
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

// portableMergeKey is YAML's merge key. It is refused rather than resolved:
// see rejectPortableKeyHazards.
const portableMergeKey = "<<"

// rejectPortableKeyHazards recurses through every mapping node in the
// document — at every nesting level, including inside sequences — and
// refuses the two ways an authored mapping can mean something other than
// what it literally says.
//
//  1. A repeated key. yaml.v3 otherwise silently keeps the last occurrence,
//     which would let a document quietly say two different things about the
//     same field.
//  2. A merge key ("<<"). yaml.v3 resolves it before the strict decode ever
//     sees the mapping, so the merged-in keys are never written where they
//     take effect: a merge can supply a field this mapping also states —
//     the duplicate this walk exists to catch — or supply one the closed
//     schema does not model, with neither gate able to see it. Refusing the
//     merge outright keeps every key an authored document carries visible at
//     the place it applies.
func rejectPortableKeyHazards(node *yaml.Node, path string) error {
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := rejectPortableKeyHazards(child, path); err != nil {
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
	if strings.TrimSpace(e.Provider.DefaultModel) == "" {
		return fmt.Errorf("provider.defaultModel is required")
	}
	if err := scaffold.ValidateObjectName(e.Provider.SecretRef.Name); err != nil {
		return fmt.Errorf("provider.secretRef.name: %w", err)
	}
	if e.Agent == nil {
		return nil
	}
	for _, list := range []struct {
		field string
		refs  []OrkaNamedRef
	}{{"agent.tools", e.Agent.Tools}, {"agent.skills", e.Agent.Skills}} {
		for i, ref := range list.refs {
			if !portableIdentifierRE.MatchString(ref.Name) {
				return fmt.Errorf("%s[%d].name: %q must be an explicit name, not server:tool syntax or YAML", list.field, i, ref.Name)
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
	if err := scaffold.ValidateObjectName(e.HarnessRef.Name); err != nil {
		return fmt.Errorf("harnessRef.name: %w", err)
	}
	if err := scaffold.ValidateObjectName(e.ModelConfigRef.Name); err != nil {
		return fmt.Errorf("modelConfigRef.name: %w", err)
	}
	if e.Tools != nil {
		for i, mcp := range e.Tools.MCP {
			if mcp.Server.Kind != "RemoteMCPServer" {
				return fmt.Errorf("tools.mcp[%d].server.kind must be %q (found %q)", i, "RemoteMCPServer", mcp.Server.Kind)
			}
			if strings.TrimSpace(mcp.Server.Name) == "" {
				return fmt.Errorf("tools.mcp[%d].server.name is required", i)
			}
		}
		for i, agent := range e.Tools.Agents {
			if err := agent.validate(); err != nil {
				return fmt.Errorf("tools.agents[%d].%w", i, err)
			}
		}
	}
	if err := validatePortableSkillRefs(e.Skills); err != nil {
		return err
	}
	return validatePortablePluginBundles(e.Plugins)
}

// validate mirrors the pinned AgentToolBinding's required/optional fields
// exactly: name and description are required, templateRef must name a
// valid same-namespace object, and isolation, when present, must be one of
// the closed enum values.
func (b KagentAgentToolBinding) validate() error {
	if strings.TrimSpace(b.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(b.Description) == "" {
		return fmt.Errorf("description is required")
	}
	if err := scaffold.ValidateObjectName(b.TemplateRef.Name); err != nil {
		return fmt.Errorf("templateRef.name: %w", err)
	}
	if b.Isolation != "" && !kagentIsolationValues[b.Isolation] {
		return fmt.Errorf("isolation must be %q or %q (found %q)", "Shared", "Dedicated", b.Isolation)
	}
	return nil
}

// validatePortableSkillRefs enforces that every kagent skill names a
// single, present, unique identity, and that its source pins an immutable
// artifact — never a bare name or a mutable reference like a tag or branch.
func validatePortableSkillRefs(refs []KagentSkillRef) error {
	seen := make(map[string]bool, len(refs))
	for i, ref := range refs {
		if strings.TrimSpace(ref.Name) == "" {
			return fmt.Errorf("skills[%d].name is required", i)
		}
		if seen[ref.Name] {
			return fmt.Errorf("skills: %q is not a unique, immutable identity (duplicate)", ref.Name)
		}
		seen[ref.Name] = true
		if err := ref.Source.validate(); err != nil {
			return fmt.Errorf("skills[%d].source: %w", i, err)
		}
	}
	return nil
}

// validatePortablePluginBundles enforces that every plugin bundle's source
// pins an immutable artifact, and that its skill selection is a set of
// explicit, non-empty names — a plugin bundle has no name of its own, so
// there is no bundle-level identity to deduplicate.
func validatePortablePluginBundles(bundles []KagentPluginBundle) error {
	for i, bundle := range bundles {
		if err := bundle.Source.validate(); err != nil {
			return fmt.Errorf("plugins[%d].source: %w", i, err)
		}
		seen := make(map[string]bool, len(bundle.Skills))
		for j, name := range bundle.Skills {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("plugins[%d].skills[%d] must be an explicit, non-empty skill name", i, j)
			}
			if seen[name] {
				return fmt.Errorf("plugins[%d].skills: %q is duplicated within one bundle's selection", i, name)
			}
			seen[name] = true
		}
	}
	return nil
}

// validate mirrors the pinned ArtifactSource's CEL rules exactly: exactly
// one of oci, git or bucket, an oci digest (never a tag), a full git
// commit ID (never a branch or tag), a non-empty bucket/key/versionId, and
// a path that is relative with no '..' segments.
func (s KagentArtifactSource) validate() error {
	set := 0
	if s.OCI != "" {
		set++
	}
	if s.Git != nil {
		set++
	}
	if s.Bucket != nil {
		set++
	}
	if set != 1 {
		return fmt.Errorf("exactly one of oci, git or bucket is required for an immutable artifact identity")
	}
	switch {
	case s.OCI != "":
		if !ociDigestRE.MatchString(s.OCI) {
			return fmt.Errorf("oci must be an exact <reference>@sha256:<64 hex> digest, not a mutable tag")
		}
	case s.Git != nil:
		if !httpURLRE.MatchString(s.Git.URL) {
			return fmt.Errorf("git.url must be an absolute http(s) URL")
		}
		if !gitCommitRE.MatchString(s.Git.Commit) {
			return fmt.Errorf("git.commit must be a full 40- or 64-character commit ID, not a branch or tag")
		}
	case s.Bucket != nil:
		if !httpURLRE.MatchString(s.Bucket.S3.Endpoint) {
			return fmt.Errorf("bucket.s3.endpoint must be an absolute http(s) URL")
		}
		if strings.TrimSpace(s.Bucket.S3.Bucket) == "" {
			return fmt.Errorf("bucket.s3.bucket is required")
		}
		if strings.TrimSpace(s.Bucket.S3.Key) == "" {
			return fmt.Errorf("bucket.s3.key is required")
		}
		if strings.TrimSpace(s.Bucket.S3.VersionID) == "" {
			return fmt.Errorf("bucket.s3.versionId is required for an immutable object reference")
		}
	}
	if len(s.Path) > 1024 {
		return fmt.Errorf("path must be at most 1024 characters")
	}
	if strings.HasPrefix(s.Path, "/") {
		return fmt.Errorf("path must be relative, not absolute")
	}
	for _, segment := range strings.Split(s.Path, "/") {
		if segment == ".." {
			return fmt.Errorf("path must not contain '..' segments")
		}
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
// extension, in TARGETS.md §7's nested provider/agent shape. The result
// validates the same way any decoded document does, and carries those exact
// encoded bytes as its source: DESIGN.md §2 frames the portable bundle
// digest over "the exact validated portable source bytes", and shorthand is
// "deterministically encoded first and framed under the same logical path",
// so an encoded document without source bytes would have no identity to
// hash.
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
					Type:         s.ProviderType,
					BaseURL:      s.BaseURL,
					DefaultModel: s.Model,
					SecretRef: OrkaSecretRefExtension{
						Name: s.SecretName,
						Key:  s.SecretKey,
					},
					RateLimit: s.ProviderRateLimit,
				},
			},
		},
	}
	if len(s.Tools) > 0 || len(s.Skills) > 0 || s.AgentRateLimit != nil {
		agentBlock := &OrkaAgentExtension{RateLimit: s.AgentRateLimit}
		for _, name := range s.Tools {
			agentBlock.Tools = append(agentBlock.Tools, OrkaNamedRef{Name: name})
		}
		for _, name := range s.Skills {
			agentBlock.Skills = append(agentBlock.Skills, OrkaNamedRef{Name: name})
		}
		agent.Extensions.Orka.Agent = agentBlock
	}
	if err := agent.validate(); err != nil {
		return nil, err
	}
	// Source must be the same bytes YAML renders, so that the portable digest
	// identifies exactly the document this shorthand stands for.
	source, err := yaml.Marshal(agent)
	if err != nil {
		return nil, fmt.Errorf("encode Orka shorthand: %w", err)
	}
	agent.source = source
	return agent, nil
}

// YAML deterministically renders the document: struct field order fixes the
// key order, so two equal documents always render identical bytes. For a
// shorthand-encoded document these are exactly its Source bytes.
func (p *PortableAgent) YAML() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(p)
}
