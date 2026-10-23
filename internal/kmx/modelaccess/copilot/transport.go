package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

const endpoint = modelaccess.CopilotEndpoint

func headers(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Editor-Version", "vscode/1.95.0")
	req.Header.Set("Editor-Plugin-Version", "copilot-chat/0.26.7")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.26.7")
	req.Header.Set("Copilot-Integration-Id", "copilot-developer-cli")
	req.Header.Set("X-Github-Api-Version", "2026-08-20")
	req.Header.Del("Openai-Intent")
}
func errorCode(b []byte) string {
	var e struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) != nil {
		return ""
	}
	if e.Error.Code != "" {
		return e.Error.Code
	}
	if e.Error.Type != "" {
		return e.Error.Type
	}
	return e.Code
}

// Only these explicit credential refusals authorize a legacy exchange. Unknown
// 403s are policy refusals, never evidence that a token exchange is appropriate.
func credentialRefusal(status int, b []byte) bool {
	return classify(status, b) == ErrAuthRevoked && (status == 401 || status == 403 && errorCode(b) == "unsupported_token")
}
func classify(status int, b []byte) error {
	switch errorCode(b) {
	case "unsupported_token":
		return ErrAuthRevoked
	case "no_copilot_subscription", "no_subscription":
		return ErrSubscription
	case "quota_exceeded", "rate_limit_exceeded":
		return ErrQuota
	case "model_not_found", "model_not_supported":
		return ErrModelUnavailable
	case "policy_denied":
		return ErrPolicy
	}
	switch status {
	case 401:
		return ErrAuthRevoked
	case 402:
		return ErrSubscription
	case 403:
		return ErrPolicy
	case 404:
		return ErrModelUnavailable
	case 429:
		return ErrQuota
	}
	return ErrUnavailable
}
func (p *Provider) exchange(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", p.exchangeURL, nil)
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Authorization", "token "+p.source)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.26.7")
	resp, err := p.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := readBounded(resp.Body, responseLimit)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return classify(resp.StatusCode, b)
	}
	var token struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if json.Unmarshal(b, &token) != nil || !validCredential(token.Token) {
		return ErrMalformed
	}
	expires := time.Unix(token.ExpiresAt, 0)
	if !expires.After(p.now().Add(30 * time.Second)) {
		return ErrAuthRevoked
	}
	p.bearer = token.Token
	p.expires = expires
	return nil
}

type catalogModel struct {
	route modelaccess.Route
	path  string
}

