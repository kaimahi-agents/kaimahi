package scaffold

import (
	"encoding/json"
	"strings"
	"testing"
)

func warehouse() UpstreamSpec {
	s := UpstreamSpec{
		Name:             "warehouse",
		URL:              "http://acme-warehouse.acme:8090/mcp",
		Service:          "acme-warehouse",
		ServiceNamespace: "acme",
		PodPort:          9090,
		PodLabels:        map[string]string{"app": "acme-warehouse", "tier": "mcp"},
		Secret:           "kaimahi-warehouse-token",
		Tools: []ToolDecl{
			{Name: "stock_get", Declared: true, Fields: []string{"sku"}},
			{Name: "stock_adjust", Declared: true, Fields: []string{"sku", "delta"}},
		},
	}
	frag, err := s.Fragment()
	if err != nil {
		panic(err)
	}
	s.Fragments = map[string]string{s.FragmentKey(): frag}
	return s
}

// --- the gateway URL, the one string nobody should retype ---

func TestTheGatewayURLIsDerivedFromTheUpstreamName(t *testing.T) {
	for name, want := range map[string]string{
		"warehouse":    "http://kaimahi-mcp-gateway.kaimahi:8081/upstream/warehouse/mcp",
		"kagent-tools": "http://kaimahi-mcp-gateway.kaimahi:8081/upstream/kagent-tools/mcp",
		"erp":          "http://kaimahi-mcp-gateway.kaimahi:8081/upstream/erp/mcp",
	} {
		if got := GatewayURL(name); got != want {
			t.Fatalf("GatewayURL(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestTheScaffoldedSeamPointsAtTheGatewayAndNeverAtTheServer(t *testing.T) {
	doc, err := GenerateUpstream(warehouse())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, `url: "http://kaimahi-mcp-gateway.kaimahi:8081/upstream/warehouse/mcp"`) {
		t.Fatalf("the RemoteMCPServer does not point at the gateway:\n%s", doc)
	}
	// The server's own URL belongs in the overlay fragment, where the
	// gateway reads it — never on the seam an agent is pointed at.
	seam := doc[strings.Index(doc, "kind: RemoteMCPServer"):]
	if strings.Contains(seam, "acme-warehouse.acme:8090") {
		t.Fatalf("the agent-facing seam names the server directly:\n%s", seam)
	}
}

func TestUpstreamNamesThatWouldCollideOrEscapeAreRefused(t *testing.T) {
	for _, bad := range []string{
		"", "Warehouse", "ware house", "-warehouse", "warehouse-", "ware/house",
		"ware..house", "kagent-tools", "slack", "github", "erp",
		strings.Repeat("a", 41),
	} {
		if err := ValidateUpstreamName(bad); err == nil {
			t.Fatalf("upstream name %q was accepted", bad)
		}
	}
	for _, ok := range []string{"warehouse", "acme-erp", "s3", "a"} {
		if err := ValidateUpstreamName(ok); err != nil {
			t.Fatalf("upstream name %q was refused: %v", ok, err)
		}
	}
}

// --- the NetworkPolicy pair ---

func TestTheNetworkPolicyPairIsProxyToServerAndServerFromProxyAndNothingWider(t *testing.T) {
	doc, err := GenerateUpstream(warehouse())
	if err != nil {
		t.Fatal(err)
	}
	egress := section(t, doc, "kaimahi-upstream-warehouse-egress", "kaimahi-upstream-warehouse-ingress")
	ingress := section(t, doc, "kaimahi-upstream-warehouse-ingress", "")

	// Out: the proxy, to those pods, on the CONTAINER port.
	for _, want := range []string{
		"      app: kaimahi-proxy", "policyTypes: [Egress]",
		`kubernetes.io/metadata.name: "acme"`,
		`"app": "acme-warehouse"`, `"tier": "mcp"`, "port: 9090",
	} {
		if !strings.Contains(egress, want) {
			t.Fatalf("egress policy is missing %q:\n%s", want, egress)
		}
	}
	// The Service's published port must not leak into the policy — the
	// rule is evaluated on the post-NAT pod address.
	if strings.Contains(egress, "8090") {
		t.Fatalf("the egress policy used the SERVICE port, not the pod port:\n%s", egress)
	}
	// In: the proxy alone, and the server may reach nothing.
	for _, want := range []string{
		"policyTypes: [Ingress, Egress]",
		"kubernetes.io/metadata.name: kaimahi",
		"      app: kaimahi-proxy", "port: 9090",
	} {
		if !strings.Contains(ingress, want) {
			t.Fatalf("ingress policy is missing %q:\n%s", want, ingress)
		}
	}
	if strings.Contains(ingress, "\n  egress:") {
		t.Fatalf("the default posture must grant the server no egress:\n%s", ingress)
	}
	// Nothing wider: no ipBlock anywhere, in either direction.
	if strings.Contains(doc, "ipBlock") {
		t.Fatalf("a scaffolded policy opened an address range:\n%s", doc)
	}
}

func TestTheServerEgressPostureIsStatedNotAssumed(t *testing.T) {
	dns := warehouse()
	dns.ServerDNS = true
	doc, err := GenerateUpstream(dns)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "k8s-app: kube-dns") || !strings.Contains(doc, "port: 53") {
		t.Fatalf("--server-egress dns did not grant DNS:\n%s", doc)
	}
	if strings.Contains(doc, "ipBlock") {
		t.Fatal("DNS egress must not come with an address range")
	}

	keep := warehouse()
	keep.ServerEgressKeep = true
	doc, err = GenerateUpstream(keep)
	if err != nil {
		t.Fatal(err)
	}
	ingress := section(t, doc, "kaimahi-upstream-warehouse-ingress", "")
	if !strings.Contains(ingress, "policyTypes: [Ingress]") {
		t.Fatalf("--server-egress keep must not list Egress:\n%s", ingress)
	}
	if !strings.Contains(doc, "says NOTHING about the server's own egress") {
		t.Fatalf("the weaker posture must say what it does not bound:\n%s", ingress)
	}
}

func TestAServiceWithNoSelectorHasNothingToPinAndIsRefused(t *testing.T) {
	s := warehouse()
	s.PodLabels = nil
	if _, err := GenerateUpstream(s); err == nil {
		t.Fatal("a policy pinned to no labels would select every pod in the namespace")
	}
}

func TestALabelValueCannotCloseTheMappingItIsEmittedInto(t *testing.T) {
	s := warehouse()
	s.PodLabels = map[string]string{"app": "x\"\n      app: kaimahi-proxy"}
	if _, err := GenerateUpstream(s); err == nil {
		t.Fatal("a crafted label value was emitted rather than refused")
	}
}

// --- the fragment, and policy_fields ---

func TestTheFragmentCarriesTheDeclarationsAndNothingElse(t *testing.T) {
	frag, err := warehouse().Fragment()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(frag), &got); err != nil {
		t.Fatalf("the fragment is not JSON: %v\n%s", err, frag)
	}
	if len(got) != 1 {
		t.Fatalf("a fragment may carry tool_upstreams alone, got %v", got)
	}
	entry := got["tool_upstreams"].(map[string]any)["warehouse"].(map[string]any)
	if entry["url"] != "http://acme-warehouse.acme:8090/mcp" {
		t.Fatalf("fragment url = %v", entry["url"])
	}
	// No credential, in any form, ever.
	for _, forbidden := range []string{"credential_file", "credential_header", "internet", "ca_file"} {
		if _, ok := entry[forbidden]; ok {
			t.Fatalf("the scaffold emitted %q", forbidden)
		}
	}
	tools := entry["tools"].(map[string]any)
	fields := tools["stock_adjust"].(map[string]any)["policy_fields"].([]any)
	if len(fields) != 2 || fields[0] != "sku" || fields[1] != "delta" {
		t.Fatalf("policy_fields lost their order: %v", fields)
	}
}

