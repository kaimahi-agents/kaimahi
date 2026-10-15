package agentsuite

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	SpecVersion = "1.0.0-draft"

	LayoutVersion = "1.0.0"

	MediaTypeArtifact                = "application/vnd.agentsuite.suite.v1"
	MediaTypeEmptyConfig             = "application/vnd.oci.empty.v1+json"
	MediaTypeContent                 = "application/vnd.agentsuite.content.v1.tar+gzip"
	MediaTypeSuite                   = "application/vnd.agentsuite.manifest.v1+json"
	MediaTypeAgent                   = "application/vnd.agentsuite.agent.v1+json"
	MediaTypeEvaluationIndex         = "application/vnd.agentsuite.eval.index.v1+json"
	MediaTypeEvaluationCase          = "application/vnd.agentsuite.eval.case.v1+json"
	MediaTypeToolProviderCatalog     = "application/vnd.agentsuite.tool.provider.catalog.v1+json"
	MediaTypeToolProvider            = "application/vnd.agentsuite.tool.provider.v1+json"
	MediaTypeToolProviderComposition = "application/vnd.agentsuite.tool.provider.composition.v1+json"
	MediaTypeComposition             = "application/vnd.agentsuite.composition.v1+json"
	MediaTypeBuildProfile            = "application/vnd.agentsuite.build.profile.v1+json"
	MediaTypeInstructions            = "text/markdown; charset=utf-8"
	MediaTypeJSONSchema              = "application/schema+json"

	ExecutionSharedSandbox = "shared-sandbox"
)

// Descriptor is an OCI-compatible content descriptor: a named, nested
// property on a suite-local reference that carries the referenced content's
// media type, digest, and size, plus optional descriptive annotations.
// Outer selector fields (such as id, version, agent, platform, and path)
// stay on the enclosing reference; Descriptor only describes the referenced
// bytes.
type Descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// descriptorAlias decodes Descriptor without recursing back into
// UnmarshalJSON.
type descriptorAlias Descriptor

// descriptorKnownFields enumerates the closed set of properties the
// Descriptor schema fragment permits (additionalProperties: false).
var descriptorKnownFields = map[string]bool{
	"mediaType":   true,
	"digest":      true,
	"size":        true,
	"annotations": true,
}

// UnmarshalJSON requires mediaType, digest, and size to be present in the
// document. A present size of zero is a legitimate empty-content descriptor;
// an omitted size is rejected. Both would otherwise decode to the same Go
// zero value, so presence is checked explicitly against the raw fields
// before the normal typed decode. It also rejects any object key outside
// the closed descriptor shape, and rejects an explicit JSON null for
// mediaType, digest, size, or a present annotations key, which would
// otherwise silently decode to the same Go zero values as legitimate
// values (notably null size and size: 0 both decode to 0).
func (d *Descriptor) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name := range fields {
		if !descriptorKnownFields[name] {
			return fmt.Errorf("descriptor has unknown field %q", name)
		}
	}
	for _, name := range []string{"mediaType", "digest", "size"} {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("descriptor is missing required field %q", name)
		}
	}
	for _, name := range []string{"mediaType", "digest", "size"} {
		if isJSONNull(fields[name]) {
			return fmt.Errorf("descriptor field %q must not be null", name)
		}
	}
	if raw, ok := fields["annotations"]; ok && isJSONNull(raw) {
		return fmt.Errorf("descriptor field %q must not be null", "annotations")
	}
	var alias descriptorAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*d = Descriptor(alias)
	return nil
}

// isJSONNull reports whether raw is the literal JSON value null.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

func (p Platform) String() string {
	if p.Variant == "" {
		return p.OS + "/" + p.Architecture
	}
	return p.OS + "/" + p.Architecture + "/" + p.Variant
}

type AgentPlatform struct {
	ID        string     `json:"id"`
	Platforms []Platform `json:"platforms"`
}

type Extension struct {
	Name     string `json:"name"`
	Critical bool   `json:"critical"`
	Digest   string `json:"digest,omitempty"`
}

type Suite struct {
	SchemaVersion            string                       `json:"schemaVersion"`
	MediaType                string                       `json:"mediaType"`
	Name                     string                       `json:"name"`
	Agents                   []ManifestRef                `json:"agents"`
	ToolProviderCatalog      ManifestRef                  `json:"toolProviderCatalog"`
	ToolProviderCompositions []ToolProviderCompositionRef `json:"toolProviderCompositions,omitempty"`
	Compositions             []CompositionRef             `json:"compositions"`
	BuildProfiles            []ManifestRef                `json:"buildProfiles"`
	Capabilities             []string                     `json:"capabilities,omitempty"`
	Extensions               []Extension                  `json:"extensions,omitempty"`
}

type ManifestRef struct {
	ID         string     `json:"id"`
	Version    string     `json:"version,omitempty"`
	Path       string     `json:"path"`
	Descriptor Descriptor `json:"descriptor"`
}