// List returns available Chat or Responses routes without selecting a default
// model or initiating sign-in. Protocol selection stays private to the transport.
func (p *Provider) List(ctx context.Context) ([]modelaccess.Route, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	models, err := p.list(ctx)
	if err != nil {
		return nil, err
	}
	routes := make([]modelaccess.Route, 0, len(models))
	for _, model := range models {
		routes = append(routes, model.route)
	}
	return routes, nil
}
func (p *Provider) list(ctx context.Context) ([]catalogModel, error) {
	if ctx.Err() != nil {
		return nil, safeError(ctx.Err())
	}
	source, err := p.credential(ctx)
	if err != nil {
		p.clear()
		return nil, err
	}
	if !validCredential(source) {
		p.clear()
		return nil, ErrAuthRevoked
	}
	if source != p.source {
		p.clear()
		p.source = source
		p.bearer = source
	}
	if p.legacy && (p.bearer == "" || !p.now().Before(p.expires.Add(-30*time.Second))) {
		if err := p.exchange(ctx); err != nil {
			return nil, err
		}
	}
	b, status, err := p.catalog(ctx)
	if err != nil {
		return nil, err
	}
	if !p.legacy && credentialRefusal(status, b) {
		p.legacy = true
		p.bearer = ""
		if err := p.exchange(ctx); err != nil {
			return nil, err
		}
		b, status, err = p.catalog(ctx)
		if err != nil {
			return nil, err
		}
	}
	if status != 200 {
		return nil, classify(status, b)
	}
	var catalog struct {
		Data *[]struct {
			ID           string `json:"id"`
			Capabilities struct {
				Type string `json:"type"`
			} `json:"capabilities"`
			Policy struct {
				State string `json:"state"`
			} `json:"policy"`
			Endpoints []string `json:"supported_endpoints"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &catalog) != nil || catalog.Data == nil {
		return nil, ErrMalformed
	}
	routes := []catalogModel{}
	seen := map[string]bool{}
	for _, m := range *catalog.Data {
		if m.Capabilities.Type != "chat" || m.Policy.State != "enabled" {
			continue
		}
		path := "/chat/completions"
		if !slices.Contains(m.Endpoints, path) {
			path = "/responses"
			if !slices.Contains(m.Endpoints, path) {
				continue
			}
		}
		r := modelaccess.Route{Provider: "copilot", Model: m.ID, Endpoint: endpoint, Auth: "copilot"}
		if r.Validate() != nil || seen[m.ID] || strings.Contains(m.ID, p.source) || strings.Contains(m.ID, p.bearer) {
			return nil, ErrMalformed
		}
		seen[m.ID] = true
		routes = append(routes, catalogModel{route: r, path: path})
	}
	return routes, nil
}
func (p *Provider) catalog(ctx context.Context) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.api+"/models", nil)
	if err != nil {
		return nil, 0, ErrUnavailable
	}
	headers(req, p.bearer)
	req.Header.Set("Accept", "application/json")
	resp, err := p.do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := readBounded(resp.Body, responseLimit)
	return b, resp.StatusCode, err
}

// Client validates the exact selected route against the catalog and returns a
// Chat-shaped RoundTripper for the owned text preview/judge callers. Chat routes
// pass JSON/SSE through; Responses routes translate privately. The caller owns
// cancellation and response-body closure. There are no inference retries,
// redirects, proxy listeners, model substitutions, or token accessors.
func (p *Provider) Client(ctx context.Context, r modelaccess.Route) (http.RoundTripper, error) {
	if r.Provider != "copilot" || r.Validate() != nil {
		return nil, ErrModelUnavailable
	}
	routes, err := p.List(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := modelaccess.Select(routes, r.Provider, r.Model); err != nil {
		return nil, unavailableWithChoices(routes)
	}
	return &transport{owner: p, route: r}, nil
}

type transport struct {
	owner *Provider
	route modelaccess.Route
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, ErrMalformed
	}
	if req.Body != nil {
		defer req.Body.Close()
	}
	if req.URL == nil || req.Method != "POST" || req.URL.String() != endpoint+"/chat/completions" {
		return nil, ErrModelUnavailable
	}
	if req.Body == nil {
		return nil, ErrMalformed
	}
	b, err := readBounded(req.Body, 4<<20)
	if err != nil {
		return nil, err
	}
	model, err := requestModel(b)
	if err != nil {
		return nil, err
	}
	if model != t.route.Model {
		return nil, ErrModelUnavailable
	}
	p := t.owner
	p.mu.Lock()
	// Establish/refresh via the read-only catalog before inference. Rechecking
	// availability also prevents a retained transport silently changing models.
	models, err := p.list(req.Context())
	token := p.bearer
	path := ""
	if err == nil {
		for _, model := range models {
			if model.route == t.route {
				path = model.path
				break
			}
		}
		if path == "" {
			routes := make([]modelaccess.Route, 0, len(models))
			for _, model := range models {
				routes = append(routes, model.route)
			}
			err = unavailableWithChoices(routes)
		}
	}
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	stream := false
	if path == "/responses" {
		b, stream, err = encodeResponsesRequest(b)
		if err != nil {
			return nil, err
		}
	}
	next := req.Clone(req.Context())
	next.URL, err = url.Parse(p.api + path)
	if err != nil {
		return nil, ErrUnavailable
	}
	next.Host = ""
	next.Body = io.NopCloser(bytes.NewReader(b))
	next.ContentLength = int64(len(b))
	next.GetBody = nil
	// Go's HTTP transport may replay requests marked idempotent after connection
	// failure. Inference is deliberately never marked replayable here.
	next.Header.Del("Idempotency-Key")
	next.Header.Del("X-Idempotency-Key")
	next.Header.Del("Proxy-Authorization")
	if path == "/responses" {
		next.Header.Del("Accept-Encoding")
		next.Header.Del("Content-Encoding")
	}
	headers(next, token)
	next.Header.Set("Content-Type", "application/json")
	next.Header.Set("Openai-Intent", "conversation-panel")
	upstream := p.http.Transport
	if upstream == nil {
		upstream = http.DefaultTransport
	}
	resp, err := upstream.RoundTrip(next)
	if err != nil {
		return nil, safeError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, err := readBounded(resp.Body, responseLimit)
		if err != nil {
			return nil, err
		}
		// A Chat refusal is reported once. Any future auth resolution belongs to
		// the next explicit request, never a resend of this inference body.
		return nil, classify(resp.StatusCode, b)
	}
	resp.Request = req
	if path == "/responses" {
		if err := adaptResponsesResponse(resp, stream); err != nil {
			resp.Body.Close()
			return nil, err
		}
	}
	resp.Body = &safeBody{ReadCloser: resp.Body}
	return resp, nil
}

// Reject ambiguous top-level keys rather than depending on the upstream JSON
// parser's duplicate/case-folding behavior for the caller's selected model.
func requestModel(b []byte) (string, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return "", ErrMalformed
	}
	seen := map[string]bool{}
	model := ""
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return "", ErrMalformed
		}
		key, ok := token.(string)
		if !ok || seen[key] || key != "model" && strings.EqualFold(key, "model") {
			return "", ErrMalformed
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return "", ErrMalformed
		}
		if key == "model" && json.Unmarshal(value, &model) != nil {
			return "", ErrMalformed
		}
	}
	if _, err := d.Token(); err != nil {
		return "", ErrMalformed
	}
	if _, err := d.Token(); err != io.EOF {
		return "", ErrMalformed
	}
	return model, nil
}

type safeBody struct{ io.ReadCloser }

func (b *safeBody) Read(v []byte) (int, error) {
	n, err := b.ReadCloser.Read(v)
	if err != nil && err != io.EOF {
		return n, safeError(err)
	}
	return n, err
}
func (b *safeBody) Close() error {
	if err := b.ReadCloser.Close(); err != nil {
		return safeError(err)
	}
	return nil
}
