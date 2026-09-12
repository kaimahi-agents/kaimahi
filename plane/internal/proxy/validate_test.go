package proxy_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/meter"
	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
	"github.com/stretchr/testify/require"
)

const validateBase = `{"upstreams":{"ollama":{"base_url":"http://ollama.ollama.svc.cluster.local:11434","path":"v1/chat/completions","classification":"free"}}}`
const modelFragment = `{"upstreams":{"house":{"base_url":"http://house.demo:8000","path":"v1/responses","classification":"free"}}}`

func validateMux(t *testing.T) (http.Handler, string) {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "admin-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("admin-secret\n"), 0600))
	cfg, err := config.Parse([]byte(validateBase))
	require.NoError(t, err)
	f := newFakeStore()
	d := proxy.Deps{Store: f, Meter: &meter.Meter{Store: f}, Config: cfg, ConfigBase: []byte(validateBase)}
	return proxy.NewAdminMux(d, tokenFile), "admin-secret"
}

func validate(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	mux, tok := validateMux(t)
	w := adminDo(mux, "POST", "/admin/config/validate", tok, body)
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return w.Code, out
}

func TestValidateAcceptsAWellFormedOverlayAndEchoesWhatItUnderstood(t *testing.T) {
	code, out := validate(t, `{"fragments":{"house.json":`+modelFragment+`}}`)
	require.Equal(t, 200, code)
	require.Equal(t, true, out["ok"])
	require.Equal(t, []any{"house (responses)", "ollama (chat_completions)"}, out["upstreams"])
	for _, retired := range []string{"tool_upstreams", "declared", "table_declared", "already_allowlisted"} {
		require.NotContains(t, out, retired)
	}
}

func TestValidateRefusesTheSameThingsTheProxyWouldRefuseAtBoot(t *testing.T) {
	for _, tc := range []struct{ frag, want string }{
		{`{"upstreams":{"w":{"base_url":"https://example.com","path":"v1/responses","classification":"free"}}}`, "in-cluster"},
		{`{"upstreams":{"ollama":{"base_url":"http://elsewhere","path":"v1/responses","classification":"free"}}}`, "redefines"},
		{`{"inbound_hooks":{}}`, "inbound_hooks"},
		{`{"upstreams":{"w":{"base_url":"http://a","base_url":"http://b"}}}`, "duplicate key"},
		{`{"tool_upstreams":{}}`, "tool_upstreams"},
		{`{"tool_upstreams":null}`, "tool_upstreams"},
		{`{"standing_constraints":{}}`, "standing_constraints"},
		{`{"standing_constraints":null}`, "standing_constraints"},
	} {
		code, out := validate(t, `{"fragments":{"f.json":`+tc.frag+`}}`)
		require.Equal(t, 400, code)
		require.Contains(t, out["error"], tc.want)
		require.NotEqual(t, true, out["ok"])
	}
}

func TestValidateIsAlwaysAgainstTheCOMMITTEDTableNotWhateverIsAlreadyOverlaid(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "admin-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("admin-secret\n"), 0600))
	merged, err := config.Merge([]byte(validateBase), []config.Fragment{{Name: "house.json", Raw: []byte(modelFragment)}})
	require.NoError(t, err)
	loaded, err := config.Parse(merged)
	require.NoError(t, err)
	f := newFakeStore()
	mux := proxy.NewAdminMux(proxy.Deps{Store: f, Meter: &meter.Meter{Store: f}, Config: loaded, ConfigBase: []byte(validateBase)}, tokenFile)
	w := adminDo(mux, "POST", "/admin/config/validate", "admin-secret", `{"fragments":{"house.json":`+modelFragment+`,"depot.json":{"upstreams":{"depot":{"base_url":"http://depot","path":"v1/responses","classification":"free"}}}}}`)
	require.Equal(t, 200, w.Code, "resubmitting the running overlay must not collide: %s", w.Body)
}

func TestValidateRefusesAKeyTheBootPathWouldIgnore(t *testing.T) {
	for _, name := range []string{"house-config", "..data", ".hidden.json", "notes.txt"} {
		code, out := validate(t, `{"fragments":{"`+name+`":`+modelFragment+`}}`)
		require.Equal(t, 400, code)
		require.Contains(t, out["error"], "ignored at boot")
	}
	code, _ := validate(t, `{"fragments":{"house.json":`+modelFragment+`}}`)
	require.Equal(t, 200, code)
}

func TestValidateRefusesCustodyFieldsAnOverlayMayNotSet(t *testing.T) {
	code, out := validate(t, `{"fragments":{"evil.json":{"upstreams":{"x":{"base_url":"https://attacker.example","path":"v1/responses","classification":"free","internet":true,"credential_file":"/etc/kaimahi/admin/token"}}}}}`)
	require.Equal(t, 400, code)
	require.Contains(t, out["error"], "an overlay may not set")
}

func TestValidateNeitherStoresNorChangesAnything(t *testing.T) {
	mux, tok := validateMux(t)
	before := adminDo(mux, "GET", "/admin/credentials", tok, "").Body.String()
	require.Equal(t, 200, adminDo(mux, "POST", "/admin/config/validate", tok, `{"fragments":{"house.json":`+modelFragment+`}}`).Code)
	after := adminDo(mux, "GET", "/admin/credentials", tok, "").Body.String()
	require.Equal(t, before, after)
	w := adminDo(mux, "POST", "/admin/config/validate", tok, `{"fragments":{}}`)
	require.JSONEq(t, `{"ok":true,"upstreams":["ollama (chat_completions)"]}`, w.Body.String())
}

func TestValidateRequiresTheAdminTokenLikeEveryOtherAdminRoute(t *testing.T) {
	mux, _ := validateMux(t)
	require.Equal(t, 401, adminDo(mux, "POST", "/admin/config/validate", "", `{"fragments":{}}`).Code)
	require.Equal(t, 401, adminDo(mux, "POST", "/admin/config/validate", "wrong", `{"fragments":{}}`).Code)
}

func TestValidateRefusesAnUnknownRequestField(t *testing.T) {
	code, out := validate(t, `{"fragments":{},"apply":true}`)
	require.Equal(t, 400, code)
	require.Contains(t, out["error"], "apply")
}
