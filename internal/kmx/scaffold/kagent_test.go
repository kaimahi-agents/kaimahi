package scaffold

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

func kagentSpec() KagentSpec {
	return KagentSpec{
		Name:         "reviewer",
		Namespace:    "agents",
		Description:  "Reviews changes",
		Runtime:      "go",
		Instructions: "Review carefully.\nState uncertainty plainly.",
		ProviderType: "openai",
		Model:        "gpt-4o-mini",
		BaseURL:      "https://models.example.invalid/v1",
		SecretName:   "reviewer-key",
		SecretKey:    "api-key",
		Tools: []KagentMCPToolBinding{{
			ServerKind: "RemoteMCPServer",
			ServerName: "cluster-tools",
			ToolNames:  []string{"get_resources", "describe-resource"},
		}},
	}
}

func kagentBundle(t *testing.T, spec KagentSpec) *KagentBundle {
	t.Helper()
	bundle, err := GenerateKagent(spec)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestKagentBundleRendersExactV0102Maps(t *testing.T) {
	spec := kagentSpec()
	bundle := kagentBundle(t, spec)
	wantSecret := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": "reviewer-key", "namespace": "agents"},
	}
	if !reflect.DeepEqual(bundle.Secret, wantSecret) {
		t.Fatalf("Secret is not metadata-only: %#v", bundle.Secret)
	}
	wantModel := map[string]any{
		"apiVersion": "kagent.dev/v1alpha2",
		"kind":       "ModelConfig",
		"metadata":   map[string]any{"name": "reviewer", "namespace": "agents"},
		"spec": map[string]any{
			"provider":        "OpenAI",
			"model":           "gpt-4o-mini",
			"apiKeySecret":    "reviewer-key",
			"apiKeySecretKey": "api-key",
			"openAI":          map[string]any{"baseUrl": "https://models.example.invalid/v1"},
		},
	}
	if !reflect.DeepEqual(bundle.ModelConfig, wantModel) {
		t.Fatalf("ModelConfig = %#v", bundle.ModelConfig)
	}
	wantAgent := map[string]any{
		"apiVersion": "kagent.dev/v1alpha2",
		"kind":       "Agent",
		"metadata":   map[string]any{"name": "reviewer", "namespace": "agents"},
		"spec": map[string]any{
			"description": "Reviews changes",
			"type":        "Declarative",
			"declarative": map[string]any{
				"runtime":       "go",
				"modelConfig":   "reviewer",
				"systemMessage": "Review carefully.\nState uncertainty plainly.",
				"tools": []any{map[string]any{
					"type": "McpServer",
					"mcpServer": map[string]any{
						"apiGroup": "kagent.dev", "kind": "RemoteMCPServer", "name": "cluster-tools",
						"toolNames": []any{"get_resources", "describe-resource"},
					},
				}},
				"deployment": map[string]any{
					"podSecurityContext": map[string]any{
						"runAsNonRoot":   true,
						"seccompProfile": map[string]any{"type": "RuntimeDefault"},
					},
					"securityContext": map[string]any{
						"allowPrivilegeEscalation": false,
						"capabilities":             map[string]any{"drop": []any{"ALL"}},
						"readOnlyRootFilesystem":   true,
					},
					"volumes":      []any{map[string]any{"name": "tmp", "emptyDir": map[string]any{}}},
					"volumeMounts": []any{map[string]any{"name": "tmp", "mountPath": "/tmp"}},
				},
			},
		},
	}
	if !reflect.DeepEqual(bundle.Agent, wantAgent) {
		t.Fatalf("Agent = %#v", bundle.Agent)
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestKagentProviderMappingAndOptionalBaseURL(t *testing.T) {
	for _, tc := range []struct {
		provider, wantProvider, wantBlock string
	}{
		{"openai", "OpenAI", "openAI"},
		{"anthropic", "Anthropic", "anthropic"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			spec := kagentSpec()
			spec.ProviderType = tc.provider
			spec.BaseURL = ""
			model := kagentBundle(t, spec).ModelConfig["spec"].(map[string]any)
			if model["provider"] != tc.wantProvider {
				t.Fatalf("provider = %v", model["provider"])
			}
			if block, ok := model[tc.wantBlock].(map[string]any); !ok || len(block) != 0 {
				t.Fatalf("provider block = %#v", model[tc.wantBlock])
			}
			other := "anthropic"
			if tc.wantBlock == other {
				other = "openAI"
			}
			if _, exists := model[other]; exists {
				t.Fatalf("unselected provider block %q exists", other)
			}
		})
	}
}

func TestKagentDefaultKeyIsExplicitInRenderedModelConfig(t *testing.T) {
	spec := kagentSpec()
	spec.SecretKey = ""
	bundle := kagentBundle(t, spec)
	got := bundle.ModelConfig["spec"].(map[string]any)["apiKeySecretKey"]
	if got != DefaultKagentSecretKey {
		t.Fatalf("default Secret key = %v", got)
	}
}

func TestKagentArtifactOrderHeaderAndDeterminism(t *testing.T) {
	bundle := kagentBundle(t, kagentSpec())
	first, err := bundle.YAML()
	if err != nil {
		t.Fatal(err)
	}
	second, err := bundle.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("Kagent artifact serialization is not deterministic")
	}
	for _, required := range []string{"exact Kagent v0.10.2", "Secret skeleton must not be applied"} {
		if !strings.Contains(first, required) {
			t.Fatalf("artifact header missing %q:\n%s", required, first)
		}
	}
	if strings.Contains(first, "runAsUser:") {
		t.Fatal("artifact guessed a Kagent image UID")
	}
	for _, required := range []string{
		"runAsNonRoot: true", "allowPrivilegeEscalation: false", "readOnlyRootFilesystem: true",
		"type: RuntimeDefault", "drop:", "- ALL", "mountPath: /tmp", "emptyDir: {}",
	} {
		if !strings.Contains(first, required) {
			t.Fatalf("artifact omitted hardened workload field %q:\n%s", required, first)
		}
	}
	decoder := yaml.NewDecoder(strings.NewReader(first))
	var kinds []string
	for {
		var doc map[string]any
		if err := decoder.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, doc["kind"].(string))
	}
	if !reflect.DeepEqual(kinds, []string{"Secret", "ModelConfig", "Agent"}) {
		t.Fatalf("artifact order = %v", kinds)
	}
}

