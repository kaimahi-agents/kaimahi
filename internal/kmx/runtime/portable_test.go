package runtime

import (
	"os"
	"strings"
	"testing"
)

func validPortableYAML(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/portable-agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// minimalPortableYAML has no extensions: it exercises the top-level
// requirements in isolation from Orka/kagent-specific validation.
func minimalPortableYAML() string {
	return `apiVersion: kmx.kaimahi.dev/v1alpha1
kind: PortableAgent
metadata:
  name: hello
spec:
  instructions: Do the thing.
  model:
    name: gpt-4o-mini
`
}

func mustReplace(t *testing.T, doc, old, new string) string {
	t.Helper()
	if !strings.Contains(doc, old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return strings.Replace(doc, old, new, 1)
}

func TestPortableValidDocumentParses(t *testing.T) {
	src := validPortableYAML(t)
	agent, err := ParsePortableAgent([]byte(src))
	if err != nil {
		t.Fatalf("valid document must parse: %v", err)
	}
	if agent.Metadata.Name != "hello" {
		t.Errorf("metadata.name = %q", agent.Metadata.Name)
	}
	if agent.Spec.Model.Name != "gpt-4o-mini" {
		t.Errorf("spec.model.name = %q", agent.Spec.Model.Name)
	}
	if agent.Extensions.Orka == nil || agent.Extensions.Orka.Namespace != "orka-system" {
		t.Fatalf("extensions.orka: %#v", agent.Extensions.Orka)
	}
	if agent.Extensions.Orka.SecretRef.Name != "hello-key" {
		t.Errorf("extensions.orka.secretRef.name = %q", agent.Extensions.Orka.SecretRef.Name)
	}
	if len(agent.Extensions.Orka.Tools) != 1 || agent.Extensions.Orka.Tools[0] != "web-search" {
		t.Errorf("extensions.orka.tools = %#v", agent.Extensions.Orka.Tools)
	}
	if agent.Extensions.Kagent == nil || agent.Extensions.Kagent.Harness.Name != "kagent" {
		t.Fatalf("extensions.kagent: %#v", agent.Extensions.Kagent)
	}
	if len(agent.Extensions.Kagent.Skills) != 1 || agent.Extensions.Kagent.Skills[0].Name != "triage" {
		t.Errorf("extensions.kagent.skills = %#v", agent.Extensions.Kagent.Skills)
	}
	if string(agent.Source()) != src {
		t.Errorf("Source() does not return the exact input bytes")
	}
}

func TestPortableRequiresExactAPIVersionAndKind(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"wrong apiVersion", "apiVersion: kmx.kaimahi.dev/v1alpha1\n", "apiVersion: kmx.kaimahi.dev/v2\n"},
		{"wrong kind", "kind: PortableAgent\n", "kind: Agent\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, minimalPortableYAML(), tc.old, tc.new)
			if _, err := ParsePortableAgent([]byte(doc)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestPortableRejectsMultipleDocuments(t *testing.T) {
	t.Run("two documents", func(t *testing.T) {
		doc := minimalPortableYAML() + "---\n" + minimalPortableYAML()
		if _, err := ParsePortableAgent([]byte(doc)); err == nil {
			t.Fatal("expected an error for a second YAML document")
		}
	})
	t.Run("empty input", func(t *testing.T) {
		if _, err := ParsePortableAgent([]byte("")); err == nil {
			t.Fatal("expected an error for an empty document")
		}
	})
}

func TestPortableRejectsDuplicateKeysAtEveryLevel(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{
			"top level",
			"apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nkind: PortableAgent\n",
		},
		{
			"metadata",
			"metadata:\n  name: hello\n  name: hello\n",
		},
		{
			"spec",
			"spec:\n  instructions: a\n  instructions: a\n",
		},
		{
			"spec.model",
			"spec:\n  model:\n    name: a\n    name: a\n",
		},
		{
			"extensions",
			"extensions:\n  orka:\n    namespace: a\n  orka:\n    namespace: b\n",
		},
		{
			"extensions.orka",
			"extensions:\n  orka:\n    namespace: a\n    namespace: b\n",
		},
		{
			"extensions.orka.provider",
			"extensions:\n  orka:\n    provider:\n      type: openai\n      type: openai\n",
		},
		{
			"extensions.kagent",
			"extensions:\n  kagent:\n    namespace: a\n    namespace: b\n",
		},
		{
			"extensions.kagent.tools list entry",
			"extensions:\n  kagent:\n    tools:\n      - server: fs\n        name: read\n        name: read\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePortableAgent([]byte(tc.doc))
			if err == nil {
				t.Fatal("expected a duplicate-key error")
			}
			if !strings.Contains(err.Error(), "duplicate") {
				t.Errorf("error does not mention duplicate: %v", err)
			}
		})
	}
}

func TestPortableRejectsUnknownFieldsAtEveryLevel(t *testing.T) {
	base := validPortableYAML(t)
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"top level", "kind: PortableAgent\n", "kind: PortableAgent\nbogus: true\n"},
		{"metadata", "metadata:\n  name: hello\n", "metadata:\n  name: hello\n  bogus: true\n"},
		{"spec", "spec:\n  instructions:", "spec:\n  bogus: true\n  instructions:"},
		{"spec.model", "model:\n    name: gpt-4o-mini\n", "model:\n    name: gpt-4o-mini\n    bogus: true\n"},
		{"extensions", "extensions:\n  orka:", "extensions:\n  bogus: true\n  orka:"},
		{"extensions.orka", "namespace: orka-system\n", "namespace: orka-system\n    bogus: true\n"},
		{"extensions.orka.provider", "type: openai\n", "type: openai\n      bogus: true\n"},
		{"extensions.orka.secretRef", "name: hello-key\n", "name: hello-key\n      bogus: true\n"},
		{"extensions.kagent", "namespace: kagent-system\n", "namespace: kagent-system\n    bogus: true\n"},
		{"extensions.kagent.harness", "harness:\n      name: kagent\n", "harness:\n      name: kagent\n      bogus: true\n"},
		{"extensions.kagent.tools list entry", "- server: filesystem\n        name: read-file\n", "- server: filesystem\n        name: read-file\n        bogus: true\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, base, tc.old, tc.new)
			_, err := ParsePortableAgent([]byte(doc))
			if err == nil {
				t.Fatal("expected an unknown-field error")
			}
			if !strings.Contains(err.Error(), "not found") {
				t.Errorf("error does not mention the unknown field: %v", err)
			}
		})
	}
}