type CompositionRef struct {
	Agent      string     `json:"agent"`
	Platform   Platform   `json:"platform"`
	Path       string     `json:"path"`
	Descriptor Descriptor `json:"descriptor"`
}

type ToolProviderCompositionRef struct {
	ID         string     `json:"id"`
	Version    string     `json:"version"`
	Platform   Platform   `json:"platform"`
	Path       string     `json:"path"`
	Descriptor Descriptor `json:"descriptor"`
}

type Agent struct {
	SchemaVersion string                    `json:"schemaVersion"`
	MediaType     string                    `json:"mediaType"`
	ID            string                    `json:"id"`
	Description   string                    `json:"description,omitempty"`
	Instructions  FileRef                   `json:"instructions"`
	Model         ModelRequirement          `json:"model"`
	ToolProviders []ToolProviderRequirement `json:"toolProviders,omitempty"`
	Invokes       []AgentInvoke             `json:"invokes,omitempty"`
	Evals         []FileRef                 `json:"evals,omitempty"`
	Extensions    []Extension               `json:"extensions,omitempty"`
}

type EvaluationIndex struct {
	SchemaVersion string           `json:"schemaVersion"`
	MediaType     string           `json:"mediaType"`
	Agent         string           `json:"agent"`
	Cases         []FileRef        `json:"cases"`
	Limits        EvaluationLimits `json:"limits"`
}

type EvaluationLimits struct {
	MaxCases     int `json:"maxCases"`
	MaxCaseBytes int `json:"maxCaseBytes"`
}

type EvaluationCase struct {
	SchemaVersion string                `json:"schemaVersion"`
	MediaType     string                `json:"mediaType"`
	ID            string                `json:"id"`
	Input         string                `json:"input"`
	Runs          *int                  `json:"runs,omitempty"`
	MinPasses     *int                  `json:"minPasses,omitempty"`
	Assertions    []EvaluationAssertion `json:"assertions"`
}

type EvaluationAssertion struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Critical     *bool  `json:"critical,omitempty"`
	Value        string `json:"value,omitempty"`
	Pattern      string `json:"pattern,omitempty"`
	Tool         string `json:"tool,omitempty"`
	Rubric       string `json:"rubric,omitempty"`
	MinScore     *int   `json:"minScore,omitempty"`
	JudgeModel   string `json:"judgeModel,omitempty"`
	JudgeVersion string `json:"judgeVersion,omitempty"`
}

type FileRef struct {
	Path       string     `json:"path"`
	Descriptor Descriptor `json:"descriptor"`
}

type ModelRequirement struct {
	Protocol    string   `json:"protocol"`
	Model       string   `json:"model"`
	EndpointEnv string   `json:"endpointEnv,omitempty"`
	SecretRefs  []string `json:"secretRefs,omitempty"`
}

type ToolProviderRequirement struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	ExecutionMode string `json:"executionMode"`
}

type AgentInvoke struct {
	Agent         string `json:"agent"`
	MaxConcurrent int    `json:"maxConcurrent,omitempty"`
	MaxDepth      int    `json:"maxDepth,omitempty"`
}

type ToolProviderCatalog struct {
	SchemaVersion string        `json:"schemaVersion"`
	MediaType     string        `json:"mediaType"`
	ToolProviders []ManifestRef `json:"toolProviders"`
}

// ToolProvider is a versioned contract and its bundled or remote implementations.
// The abstraction is protocol-neutral; the declared protocol contributes to identity.
type ToolProvider struct {
	SchemaVersion string                `json:"schemaVersion"`
	MediaType     string                `json:"mediaType"`
	ID            string                `json:"id"`
	Version       string                `json:"version"`
	Retained      *bool                 `json:"retained,omitempty"`
	Protocol      string                `json:"protocol"`
	Revision      string                `json:"revision"`
	Tools         []Tool                `json:"tools"`
	Variants      []ToolProviderVariant `json:"variants,omitempty"`
	Remote        *RemoteToolProvider   `json:"remote,omitempty"`
	Extensions    []Extension           `json:"extensions,omitempty"`
}

// Tool is one model-callable function exposed by a ToolProvider.
type Tool struct {
	Name                   string                  `json:"name"`
	Description            string                  `json:"description,omitempty"`
	InputSchema            FileRef                 `json:"inputSchema"`
	OutputSchema           *FileRef                `json:"outputSchema,omitempty"`
	Effects                []string                `json:"effects,omitempty"`
	Effect                 string                  `json:"effect,omitempty"`
	Approval               string                  `json:"approval,omitempty"`
	DelegatedAuthorization *DelegatedAuthorization `json:"delegatedAuthorization,omitempty"`
}