func TestKagentArtifactRejectsWrongOrderAndInvalidContent(t *testing.T) {
	bundle := kagentBundle(t, kagentSpec())
	documents := make([][]byte, 0, 3)
	for _, doc := range bundle.Documents() {
		encoded, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		documents = append(documents, encoded)
	}
	if out, err := KagentArtifact([][]byte{documents[1], documents[0], documents[2]}); err == nil || out != "" {
		t.Fatal("artifact accepted documents out of review order")
	}
	if out, err := KagentArtifact(documents[:2]); err == nil || out != "" {
		t.Fatal("artifact accepted an incomplete bundle")
	}
	mutated := append([][]byte(nil), documents...)
	mutated[2] = []byte(strings.Replace(string(mutated[2]), "runtime: go", "runtime: rust", 1))
	if out, err := KagentArtifact(mutated); err == nil || out != "" {
		t.Fatal("artifact accepted structurally invalid final bytes")
	}
	extra := append([]byte(nil), documents[2]...)
	extra = append(extra, []byte("---\nkind: ConfigMap\n")...)
	if out, err := KagentArtifact([][]byte{documents[0], documents[1], extra}); err == nil || out != "" {
		t.Fatal("artifact accepted an extra YAML document")
	}
	duplicate := append([]byte(nil), documents[2]...)
	duplicate = append(duplicate, []byte("kind: Agent\n")...)
	if out, err := KagentArtifact([][]byte{documents[0], documents[1], duplicate}); err == nil || out != "" {
		t.Fatal("artifact accepted a duplicate YAML key")
	}
	badUTF8 := append([]byte(nil), documents[2]...)
	badUTF8 = append(badUTF8, 0xff)
	if out, err := KagentArtifact([][]byte{documents[0], documents[1], badUTF8}); err == nil || out != "" {
		t.Fatal("artifact accepted invalid UTF-8")
	}
}

