package krm

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const (
	providerDoc = `apiVersion: kmx.kaimahi.dev/v1alpha1
kind: Provider
metadata:
  name: default-chat
spec:
  type: openai
  model: qwen2.5:3b
  openAI:
    baseURL: http://ollama.ollama:11434/v1
  credentials:
    secret:
      name: local-model
      key: api-key
`
	webSearchDoc = `apiVersion: kmx.kaimahi.dev/v1alpha1
kind: Tool
metadata:
  name: web-search
spec:
  description: Search the web and return the top results
  parameters:
    type: object
    required:
      - query
    properties:
      query:
        type: string
  http:
    method: POST
    url: https://search.example.com/query
    timeout: 30s
    credentials:
      secret:
        name: search-credentials
        key: api-key
`
	fetchPageDoc = `apiVersion: kmx.kaimahi.dev/v1alpha1
kind: Tool
metadata:
  name: fetch-page
spec:
  description: Fetch a web page and return its text
  parameters:
    type: object
    required:
      - url
    properties:
      url:
        type: string
  http:
    method: POST
    url: https://fetch.example.com/page
`
	researcherDoc = `apiVersion: kmx.kaimahi.dev/v1alpha1
kind: Agent
metadata:
  name: researcher
spec:
  description: Researches a requested topic
  instructions: |
    Search authoritative sources and summarize the findings.
  provider: default-chat
  tools:
    - web-search
    - fetch-page
  allowedAgents:
    - summarizer
`
	summarizerDoc = `apiVersion: kmx.kaimahi.dev/v1alpha1
kind: Agent
metadata:
  name: summarizer
spec:
  description: Summarizes research
  instructions: |
    Produce a concise summary with citations.
  provider: default-chat
  tools:
    - fetch-page
`
)

// docs is the example graph as authored documents; tests replace entries.
type docs struct{ provider, webSearch, fetchPage, researcher, summarizer string }

func example() docs {
	return docs{providerDoc, webSearchDoc, fetchPageDoc, researcherDoc, summarizerDoc}
}

