package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// Configuration contains routing metadata only. Tokens remain host-side and are
// refreshed from Azure CLI credentials; no Azure key/token enters Kubernetes.
type foundryChatConfig struct {
	Endpoint   string `json:"endpoint"`
	Deployment string `json:"deployment"`
	Tenant     string `json:"tenant,omitempty"`
	Audience   string `json:"audience,omitempty"`
}

func (c foundryChatConfig) validate() error {
	if c.Audience != "" && c.Audience != "https://cognitiveservices.azure.com/.default" && c.Audience != "https://ai.azure.com/.default" {
		return fmt.Errorf("unsupported Foundry token audience")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" {
		return fmt.Errorf("use an HTTPS Foundry resource endpoint")
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".openai.azure.com") && !strings.HasSuffix(host, ".services.ai.azure.com") {
		return fmt.Errorf("endpoint must be an Azure OpenAI or Foundry resource host")
	}
	if u.Path != "" && u.Path != "/" && strings.TrimSuffix(u.Path, "/") != "/openai/v1" {
		return fmt.Errorf("use the resource root or /openai/v1 endpoint")
	}
	if strings.TrimSpace(c.Deployment) == "" || strings.ContainsAny(c.Deployment, "\r\n") {
		return fmt.Errorf("deployment name is required")
	}
	return nil
}

func foundryChatConfigPath() (string, error) {
	dir, err := config.StateDir()
	return filepath.Join(dir, "foundry-inference.json"), err
}
func loadFoundryChatConfig() (foundryChatConfig, error) {
	var c foundryChatConfig
	path, err := foundryChatConfigPath()
	if err != nil {
		return c, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	return c, c.validate()
}
func saveFoundryChatConfig(c foundryChatConfig) error {
	if err := c.validate(); err != nil {
		return err
	}
	path, err := foundryChatConfigPath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateAgentFile(path, raw)
}

type foundryChatClient struct {
	config     foundryChatConfig
	credential azcore.TokenCredential
	http       *http.Client
	mu         sync.Mutex
	token      azcore.AccessToken
}

func newFoundryChatClient(c foundryChatConfig) (*foundryChatClient, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	credential, err := azidentity.NewAzureCLICredential(&azidentity.AzureCLICredentialOptions{TenantID: c.Tenant})
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &foundryChatClient{config: c, credential: credential, http: &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *foundryChatClient) bearer(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token.Token != "" && time.Until(c.token.ExpiresOn) > 2*time.Minute {
		return c.token.Token, nil
	}
	audience := c.config.Audience
	if audience == "" {
		audience = "https://cognitiveservices.azure.com/.default"
	}
	token, err := c.credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{audience}})
	if err != nil {
		return "", fmt.Errorf("Foundry sign-in failed; run az login and check the selected tenant")
	}
	c.token = token
	return token.Token, nil
}

type foundryToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type foundryMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content"`
	ToolCalls  []foundryToolCall `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
}

func (c *foundryChatClient) complete(ctx context.Context, messages []foundryMessage, tools []copilotTool) (foundryMessage, error) {
	var empty foundryMessage
	token, err := c.bearer(ctx)
	if err != nil {
		return empty, err
	}
	request := map[string]any{"model": c.config.Deployment, "messages": messages, "max_completion_tokens": 1024}
	if len(tools) > 0 {
		var functions []any
		for _, tool := range tools {
			functions = append(functions, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters}})
		}
		request["tools"] = functions
		request["parallel_tool_calls"] = false
	}
	body, err := json.Marshal(request)
	if err != nil {
		return empty, err
	}
	if len(body) > 128<<10 {
		return empty, fmt.Errorf("Foundry turn exceeds 128 KiB")
	}
	endpoint := strings.TrimSuffix(c.config.Endpoint, "/")
	if !strings.HasSuffix(endpoint, "/openai/v1") {
		endpoint += "/openai/v1"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return empty, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return empty, fmt.Errorf("Foundry request failed; check host network access or cancellation")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == 401 {
			c.mu.Lock()
			c.token = azcore.AccessToken{}
			c.mu.Unlock()
		}
		return empty, fmt.Errorf("Foundry HTTP %d; check inference role, deployment compatibility and quota (no automatic replay)", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return empty, fmt.Errorf("Foundry response unreadable or too large")
	}
	var response struct {
		Choices []struct {
			Message foundryMessage `json:"message"`
			Finish  string         `json:"finish_reason"`
		}
	}
	if err = json.Unmarshal(raw, &response); err != nil || len(response.Choices) != 1 {
		return empty, fmt.Errorf("invalid Foundry completion")
	}
	choice := response.Choices[0]
	if choice.Finish != "stop" && choice.Finish != "tool_calls" {
		return empty, fmt.Errorf("Foundry completion ended with %s", safeTerminal(choice.Finish))
	}
	if strings.Contains(choice.Message.Content, token) || strings.Contains(safeTerminal(choice.Message.Content), token) {
		return empty, fmt.Errorf("refusing credential material in model response")
	}
	return choice.Message, nil
}

func (b *orkaChatBackend) foundryTurn(ctx context.Context, instructions, message string, tools []copilotTool, r *chatRenderer) (string, error) {
	return runFoundryTurn(ctx, b.app.foundryClient, instructions, message, tools, b.executeCopilotTool, r, b.agent)
}

func runFoundryTurn(ctx context.Context, client *foundryChatClient, instructions, message string, tools []copilotTool, execute hostToolExecutor, r *chatRenderer, agent string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("configure Foundry with /inference-foundry")
	}
	messages := []foundryMessage{{Role: "system", Content: instructions + "\nAnswer concisely. Use registered tools for live facts; never invent tool output."}, {Role: "user", Content: message}}
	started := time.Now()
	var modelTime, toolTime time.Duration
	seen := map[string]bool{}
	toolCalls := 0
	for step := 0; step < 8; step++ {
		s := time.Now()
		reply, err := client.complete(ctx, messages, tools)
		modelTime += time.Since(s)
		if err != nil {
			return "", err
		}
		if len(reply.ToolCalls) == 0 {
			if strings.TrimSpace(reply.Content) == "" {
				return "", fmt.Errorf("Foundry returned no answer")
			}
			if r.verboseEnabled() {
				r.assistantOperation(agent, "TIMING", "", colorBlue, fmt.Sprintf("Foundry model/auth/network: %s; tools: %s; turn: %s", modelTime.Round(time.Millisecond), toolTime.Round(time.Millisecond), time.Since(started).Round(time.Millisecond)))
			}
			return safeTerminal(reply.Content), nil
		}
		messages = append(messages, reply)
		if step == 7 {
			return "", fmt.Errorf("Foundry exceeded the eight-step tool loop limit before another tool execution")
		}
		for _, call := range reply.ToolCalls {
			if call.ID == "" || call.Type != "function" || seen[call.ID] {
				return "", fmt.Errorf("invalid Foundry tool call")
			}
			seen[call.ID] = true
			toolCalls++
			if toolCalls > 8 {
				return "", fmt.Errorf("Foundry exceeded the eight-tool-call limit")
			}
			var selected *copilotTool
			for i := range tools {
				if tools[i].Name == call.Function.Name {
					selected = &tools[i]
					break
				}
			}
			if selected == nil {
				return "", fmt.Errorf("Foundry requested an unregistered tool")
			}
			var args any
			if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || selected.schema.Validate(args) != nil {
				return "", fmt.Errorf("Foundry tool arguments failed schema validation")
			}
			r.assistantOperation(agent, "TOOL CALL", selected.Name, colorBlue, "Executing registered HTTP tool")
			s = time.Now()
			result, err := execute(ctx, *selected, []byte(call.Function.Arguments))
			toolTime += time.Since(s)
			if err != nil {
				return "", err
			}
			if len(result) > 32<<10 {
				return "", fmt.Errorf("tool response exceeds 32 KiB")
			}
			messages = append(messages, foundryMessage{Role: "tool", Content: string(result), ToolCallID: call.ID})
		}
	}
	return "", fmt.Errorf("Foundry exceeded the eight-step tool loop limit")
}
