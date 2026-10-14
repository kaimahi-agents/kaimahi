package agentsuite

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

const (
	ExecutionHTTPV1       = "http-v1"
	AgentKitMountedConfig = "agentkit-v0-mounted-v1"
)

// ExecutionContract is an experimental declaration of a built image's interface.
// The configuration name selects exact semantics, not arbitrary process overrides.
// It does not select a member runtime or require an image for native execution.
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

// UnmarshalJSON preserves required-field presence before decoding zero values.
func (c *ExecutionContract) UnmarshalJSON(data []byte) error {
	type wire ExecutionContract
	var decoded wire
	if err := decodeRequiredContract(data, &decoded, "kind", "protocol", "port", "healthPath", "inputs"); err != nil {
		return err
	}
	*c = ExecutionContract(decoded)
	return nil
}

// UnmarshalJSON requires an explicit secret classification, including false.
func (input *ExecutionInput) UnmarshalJSON(data []byte) error {
	type wire ExecutionInput
	var decoded wire
	if err := decodeRequiredContract(data, &decoded, "name", "environment", "secret"); err != nil {
		return err
	}
	*input = ExecutionInput(decoded)
	return nil
}

func decodeRequiredContract(data []byte, out any, required ...string) error {
	if err := Unmarshal(data, out); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range required {
		if raw, ok := fields[name]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("contract requires explicit non-null %s", name)
		}
	}
	return nil
}

func ValidateExecutionContract(c ExecutionContract) error {
	if c.Configuration != "" && c.Configuration != AgentKitMountedConfig {
		return errors.New("unsupported execution configuration ABI")
	}
	if c.Configuration == AgentKitMountedConfig && (c.Port != 8080 || c.HealthPath != "/healthz") {
		return errors.New("mounted AgentKit configuration requires port 8080 and /healthz")
	}
	if c.Kind != ExecutionHTTPV1 {
		return fmt.Errorf("unsupported execution contract %q", c.Kind)
	}
	if c.Protocol != "openai-chat-v1" {
		return errors.New("HTTP execution requires openai-chat-v1")
	}
	if c.Port < 1024 || c.Port > 65535 {
		return errors.New("execution port must be between 1024 and 65535")
	}
	if !strings.HasPrefix(c.HealthPath, "/") || path.Clean(c.HealthPath) != c.HealthPath || strings.ContainsAny(c.HealthPath, "?#\\\r\n") {
		return errors.New("healthPath must be a normalized HTTP path")
	}
	names, envs := map[string]bool{}, map[string]bool{}
	for _, input := range c.Inputs {
		if !identifierPattern.MatchString(input.Name) || names[input.Name] || !envPattern.MatchString(input.Environment) || envs[input.Environment] || unsafeInjectionEnv(input.Environment) || input.Environment == "PATH" || input.Environment == "HOME" || input.Environment == "AGENTKIT_PROTOCOL" || input.Environment == "AGENTKIT_PORT" {
			return errors.New("execution inputs must be valid, unique and non-injecting")
		}
		names[input.Name], envs[input.Environment] = true, true
	}
	return nil
}
