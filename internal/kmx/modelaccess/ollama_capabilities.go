package modelaccess

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// OllamaCapabilities reads the selected installed model's advertised metadata.
// The read-only show request neither loads nor downloads a model. Output limits
// absent from the API stay unknown, even when a context length is reported.
func OllamaCapabilities(ctx context.Context, route Route) (Capabilities, error) {
	if route.Provider != "ollama" || route.Validate() != nil {
		return Capabilities{}, errors.New("invalid Ollama route")
	}
	access, err := NewEndpointAccess(route, os.Getenv(route.KeyEnv), 0)
	if err != nil {
		return Capabilities{}, err
	}
	return ollamaCapabilities(ctx, access)
}

func ollamaCapabilities(ctx context.Context, access *EndpointAccess) (Capabilities, error) {
	// Retain gateway prefixes when replacing the selected Chat API suffix.
	base := strings.TrimSuffix(strings.TrimRight(access.route.Endpoint, "/"), "/v1")
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"model": access.route.Model})
	raw, err := access.read(ctx, "POST", base+"/api/show", body)
	if err != nil {
		return Capabilities{}, err
	}
	var metadata struct {
		Capabilities *[]string                  `json:"capabilities"`
		Info         map[string]json.RawMessage `json:"model_info"`
	}
	if json.Unmarshal(raw, &metadata) != nil {
		return Capabilities{}, errors.New("invalid Ollama metadata")
	}
	c := Capabilities{}
	if metadata.Capabilities != nil {
		reasoning, tools := false, false
		for _, v := range *metadata.Capabilities {
			reasoning = reasoning || v == "thinking"
			tools = tools || v == "tools"
		}
		c.Reasoning, c.ToolCalling = &reasoning, &tools
	}
	for key, raw := range metadata.Info {
		if strings.HasSuffix(key, ".context_length") {
			var limit int
			if json.Unmarshal(raw, &limit) != nil || limit < 1 || limit > 10000000 {
				return Capabilities{}, errors.New("invalid Ollama context length")
			}
			if c.ContextTokens == 0 || limit < c.ContextTokens {
				c.ContextTokens = limit
			}
		}
	}
	return c, nil
}
