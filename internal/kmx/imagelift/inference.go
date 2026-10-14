package imagelift

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// InferenceBinding is deployment-owned. Capabilities are operator declarations
// for this exact model/endpoint, not inferred from provider branding or /models.
type InferenceBinding struct {
	Model        string                           `json:"model"`
	Endpoint     string                           `json:"endpoint"`
	Credential   *agentsuite.SecretKeyRef         `json:"credential,omitempty"`
	Capabilities agentsuite.InferenceRequirements `json:"capabilities"`
	Evidence     string                           `json:"evidence"`
}

func (b InferenceBinding) Validate() error {
	u, err := url.Parse(b.Endpoint)
	if err != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("inference endpoint must be an HTTP(S) URL without credentials, query or fragment")
	}
	if strings.TrimSpace(b.Model) == "" || len(b.Model) > 256 || strings.ContainsAny(b.Model, "\r\n\x00") || secretshapes.Match(b.Model) != nil {
		return errors.New("invalid inference model identifier")
	}
	if b.Evidence != "operator-declared" {
		return errors.New("inference capability evidence must explicitly be operator-declared")
	}
	if b.Credential != nil && (!namePattern.MatchString(b.Credential.Name) || !keyPattern.MatchString(b.Credential.Key)) {
		return errors.New("invalid inference credential reference")
	}
	return b.Capabilities.Validate()
}

func runtimeConfig(record agentsuite.ImageDeployment, env Environment) ([]byte, error) {
	if env.Instructions == "" {
		return nil, errors.New("mounted configuration requires resolved suite instructions")
	}
	if env.Inference == nil || record.Inference == nil {
		return nil, errors.New("deployment requires an explicit inference binding")
	}
	if err := env.Inference.Validate(); err != nil {
		return nil, err
	}
	if err := record.Inference.Match(env.Inference.Capabilities); err != nil {
		return nil, err
	}
	if record.Inference.ToolCalling {
		return nil, errors.New("HTTP adapter does not yet implement ToolProviders")
	}
	for _, input := range record.Execution.Inputs {
		if input.Environment != "AGENTKIT_AUTH_TOKEN" && input.Environment != "AGENTKIT_BIND" {
			return nil, errors.New("mounted HTTP configuration supports only agent authentication and listen inputs")
		}
	}
	model := map[string]any{"provider": "openai-compatible", "name": env.Inference.Model, "baseURL": env.Inference.Endpoint}
	if env.Inference.Credential != nil {
		model["apiKeyEnv"] = "KMX_INFERENCE_KEY"
	}
	return json.Marshal(map[string]any{
		"abiVersion": "v0", "metadata": map[string]string{"name": record.Agent},
		"model": model, "instructions": env.Instructions, "tools": []any{},
		"expose": map[string]any{"openai": true, "port": record.Execution.Port},
	})
}