func TestPortableRequiresNameInstructionsModel(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"missing metadata.name", "  name: hello\n", "  name: \"\"\n"},
		{"missing spec.instructions", "instructions: Do the thing.\n", "instructions: \"\"\n"},
		{"missing spec.model.name", "    name: gpt-4o-mini\n", "    name: \"\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, minimalPortableYAML(), tc.old, tc.new)
			if _, err := ParsePortableAgent([]byte(doc)); err == nil {
				t.Fatal("expected a required-field error")
			}
		})
	}
}

func TestPortableExtensionsRequireAPIVersionAndNamespace(t *testing.T) {
	base := validPortableYAML(t)
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"orka missing apiVersion", "apiVersion: core.orka.ai/v1alpha1\n", "apiVersion: \"\"\n"},
		{"orka missing namespace", "namespace: orka-system\n", "namespace: \"\"\n"},
		{"kagent missing apiVersion", "apiVersion: kagent.dev/v1alpha3\n", "apiVersion: \"\"\n"},
		{"kagent missing namespace", "namespace: kagent-system\n", "namespace: \"\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, base, tc.old, tc.new)
			if _, err := ParsePortableAgent([]byte(doc)); err == nil {
				t.Fatal("expected an extension apiVersion/namespace error")
			}
		})
	}
}

func TestPortableRejectsInlineSecretValues(t *testing.T) {
	// Assembled at run time, never as a literal credential shape in the tree.
	secret := "sk-" + strings.Repeat("A", 24)

	t.Run("top-level instructions", func(t *testing.T) {
		doc := mustReplace(t, minimalPortableYAML(), "Do the thing.", "Do the thing. Token "+secret)
		_, err := ParsePortableAgent([]byte(doc))
		if err == nil {
			t.Fatal("expected a credential-shape error")
		}
		if !strings.Contains(err.Error(), "credential") {
			t.Errorf("error does not name the credential: %v", err)
		}
	})

	t.Run("nested extension field", func(t *testing.T) {
		doc := mustReplace(t, validPortableYAML(t), "Answer plainly and cite sources.", "Answer plainly. Token "+secret)
		if _, err := ParsePortableAgent([]byte(doc)); err == nil {
			t.Fatal("expected a credential-shape error from a nested extension field")
		}
	})
}

