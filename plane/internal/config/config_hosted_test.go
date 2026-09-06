package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
)

// The load-time half of the egress rule. A hosted upstream is
// https/443, no userinfo, and marked; an unmarked upstream must look
// in-cluster, so a public hostname can never take the plain dial.
func TestParseHostedUpstreamShape(t *testing.T) {
	base := `{"upstreams": {"o": {"base_url": "http://o", "path": "p", "classification": "free"}}, "tool_upstreams": `
	ok := map[string]string{
		"hosted https":              `{"gh": {"url": "https://api.githubcopilot.com/mcp/", "internet": true}}`,
		"hosted explicit 443":       `{"gh": {"url": "https://api.githubcopilot.com:443/mcp/", "internet": true}}`,
		"hosted with ca_file":       `{"e": {"url": "https://mcp-echo.kaimahi-ci.test/mcp", "internet": true, "ca_file": "/etc/kaimahi/upstream-ca/echo.crt"}}`,
		"hosted public IP literal":  `{"e": {"url": "https://203.0.113.10/mcp", "internet": true}}`,
		"in-cluster service":        `{"t": {"url": "http://kagent-tools:8084/mcp"}}`,
		"in-cluster service.ns":     `{"t": {"url": "http://kagent-tools.kagent:8084/mcp"}}`,
		"in-cluster full suffix":    `{"t": {"url": "http://kagent-tools.kagent.svc.cluster.local:8084/mcp"}}`,
		"in-cluster private IP":     `{"t": {"url": "http://10.96.0.5:8084/mcp"}}`,
		"in-cluster loopback https": `{"t": {"url": "https://127.0.0.1:8443/mcp"}}`,
	}
	for name, good := range ok {
		_, err := config.Parse([]byte(base + good + "}"))
		require.NoError(t, err, name)
	}
	bad := map[string]string{
		"hosted over http":              `{"gh": {"url": "http://api.githubcopilot.com/mcp/", "internet": true}}`,
		"hosted on another port":        `{"gh": {"url": "https://api.githubcopilot.com:8443/mcp/", "internet": true}}`,
		"hosted with userinfo":          `{"gh": {"url": "https://user:pw@api.githubcopilot.com/mcp/", "internet": true}}`,
		"hosted private IP literal":     `{"gh": {"url": "https://10.0.0.1/mcp", "internet": true}}`,
		"hosted metadata IP literal":    `{"gh": {"url": "https://169.254.169.254/mcp", "internet": true}}`,
		"public host without marker":    `{"gh": {"url": "https://api.githubcopilot.com/mcp/"}}`,
		"public IP without marker":      `{"gh": {"url": "https://203.0.113.10/mcp"}}`,
		"ca_file without marker":        `{"t": {"url": "http://kagent-tools.kagent:8084/mcp", "ca_file": "/etc/x"}}`,
		"three-label name without mark": `{"t": {"url": "http://mcp-echo.kaimahi-ci.test/mcp"}}`,
		// `github.com` has the shape of `service.namespace`; over https it
		// is what a public host looks like and must be marked.
		"two-label https without marker": `{"t": {"url": "https://github.com/mcp"}}`,
	}
	for name, raw := range bad {
		_, err := config.Parse([]byte(base + raw + "}"))
		require.Error(t, err, name)
	}
	// The same rules on the LLM table: Copilot without the marker is a
	// public host taking the plain dial, refused at load.
	_, err := config.Parse([]byte(`{"upstreams": {"copilot": {"base_url": "https://api.githubcopilot.com", "path": "chat/completions", "classification": "metered"}}}`))
	require.Error(t, err)
	_, err = config.Parse([]byte(`{"upstreams": {"copilot": {"base_url": "http://api.githubcopilot.com", "path": "chat/completions", "classification": "metered", "internet": true}}}`))
	require.Error(t, err)
}

func TestInternetHostsCoverBothTables(t *testing.T) {
	c, err := config.Parse([]byte(`{
	  "upstreams": {
	    "ollama": {"base_url": "http://ollama.ollama.svc.cluster.local:11434", "path": "p", "classification": "free"},
	    "copilot": {"base_url": "https://api.githubcopilot.com", "path": "chat/completions", "classification": "metered", "internet": true}
	  },
	  "tool_upstreams": {
	    "kagent-tools": {"url": "http://kagent-tools.kagent:8084/mcp"},
	    "github": {"url": "https://api.githubcopilot.com/mcp/", "internet": true},
	    "echo": {"url": "https://MCP-Echo.kaimahi-ci.test/mcp", "internet": true, "ca_file": "/etc/kaimahi/upstream-ca/echo.crt"}
	  }
	}`))
	require.NoError(t, err)
	hosts := c.InternetHosts()
	got := map[string]string{}
	for _, h := range hosts {
		got[h.Name] = h.CAFile
	}
	// One entry per host: Copilot and the GitHub MCP server share a host
	// and a trust decision (system roots); the echo host is lowercased.
	require.Equal(t, map[string]string{"api.githubcopilot.com": "", "mcp-echo.kaimahi-ci.test": "/etc/kaimahi/upstream-ca/echo.crt"}, got)
	require.Len(t, hosts, 2)
}
