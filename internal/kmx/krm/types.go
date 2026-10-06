// Package krm defines kmx's resource model: independent Provider, Tool and
// Agent resources that Agents reference by name, so one Provider or Tool can
// be shared by several Agents. It decodes each resource strictly, validates
// it, and checks every reference in a Graph before anything is rendered.
//
// The package knows nothing about directories, packaging or runtimes. The
// bundle package loads a Graph from disk; runtime renderers translate one.
package krm

const (
	APIVersion = "kmx.kaimahi.dev/v1alpha1"

	KindProvider = "Provider"
	KindTool     = "Tool"
	KindAgent    = "Agent"

	ProviderOpenAI      = "openai"
	ProviderAnthropic   = "anthropic"
	ProviderAzureOpenAI = "azure-openai"
)

type Metadata struct {
	Name string `yaml:"name"`
}

// SecretKeyRef names one key of a separately provisioned Secret. It never
// carries the credential value.
type SecretKeyRef struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

type Credentials struct {
	Secret SecretKeyRef `yaml:"secret"`
}

// RateLimits carries only stated limits; nil means unstated.
type RateLimits struct {
	RequestsPerMinute *int32 `yaml:"requestsPerMinute,omitempty"`
	TokensPerMinute   *int64 `yaml:"tokensPerMinute,omitempty"`
}

// Provider is a reusable model connection.
type Provider struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   Metadata     `yaml:"metadata"`
	Spec       ProviderSpec `yaml:"spec"`

	source []byte
}

type ProviderSpec struct {
	Type string `yaml:"type"`
	// Model is the model identifier; for azure-openai it is the deployment name.
	Model       string          `yaml:"model"`
	OpenAI      *OpenAIProvider `yaml:"openAI,omitempty"`
	Azure       *AzureProvider  `yaml:"azure,omitempty"`
	Credentials Credentials     `yaml:"credentials"`
	RateLimits  *RateLimits     `yaml:"rateLimits,omitempty"`
}

type OpenAIProvider struct {
	BaseURL string `yaml:"baseURL,omitempty"`
}

type AzureProvider struct {
	Endpoint   string `yaml:"endpoint"`
	APIVersion string `yaml:"apiVersion,omitempty"`
}

// Tool is a reusable callable capability. HTTP is the only implementation.
type Tool struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
	Spec       ToolSpec `yaml:"spec"`

	source []byte
}

type ToolSpec struct {
	Description string `yaml:"description"`
	// Parameters is the JSON Schema of the tool's input object.
	Parameters map[string]any `yaml:"parameters,omitempty"`
	HTTP       *HTTPTool      `yaml:"http,omitempty"`
}

type HTTPTool struct {
	Method      string            `yaml:"method"`
	URL         string            `yaml:"url"`
	Timeout     string            `yaml:"timeout,omitempty"`
	Headers     map[string]string `yaml:"headers,omitempty"`
	Credentials *Credentials      `yaml:"credentials,omitempty"`
}

// Agent names the Provider and Tools it uses and the Agents it may delegate to.
type Agent struct {
	APIVersion string    `yaml:"apiVersion"`
	Kind       string    `yaml:"kind"`
	Metadata   Metadata  `yaml:"metadata"`
	Spec       AgentSpec `yaml:"spec"`

	source []byte
}

type AgentSpec struct {
	Description  string   `yaml:"description,omitempty"`
	Instructions string   `yaml:"instructions"`
	Provider     string   `yaml:"provider"`
	Tools        []string `yaml:"tools,omitempty"`
	// AllowedAgents is an explicit delegation allowlist. Bundle membership
	// never grants delegation.
	AllowedAgents []string `yaml:"allowedAgents,omitempty"`
}

// Source returns a copy of the exact bytes the resource was authored as.
func (p *Provider) Source() []byte { return clone(p.source) }
func (t *Tool) Source() []byte     { return clone(t.source) }
func (a *Agent) Source() []byte    { return clone(a.source) }

func clone(b []byte) []byte { return append([]byte(nil), b...) }