func TestAnEmptyPolicyFieldsListIsEmittedAsAnEmptyListNotOmitted(t *testing.T) {
	// The plane distinguishes "no argument is policy-relevant" from
	// "somebody forgot the key" and refuses the second. A scaffold that
	// dropped the empty list would produce a config that will not load.
	s := warehouse()
	s.Tools = []ToolDecl{{Name: "ping", Declared: true, Fields: []string{}}}
	frag, err := s.Fragment()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frag, `"policy_fields": []`) {
		t.Fatalf("an explicit verb-level binding was not emitted:\n%s", frag)
	}
}

func TestATooWithNoDeclarationGetsNoEntryAtAll(t *testing.T) {
	s := warehouse()
	s.Tools = []ToolDecl{{Name: "whole_object"}}
	frag, err := s.Fragment()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(frag, "whole_object") || strings.Contains(frag, "tools") {
		t.Fatalf("`--tool name:*` must produce NO declaration:\n%s", frag)
	}
}

func TestTheWeakestSettingIsCalledOutInTheDocumentItself(t *testing.T) {
	s := warehouse()
	s.Tools = []ToolDecl{{Name: "stock_adjust", Declared: true, Fields: []string{}}}
	s.Fragments = map[string]string{s.FragmentKey(): must(t, s.Fragment)}
	doc, err := GenerateUpstream(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "WEAKEST SETTING IN USE") || !strings.Contains(doc, "stock_adjust") {
		t.Fatalf("a verb-level binding must be visible to whoever reviews the file:\n%s", doc)
	}
	// And it must NOT appear when nothing is verb-level.
	plain, err := GenerateUpstream(warehouse())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "WEAKEST SETTING IN USE") {
		t.Fatal("the warning fired with no verb-level declaration")
	}
}

