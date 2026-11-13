package modelaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
)

var errEndpoint = errors.New("model endpoint request failed; no retry was attempted")

// EndpointAccess implements the shared invocation access interface for explicit
// OpenAI-compatible and Anthropic Messages endpoints. Its credential is private
// memory only; callers pass it through an owned pipe to resident workers.
type EndpointAccess struct {
	route  Route
	key    string
	output int
	next   http.RoundTripper
}

var _ PreviewAccess = (*EndpointAccess)(nil)

func NewEndpointAccess(route Route, key string, output int) (*EndpointAccess, error) {
	if err := route.ValidateEndpoint(); err != nil {
		return nil, err
	}
	if output < 0 || output > 10000000 {
		return nil, errors.New("invalid endpoint route")
	}
	if (route.KeyEnv != "") != (key != "") || len(key) > 32768 || !utf8.ValidString(key) || strings.ContainsAny(key, "\r\n\x00") {
		return nil, errors.New("model endpoint credential is missing or invalid")
	}
	if output == 0 {
		output = 1024
	}
	// DefaultTransport owns the shared pool; authentication stays on each request.
	access := &EndpointAccess{route: route, key: key, output: output, next: http.DefaultTransport}
	if err := access.CheckContent(route.Model, route.Endpoint, route.KeyEnv); err != nil {
		return nil, err
	}
	return access, nil
}

func (a *EndpointAccess) CheckContent(values ...string) error {
	for _, value := range values {
		if !utf8.ValidString(value) || a.key != "" && strings.Contains(value, a.key) || secretshapes.Match(value) != nil {
			return errors.New("model content withheld: credential-shaped or invalid content")
		}
	}
	return nil
}
func (a *EndpointAccess) Validate(ctx context.Context) error { return ctx.Err() }
func (a *EndpointAccess) Client(route Route) (http.RoundTripper, error) {
	if route != a.route {
		return nil, errors.New("model route changed")
	}
	return a, nil
}

func (a *EndpointAccess) checkJSON(raw []byte) error {
	if !agentruntime.JSONKeysUnique(raw) {
		return errEndpoint
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return errEndpoint
	}
	var walk func(any) error
	walk = func(value any) error {
		switch v := value.(type) {
		case string:
			return a.CheckContent(v)
		case []any:
			for _, item := range v {
				if err := walk(item); err != nil {
					return err
				}
			}
		case map[string]any:
			for k, item := range v {
				if err := a.CheckContent(k); err != nil {
					return err
				}
				if err := walk(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}

func (a *EndpointAccess) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != "POST" || req.URL.String() != strings.TrimRight(a.route.Endpoint, "/")+"/chat/completions" {
		return nil, errEndpoint
	}
	defer req.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(req.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || a.checkJSON(raw) != nil {
		return nil, errEndpoint
	}
	var body struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Stream bool `json:"stream"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || body.Model != a.route.Model || body.Stream || len(body.Messages) == 0 {
		return nil, errEndpoint
	}
	payload := map[string]any{"model": body.Model, "messages": body.Messages, "stream": false}
	url := req.URL.String()
	if a.route.Provider == "anthropic" {
		if body.Messages[0].Role != "system" || len(body.Messages) < 2 {
			return nil, errEndpoint
		}
		payload["system"] = body.Messages[0].Content
		payload["messages"] = body.Messages[1:]
		payload["max_tokens"] = a.output
		url = strings.TrimRight(a.route.Endpoint, "/") + "/messages"
	} else if a.route.Provider == "ollama" || a.route.Provider == "foundry-local" || a.route.Provider == "compatible" {
		payload["max_tokens"] = a.output
	} else {
		payload["max_completion_tokens"] = a.output
	}
	raw, err = json.Marshal(payload)
	if err != nil || len(raw) > 1<<20 {
		return nil, errEndpoint
	}
	raw, err = a.read(req.Context(), "POST", url, raw)
	if err != nil {
		return nil, err
	}
	if a.route.Provider == "anthropic" {
		var answer struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Model   string `json:"model"`
			Stop    string `json:"stop_reason"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &answer) != nil || answer.Type != "message" || answer.Role != "assistant" || answer.Model != a.route.Model || answer.Stop != "end_turn" {
			return nil, errEndpoint
		}
		var text strings.Builder
		for _, part := range answer.Content {
			switch part.Type {
			case "text":
				text.WriteString(part.Text)
			case "thinking", "redacted_thinking": // Private reasoning is never answer content.
			default:
				return nil, errEndpoint
			}
		}
		if text.Len() == 0 {
			return nil, errEndpoint
		}
		raw, _ = json.Marshal(map[string]any{"model": answer.Model, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": text.String()}}}})
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw)), Request: req}, nil
}

// read performs one bounded request to the selected origin. Fixed callers own
// path selection; redirects, raw service errors, and credential echoes refuse.
func (a *EndpointAccess) read(ctx context.Context, method, target string, body []byte) ([]byte, error) {
	origin, _ := url.Parse(a.route.Endpoint)
	u, err := url.Parse(target)
	if err != nil || u.Scheme != origin.Scheme || u.Host != origin.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(body) > 1<<20 {
		return nil, errEndpoint
	}
	if len(body) > 0 && a.checkJSON(body) != nil {
		return nil, errEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, errEndpoint
	}
	req.Header.Set("Content-Type", "application/json")
	version.SetUserAgent(req)
	if a.route.Provider == "anthropic" {
		req.Header.Set("anthropic-version", "2023-06-01")
		if a.key != "" {
			req.Header.Set("x-api-key", a.key)
		}
	} else if a.key != "" {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	resp, err := a.next.RoundTrip(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errEndpoint
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errEndpoint
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(raw) > 1<<20 || a.checkJSON(raw) != nil {
		return nil, errEndpoint
	}
	return raw, nil
}

// Probe checks access to the exact selected model without sending inference.
// This gates replacement readiness, not a claim that a later turn will succeed.
func (a *EndpointAccess) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	endpoint := strings.TrimRight(a.route.Endpoint, "/") + "/models"
	if a.route.Provider == "anthropic" {
		endpoint += "/" + url.PathEscape(a.route.Model)
	}
	raw, err := a.read(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	if a.route.Provider == "anthropic" {
		var model struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &model) != nil || model.Type != "model" || model.ID != a.route.Model {
			return errors.New("selected endpoint model is unavailable")
		}
		return nil
	}
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &models) != nil {
		return errors.New("invalid endpoint model inventory")
	}
	matches := 0
	for _, model := range models.Data {
		if model.ID == a.route.Model {
			matches++
		}
	}
	if matches != 1 {
		return errors.New("selected endpoint model is unavailable or ambiguous")
	}
	return nil
}
