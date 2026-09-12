package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
)

func TestParseValid(t *testing.T) {
	c, err := config.Parse([]byte(`{
	  "upstreams": {
	    "ollama": {"base_url": "http://ollama.ollama.svc:11434", "path": "v1/chat/completions", "classification": "free"},
	    "copilot": {"base_url": "https://api.githubcopilot.com", "path": "chat/completions",
	                "classification": "metered", "credential_file": "/etc/x/token", "internet": true,
	                "prices": {"gpt-5-mini": {"in_cents_per_1m": 25, "out_cents_per_1m": 200}}}
	  }
	}`))
	require.NoError(t, err)
	require.Len(t, c.Upstreams, 2)
	require.Equal(t, config.ClassFree, c.Upstreams["ollama"].Classification)
	require.Equal(t, 25, c.Upstreams["copilot"].Prices["gpt-5-mini"].InCentsPer1M)
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"no upstreams":         `{"upstreams": {}}`,
		"missing class":        `{"upstreams": {"a": {"base_url": "http://x", "path": "v1/chat/completions"}}}`,
		"inferred class":       `{"upstreams": {"a": {"base_url": "http://x", "path": "v1/chat/completions", "classification": "local"}}}`,
		"free with prices":     `{"upstreams": {"a": {"base_url": "http://x", "path": "v1/chat/completions", "classification": "free", "prices": {"m": {"in_cents_per_1m": 1, "out_cents_per_1m": 1}}}}}`,
		"bad base_url":         `{"upstreams": {"a": {"base_url": "not a url", "path": "v1/chat/completions", "classification": "free"}}}`,
		"leading-slash path":   `{"upstreams": {"a": {"base_url": "http://x", "path": "/p", "classification": "free"}}}`,
		"empty path":           `{"upstreams": {"a": {"base_url": "http://x", "path": "", "classification": "free"}}}`,
		"negative price":       `{"upstreams": {"a": {"base_url": "http://x", "path": "v1/chat/completions", "classification": "metered", "prices": {"m": {"in_cents_per_1m": -1, "out_cents_per_1m": 1}}}}}`,
		"unknown field (typo)": `{"upstreams": {"a": {"base_url": "http://x", "path": "v1/chat/completions", "classification": "free", "credental_file": "x"}}}`,
	}
	for name, raw := range cases {
		_, err := config.Parse([]byte(raw))
		require.Error(t, err, name)
	}
}

func TestParseToolUpstreams(t *testing.T) {
	c, err := config.Parse([]byte(`{
	  "upstreams": {"o": {"base_url": "http://o", "path": "v1/chat/completions", "classification": "free"}},
	  "tool_upstreams": {"kagent-tools": {"url": "http://kagent-tools.kagent:8084/mcp"}}
	}`))
	require.NoError(t, err)
	require.Equal(t, "http://kagent-tools.kagent:8084/mcp", c.ToolUpstreams["kagent-tools"].URL)

	// Optional: a config with only LLM upstreams still parses.
	c, err = config.Parse([]byte(`{"upstreams": {"o": {"base_url": "http://o", "path": "v1/chat/completions", "classification": "free"}}}`))
	require.NoError(t, err)
	require.Empty(t, c.ToolUpstreams)

	base := `{"upstreams": {"o": {"base_url": "http://o", "path": "v1/chat/completions", "classification": "free"}}, "tool_upstreams": `
	for name, bad := range map[string]string{
		"empty url":     `{"t": {"url": ""}}`,
		"relative url":  `{"t": {"url": "not-a-url"}}`,
		"non-http":      `{"t": {"url": "ftp://x/mcp"}}`,
		"unknown field": `{"t": {"url": "http://x/mcp", "extra": true}}`,
	} {
		_, err := config.Parse([]byte(base + bad + `}`))
		require.Error(t, err, name)
	}
}

// A tool upstream may carry its OWN credential (the Slack MCP
// server's SLACK_MCP_API_KEY), named — never valued — in the committed
// table, exactly like the LLM upstreams' credential_file.
func TestParseKeyedToolUpstreams(t *testing.T) {
	c, err := config.Parse([]byte(`{
	  "upstreams": {"o": {"base_url": "http://o", "path": "v1/chat/completions", "classification": "free"}},
	  "tool_upstreams": {"slack": {
	    "url": "http://kaimahi-slack-mcp.kaimahi:13080/mcp",
	    "credential_file": "/etc/kaimahi/upstream-creds/slack/mcp-api-key",
	    "credential_header": "Authorization"
	  }}
	}`))
	require.NoError(t, err)
	require.Equal(t, "/etc/kaimahi/upstream-creds/slack/mcp-api-key", c.ToolUpstreams["slack"].CredentialFile)
	require.Equal(t, "Authorization", c.ToolUpstreams["slack"].CredentialHeader)

	base := `{"upstreams": {"o": {"base_url": "http://o", "path": "v1/chat/completions", "classification": "free"}}, "tool_upstreams": `
	for name, bad := range map[string]string{
		// A header with no file would silently forward bare — the
		// confusing direction of fail-open. Reject at load.
		"header without file": `{"t": {"url": "http://x/mcp", "credential_header": "Authorization"}}`,
		"malformed header":    `{"t": {"url": "http://x/mcp", "credential_file": "/f", "credential_header": "X Api Key"}}`,
		// Key material never belongs in the committed table.
		"inline credential": `{"t": {"url": "http://x/mcp", "credential": "xoxb-secret"}}`,
	} {
		_, err := config.Parse([]byte(base + bad + `}`))
		require.Error(t, err, name)
	}
}

// PathProtocol matches whole segments. A suffix match would read
// `v1/xresponses` as the Responses API — guessing a protocol for a path
// that names none, which is the one thing it exists to stop. The
// scaffolder carries a copy of this rule and the same table
// (internal/kmx/scaffold/model_test.go).
func TestPathProtocolMatchesSegmentsNotSuffixes(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"v1/chat/completions", config.ProtocolChatCompletions},
		{"/v1/chat/completions/", config.ProtocolChatCompletions},
		{"openai/deployments/gpt/chat/completions", config.ProtocolChatCompletions},
		{"chat/completions", config.ProtocolChatCompletions},
		{"v1/responses", config.ProtocolResponses},
		{"responses", config.ProtocolResponses},
		{"api/generate", ""},
		{"", ""},
		{"v1/xresponses", ""},
		{"v1/notchat/completions", ""},
		{"v1/responsesx", ""},
	} {
		if got := config.PathProtocol(tc.path); got != tc.want {
			t.Fatalf("PathProtocol(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
	// And the consequence at load: a path naming no protocol must demand
	// a declaration rather than being handed one.
	_, err := config.Parse([]byte(`{"upstreams": {"a": {"base_url": "http://x", "path": "v1/xresponses", "classification": "free"}}}`))
	if err == nil || !strings.Contains(err.Error(), "names no protocol") {
		t.Fatalf("want a demand for an explicit protocol, got: %v", err)
	}
}
