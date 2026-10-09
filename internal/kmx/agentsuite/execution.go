package agentsuite

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gowebpki/jcs"
	"path"
	"slices"
	"strings"
)

const (
	ExecutionHTTPV1          = "kubernetes-http-v1"
	ImageDeploymentLabel     = "org.agentsuite.image-deployment"
	ImageDeploymentMediaType = "application/vnd.agentsuite.image.deployment.v1+json"
	AgentKitMountedConfig    = "agentkit-v0-mounted-v1"
	AgentKitConfigPath       = "/run/agentsuite/agent.json"
)

// ExecutionContract describes the versioned interface a built agent exposes.
// Environment values are supplied through named slots, not arbitrary overrides.
type ExecutionContract struct {
	Configuration string           `json:"configuration,omitempty"`
	Kind          string           `json:"kind"`
	Protocol      string           `json:"protocol"`
	Port          int              `json:"port"`
	HealthPath    string           `json:"healthPath"`
	Inputs        []ExecutionInput `json:"inputs"`
}

type ExecutionInput struct {
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Secret      bool   `json:"secret"`
}

// ImageDeployment is embedded in the digest-bound OCI image config label.
// It declares source identity, not filesystem conformance or publisher trust.
type ImageDeployment struct {
	Inference         *InferenceRequirements `json:"inference,omitempty"`
	SchemaVersion     string                 `json:"schemaVersion"`
	MediaType         string                 `json:"mediaType"`
	SuiteReference    string                 `json:"suiteReference"`
	SuiteDigest       string                 `json:"suiteDigest"`
	Agent             string                 `json:"agent"`
	Platform          Platform               `json:"platform"`
	CompositionDigest string                 `json:"compositionDigest"`
	BuildProfile      string                 `json:"buildProfile"`
	Execution         ExecutionContract      `json:"execution"`
}

func ValidateExecutionContract(contract ExecutionContract) error {
	if contract.Configuration != "" && contract.Configuration != AgentKitMountedConfig {
		return errors.New("unsupported execution configuration ABI")
	}
	if contract.Configuration == AgentKitMountedConfig && (contract.Port != 8080 || contract.HealthPath != "/healthz") {
		return errors.New("mounted AgentKit HTTP configuration requires port 8080 and /healthz")
	}
	if contract.Kind != ExecutionHTTPV1 {
		return fmt.Errorf("unsupported execution contract %q", contract.Kind)
	}
	if contract.Protocol != "openai-chat-v1" {
		return errors.New("kubernetes-http-v1 requires the openai-chat-v1 invocation protocol")
	}
	if contract.Port < 1024 || contract.Port > 65535 {
		return errors.New("execution port must be between 1024 and 65535")
	}
	if !strings.HasPrefix(contract.HealthPath, "/") || path.Clean(contract.HealthPath) != contract.HealthPath || strings.ContainsAny(contract.HealthPath, "?#\\\r\n") {
		return errors.New("healthPath must be an absolute normalized HTTP path")
	}
	names, envs := map[string]bool{}, map[string]bool{}
	for _, input := range contract.Inputs {
		if !identifierPattern.MatchString(input.Name) || names[input.Name] || !envPattern.MatchString(input.Environment) || envs[input.Environment] || unsafeInjectionEnv(input.Environment) || input.Environment == "PATH" || input.Environment == "HOME" || input.Environment == "AGENTKIT_PROTOCOL" || input.Environment == "AGENTKIT_PORT" {
			return errors.New("execution input names and environment names must be valid, unique and non-injecting")
		}
		names[input.Name], envs[input.Environment] = true, true
	}
	return nil
}

