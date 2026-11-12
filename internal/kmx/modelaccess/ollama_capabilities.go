package modelaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
)

// OllamaCapabilities reads the selected installed model's advertised metadata.
// The read-only show request neither loads nor downloads a model. Output limits
// absent from the API stay unknown, even when a context length is reported.
func OllamaCapabilities(ctx context.Context, route Route) (Capabilities, error) {
	if route.Provider != "ollama" || route.Validate() != nil {
		return Capabilities{}, errors.New("invalid Ollama route")
	}
	base, err := cleanBase(route.Endpoint)
	if err != nil {
		return Capabilities{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"model": route.Model})
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/api/show", bytes.NewReader(body))
	if err != nil {
		return Capabilities{}, errors.New("Ollama metadata unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	version.SetUserAgent(req)
	client := &http.Client{Timeout: probeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Capabilities{}, ctx.Err()
		}
		return Capabilities{}, errors.New("Ollama metadata unavailable")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, probeLimit+1))
	if ctx.Err() != nil {
		return Capabilities{}, ctx.Err()
	}
	if err != nil || len(raw) > probeLimit || resp.StatusCode != 200 || !agentruntime.JSONKeysUnique(raw) {
		return Capabilities{}, errors.New("invalid Ollama metadata")
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
