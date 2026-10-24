package localtools

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpProtocol = "2025-11-25"

// NewMCPHost snapshots only the explicitly bound environment credential. It does
// no network IO: each actual read owns and closes its own MCP session.
func NewMCPHost(ctx context.Context, b Binding) (*Host, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !b.valid() {
		return nil, errInvalidBinding
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.MaxResponseHeaderBytes = 8192
	base.DisableKeepAlives = true
	return newMCPHost(b, os.Getenv(b.credentialEnv), base)
}

func newMCPHost(b Binding, credential string, base http.RoundTripper) (*Host, error) {
	if credential == "" || base == nil {
		return nil, errInvalidBinding
	}
	for _, c := range credential {
		if c < 33 || c > 126 {
			return nil, errInvalidBinding
		}
	}
	var h *Host
	var err error
	h, err = NewHost(b, credential, func(ctx context.Context) ([]PullRequest, error) { return readMCP(ctx, h, base) })
	return h, err
}

func readMCP(ctx context.Context, h *Host, base http.RoundTripper) (rows []PullRequest, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return nil, errReadFailed
	}
	// Close idle sockets on the underlying transport as well as the protocol
	// session. SDK session contexts detach from Connect's context.
	cleanup := &http.Client{Transport: base}
	defer cleanup.CloseIdleConnections()
	client := mcp.NewClient(&mcp.Implementation{Name: "kmx-local-read", Version: "1"}, &mcp.ClientOptions{
		Logger: slog.New(slog.DiscardHandler), Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	transport := &mcpHTTP{base: base, host: h, ctx: ctx, used: map[string]bool{}}
	session, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpointURL, HTTPClient: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: maxMCPBytes}, &mcp.ClientSessionOptions{ProtocolVersion: mcpProtocol})
	if e != nil {
		return nil, errReadFailed
	}
	defer func() {
		if session.Close() != nil || ctx.Err() != nil {
			rows = nil
			err = errReadFailed
		}
	}()
	if session.InitializeResult().ProtocolVersion != mcpProtocol {
		return nil, errReadFailed
	}
	tools, e := session.ListTools(ctx, nil)
	if e != nil || tools.NextCursor != "" {
		return nil, errReadFailed
	}
	matched := 0
	for _, tool := range tools.Tools {
		if tool.Name == "search_pull_requests" {
			if !compatibleSearchSchema(tool.InputSchema) {
				return nil, errReadFailed
			}
			matched++
		}
	}
	if matched != 1 {
		return nil, errReadFailed
	}
	result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_pull_requests", Arguments: map[string]any{
		"query": "repo:" + h.binding.repository + " is:pr is:merged", "perPage": 20, "page": 1, "fields": []string{"number", "title", "pull_request", "repository_url"}, "sort": "updated", "order": "desc",
	}})
	if e != nil || result == nil || result.IsError || result.NeedsInput() || len(result.InputRequests) != 0 || result.RequestState != "" || result.StructuredContent != nil || len(result.Content) != 1 {
		return nil, errReadFailed
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || h.Check(text.Text) != nil {
		return nil, errReadFailed
	}
	return decodePullRequests([]byte(text.Text), h.binding)
}

// Only the input contract used by our fixed read is accepted. Remote schema
// descriptions and unrelated tool declarations grant no authority.
func compatibleSearchSchema(value any) bool {
	raw, err := json.Marshal(value)
	if err != nil {
		return false
	}
	type property struct {
		Type  string
		Enum  []string
		Items struct {
			Type string
			Enum []string
		}
	}
	var schema struct {
		Type       string
		Required   []string
		Properties map[string]property
	}
	if json.Unmarshal(raw, &schema) != nil || schema.Type != "object" || schema.Properties["query"].Type != "string" {
		return false
	}
	for _, name := range schema.Required {
		if name != "query" {
			return false
		}
	}
	for _, name := range []string{"perPage", "page"} {
		if kind := schema.Properties[name].Type; kind != "number" && kind != "integer" {
			return false
		}
	}
	fields := schema.Properties["fields"]
	if fields.Type != "array" || fields.Items.Type != "string" {
		return false
	}
	for _, name := range []string{"number", "title", "pull_request", "repository_url"} {
		if !slices.Contains(fields.Items.Enum, name) {
			return false
		}
	}
	return schema.Properties["sort"].Type == "string" && slices.Contains(schema.Properties["sort"].Enum, "updated") && schema.Properties["order"].Type == "string" && slices.Contains(schema.Properties["order"].Enum, "desc")
}