// DelegatedAuthorization declares that a Tool requires delegated-user
// authorization before it may be called. It never contains a credential
// value, consent state, token lifecycle state, or a deployment-specific
// Connection identifier; the deployment/runtime resolves the requirement to
// a verified requester and a live user-owned connection.
type DelegatedAuthorization struct {
	Service    string             `json:"service"`
	Scope      string             `json:"scope"`
	Credential BrokeredCredential `json:"credential"`
}

// BrokeredCredential declares that credential resolution and injection for a
// delegated-user authorization requirement are brokered entirely by the
// runtime. It never contains a credential value.
type BrokeredCredential struct {
	Resolution string `json:"resolution"`
	BindingRef string `json:"bindingRef"`
}

type RemoteToolProvider struct {
	Transport    string                     `json:"transport"`
	EndpointRef  string                     `json:"endpointRef"`
	Headers      []RemoteHeader             `json:"headers,omitempty"`
	Network      []RemoteNetworkRequirement `json:"network"`
	Timeouts     RemoteTimeouts             `json:"timeouts"`
	Cancellation string                     `json:"cancellation"`
	Connection   string                     `json:"connection"`
}

type RemoteHeader struct {
	Name      string       `json:"name"`
	SecretRef SecretKeyRef `json:"secretRef"`
}

type SecretKeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type RemoteNetworkRequirement struct {
	DestinationRef string `json:"destinationRef"`
}

type RemoteTimeouts struct {
	ConnectMilliseconds int `json:"connectMilliseconds"`
	RequestMilliseconds int `json:"requestMilliseconds"`
}

type ToolProviderVariant struct {
	Platform      Platform                 `json:"platform"`
	VariantDigest string                   `json:"variantDigest"`
	InstallRoot   string                   `json:"installRoot"`
	Relocatable   bool                     `json:"relocatable,omitempty"`
	PayloadRoot   string                   `json:"payloadRoot"`
	Entrypoint    string                   `json:"entrypoint"`
	Arguments     []string                 `json:"arguments,omitempty"`
	SearchPath    []string                 `json:"searchPath,omitempty"`
	Environment   []EnvironmentName        `json:"environment,omitempty"`
	WritablePaths []string                 `json:"writablePaths,omitempty"`
	Network       []NetworkAccess          `json:"network,omitempty"`
	Runtime       RuntimeRequirement       `json:"runtime"`
	Files         []InventoryEntry         `json:"files"`
	Dependencies  []ToolProviderDependency `json:"dependencies,omitempty"`
	SBOM          *Descriptor              `json:"sbom,omitempty"`
	Provenance    *Descriptor              `json:"provenance,omitempty"`
}

type EnvironmentName struct {
	Name     string `json:"name"`
	Required bool   `json:"required,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	Delivery string `json:"delivery,omitempty"`
}

type NetworkAccess struct {
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
}

type RuntimeRequirement struct {
	ABI           string   `json:"abi"`
	ABIVersion    string   `json:"abiVersion,omitempty"`
	CPUBaseline   string   `json:"cpuBaseline"`
	RequiredPaths []string `json:"requiredBasePaths,omitempty"`
}

type InventoryEntry struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	Mode       uint32 `json:"mode"`
	UID        int    `json:"uid"`
	GID        int    `json:"gid"`
	Size       int64  `json:"size,omitempty"`
	Digest     string `json:"digest,omitempty"`
	LinkTarget string `json:"linkTarget,omitempty"`
	Component  string `json:"component,omitempty"`
}

type ToolProviderDependency struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	VariantDigest string `json:"variantDigest"`
}

type PlatformImage struct {
	Platform Platform   `json:"platform"`
	ImageRef string     `json:"imageRef"`
	Image    Descriptor `json:"image"`
}

type Composition struct {
	SchemaVersion string                 `json:"schemaVersion"`
	MediaType     string                 `json:"mediaType"`
	Agent         string                 `json:"agent"`
	Platform      Platform               `json:"platform"`
	BuildProfile  string                 `json:"buildProfile"`
	ToolProviders []ResolvedToolProvider `json:"toolProviders"`
}

type ResolvedToolProvider struct {
	ID             string `json:"id"`
	Version        string `json:"version"`
	ManifestDigest string `json:"manifestDigest"`
	VariantDigest  string `json:"variantDigest,omitempty"`
	ExecutionMode  string `json:"executionMode"`
}

type ToolProviderComposition struct {
	SchemaVersion  string   `json:"schemaVersion"`
	MediaType      string   `json:"mediaType"`
	ID             string   `json:"id"`
	Version        string   `json:"version"`
	ManifestDigest string   `json:"manifestDigest"`
	Platform       Platform `json:"platform"`
	VariantDigest  string   `json:"variantDigest"`
	BuildProfile   string   `json:"buildProfile"`
}

type BuildProfile struct {
	SchemaVersion string          `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	ID            string          `json:"id"`
	RuntimeBase   []PlatformImage `json:"runtimeBase"`
	Harness       []PlatformImage `json:"harness"`
	SourceEpoch   int64           `json:"sourceEpoch"`
}
