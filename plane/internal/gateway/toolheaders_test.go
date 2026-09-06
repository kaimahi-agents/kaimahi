package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// Committed extra_headers reach the tool server. This is the lever
// that narrows a hosted server BEFORE discovery — GitHub's and Azure
// DevOps' both read X-MCP-Toolsets — so a tool the plane does not want
// is never offered and never projected, which no allowlist can achieve
// on its own.
func TestToolUpstreamExtraHeadersReachTheUpstream(t *testing.T) {
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	}))
	defer up.Close()

	credPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(credPath, []byte("upstream-secret\n"), 0o600))
	fs := &fakeStore{credential: store.Credential{Name: "release-agent"}, allow: []string{"list_tags"}}
	h := NewMux(Deps{Store: fs, Upstreams: map[string]config.ToolUpstream{
		"kagent-tools": {
			URL:            up.URL + "/mcp",
			CredentialFile: credPath,
			ExtraHeaders: map[string]string{
				"X-MCP-Toolsets":      "repos,actions",
				"X-MCP-Exclude-Tools": "delete_repository",
			},
		},
	}})

	rec := post(h, goodToken, rpc(t, "tools/call",
		map[string]any{"name": "list_tags", "arguments": map[string]any{"owner": "o", "repo": "r"}}))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "repos,actions", got.Get("X-MCP-Toolsets"))
	require.Equal(t, "delete_repository", got.Get("X-MCP-Exclude-Tools"))
	// The credential is injected LAST and is what the upstream sees; the
	// agent's own kmh_ token never leaves the gateway.
	require.Equal(t, "Bearer upstream-secret", got.Get("Authorization"))
	require.NotContains(t, got.Get("Authorization"), goodToken)
}

// The ordering guarantee, asserted rather than assumed. Load already
// refuses an extra header naming the credential slot; if one ever
// reached the forward anyway, the credential must still win.
func TestExtraHeadersCannotDisplaceTheInjectedCredential(t *testing.T) {
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	}))
	defer up.Close()

	credPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(credPath, []byte("real-secret"), 0o600))
	fs := &fakeStore{credential: store.Credential{Name: "release-agent"}, allow: []string{"list_tags"}}
	h := NewMux(Deps{Store: fs, Upstreams: map[string]config.ToolUpstream{
		"kagent-tools": {
			URL:            up.URL + "/mcp",
			CredentialFile: credPath,
			ExtraHeaders:   map[string]string{"Authorization": "Bearer forged"},
		},
	}})

	rec := post(h, goodToken, rpc(t, "tools/call",
		map[string]any{"name": "list_tags", "arguments": map[string]any{}}))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "Bearer real-secret", got.Get("Authorization"))
}
