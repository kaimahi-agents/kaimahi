package convert

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func outputDocs(t *testing.T, b []byte) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	d := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var doc map[string]any
		if err := d.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		meta := doc["metadata"].(map[string]any)
		key := doc["kind"].(string) + "/" + meta["name"].(string)
		if out[key] != nil {
			t.Fatalf("duplicate output %s", key)
		}
		if meta["namespace"] != "demo" {
			t.Fatalf("namespace lost: %v", meta)
		}
		if doc["kind"] != "ConfigMap" && doc["apiVersion"] != "core.orka.ai/v1alpha1" {
			t.Fatal(doc)
		}
		out[key] = doc
	}
	return out
}

// Catches dropped providers, wrong prompt references, altered JSON schemas, and
// routes that advertise names different from those the adapter actually calls.
func TestConvertSupportedFixtures(t *testing.T) {
	for _, tc := range []struct {
		file, provider, model, endpoint string
		configmap                       bool
	}{
		{"literal.yaml", "openai", "gpt-4.1-mini", "https://api.openai.com/v1", false},
		{"configmap.yaml", "anthropic", "claude-sonnet-4-5", "https://api.anthropic.com", true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			input, schemas := fixture(t, tc.file), fixture(t, "schemas.json")
			b, err := Convert(input, schemas)
			if err != nil {
				t.Fatal(err)
			}
			again, err := Convert(input, schemas)
			if err != nil || !bytes.Equal(b, again) {
				t.Fatalf("not deterministic: %v", err)
			}
			docs := outputDocs(t, b)
			wantCount := 4
			if tc.configmap {
				wantCount++
			}
			if len(docs) != wantCount {
				t.Fatalf("got %d documents: %s", len(docs), b)
			}
			provider := docs["Provider/selected-model"]["spec"].(map[string]any)
			if provider["type"] != tc.provider || provider["defaultModel"] != tc.model || provider["baseURL"] != tc.endpoint {
				t.Fatal(provider)
			}
			secret := provider["secretRef"].(map[string]any)
			if secret["name"] != "llm-key" || secret["key"] != "api-key" {
				t.Fatal(secret)
			}
			agent := docs["Agent/cluster-helper"]["spec"].(map[string]any)
			if agent["providerRef"].(map[string]any)["name"] != "selected-model" || agent["model"].(map[string]any)["name"] != tc.model {
				t.Fatal(agent)
			}
			prompt := agent["systemPrompt"].(map[string]any)
			if tc.configmap {
				ref := prompt["configMapRef"].(map[string]any)
				if ref["name"] != "instructions" || ref["key"] != "prompt" {
					t.Fatal(ref)
				}
				if docs["ConfigMap/instructions"]["data"].(map[string]any)["prompt"] != "Inspect Kubernetes resources using the selected tools." {
					t.Fatal(docs)
				}
			} else if prompt["inline"] != "Inspect Kubernetes resources using the selected tools." {
				t.Fatal(prompt)
			}
			refs := agent["tools"].([]any)
			if len(refs) != 1 {
				t.Fatal(refs)
			}
			name := refs[0].(map[string]any)["name"].(string)
			if !regexp.MustCompile(`^shim-[a-f0-9]{32}$`).MatchString(name) {
				t.Fatalf("unsafe generated name: %q", name)
			}
			tool := docs["Tool/"+name]
			spec := tool["spec"].(map[string]any)
			if spec["description"] != "Get Kubernetes resources" {
				t.Fatal(spec)
			}
			var snapshot map[string]any
			if err := yaml.Unmarshal(schemas, &snapshot); err != nil {
				t.Fatal(err)
			}
			wantSchema := snapshot["cluster-tools"].(map[string]any)["tools"].([]any)[0].(map[string]any)["inputSchema"]
			if !reflect.DeepEqual(spec["parameters"], wantSchema) {
				t.Fatalf("parameters changed: got %#v, want %#v", spec["parameters"], wantSchema)
			}
			if !bytes.Contains(b, []byte("9007199254740993")) {
				t.Fatal("schema integer rounded")
			}
			http := spec["http"].(map[string]any)
			if http["url"] != "http://kagent-shim-adapter.demo.svc:8080/tools/"+name || http["method"] != "POST" || http["timeout"] != "60s" {
				t.Fatal(http)
			}
			annotations := tool["metadata"].(map[string]any)["annotations"].(map[string]any)
			if annotations["kagent-shim.kaimahi.ai/source-description"] != "Cluster MCP endpoint" {
				t.Fatal(annotations)
			}
			data := docs["ConfigMap/kagent-shim-adapter"]["data"].(map[string]any)
			var routes struct {
				Tools map[string]struct {
					URL, Name string
					Headers   map[string]struct{ Name, Key string }
				}
			}
			if err := json.Unmarshal([]byte(data["routes.json"].(string)), &routes); err != nil {
				t.Fatal(err)
			}
			route := routes.Tools[name]
			if len(routes.Tools) != 1 || route.URL != "http://mcp.demo.svc:8080/mcp" || route.Name != "k8s_get_resources" {
				t.Fatal(routes)
			}
			if tc.configmap {
				if len(route.Headers) != 0 || strings.Contains(data["routes.json"].(string), "headers") {
					t.Fatal(route)
				}
			} else if route.Headers["Authorization"].Name != "mcp-key" || route.Headers["Authorization"].Key != "token" {
				t.Fatal(route)
			}
		})
	}
}