func (d docs) graph() (*Graph, error) {
	p, err := DecodeProvider([]byte(d.provider))
	if err != nil {
		return nil, err
	}
	var tools []*Tool
	for _, doc := range []string{d.webSearch, d.fetchPage} {
		t, err := DecodeTool([]byte(doc))
		if err != nil {
			return nil, err
		}
		tools = append(tools, t)
	}
	var agents []*Agent
	for _, doc := range []string{d.researcher, d.summarizer} {
		a, err := DecodeAgent([]byte(doc))
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return NewGraph([]*Provider{p}, tools, agents)
}

func mustGraph(t *testing.T, d docs) *Graph {
	t.Helper()
	g, err := d.graph()
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func replace(t *testing.T, doc, old, new string) string {
	t.Helper()
	if !strings.Contains(doc, old) {
		t.Fatalf("document does not contain %q", old)
	}
	return strings.Replace(doc, old, new, 1)
}

func TestGraphSharesOneProviderAndToolAcrossAgents(t *testing.T) {
	g := mustGraph(t, example())
	var names []string
	for _, tool := range g.Tools {
		names = append(names, tool.Metadata.Name)
	}
	if !reflect.DeepEqual(names, []string{"fetch-page", "web-search"}) {
		t.Fatalf("tools are not ordered by name: %v", names)
	}
	for _, a := range g.Agents {
		if a.Spec.Provider != "default-chat" {
			t.Fatalf("%s provider = %q", a.Metadata.Name, a.Spec.Provider)
		}
	}
	if got := g.Agent("researcher").Spec.AllowedAgents; !reflect.DeepEqual(got, []string{"summarizer"}) {
		t.Fatalf("researcher allowedAgents = %v", got)
	}
	if got := g.Provider("default-chat").Source(); string(got) != providerDoc {
		t.Fatal("Provider source is not the exact authored bytes")
	}
}

func identities(t *testing.T, d docs) map[string]string {
	t.Helper()
	g := mustGraph(t, d)
	out := map[string]string{}
	for _, a := range g.Agents {
		id, err := g.AgentIdentity(a.Metadata.Name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, "sha256:") || len(id) != len("sha256:")+64 {
			t.Fatalf("identity %q is not a sha256 digest", id)
		}
		out[a.Metadata.Name] = id
	}
	return out
}

func TestAgentIdentityCoversDependencies(t *testing.T) {
	base := identities(t, example())
	if again := identities(t, example()); !reflect.DeepEqual(base, again) {
		t.Fatal("identity is not deterministic")
	}
	if base["researcher"] == base["summarizer"] {
		t.Fatal("distinct Agents share an identity")
	}
	for _, tc := range []struct {
		name    string
		change  func(*docs)
		changed []string
	}{
		{"shared Provider", func(d *docs) { d.provider = strings.Replace(d.provider, "qwen2.5:3b", "qwen2.5:7b", 1) }, []string{"researcher", "summarizer"}},
		{"shared Tool", func(d *docs) { d.fetchPage = strings.Replace(d.fetchPage, "its text", "its body", 1) }, []string{"researcher", "summarizer"}},
		{"researcher-only Tool", func(d *docs) { d.webSearch = strings.Replace(d.webSearch, "top results", "best results", 1) }, []string{"researcher"}},
		{"delegation target", func(d *docs) { d.summarizer = strings.Replace(d.summarizer, "concise", "short", 1) }, []string{"researcher", "summarizer"}},
		{"coordinator", func(d *docs) { d.researcher = strings.Replace(d.researcher, "authoritative", "reliable", 1) }, []string{"researcher"}},
		{"comment only", func(d *docs) { d.summarizer = "# reviewed\n" + d.summarizer }, []string{"researcher", "summarizer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := example()
			tc.change(&d)
			got := identities(t, d)
			var changed []string
			for _, name := range []string{"researcher", "summarizer"} {
				if got[name] != base[name] {
					changed = append(changed, name)
				}
			}
			if !reflect.DeepEqual(changed, tc.changed) {
				t.Fatalf("changed identities = %v, want %v", changed, tc.changed)
			}
		})
	}
	if _, err := mustGraph(t, example()).AgentIdentity("missing"); err == nil {
		t.Fatal("identity of an unknown Agent succeeded")
	}
}

func TestAgentIdentityTerminatesOnDelegationCycles(t *testing.T) {
	d := example()
	d.summarizer = replace(t, d.summarizer, "  tools:\n", "  allowedAgents:\n    - researcher\n  tools:\n")
	ids := identities(t, d)
	if ids["researcher"] == ids["summarizer"] {
		t.Fatal("Agents in a cycle share an identity")
	}
}

func TestAffectedAgents(t *testing.T) {
	g := mustGraph(t, example())
	for _, tc := range []struct {
		kind, name string
		want       []string
	}{
		{KindProvider, "default-chat", []string{"researcher", "summarizer"}},
		{KindTool, "fetch-page", []string{"researcher", "summarizer"}},
		{KindTool, "web-search", []string{"researcher"}},
		{KindAgent, "summarizer", []string{"researcher", "summarizer"}},
		{KindAgent, "researcher", []string{"researcher"}},
	} {
		got, err := g.AffectedAgents(tc.kind, tc.name)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("AffectedAgents(%s, %s) = %v, want %v", tc.kind, tc.name, got, tc.want)
		}
	}
	if _, err := g.AffectedAgents("Schedule", "x"); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestDecodeProviderAcceptsEveryType(t *testing.T) {
	const openAI = "type: openai\n  model: qwen2.5:3b\n  openAI:\n    baseURL: http://ollama.ollama:11434/v1\n"
	for _, spec := range []string{
		"type: anthropic\n  model: claude-sonnet\n",
		"type: openai\n  model: gpt-4.1\n",
		"type: azure-openai\n  model: gpt-4o\n  azure:\n    endpoint: https://example.openai.azure.com\n    apiVersion: 2024-10-21\n",
	} {
		if _, err := DecodeProvider([]byte(replace(t, providerDoc, openAI, spec))); err != nil {
			t.Errorf("%q: %v", spec, err)
		}
	}
}

func TestDecodeRefuses(t *testing.T) {
	credential := secretshapes.All()[0].Example
	const openAI = "type: openai\n  model: qwen2.5:3b\n  openAI:\n    baseURL: http://ollama.ollama:11434/v1\n"
	provider := func(old, new string) func(*testing.T) error {
		return func(t *testing.T) error {
			_, err := DecodeProvider([]byte(replace(t, providerDoc, old, new)))
			return err
		}
	}
	tool := func(old, new string) func(*testing.T) error {
		return func(t *testing.T) error { _, err := DecodeTool([]byte(replace(t, fetchPageDoc, old, new))); return err }
	}
	agent := func(doc, old, new string) func(*testing.T) error {
		return func(t *testing.T) error { _, err := DecodeAgent([]byte(replace(t, doc, old, new))); return err }
	}
	for _, tc := range []struct {
		name   string
		decode func(*testing.T) error
		want   string
	}{
		{"wrong apiVersion", agent(summarizerDoc, "v1alpha1", "v1beta1"), `apiVersion must be "kmx.kaimahi.dev/v1alpha1"`},
		{"wrong kind", func(t *testing.T) error { _, err := DecodeTool([]byte(summarizerDoc)); return err }, `kind must be "Tool"`},
		{"invalid name", agent(summarizerDoc, "name: summarizer", "name: Summarizer"), "metadata.name"},
		{"self delegation", agent(researcherDoc, "- summarizer", "- researcher"), "cannot delegate to itself"},
		{"empty allowedAgents", agent(researcherDoc, "allowedAgents:\n    - summarizer", "allowedAgents: []"), "allowedAgents must name at least one"},
		{"duplicate Tool reference", agent(researcherDoc, "- fetch-page", "- web-search"), "listed more than once"},
		{"missing instructions", agent(summarizerDoc, "Produce a concise summary with citations.", " "), "spec.instructions is required"},
		{"unknown field", agent(summarizerDoc, "provider:", "model: gpt\n  provider:"), "field model not found"},
		{"duplicate key", agent(summarizerDoc, "provider: default-chat", "provider: default-chat\n  provider: default-chat"), "duplicate key"},
		{"alias", agent(summarizerDoc, "provider: default-chat", "provider: &p default-chat\n  extra: *p"), "alias"},
		{"merge key", agent(summarizerDoc, "spec:\n", "spec:\n  <<: {}\n"), "merge key"},
		{"second document", agent(summarizerDoc, "    - fetch-page\n", "    - fetch-page\n---\n"+summarizerDoc), "exactly one YAML document"},
		{"credential value", agent(summarizerDoc, "Produce", credential), "never a credential value"},
		{"Provider without credentials", provider("  credentials:\n    secret:\n      name: local-model\n      key: api-key\n", ""), "spec.credentials.secret"},
		{"Provider URL with userinfo", provider("http://ollama", "http://user:pw@ollama"), "no credentials, query or fragment"},
		{"Azure endpoint with path", provider(openAI, "type: azure-openai\n  model: gpt-4o\n  azure:\n    endpoint: https://example.openai.azure.com/openai/v1\n"), "Azure resource root"},
		{"Azure without azure block", provider(openAI, "type: azure-openai\n  model: gpt-4o\n"), "spec.azure is required"},
		{"unsupported Provider type", provider("type: openai", "type: bedrock"), "spec.type must be one of"},
		{"zero rate limit", provider("  credentials:", "  rateLimits:\n    requestsPerMinute: 0\n  credentials:"), "requestsPerMinute must be positive"},
		{"Tool without http", tool("  http:\n    method: POST\n    url: https://fetch.example.com/page\n", ""), "spec.http is required"},
		{"Tool method", tool("method: POST", "method: post"), "spec.http.method"},
		{"Tool URL with userinfo", tool("https://fetch", "https://user:pw@fetch"), "no credentials or fragment"},
		{"Tool Authorization header", tool("    method: POST\n", "    method: POST\n    headers:\n      Authorization: Bearer x\n"), "use spec.http.credentials"},
		{"Tool timeout", tool("    url: https://fetch.example.com/page\n", "    url: https://fetch.example.com/page\n    timeout: soon\n"), "spec.http.timeout"},
		{"Tool parameters not an object", tool("    type: object\n", "    type: string\n"), "type: object"},
		{"Tool parameters not a schema", tool("      - url\n", "      - 7\n"), "not a valid JSON Schema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.decode(t)
			if err == nil {
				t.Fatal("decode succeeded")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
			if strings.Contains(err.Error(), credential) || strings.Contains(err.Error(), "user:pw") {
				t.Fatalf("error echoes a credential: %q", err)
			}
		})
	}
}

func TestNewGraphRefuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*docs)
		want   string
	}{
		{"unknown Provider", func(d *docs) {
			d.summarizer = strings.Replace(d.summarizer, "provider: default-chat", "provider: other-chat", 1)
		},
			`Agent "summarizer" uses unknown Provider "other-chat"`},
		{"unknown Tool", func(d *docs) { d.summarizer = strings.Replace(d.summarizer, "- fetch-page", "- read-page", 1) },
			`Agent "summarizer" uses unknown Tool "read-page"`},
		{"unknown delegation target", func(d *docs) { d.researcher = strings.Replace(d.researcher, "- summarizer", "- reviewer", 1) },
			`allows delegation to unknown Agent "reviewer"`},
		{"duplicate Tool", func(d *docs) { d.webSearch = strings.Replace(d.webSearch, "name: web-search", "name: fetch-page", 1) },
			`Tool "fetch-page" is defined more than once`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := example()
			tc.change(&d)
			_, err := d.graph()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNewGraphReportsEveryBrokenReference(t *testing.T) {
	d := example()
	d.summarizer = strings.Replace(d.summarizer, "provider: default-chat", "provider: other-chat", 1)
	d.researcher = strings.Replace(d.researcher, "- summarizer", "- reviewer", 1)
	_, err := d.graph()
	if err == nil {
		t.Fatal("graph accepted")
	}
	for _, want := range []string{`"other-chat"`, `"reviewer"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not report %s", err, want)
		}
	}
}

func TestToolParametersResolveOnlyLocalReferences(t *testing.T) {
	external := filepath.Join(t.TempDir(), "query.json")
	if err := os.WriteFile(external, []byte(`{"type": "string"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := (&url.URL{Scheme: "file", Path: filepath.ToSlash(external)}).String()
	withRef := replace(t, fetchPageDoc, "      url:\n        type: string\n", "      url:\n        $ref: "+ref+"\n")
	if _, err := DecodeTool([]byte(withRef)); err == nil || !strings.Contains(err.Error(), "not a valid JSON Schema") {
		t.Fatalf("external $ref: err = %v", err)
	}
	local := replace(t, fetchPageDoc, "      url:\n        type: string\n", "      url:\n        $ref: \"#/$defs/url\"\n    $defs:\n      url:\n        type: string\n")
	if _, err := DecodeTool([]byte(local)); err != nil {
		t.Fatalf("local $ref: %v", err)
	}
}
