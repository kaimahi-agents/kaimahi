// Package adapter is a deliberately small, trusted cluster-local HTTP-to-MCP
// bridge. Only configured destinations and remote tool names can be invoked.
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	invocationTimeout = 45 * time.Second
	maxRequestBytes   = 1 << 20
	maxResponseBytes  = 4 << 20
)

type Adapter struct {
	routes     map[string]route
	secretsDir string
	transport  *http.Transport
}

func (a *Adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Exact whitelist: no redirecting/cleaning paths or accepting encoded aliases.
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.EscapedPath() != r.URL.Path {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		return
	}
	route, ok := a.routes[strings.TrimPrefix(r.URL.Path, "/tools/")]
	if !strings.HasPrefix(r.URL.Path, "/tools/") || !ok {
		http.NotFound(w, r)
		return
	}
	// Orka Tool reconciliation probes HEAD; this checks only local readiness.
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), invocationTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = http.NewResponseController(w).SetReadDeadline(deadline)
	defer http.NewResponseController(w).SetReadDeadline(time.Time{})
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		status := http.StatusBadRequest
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "invalid request body", status)
		return
	}
	if err := validateObject(body); err != nil {
		http.Error(w, "expected one JSON object with unique keys", http.StatusBadRequest)
		return
	}
	headers, err := a.headers(route)
	if err != nil {
		http.Error(w, "secret resolution failed", http.StatusInternalServerError)
		return
	}
	httpClient := &http.Client{
		Transport:     &invocationTransport{base: a.transport, headers: headers, invocation: ctx},
		Timeout:       invocationTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") },
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "kagent-shim-spike", Version: "0.1.0"}, &mcp.ClientOptions{
		Capabilities:   &mcp.ClientCapabilities{},
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
		// Nil SDK loggers discard: upstream diagnostics may contain secrets.
	})
	transport := &closingTransport{StreamableClientTransport: &mcp.StreamableClientTransport{
		Endpoint: route.URL, HTTPClient: httpClient,
		MaxRetries: -1, DisableStandaloneSSE: true,
	}}
	defer transport.Close()
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		upstreamError(w, ctx, "MCP initialization failed")
		return
	}
	defer session.Close()
	// One attempt only: transport failure may follow a completed write tool.
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: route.Name, Arguments: json.RawMessage(body)})
	if err != nil {
		upstreamError(w, ctx, "MCP tool call failed")
		return
	}
	if result.IsError {
		upstreamError(w, ctx, "MCP tool returned an error")
		return
	}
	if result.NeedsInput() {
		upstreamError(w, ctx, "MCP tool requires unsupported interaction")
		return
	}
	// Metadata may be client-only; audience annotations need a projection we
	// do not implement. Refuse rather than exposing them to Orka's model.
	for key := range result.Meta {
		if key != mcp.MetaKeyServerInfo {
			upstreamError(w, ctx, "unsupported MCP result metadata")
			return
		}
	}
	// Newer negotiated protocols attach serverInfo to every stateless result.
	// It identifies the MCP peer, not model-visible tool output.
	result.Meta = nil
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok || len(text.Meta) != 0 || text.Annotations != nil {
			upstreamError(w, ctx, "unsupported MCP content kind or metadata")
			return
		}
	}
	if unsafeStructuredNumber(result.StructuredContent) {
		upstreamError(w, ctx, "structured MCP number exceeds safe precision")
		return
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > maxResponseBytes {
		upstreamError(w, ctx, "invalid MCP result")
		return
	}
	// Keep text and structuredContent, not just concatenated text.
	// Orka's HTTP executor passes this JSON to the model.
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// The SDK decodes structured JSON numbers as float64. Refuse the unsafe
// integer boundary too: 2^53+1 may already have rounded down to 2^53.
func unsafeStructuredNumber(value any) bool {
	switch v := value.(type) {
	case float64:
		return v >= 1<<53 || v <= -(1<<53)
	case map[string]any:
		for _, child := range v {
			if unsafeStructuredNumber(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if unsafeStructuredNumber(child) {
				return true
			}
		}
	}
	return false
}

func upstreamError(w http.ResponseWriter, ctx context.Context, stage string) {
	status := http.StatusBadGateway
	if ctx.Err() != nil {
		status = http.StatusGatewayTimeout
	}
	http.Error(w, stage, status)
}
