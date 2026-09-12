package config_test

// The client's half of a translating seam, at load.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
)

// Each refusal below is a declaration nothing could honour, and the
// alternative to refusing it here is a seam that accepts a request and
// forwards it in a shape the endpoint cannot read — which is the failure
// the translation exists to remove, reintroduced from the config file.
func TestATranslatingUpstreamIsRefusedUnlessItIsThePairingThisPlaneCanTranslate(t *testing.T) {
	entry := func(clientPath, path string) []byte {
		return []byte(`{"upstreams": {"orka": {"base_url": "http://orka-api.orka-system:8080/openai",` +
			`"path": "` + path + `", "client_path": "` + clientPath + `", "classification": "free"}}}`)
	}
	// The one pairing that works, and the two facts it turns on.
	c, err := config.Parse(entry("v1/responses", "v1/chat/completions"))
	require.NoError(t, err)
	require.True(t, c.Upstreams["orka"].Translates())
	require.Equal(t, "v1/responses", c.Upstreams["orka"].AcceptedPath())

	for _, tc := range []struct{ name, clientPath, path, says string }{
		{"a client path naming no protocol", "v1/generate", "v1/chat/completions", "names no protocol"},
		{"both halves the same protocol", "chat/completions", "v1/chat/completions", "nothing to translate"},
		{"the pairing backwards", "v1/chat/completions", "v1/responses", "and no other pairing"},
		{"a leading slash", "/v1/responses", "v1/chat/completions", "no leading slash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Parse(entry(tc.clientPath, tc.path))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.says)
		})
	}

	// An upstream with no client_path is what every upstream was before
	// this field existed: one path, and nothing translated.
	plain, err := config.Parse([]byte(`{"upstreams": {"ollama": {"base_url": "http://ollama.ollama:11434",` +
		`"path": "v1/chat/completions", "classification": "free"}}}`))
	require.NoError(t, err)
	require.False(t, plain.Upstreams["ollama"].Translates())
	require.Equal(t, "v1/chat/completions", plain.Upstreams["ollama"].AcceptedPath())
}

// Model extra headers are applied after the custody-held credential;
// the load-time check must prevent them from displacing it.
func TestACommittedModelHeaderMayNotDisplaceTheInjectedCredential(t *testing.T) {
	table := func(header string) []byte {
		return []byte(`{"upstreams": {"orka": {"base_url": "http://orka-api.orka-system:8080/openai",` +
			`"path": "v1/chat/completions", "classification": "free",` +
			`"credential_file": "/etc/kaimahi/upstream-creds/orka/token",` +
			`"extra_headers": {"` + header + `": "disabled"}}}}`)
	}
	for _, header := range []string{"Authorization", "authorization", "AUTHORIZATION"} {
		_, err := config.Parse(table(header))
		require.Error(t, err, header)
		require.Contains(t, err.Error(), "would displace the injected credential")
	}
	// The other half of the check: an upstream whose credential goes in a
	// header of its own is guarded on THAT header, not only on
	// Authorization. Without this case the credential_header clause could
	// be deleted and every assertion above would still pass.
	keyed := []byte(`{"upstreams": {"vendor": {"base_url": "http://vendor.demo:8000",` +
		`"path": "v1/chat/completions", "classification": "free",` +
		`"credential_file": "/etc/kaimahi/upstream-creds/vendor/api-key",` +
		`"credential_header": "X-Api-Key",` +
		`"extra_headers": {"x-api-KEY": "not the credential"}}}}`)
	_, err := config.Parse(keyed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "would displace the injected credential")
	_, err = config.Parse(table("X-Orka-Tools"))
	require.NoError(t, err)
	_, err = config.Parse(table("not a header name"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid extra header name")
}