// Each mutation represents unsupported source semantics that must never be
// silently dropped, including explicitly false, empty, or null values.
func TestConvertRefuses(t *testing.T) {
	literal, cm, schemas := string(fixture(t, "literal.yaml")), string(fixture(t, "configmap.yaml")), string(fixture(t, "schemas.json"))
	replace := func(old, new string) string {
		if !strings.Contains(literal, old) {
			t.Fatalf("bad test mutation %q", old)
		}
		return strings.Replace(literal, old, new, 1)
	}
	cases := []struct{ name, input, schemas, want string }{
		{"stream false", replace("    modelConfig:", "    stream: false\n    modelConfig:"), schemas, "Agent/cluster-helper.spec.declarative.stream"},
		{"runtime null", replace("    modelConfig:", "    runtime: null\n    modelConfig:"), schemas, "Agent/cluster-helper.spec.declarative.runtime"},
		{"memory empty", replace("    modelConfig:", "    memory: {}\n    modelConfig:"), schemas, ".memory"},
		{"BYO", replace("type: Declarative", "type: BYO"), schemas, ".spec.type"},
		{"nested unknown", replace("          toolNames:", "          mystery: false\n          toolNames:"), schemas, ".mcpServer.mystery"},
		{"temperature", replace("    baseUrl:", "    temperature: '0'\n    baseUrl:"), schemas, ".openAI.temperature"},
		{"maxTokens null", replace("    baseUrl:", "    maxTokens: null\n    baseUrl:"), schemas, ".openAI.maxTokens"},
		{"passthrough false", replace("  provider: OpenAI", "  provider: OpenAI\n  apiKeyPassthrough: false"), schemas, ".apiKeyPassthrough"},
		{"metadata labels", replace("  name: cluster-helper", "  labels: {}\n  name: cluster-helper"), schemas, ".metadata.labels"},
		{"metadata annotation null", replace("  name: cluster-helper", "  annotations: null\n  name: cluster-helper"), schemas, ".metadata.annotations"},
		{"status", literal + "status: {}\n", schemas, ".status"},
		{"duplicate key", replace("  provider: OpenAI", "  provider: OpenAI\n  provider: Anthropic"), schemas, "duplicate key"},
		{"duplicate document", literal + "---\n" + literal, schemas, "duplicate resource"},
		{"alias", replace("    systemMessage: Inspect Kubernetes resources using the selected tools.", "    systemMessage: &prompt text\n    stream: *prompt"), schemas, "anchors and aliases"},
		{"merge", replace("  type: Declarative", "  <<: {type: Declarative}\n  type: Declarative"), schemas, "merge"},
		{"custom tag", replace("  model: gpt-4.1-mini", "  model: !custom gpt-4.1-mini"), schemas, "tag"},
		{"wrong collection tag", replace("  openAI:", "  openAI: !!str"), schemas, "tag"},
		{"empty Agent tools", replace("    tools:\n      - type: McpServer\n        mcpServer:\n          name: cluster-tools\n          kind: RemoteMCPServer\n          apiGroup: kagent.dev\n          toolNames: [k8s_get_resources]", "    tools: []"), schemas, ".tools"},
		{"duplicate tool reference", replace("          toolNames: [k8s_get_resources]\n", "          toolNames: [k8s_get_resources]\n      - type: McpServer\n        mcpServer: {name: cluster-tools, kind: RemoteMCPServer, apiGroup: kagent.dev, toolNames: [k8s_get_resources]}\n"), schemas, "duplicate tool server reference"},
		{"duplicate header", literal + "    - name: authorization\n      valueFrom: {type: Secret, name: other-key, key: token}\n", schemas, "duplicate header"},
		{"transport header", replace("- name: Authorization", "- name: Content-Type"), schemas, "transport/protocol header"},
		{"invalid header", replace("- name: Authorization", "- name: 'Invalid Header'"), schemas, "invalid HTTP header"},
		{"invalid key", replace("  apiKeySecretKey: api-key", "  apiKeySecretKey: ../key"), schemas, ".apiKeySecretKey"},
		{"invalid name", replace("  name: cluster-helper", "  name: cluster_helper"), schemas, ".metadata.name"},
		{"invalid URL", replace("http://mcp.demo.svc:8080/mcp", "file:///mcp"), schemas, ".url"},
		{"duplicate schema tool", literal, `{"cluster-tools":{"tools":[{"name":"k8s_get_resources","description":"x","inputSchema":{}},{"name":"k8s_get_resources","description":"x","inputSchema":{}}]}}`, "duplicate tool schema"},
		{"duplicate schema leaf", literal, strings.Replace(schemas, `"additionalProperties": false`, `"additionalProperties": false, "additionalProperties": true`, 1), "duplicate key"},
		{"empty source", "", schemas, "exactly one"},
		{"empty document", literal + "---\n", schemas, "unsupported resource"},
		{"missing prompt", replace("    systemMessage: Inspect Kubernetes resources using the selected tools.\n", ""), schemas, "exactly one"},
		{"null allowed field", replace("  model: gpt-4.1-mini", "  model: null"), schemas, ".model"},
		{"missing Agent tools", replace("    tools:\n      - type: McpServer\n        mcpServer:\n          name: cluster-tools\n          kind: RemoteMCPServer\n          apiGroup: kagent.dev\n          toolNames: [k8s_get_resources]\n", ""), schemas, ".tools"},
		{"empty selection", replace("toolNames: [k8s_get_resources]", "toolNames: []"), schemas, ".toolNames"},
		{"missing selection", replace("          toolNames: [k8s_get_resources]\n", ""), schemas, ".toolNames"},
		{"duplicate selection", replace("toolNames: [k8s_get_resources]", "toolNames: [k8s_get_resources, k8s_get_resources]"), schemas, "duplicate tool"},
		{"missing remote", replace("          name: cluster-tools", "          name: missing"), schemas, "missing RemoteMCPServer"},
		{"missing model", replace("modelConfig: selected-model", "modelConfig: missing"), schemas, "missing ModelConfig"},
		{"wrong apiGroup", replace("apiGroup: kagent.dev", "apiGroup: other.dev"), schemas, ".apiGroup"},
		{"missing kind", replace("          kind: RemoteMCPServer\n", ""), schemas, ".kind"},
		{"wrong protocol", replace("protocol: STREAMABLE_HTTP", "protocol: SSE"), schemas, ".protocol"},
		{"cross namespace reference", replace("          name: cluster-tools", "          name: cluster-tools\n          namespace: other"), schemas, "cross-namespace"},
		{"cross namespace resource", replace("  name: selected-model\n  namespace: demo", "  name: selected-model\n  namespace: other"), schemas, "cross-namespace"},
		{"absent namespace", replace("  namespace: demo\n", ""), schemas, ".namespace"},
		{"userinfo secret", replace("http://mcp.demo.svc:8080/mcp", "https://user:sensitive@mcp.demo.svc/mcp"), schemas, ".url"},
		{"query secret", replace("http://mcp.demo.svc:8080/mcp", "https://mcp.demo.svc/mcp?token=sensitive"), schemas, ".url"},
		{"provider query", replace("https://api.openai.com/v1", "https://api.openai.com/v1?key=sensitive"), schemas, ".baseUrl"},
		{"fragment secret", replace("http://mcp.demo.svc:8080/mcp", "https://mcp.demo.svc/mcp#sensitive"), schemas, ".url"},
		{"literal header", replace("      valueFrom:", "      value: sensitive\n      valueFrom:"), schemas, ".value"},
		{"header configmap", replace("        type: Secret", "        type: ConfigMap"), schemas, ".valueFrom.type"},
		{"TLS false", literal + "  tls: {disableVerify: false}\n", schemas, ".tls"},
		{"timeout", literal + "  timeout: 30s\n", schemas, ".timeout"},
		{"wrong provider", replace("provider: OpenAI", "provider: Gemini"), schemas, ".provider"},
		{"mismatched provider config", replace("  openAI:", "  anthropic:"), schemas, ".anthropic"},
		{"inline api key", replace("  apiKeySecret: llm-key", "  apiKey: sensitive\n  apiKeySecret: llm-key"), schemas, ".apiKey"},
		{"missing secret key", replace("  apiKeySecretKey: api-key\n", ""), schemas, ".apiKeySecretKey"},
		{"secret document", literal + "---\napiVersion: v1\nkind: Secret\nmetadata: {name: private, namespace: demo}\ndata: {token: sensitive}\n", schemas, "Secret"},
		{"unknown doc", literal + "---\napiVersion: v1\nkind: Service\nmetadata: {name: unused, namespace: demo}\n", schemas, "Service"},
		{"unreferenced remote", literal + "---\n" + strings.Replace(strings.Split(literal, "---\n")[2], "name: cluster-tools", "name: unused", 1), schemas, "unreferenced"},
		{"missing schema server", literal, `{}`, "schema"},
		{"missing schema tool", literal, `{"cluster-tools":{"tools":[]}}`, "schema"},
		{"unknown schema field", literal, strings.Replace(schemas, `"description": "Get Kubernetes resources",`, `"description": "Get Kubernetes resources", "outputSchema": {},`, 1), ".outputSchema"},
		{"discovery hints", literal, strings.Replace(schemas, `"description": "Get Kubernetes resources",`, `"description": "Get Kubernetes resources", "annotations": {"readOnlyHint": false},`, 1), ".annotations"},
		{"duplicate snapshot name", literal, `{"cluster-tools":{"tools":[]},"cluster-tools":{"tools":[]}}`, "duplicate key"},
		{"schema null", literal, `{"cluster-tools":{"tools":[{"name":"k8s_get_resources","description":"x","inputSchema":null}]}}`, ".inputSchema"},
		{"schema not json", literal, "cluster-tools: {tools: []}", "JSON"},
		{"unreferenced snapshot", literal, strings.Replace(schemas, "{", `{"unused":{"tools":[]},`, 1), "unreferenced"},
		{"missing prompt configmap", strings.Split(cm, "---\napiVersion: v1")[0], schemas, "missing ConfigMap"},
		{"missing prompt key", strings.Replace(cm, "key: prompt", "key: missing", 1), schemas, "missing ConfigMap key"},
		{"extra prompt key", cm + "  unused: not-preserved\n", schemas, "unreferenced ConfigMap key"},
		{"prompt configmap collision", strings.ReplaceAll(cm, "instructions", "kagent-shim-adapter"), schemas, "collision"},
		{"both prompts", replace("    modelConfig:", "    systemMessageFrom: {type: ConfigMap, name: instructions, key: prompt}\n    modelConfig:"), schemas, "exactly one"},
		{"secret prompt", strings.Replace(cm, "type: ConfigMap", "type: Secret", 1), schemas, ".systemMessageFrom.type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Convert([]byte(tc.input), []byte(tc.schemas))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want refusal containing %q, got %v; output=%s", tc.want, err, b)
			}
			if len(b) != 0 {
				t.Fatalf("partial output on refusal: %s", b)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("credential leaked in diagnostic: %v", err)
			}
		})
	}
}

