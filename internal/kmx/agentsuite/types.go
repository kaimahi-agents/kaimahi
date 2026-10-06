package agentsuite

const (
	SpecVersion = "1.0.0-draft"

	LayoutVersion = "1.0.0"

	MediaTypeArtifact       = "application/vnd.agentsuite.suite.v1"
	MediaTypeEmptyConfig    = "application/vnd.oci.empty.v1+json"
	MediaTypeContent        = "application/vnd.agentsuite.content.v1.tar+gzip"
	MediaTypeSuite          = "application/vnd.agentsuite.manifest.v1+json"
	MediaTypeAgent          = "application/vnd.agentsuite.agent.v1+json"
	MediaTypeToolCatalog    = "application/vnd.agentsuite.tool-catalog.v1+json"
	MediaTypeTool           = "application/vnd.agentsuite.tool.v1+json"
	MediaTypeComposition    = "application/vnd.agentsuite.composition.v1+json"
	MediaTypeBuildProfile   = "application/vnd.agentsuite.build-profile.v1+json"
	MediaTypeSandboxBinding = "application/vnd.agentsuite.sandbox-binding.v1+json"

	ExecutionSharedSandbox = "shared-sandbox"
)

type Descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
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
	SchemaVersion string           `json:"schemaVersion"`
	MediaType     string           `json:"mediaType"`
	Name          string           `json:"name"`
	Agents        []ManifestRef    `json:"agents"`
	ToolCatalog   ManifestRef      `json:"toolCatalog"`
	Compositions  []CompositionRef `json:"compositions"`
	BuildProfiles []ManifestRef    `json:"buildProfiles"`
	Capabilities  []string         `json:"capabilities,omitempty"`
	Extensions    []Extension      `json:"extensions,omitempty"`
}

type ManifestRef struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path"`
	Digest  string `json:"digest"`
}

type CompositionRef struct {
	Agent    string   `json:"agent"`
	Platform Platform `json:"platform"`
	Path     string   `json:"path"`
	Digest   string   `json:"digest"`
}

type Agent struct {
	SchemaVersion string            `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ID            string            `json:"id"`
	Description   string            `json:"description,omitempty"`
	Instructions  FileRef           `json:"instructions"`
	Model         ModelRequirement  `json:"model"`
	Tools         []ToolRequirement `json:"tools,omitempty"`
	Invokes       []AgentInvoke     `json:"invokes,omitempty"`
	Extensions    []Extension       `json:"extensions,omitempty"`
}

type FileRef struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type ModelRequirement struct {
	Protocol    string   `json:"protocol"`
	Model       string   `json:"model"`
	EndpointEnv string   `json:"endpointEnv,omitempty"`
	SecretRefs  []string `json:"secretRefs,omitempty"`
}

type ToolRequirement struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	ExecutionMode string `json:"executionMode"`
}

type AgentInvoke struct {
	Agent         string `json:"agent"`
	MaxConcurrent int    `json:"maxConcurrent,omitempty"`
	MaxDepth      int    `json:"maxDepth,omitempty"`
}

type ToolCatalog struct {
	SchemaVersion string        `json:"schemaVersion"`
	MediaType     string        `json:"mediaType"`
	Tools         []ManifestRef `json:"tools"`
}

type Tool struct {
	SchemaVersion string        `json:"schemaVersion"`
	MediaType     string        `json:"mediaType"`
	ID            string        `json:"id"`
	Version       string        `json:"version"`
	Provider      ToolProvider  `json:"provider"`
	Variants      []ToolVariant `json:"variants"`
	Extensions    []Extension   `json:"extensions,omitempty"`
}

type ToolProvider struct {
	Protocol   string          `json:"protocol"`
	Revision   string          `json:"revision"`
	Operations []ToolOperation `json:"operations"`
}

type ToolOperation struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	InputSchema  FileRef  `json:"inputSchema"`
	OutputSchema *FileRef `json:"outputSchema,omitempty"`
	Effects      []string `json:"effects,omitempty"`
}

type ToolVariant struct {
	Platform      Platform           `json:"platform"`
	VariantDigest string             `json:"variantDigest"`
	InstallRoot   string             `json:"installRoot"`
	Relocatable   bool               `json:"relocatable,omitempty"`
	PayloadRoot   string             `json:"payloadRoot"`
	Entrypoint    string             `json:"entrypoint"`
	Arguments     []string           `json:"arguments,omitempty"`
	SearchPath    []string           `json:"searchPath,omitempty"`
	Environment   []EnvironmentName  `json:"environment,omitempty"`
	WritablePaths []string           `json:"writablePaths,omitempty"`
	Network       []NetworkAccess    `json:"network,omitempty"`
	Runtime       RuntimeRequirement `json:"runtime"`
	Files         []InventoryEntry   `json:"files"`
	Dependencies  []BundleDependency `json:"dependencies,omitempty"`
	SBOM          *Descriptor        `json:"sbom,omitempty"`
	Provenance    *Descriptor        `json:"provenance,omitempty"`
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

type BundleDependency struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	VariantDigest string `json:"variantDigest"`
}

type PlatformImage struct {
	Platform Platform   `json:"platform"`
	Image    Descriptor `json:"image"`
}

type Composition struct {
	SchemaVersion string         `json:"schemaVersion"`
	MediaType     string         `json:"mediaType"`
	Agent         string         `json:"agent"`
	Platform      Platform       `json:"platform"`
	BuildProfile  string         `json:"buildProfile"`
	Tools         []ResolvedTool `json:"tools"`
}

type ResolvedTool struct {
	ID             string `json:"id"`
	Version        string `json:"version"`
	ManifestDigest string `json:"manifestDigest"`
	VariantDigest  string `json:"variantDigest,omitempty"`
	ExecutionMode  string `json:"executionMode"`
}

type BuildProfile struct {
	SchemaVersion string          `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	ID            string          `json:"id"`
	RuntimeBase   []PlatformImage `json:"runtimeBase"`
	Harness       []PlatformImage `json:"harness"`
	SourceEpoch   int64           `json:"sourceEpoch"`
}

type SandboxBinding struct {
	SchemaVersion string     `json:"schemaVersion"`
	MediaType     string     `json:"mediaType"`
	SuiteDigest   string     `json:"suiteDigest"`
	Agent         string     `json:"agent"`
	Platform      Platform   `json:"platform"`
	BuildProfile  string     `json:"buildProfile"`
	Composition   Descriptor `json:"composition"`
	Inventory     Descriptor `json:"inventory"`
}