func TestKagentArtifactCanonicalizesValidDocumentFraming(t *testing.T) {
	bundle := kagentBundle(t, kagentSpec())
	documents := make([][]byte, 0, 3)
	for _, doc := range bundle.Documents() {
		encoded, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		documents = append(documents, encoded)
	}
	documents[0] = bytes.TrimSuffix(documents[0], []byte("\n"))
	documents[1] = append([]byte("---\n"), documents[1]...)
	artifact, err := KagentArtifact(documents)
	if err != nil {
		t.Fatalf("valid YAML framing was refused: %v", err)
	}
	decoder := yaml.NewDecoder(strings.NewReader(artifact))
	var kinds []string
	for {
		var doc map[string]any
		if err := decoder.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, doc["kind"].(string))
	}
	if !reflect.DeepEqual(kinds, []string{"Secret", "ModelConfig", "Agent"}) {
		t.Fatalf("canonical artifact kinds = %v", kinds)
	}
}

func TestKagentAgentWithoutToolsOmitsTools(t *testing.T) {
	spec := kagentSpec()
	spec.Tools = nil
	declarative := kagentBundle(t, spec).Agent["spec"].(map[string]any)["declarative"].(map[string]any)
	if _, exists := declarative["tools"]; exists {
		t.Fatal("unstated MCP tools rendered")
	}
}

func TestKagentSupportsBothExplicitDeclarativeRuntimes(t *testing.T) {
	for _, runtimeName := range []string{"go", "python"} {
		t.Run(runtimeName, func(t *testing.T) {
			spec := kagentSpec()
			spec.Runtime = runtimeName
			declarative := kagentBundle(t, spec).Agent["spec"].(map[string]any)["declarative"].(map[string]any)
			if declarative["runtime"] != runtimeName {
				t.Fatalf("runtime = %v", declarative["runtime"])
			}
		})
	}
}

func TestKagentRejectsUnsafeInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*KagentSpec)
	}{
		{"name", func(s *KagentSpec) { s.Name = "Reviewer" }},
		{"namespace", func(s *KagentSpec) { s.Namespace = "Agents" }},
		{"description omitted", func(s *KagentSpec) { s.Description = "" }},
		{"description newline", func(s *KagentSpec) { s.Description = "review\nkind: Secret" }},
		{"runtime omitted", func(s *KagentSpec) { s.Runtime = "" }},
		{"runtime alias", func(s *KagentSpec) { s.Runtime = "golang" }},
		{"instructions blank", func(s *KagentSpec) { s.Instructions = " \n\t" }},
		{"model", func(s *KagentSpec) { s.Model = "" }},
		{"provider alias", func(s *KagentSpec) { s.ProviderType = "OpenAI" }},
		{"base URL", func(s *KagentSpec) { s.BaseURL = "https://user:password@example.invalid" }},
		{"Secret name", func(s *KagentSpec) { s.SecretName = "review/key" }},
		{"Secret collides with Agent", func(s *KagentSpec) { s.SecretName = s.Name }},
		{"Secret key", func(s *KagentSpec) { s.SecretKey = "review/key" }},
		{"MCP kind", func(s *KagentSpec) { s.Tools[0].ServerKind = "MCPServer" }},
		{"MCP server", func(s *KagentSpec) { s.Tools[0].ServerName = "cluster:read" }},
		{"empty allowlist", func(s *KagentSpec) { s.Tools[0].ToolNames = nil }},
		{"tool shortcut", func(s *KagentSpec) { s.Tools[0].ToolNames[0] = "cluster-tools:get_resources" }},
		{"duplicate tool", func(s *KagentSpec) { s.Tools[0].ToolNames = []string{"read", "read"} }},
		{"duplicate server", func(s *KagentSpec) { s.Tools = append(s.Tools, s.Tools[0]) }},
		{"multiple servers", func(s *KagentSpec) {
			s.Tools = append(s.Tools, KagentMCPToolBinding{ServerKind: KagentRemoteMCPServerKind, ServerName: "other-tools", ToolNames: []string{"read"}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := kagentSpec()
			tc.edit(&spec)
			if bundle, err := GenerateKagent(spec); err == nil || bundle != nil {
				t.Fatalf("unsafe input produced bundle: %#v, %v", bundle, err)
			}
		})
	}
}

func TestKagentBundleValidationRejectsStructuralMutation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*KagentBundle)
	}{
		{"Secret data", func(b *KagentBundle) { b.Secret["data"] = map[string]any{} }},
		{"Secret metadata", func(b *KagentBundle) { b.Secret["metadata"].(map[string]any)["labels"] = map[string]any{} }},
		{"version", func(b *KagentBundle) { b.Agent["apiVersion"] = "kagent.dev/v1alpha1" }},
		{"namespace", func(b *KagentBundle) { b.Agent["metadata"].(map[string]any)["namespace"] = "other" }},
		{"model ref", func(b *KagentBundle) {
			b.Agent["spec"].(map[string]any)["declarative"].(map[string]any)["modelConfig"] = "other"
		}},
		{"runtime", func(b *KagentBundle) {
			delete(b.Agent["spec"].(map[string]any)["declarative"].(map[string]any), "runtime")
		}},
		{"security context", func(b *KagentBundle) {
			b.Agent["spec"].(map[string]any)["declarative"].(map[string]any)["deployment"].(map[string]any)["securityContext"].(map[string]any)["readOnlyRootFilesystem"] = false
		}},
		{"tmp mount", func(b *KagentBundle) {
			delete(b.Agent["spec"].(map[string]any)["declarative"].(map[string]any)["deployment"].(map[string]any), "volumeMounts")
		}},
		{"tool apiGroup", func(b *KagentBundle) {
			b.Agent["spec"].(map[string]any)["declarative"].(map[string]any)["tools"].([]any)[0].(map[string]any)["mcpServer"].(map[string]any)["apiGroup"] = "other.dev"
		}},
		{"provider arm", func(b *KagentBundle) { b.ModelConfig["spec"].(map[string]any)["anthropic"] = map[string]any{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := kagentBundle(t, kagentSpec())
			tc.edit(bundle)
			if err := bundle.Validate(); err == nil {
				t.Fatal("mutated bundle validated")
			}
			if text, err := bundle.YAML(); err == nil || text != "" {
				t.Fatal("mutated bundle rendered")
			}
		})
	}
	var nilBundle *KagentBundle
	if err := nilBundle.Validate(); err == nil {
		t.Fatal("nil bundle validated")
	}
}

func TestKagentRejectsCredentialShapesAcrossInputsAndOutput(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			for _, edit := range []func(*KagentSpec){
				func(s *KagentSpec) { s.Name = shape.Example },
				func(s *KagentSpec) { s.Namespace = shape.Example },
				func(s *KagentSpec) { s.Description = shape.Example },
				func(s *KagentSpec) { s.Runtime = shape.Example },
				func(s *KagentSpec) { s.Instructions = shape.Example },
				func(s *KagentSpec) { s.ProviderType = shape.Example },
				func(s *KagentSpec) { s.Model = shape.Example },
				func(s *KagentSpec) { s.BaseURL = shape.Example },
				func(s *KagentSpec) { s.SecretName = shape.Example },
				func(s *KagentSpec) { s.SecretKey = shape.Example },
				func(s *KagentSpec) { s.Tools[0].ServerName = shape.Example },
				func(s *KagentSpec) { s.Tools[0].ToolNames[0] = shape.Example },
			} {
				spec := kagentSpec()
				edit(&spec)
				if _, err := GenerateKagent(spec); err == nil || strings.Contains(err.Error(), shape.Example) {
					t.Fatal("credential-shaped input was accepted or echoed")
				}
			}
			bundle := kagentBundle(t, kagentSpec())
			bundle.Agent["spec"].(map[string]any)["description"] = shape.Example
			if out, err := bundle.YAML(); err == nil || out != "" || strings.Contains(err.Error(), shape.Example) {
				t.Fatal("credential-shaped mutated output rendered or echoed")
			}
		})
	}
}