func TestMultipleServersAndDocumentOrder(t *testing.T) {
	input, schemas := string(fixture(t, "literal.yaml")), string(fixture(t, "schemas.json"))
	input = strings.Replace(input, "          toolNames: [k8s_get_resources]\n", "          toolNames: [k8s_get_resources]\n      - type: McpServer\n        mcpServer: {name: other-tools, kind: RemoteMCPServer, apiGroup: kagent.dev, toolNames: [k8s_describe_resource]}\n", 1)
	input += "---\n" + strings.Replace(strings.Split(input, "---\n")[2], "name: cluster-tools", "name: other-tools", 1)
	schemas = strings.TrimSuffix(strings.TrimSpace(schemas), "}") + "," + strings.TrimPrefix(strings.ReplaceAll(strings.ReplaceAll(schemas, "cluster-tools", "other-tools"), "k8s_get_resources", "k8s_describe_resource"), "{")
	output, err := Convert([]byte(input), []byte(schemas))
	if err != nil {
		t.Fatal(err)
	}
	docs := outputDocs(t, output)
	if len(docs) != 5 {
		t.Fatalf("got %d docs", len(docs))
	}
	refs := docs["Agent/cluster-helper"]["spec"].(map[string]any)["tools"].([]any)
	if len(refs) != 2 || refs[0].(map[string]any)["name"] == refs[1].(map[string]any)["name"] {
		t.Fatal(refs)
	}
	parts := strings.Split(input, "---\n")
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	reordered, err := Convert([]byte(strings.Join(parts, "---\n")), []byte(schemas))
	if err != nil || !bytes.Equal(reordered, output) {
		t.Fatalf("document order changed conversion: %v", err)
	}
}