func TestTheOverlayCarriesEveryFragmentSoTheReviewedFileIsWhatTheClusterHolds(t *testing.T) {
	s := warehouse()
	s.Fragments["depot.json"] = "{\n  \"tool_upstreams\": {\"depot\": {\"url\": \"http://d.acme:80/mcp\"}}\n}\n"
	doc, err := GenerateUpstream(s)
	if err != nil {
		t.Fatal(err)
	}
	// A ConfigMap apply REPLACES data, so an emitted map missing an
	// existing key would silently un-onboard that server.
	for _, want := range []string{`"depot.json": |2`, `"warehouse.json": |2`, `"depot"`} {
		if !strings.Contains(doc, want) {
			t.Fatalf("the emitted overlay lost %q:\n%s", want, doc)
		}
	}
}

// --- the policy_fields prompt, which is the whole governance moment ---

func TestNamingAToolWithoutDeclaringAnythingIsRefusedWithTheConsequences(t *testing.T) {
	_, err := ParseToolDecls([]string{"stock_adjust"})
	if err == nil {
		t.Fatal("a bare tool name was accepted — kmx must not choose policy_fields")
	}
	// The consequence of each option is stated at the point of
	// choosing, not left in a document nobody opens.
	for _, want := range []string{
		"stock_adjust:amount_cents", "stock_adjust:", "stock_adjust:*",
		"WEAKEST", "welded", "WHOLE argument object", "brittle",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not state %q:\n%s", want, err)
		}
	}
}

func TestNamingNoToolsAtAllIsRefusedTheSameWay(t *testing.T) {
	_, err := ParseToolDecls(nil)
	if err == nil || !strings.Contains(err.Error(), "WEAKEST") {
		t.Fatalf("want the same guidance with no tools named, got: %v", err)
	}
}

func TestTheThreeDeclarationsParseToTheThreeDifferentThings(t *testing.T) {
	got, err := ParseToolDecls([]string{"c_whole:*", "a_fields:sku, delta", "b_verb:"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != "a_fields" || got[1].Name != "b_verb" || got[2].Name != "c_whole" {
		t.Fatalf("declarations are not sorted by name: %+v", got)
	}
	if !got[0].Declared || len(got[0].Fields) != 2 || got[0].Fields[1] != "delta" {
		t.Fatalf("fields: %+v", got[0])
	}
	if !got[1].VerbLevel() {
		t.Fatalf("`name:` must be the explicit verb-level binding: %+v", got[1])
	}
	if got[2].Declared {
		t.Fatalf("`name:*` must produce no declaration: %+v", got[2])
	}
}

func TestBadToolAndFieldNamesAreRefused(t *testing.T) {
	for _, bad := range []string{
		"sto ck:sku", "_stock:sku", "stock:sku field", "stock:a.b",
		"stock:" + strings.Repeat("f", 65), "stock:sku,sku",
	} {
		if _, err := ParseToolDecls([]string{bad}); err == nil {
			t.Fatalf("--tool %q was accepted", bad)
		}
	}
	if _, err := ParseToolDecls([]string{"stock:sku", "stock:delta"}); err == nil {
		t.Fatal("the same tool declared twice was accepted")
	}
}

func section(t *testing.T, doc, from, to string) string {
	t.Helper()
	i := strings.Index(doc, from)
	if i < 0 {
		t.Fatalf("document %q not found in:\n%s", from, doc)
	}
	rest := doc[i:]
	if to != "" {
		if j := strings.Index(rest, to); j > 0 {
			rest = rest[:j]
		}
	}
	return rest
}

func must(t *testing.T, f func() (string, error)) string {
	t.Helper()
	s, err := f()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
