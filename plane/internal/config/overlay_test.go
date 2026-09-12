package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const overlayBase = `{"upstreams":{"ollama":{"base_url":"http://ollama.ollama.svc.cluster.local:11434","path":"v1/chat/completions","classification":"free"}}}`

func mergeParse(t *testing.T, frags ...Fragment) (Config, error) {
	t.Helper()
	merged, err := Merge([]byte(overlayBase), frags)
	if err != nil {
		return Config{}, err
	}
	return Parse(merged)
}

func TestTwoOverlaysMayNotDefineTheSameName(t *testing.T) {
	_, err := mergeParse(t,
		Fragment{Name: "a.json", Raw: []byte(`{"upstreams":{"house":{"base_url":"http://a","path":"v1/responses","classification":"free"}}}`)},
		Fragment{Name: "b.json", Raw: []byte(`{"upstreams":{"house":{"base_url":"http://b","path":"v1/responses","classification":"free"}}}`)},
	)
	require.ErrorContains(t, err, "overlay a.json")
	require.ErrorContains(t, err, "b.json")
}

func TestAnOverlayMayNotSetTheSeamsItDoesNotOwn(t *testing.T) {
	for _, block := range []string{"inbound_hooks", "approval_notifier", "tool_upstreams", "standing_constraints"} {
		_, err := mergeParse(t, Fragment{Name: "x.json", Raw: []byte(`{"` + block + `":{}}`)})
		require.ErrorContains(t, err, block)
	}
}

func TestADuplicateKeyInAnOverlayIsRefusedNotCollapsed(t *testing.T) {
	_, err := mergeParse(t, Fragment{Name: "dup.json", Raw: []byte(`{"upstreams":{"house":{"base_url":"http://real","base_url":"http://evil","path":"v1/responses","classification":"free"}}}`)})
	require.ErrorContains(t, err, `duplicate key "base_url"`)
}

func TestABlockThatIsNotAnObjectIsRefusedRatherThanMergingNothing(t *testing.T) {
	for _, bad := range []string{`[]`, `"house"`, `7`} {
		_, err := mergeParse(t, Fragment{Name: "f.json", Raw: []byte(`{"upstreams":` + bad + `}`)})
		require.ErrorContains(t, err, "want an object of names")
	}
	_, err := mergeParse(t, Fragment{Name: "f.json", Raw: []byte(`{"upstreams":null}`)})
	require.NoError(t, err)
}

func TestADuplicateKeyInTheCommittedTableIsRefusedToo(t *testing.T) {
	_, err := Merge([]byte(`{"upstreams":{},"upstreams":{}}`), nil)
	require.ErrorContains(t, err, "duplicate key")
}

func TestAnOverlayGoesThroughEveryRuleParseAlreadyEnforces(t *testing.T) {
	for _, tc := range []struct{ entry, want string }{
		{`{"base_url":"https://example.com","path":"v1/responses","classification":"free"}`, "in-cluster"},
		{`{"base_url":"http://w","path":"v1/responses"}`, "classification"},
		{`{"base_url":"http://w","path":"v1/responses","protocol":"chat_completions","classification":"free"}`, "protocol"},
		{`{"base_url":"http://w","path":"v1/responses","classification":"free","nope":1}`, "nope"},
	} {
		_, err := mergeParse(t, Fragment{Name: "f.json", Raw: []byte(`{"upstreams":{"house":` + tc.entry + `}}`)})
		require.ErrorContains(t, err, tc.want)
	}
}

func TestReadSkipsTheSymlinksAConfigMapVolumePlants(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "upstreams.json")
	require.NoError(t, os.WriteFile(base, []byte(overlayBase), 0600))
	overlay := filepath.Join(dir, "upstreams.d")
	require.NoError(t, os.MkdirAll(filepath.Join(overlay, "..2026_09_03"), 0700))
	for name, body := range map[string]string{
		"b.json": `{"upstreams":{"b":{"base_url":"http://b","path":"v1/responses","classification":"free"}}}`,
		"a.json": `{"upstreams":{"a":{"base_url":"http://a","path":"v1/responses","classification":"free"}}}`,
		"..data": "not json", "notes.txt": "not json either",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(overlay, name), []byte(body), 0600))
	}
	_, frags, err := Read(base, overlay)
	require.NoError(t, err)
	require.Len(t, frags, 2)
	require.Equal(t, "a.json", frags[0].Name)
	require.Equal(t, "b.json", frags[1].Name)
	cfg, err := loadDir(t, base, overlay)
	require.NoError(t, err)
	require.Len(t, cfg.Upstreams, 3)
}