func TestGeneratedNamesIncludeBothSourceIdentities(t *testing.T) {
	input, schemas := fixture(t, "literal.yaml"), fixture(t, "schemas.json")
	seen := map[string]bool{}
	for _, tc := range []struct{ old, new string }{{"unchanged", "unchanged"}, {"cluster-tools", "other-tools"}, {"k8s_get_resources", "other_tool"}} {
		b, err := Convert(bytes.ReplaceAll(input, []byte(tc.old), []byte(tc.new)), bytes.ReplaceAll(schemas, []byte(tc.old), []byte(tc.new)))
		if err != nil {
			t.Fatal(err)
		}
		refs := outputDocs(t, b)["Agent/cluster-helper"]["spec"].(map[string]any)["tools"].([]any)
		name := refs[0].(map[string]any)["name"].(string)
		if seen[name] {
			t.Fatalf("source identity ignored: %q", name)
		}
		seen[name] = true
	}
}

func TestOptionalProviderBaseURL(t *testing.T) {
	input := bytes.ReplaceAll(fixture(t, "literal.yaml"), []byte("  openAI:\n    baseUrl: https://api.openai.com/v1\n"), nil)
	b, err := Convert(input, fixture(t, "schemas.json"))
	if err != nil {
		t.Fatal(err)
	}
	provider := outputDocs(t, b)["Provider/selected-model"]["spec"].(map[string]any)
	if _, ok := provider["baseURL"]; ok {
		t.Fatal("invented a baseURL")
	}
}

func TestRefuseBuiltInSourceNames(t *testing.T) {
	for _, name := range []string{"web_search", "file_read", "request_approval", "delegate_task", "remember", "create_ai_task", "check_pr_review_marker", "delete_session"} {
		t.Run(name, func(t *testing.T) {
			input := bytes.ReplaceAll(fixture(t, "literal.yaml"), []byte("k8s_get_resources"), []byte(name))
			schemas := bytes.ReplaceAll(fixture(t, "schemas.json"), []byte("k8s_get_resources"), []byte(name))
			if _, err := Convert(input, schemas); err == nil || !strings.Contains(err.Error(), "built-in") {
				t.Fatalf("got %v", err)
			}
		})
	}
}
