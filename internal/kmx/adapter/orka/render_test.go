package orka

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/krm"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
)

// The example resources are shared with the bundle package's fixture.
const example = "../../bundle/testdata/research-team"

var opts = Options{Namespace: "research", Labels: map[string]string{"kmx.kaimahi.dev/bundle": "research-team"}}

// graph decodes the example resources, applying edits keyed by relative path.
func graph(t *testing.T, edits map[string]func(string) string) *krm.Graph {
	t.Helper()
	read := func(rel string) []byte {
		data, err := os.ReadFile(filepath.Join(example, rel))
		if err != nil {
			t.Fatal(err)
		}
		if edit := edits[rel]; edit != nil {
			return []byte(edit(string(data)))
		}
		return data
	}
	p, err := krm.DecodeProvider(read("providers/default-chat.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var tools []*krm.Tool
	for _, rel := range []string{"tools/fetch-page.yaml", "tools/web-search.yaml"} {
		tool, err := krm.DecodeTool(read(rel))
		if err != nil {
			t.Fatal(err)
		}
		tools = append(tools, tool)
	}
	var agents []*krm.Agent
	for _, rel := range []string{"agents/researcher.yaml", "agents/summarizer.yaml"} {
		a, err := krm.DecodeAgent(read(rel))
		if err != nil {
			t.Fatal(err)
		}
		agents = append(agents, a)
	}
	g, err := krm.NewGraph([]*krm.Provider{p}, tools, agents)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func kindNames(r *Rendered) []string {
	var out []string
	for _, doc := range r.Documents {
		out = append(out, doc["kind"].(string)+"/"+doc["metadata"].(map[string]any)["name"].(string))
	}
	return out
}

func find(t *testing.T, r *Rendered, kind, name string) map[string]any {
	t.Helper()
	for _, doc := range r.Documents {
		if doc["kind"] == kind && doc["metadata"].(map[string]any)["name"] == name {
			return doc
		}
	}
	t.Fatalf("%s/%s not rendered", kind, name)
	return nil
}

func validateAgainst(t *testing.T, target string, r *Rendered) error {
	t.Helper()
	v, err := orkaschema.Offline(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range r.Documents {
		// orkaschema embeds Agent, Provider and Task CRDs only.
		if doc["kind"] == "Tool" {
			continue
		}
		if err := v.Validate(doc); err != nil {
			return err
		}
	}
	return nil
}

func TestRenderSharesProviderAndToolAcrossAgents(t *testing.T) {
	r, err := Render(graph(t, nil), opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Provider/default-chat", "Tool/fetch-page", "Tool/web-search", "Agent/researcher", "Agent/summarizer"}
	if got := kindNames(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("documents = %v, want %v", got, want)
	}
	for _, name := range []string{"researcher", "summarizer"} {
		spec := find(t, r, "Agent", name)["spec"].(map[string]any)
		if ref := spec["providerRef"].(map[string]any); ref["name"] != "default-chat" || ref["namespace"] != "research" {
			t.Fatalf("%s providerRef = %v", name, ref)
		}
	}
	researcher := find(t, r, "Agent", "researcher")["spec"].(map[string]any)
	if got := researcher["coordination"]; !reflect.DeepEqual(got, map[string]any{
		"enabled": true, "allowedAgents": []any{map[string]any{"name": "summarizer"}},
	}) {
		t.Fatalf("researcher coordination = %v", got)
	}
	if _, ok := find(t, r, "Agent", "summarizer")["spec"].(map[string]any)["coordination"]; ok {
		t.Fatal("summarizer rendered coordination it did not ask for")
	}
	tool := find(t, r, "Tool", "web-search")["spec"].(map[string]any)["http"].(map[string]any)
	if got := tool["authSecretRef"]; !reflect.DeepEqual(got, map[string]any{"name": "search-credentials", "key": "api-key"}) {
		t.Fatalf("web-search authSecretRef = %v", got)
	}
	wantSecrets := []krm.SecretKeyRef{{Name: "local-model", Key: "api-key"}, {Name: "search-credentials", Key: "api-key"}}
	if !reflect.DeepEqual(r.Secrets, wantSecrets) {
		t.Fatalf("secrets = %v, want %v", r.Secrets, wantSecrets)
	}
	for _, target := range []string{"", "v0.1.3", "main"} {
		if err := validateAgainst(t, target, r); err != nil {
			t.Errorf("Orka %q schema: %v", target, err)
		}
	}
}

func TestRenderYAMLIsDeterministic(t *testing.T) {
	g := graph(t, nil)
	r, err := Render(g, opts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.YAML()
	if err != nil {
		t.Fatal(err)
	}
	summarizer := `apiVersion: core.orka.ai/v1alpha1
kind: Agent
metadata:
    annotations:
        kaimahi.dev/description: Summarizes research
    labels:
        kmx.kaimahi.dev/bundle: research-team
    name: summarizer
    namespace: research
spec:
    providerRef:
        name: default-chat
        namespace: research
    systemPrompt:
        inline: |
            Produce a concise summary with citations.
    tools:
        - name: fetch-page
`
	if !strings.HasSuffix(got, "---\n"+summarizer) {
		t.Fatalf("last document is not the expected summarizer Agent:\n%s", got)
	}
	again, _ := Render(g, opts)
	if y, _ := again.YAML(); y != got {
		t.Fatal("rendering is not deterministic")
	}
}

func TestRenderWithoutLabels(t *testing.T) {
	r, err := Render(graph(t, nil), Options{Namespace: "research"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Documents[0]["metadata"].(map[string]any)["labels"]; ok {
		t.Fatal("labels rendered without being requested")
	}
}

func TestRenderRefusesUnresolvedReferences(t *testing.T) {
	g := graph(t, nil)
	partial := &krm.Graph{Providers: g.Providers, Tools: g.Tools, Agents: []*krm.Agent{g.Agent("researcher")}}
	if _, err := Render(partial, opts); err == nil || !strings.Contains(err.Error(), `delegate to Agent "summarizer"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderRefusesInvalidNamespace(t *testing.T) {
	if _, err := Render(graph(t, nil), Options{Namespace: "Not_A_Namespace"}); err == nil {
		t.Fatal("invalid namespace accepted")
	}
}

func providerSpec(spec string) map[string]func(string) string {
	return map[string]func(string) string{"providers/default-chat.yaml": func(doc string) string {
		head, _, _ := strings.Cut(doc, "spec:\n")
		return head + "spec:\n" + spec
	}}
}

func TestRenderAzureProvider(t *testing.T) {
	r, err := Render(graph(t, providerSpec(`  type: azure-openai
  model: gpt-4o
  azure:
    endpoint: https://example.openai.azure.com
    apiVersion: 2024-10-21
  credentials:
    secret:
      name: azure-openai
      key: api-key
`)), opts)
	if err != nil {
		t.Fatal(err)
	}
	spec := find(t, r, "Provider", "default-chat")["spec"].(map[string]any)
	want := map[string]any{
		"type": "azure-openai", "defaultModel": "gpt-4o", "baseURL": "https://example.openai.azure.com",
		"azure":     map[string]any{"deploymentName": "gpt-4o", "apiVersion": "2024-10-21"},
		"secretRef": map[string]any{"name": "azure-openai", "key": "api-key"},
	}
	if !reflect.DeepEqual(spec, want) {
		t.Fatalf("spec = %v, want %v", spec, want)
	}
	if err := validateAgainst(t, "", r); err != nil {
		t.Fatal(err)
	}
}

// Orka v0.1.3 accepts Provider rate limits; v0.2.0 removed the field, so its
// schema refuses them rather than silently dropping the limit.
func TestRenderProviderRateLimitsFollowTheSelectedOrkaSchema(t *testing.T) {
	r, err := Render(graph(t, providerSpec(`  type: openai
  model: gpt-4.1
  credentials:
    secret:
      name: openai
      key: api-key
  rateLimits:
    requestsPerMinute: 60
    tokensPerMinute: 120000
`)), opts)
	if err != nil {
		t.Fatal(err)
	}
	got := find(t, r, "Provider", "default-chat")["spec"].(map[string]any)["rateLimit"]
	if !reflect.DeepEqual(got, map[string]any{"requestsPerMinute": int32(60), "tokensPerMinute": int64(120000)}) {
		t.Fatalf("rateLimit = %#v", got)
	}
	if err := validateAgainst(t, "v0.1.3", r); err != nil {
		t.Fatalf("v0.1.3: %v", err)
	}
	if err := validateAgainst(t, "", r); err == nil {
		t.Fatal("v0.2.0 schema accepted a field it does not define")
	}
}