func TestAnAbsentOverlayDirectoryIsAnEmptyOverlayNotAnError(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "upstreams.json")
	require.NoError(t, os.WriteFile(base, []byte(overlayBase), 0600))
	cfg, err := loadDir(t, base, filepath.Join(dir, "nothing-here"))
	require.NoError(t, err)
	require.Len(t, cfg.Upstreams, 1)
}

func TestMergingNothingReproducesTheCommittedTable(t *testing.T) {
	merged, err := Merge([]byte(overlayBase), nil)
	require.NoError(t, err)
	require.JSONEq(t, overlayBase, string(merged))
}

// A field added to Upstream must be deliberately classified, not admitted
// into an operator ConfigMap by silence.
func TestEveryUpstreamFieldIsClassifiedAsSafeOrDenied(t *testing.T) {
	safe := map[string]bool{"base_url": true, "path": true, "protocol": true, "client_path": true, "classification": true}
	denied := map[string]bool{}
	for _, f := range modelCustodyFields {
		denied[f] = true
	}
	typ := reflect.TypeOf(Upstream{})
	for i := range typ.NumField() {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		require.True(t, safe[tag] || denied[tag], "classify new Upstream field %q as safe or denied", tag)
	}
	require.NotEmpty(t, denied)
}

func TestAnOverlayModelUpstreamMayNotCarryCustodyOrPrices(t *testing.T) {
	for _, entry := range []string{
		`{"base_url":"http://m.demo:8000","path":"v1/responses","classification":"free","credential_file":"/etc/kaimahi/admin/token"}`,
		`{"base_url":"http://m.demo:8000","path":"v1/responses","classification":"free","Credential_File":"/etc/kaimahi/admin/token"}`,
		`{"base_url":"http://m.demo:8000","path":"v1/responses","classification":"free","credential_header":"x-api-key"}`,
		`{"base_url":"https://evil.example","path":"v1/responses","classification":"free","internet":true}`,
		`{"base_url":"https://evil.example","path":"v1/responses","classification":"free","INTERNET":true}`,
		`{"base_url":"http://m.demo:8000","path":"v1/responses","classification":"free","ca_file":"/etc/kaimahi/ca.pem"}`,
		`{"base_url":"http://m.demo:8000","path":"v1/responses","classification":"free","extra_headers":{"x-tenant":"acme"}}`,
		`{"base_url":"http://m.demo:8000","path":"v1/responses","classification":"metered","prices":{"m":{"in_cents_per_1m":0,"out_cents_per_1m":0}}}`,
	} {
		_, err := mergeParse(t, Fragment{Name: "m.json", Raw: []byte(`{"upstreams":{"house":` + entry + `}}`)})
		require.ErrorContains(t, err, "which an overlay may not set")
	}
}

func TestAnOverlayMayAddAModelUpstream(t *testing.T) {
	cfg, err := mergeParse(t, Fragment{Name: "house.json", Raw: []byte(`{"upstreams":{"house":{"base_url":"http://vllm.demo:8000","path":"v1/responses","classification":"free"}}}`)})
	require.NoError(t, err)
	require.Equal(t, "v1/responses", cfg.Upstreams["house"].Path)
	require.Equal(t, ProtocolResponses, cfg.Upstreams["house"].Protocol)
	require.Contains(t, cfg.Upstreams, "ollama")
}

func TestAnOverlayModelUpstreamMustLookInCluster(t *testing.T) {
	_, err := mergeParse(t, Fragment{Name: "h.json", Raw: []byte(`{"upstreams":{"house":{"base_url":"http://api.example.com","path":"v1/responses","classification":"free"}}}`)})
	require.ErrorContains(t, err, "in-cluster")
}

func TestAnOverlayMayNotRedefineACommittedModelUpstream(t *testing.T) {
	_, err := mergeParse(t, Fragment{Name: "o.json", Raw: []byte(`{"upstreams":{"ollama":{"base_url":"http://attacker.demo:8000","path":"v1/responses","classification":"free"}}}`)})
	require.ErrorContains(t, err, `redefines upstream "ollama"`)
	require.ErrorContains(t, err, "the committed table")
	require.ErrorContains(t, err, "o.json")
}

func loadDir(t *testing.T, path, dir string) (Config, error) {
	t.Helper()
	base, frags, err := Read(path, dir)
	if err != nil {
		return Config{}, err
	}
	merged, err := Merge(base, frags)
	if err != nil {
		return Config{}, err
	}
	return Parse(merged)
}