func DecodeImageDeployment(data []byte) (*ImageDeployment, error) {
	if len(data) > 16<<10 {
		return nil, errors.New("image deployment record exceeds 16 KiB")
	}
	var record ImageDeployment
	if err := decodeStrict(data, &record); err != nil {
		return nil, err
	}
	if record.SchemaVersion != SpecVersion || record.MediaType != ImageDeploymentMediaType || !validDigest(record.SuiteDigest) || !validDigest(record.CompositionDigest) || !identifierPattern.MatchString(record.Agent) || !identifierPattern.MatchString(record.BuildProfile) {
		return nil, errors.New("invalid image deployment identity")
	}
	if !strings.HasSuffix(record.SuiteReference, "@"+record.SuiteDigest) {
		return nil, errors.New("suiteReference must pin suiteDigest")
	}
	if err := validatePlatform(record.Platform); err != nil {
		return nil, err
	}
	if err := ValidateExecutionContract(record.Execution); err != nil {
		return nil, err
	}
	if (record.Inference != nil) != (record.Execution.Configuration == AgentKitMountedConfig) {
		return nil, errors.New("mounted configuration requires inference capabilities")
	}
	if record.Inference != nil {
		if err := record.Inference.Validate(); err != nil {
			return nil, err
		}
	}
	return &record, nil
}

// ValidateImageSource compares the image declaration with the verified suite.
// The first execution adapter refuses features it cannot install or enforce.
func ValidateImageSource(root string, record ImageDeployment) error {
	if _, err := EncodeImageDeployment(record); err != nil {
		return err
	}
	content, err := loadDirectory(root)
	if err != nil {
		return err
	}
	if _, err := validateContent(content); err != nil {
		return err
	}
	raw, err := content.data("agentsuite.json")
	if err != nil {
		return err
	}
	var suite Suite
	if err := decodeStrict(raw, &suite); err != nil {
		return err
	}
	var agent Agent
	found := false
	for _, ref := range suite.Agents {
		if ref.ID != record.Agent {
			continue
		}
		raw, err = content.data(ref.Path)
		if err != nil {
			return err
		}
		if err := decodeStrict(raw, &agent); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return errors.New("image references an absent suite agent")
	}
	if (agent.Model.Capabilities == nil) != (record.Inference == nil) || record.Inference != nil && *record.Inference != *agent.Model.Capabilities {
		return errors.New("image inference requirements differ from suite")
	}
	if len(agent.ToolProviders) != 0 || len(agent.Invokes) != 0 {
		return errors.New("kubernetes-http-v1 does not support ToolProviders or invocation edges")
	}
	if agent.Model.Protocol != "openai-compatible" || len(agent.Model.SecretRefs) != 0 {
		return errors.New("kubernetes-http-v1 requires an openai-compatible model without unresolved model.secretRefs")
	}
	if agent.Model.EndpointEnv != "" {
		bound := false
		for _, input := range record.Execution.Inputs {
			bound = bound || input.Environment == agent.Model.EndpointEnv && !input.Secret
		}
		if !bound {
			return errors.New("model endpointEnv has no non-secret execution input")
		}
	}
	found = false
	for _, ref := range suite.Compositions {
		if ref.Agent != record.Agent || ref.Platform != record.Platform {
			continue
		}
		if ref.Digest != record.CompositionDigest {
			return errors.New("image composition digest differs from suite")
		}
		var composition Composition
		raw, err = content.data(ref.Path)
		if err != nil {
			return err
		}
		if err := decodeStrict(raw, &composition); err != nil {
			return err
		}
		if composition.BuildProfile != record.BuildProfile {
			return errors.New("image build profile differs from composition")
		}
		found = true
	}
	if !found {
		return errors.New("image has no matching suite composition")
	}
	for _, ref := range suite.BuildProfiles {
		if ref.ID != record.BuildProfile {
			continue
		}
		var profile BuildProfile
		raw, err = content.data(ref.Path)
		if err != nil {
			return err
		}
		if err := decodeStrict(raw, &profile); err != nil {
			return err
		}
		if profile.Execution == nil || profile.Execution.Configuration != record.Execution.Configuration || profile.Execution.Kind != record.Execution.Kind || profile.Execution.Protocol != record.Execution.Protocol || profile.Execution.Port != record.Execution.Port || profile.Execution.HealthPath != record.Execution.HealthPath || !slices.Equal(profile.Execution.Inputs, record.Execution.Inputs) {
			return errors.New("image execution contract differs from build profile")
		}
		return nil
	}
	return errors.New("image references an absent build profile")
}

// EncodeImageDeployment emits canonical label bytes for image builders.
func EncodeImageDeployment(record ImageDeployment) (string, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	if _, err := DecodeImageDeployment(data); err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(data)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}