func TestPortableRejectsRuntimeExtensionMismatch(t *testing.T) {
	base := validPortableYAML(t)
	t.Run("orka apiVersion is kagent's", func(t *testing.T) {
		doc := mustReplace(t, base, "apiVersion: core.orka.ai/v1alpha1\n", "apiVersion: kagent.dev/v1alpha3\n")
		if _, err := ParsePortableAgent([]byte(doc)); err == nil {
			t.Fatal("expected a runtime/extension mismatch error")
		}
	})
	t.Run("kagent apiVersion is orka's", func(t *testing.T) {
		doc := mustReplace(t, base, "apiVersion: kagent.dev/v1alpha3\n", "apiVersion: core.orka.ai/v1alpha1\n")
		if _, err := ParsePortableAgent([]byte(doc)); err == nil {
			t.Fatal("expected a runtime/extension mismatch error")
		}
	})
}

func TestPortableKagentSkillAndPluginIdentitiesAreImmutable(t *testing.T) {
	base := validPortableYAML(t)
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{
			"missing skill name",
			"skills:\n      - name: triage\n",
			"skills:\n      - name: \"\"\n",
		},
		{
			"duplicate skill name",
			"skills:\n      - name: triage\n",
			"skills:\n      - name: triage\n      - name: triage\n",
		},
		{
			"missing plugin name",
			"plugins:\n      - name: audit-log\n",
			"plugins:\n      - name: \"\"\n",
		},
		{
			"duplicate plugin name",
			"plugins:\n      - name: audit-log\n",
			"plugins:\n      - name: audit-log\n      - name: audit-log\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, base, tc.old, tc.new)
			if _, err := ParsePortableAgent([]byte(doc)); err == nil {
				t.Fatal("expected an immutable-identity error")
			}
		})
	}
}

func TestPortableSourceIsDefensivelyCopied(t *testing.T) {
	original := []byte(validPortableYAML(t))
	input := make([]byte, len(original))
	copy(input, original)

	agent, err := ParsePortableAgent(input)
	if err != nil {
		t.Fatalf("valid document must parse: %v", err)
	}

	// Mutating the caller's slice after Decode must not change the result.
	for i := range input {
		input[i] = '#'
	}
	if string(agent.Source()) != string(original) {
		t.Fatal("Source() reflects mutation of the caller's original slice")
	}

	// Mutating one returned copy must not change the next.
	first := agent.Source()
	for i := range first {
		first[i] = '#'
	}
	if string(agent.Source()) != string(original) {
		t.Fatal("Source() returned a shared, not a defensive, copy")
	}
}

func TestPortableOrkaShorthandRoundTrip(t *testing.T) {
	shorthand := OrkaShorthand{
		Name:         "hello",
		Namespace:    "orka-system",
		Instructions: "You are a helpful, careful agent.",
		ProviderType: "openai",
		Model:        "gpt-4o-mini",
		SecretName:   "hello-key",
		SecretKey:    "api-key",
		Tools:        []string{"web-search"},
		Skills:       []string{"triage"},
	}

	agent, err := EncodeOrkaShorthand(shorthand)
	if err != nil {
		t.Fatalf("EncodeOrkaShorthand: %v", err)
	}

	first, err := agent.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	second, err := agent.YAML()
	if err != nil {
		t.Fatalf("YAML (second call): %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("Orka shorthand serialization is not deterministic")
	}

	parsed, err := ParsePortableAgent(first)
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if parsed.Metadata.Name != shorthand.Name {
		t.Errorf("metadata.name = %q", parsed.Metadata.Name)
	}
	if parsed.Spec.Model.Name != shorthand.Model {
		t.Errorf("spec.model.name = %q", parsed.Spec.Model.Name)
	}
	if parsed.Extensions.Kagent != nil {
		t.Errorf("Orka shorthand must not synthesize a kagent extension")
	}
	if parsed.Extensions.Orka == nil {
		t.Fatal("Orka shorthand must produce an orka extension")
	}
	if parsed.Extensions.Orka.SecretRef.Name != shorthand.SecretName {
		t.Errorf("extensions.orka.secretRef.name = %q", parsed.Extensions.Orka.SecretRef.Name)
	}
	if len(parsed.Extensions.Orka.Tools) != 1 || parsed.Extensions.Orka.Tools[0] != "web-search" {
		t.Errorf("extensions.orka.tools = %#v", parsed.Extensions.Orka.Tools)
	}
}
