package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/gateway"
	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
)

// ONE hardened client for BOTH seams. The proxy's Copilot path and
// the gateway's hosted tool upstreams must hold the very same client,
// and it is a hardened one (its transport refuses plain http before
// touching the network).
func TestBothSeamsShareTheOneHardenedClient(t *testing.T) {
	cfg, err := config.Parse([]byte(`{
	  "upstreams": {
	    "ollama": {"base_url": "http://ollama.ollama.svc.cluster.local:11434", "path": "v1/chat/completions", "classification": "free"},
	    "copilot": {"base_url": "https://api.githubcopilot.com", "path": "chat/completions", "classification": "metered", "internet": true}
	  },
	  "tool_upstreams": {
	    "kagent-tools": {"url": "http://kagent-tools.kagent:8084/mcp"},
	    "github": {"url": "https://api.githubcopilot.com/mcp/", "internet": true}
	  }
	}`))
	require.NoError(t, err)
	// Boot-time vetting resolves the real host; offline, that is a
	// warning, not a refusal, and the client is still built.
	client, err := hardenedClient(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, client)
	pd, gd := wireInternet(proxy.Deps{Config: cfg}, gateway.Deps{Upstreams: cfg.ToolUpstreams}, client)
	require.Same(t, client, pd.InternetClient)
	require.Same(t, client, gd.InternetClient)
	require.Same(t, pd.InternetClient, gd.InternetClient)
	_, err = client.Get("http://api.githubcopilot.com/")
	require.Error(t, err, "the shared client is the hardened one: plain http is refused")
}

// An internet upstream whose host is a private address never gets a
// client: the config is refused at load, loudly.
func TestHardenedClientRefusesAPrivateHostAtLoad(t *testing.T) {
	cfg, err := config.Parse([]byte(`{
	  "upstreams": {"o": {"base_url": "http://o", "path": "v1/chat/completions", "classification": "free"}},
	  "tool_upstreams": {"inside": {"url": "https://localhost/mcp", "internet": true}}
	}`))
	require.NoError(t, err, "the shape check cannot know what a name resolves to; the boot-time vet can")
	_, err = hardenedClient(context.Background(), cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "refused at config load")
}
