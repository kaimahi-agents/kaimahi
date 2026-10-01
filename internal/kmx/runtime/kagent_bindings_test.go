package runtime

import (
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const validKagentBindingsYAML = `# Creation target only; other targets supply their own bindings.
apiVersion: kmx.kaimahi.dev/v1alpha1
kind: KagentBindings
namespace: agents
modelConfig:
    provider: openai
    baseURL: https://models.example.invalid/v1
    secretRef:
        name: review-key
        key: api-key
`

func validKagentBindings() KagentBindings {
	return KagentBindings{
		Namespace: "agents",
		ModelConfig: KagentModelConfigBindings{
			Provider: "openai",
			BaseURL:  "https://models.example.invalid/v1",
			SecretRef: KagentSecretRefBindings{
				Name: "review-key",
				Key:  "api-key",
			},
		},
	}
}

func TestKagentRuntimeID(t *testing.T) {
	if Kagent != ID("kagent") {
		t.Fatalf("Kagent runtime ID = %q", Kagent)
	}
}

func TestKagentBindingsStrictlyDecodeAndRoundTrip(t *testing.T) {
	bindings, err := ParseKagentBindings([]byte(validKagentBindingsYAML))
	if err != nil {
		t.Fatal(err)
	}
	if bindings.Namespace != "agents" || bindings.ModelConfig.Provider != "openai" ||
		bindings.ModelConfig.BaseURL != "https://models.example.invalid/v1" ||
		bindings.ModelConfig.SecretRef.Name != "review-key" || bindings.ModelConfig.SecretRef.Key != "api-key" {
		t.Fatalf("bindings lost target fields: %+v", bindings)
	}
	encoded, err := EncodeKagentBindings(bindings)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != validKagentBindingsYAML {
		t.Fatalf("bindings encoding changed:\ngot:\n%s\nwant:\n%s", encoded, validKagentBindingsYAML)
	}
	if _, err := ParseKagentBindings(encoded); err != nil {
		t.Fatalf("encoded bindings did not parse: %v", err)
	}
}

func TestKagentBindingsAcceptBothProviders(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			doc := strings.Replace(validKagentBindingsYAML, "    provider: openai", "    provider: "+provider, 1)
			bindings, err := ParseKagentBindings([]byte(doc))
			if err != nil {
				t.Fatal(err)
			}
			if bindings.ModelConfig.Provider != provider {
				t.Fatalf("provider = %q", bindings.ModelConfig.Provider)
			}
		})
	}
}

func TestEncodeKagentBindingsDefaultsOnlyTheDocumentKey(t *testing.T) {
	bindings := validKagentBindings()
	bindings.ModelConfig.SecretRef.Key = ""
	encoded, err := EncodeKagentBindings(bindings)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "        key: "+scaffold.DefaultKagentSecretKey+"\n") {
		t.Fatalf("default key not stated in encoded bindings:\n%s", encoded)
	}
	if bindings.ModelConfig.SecretRef.Key != "" {
		t.Fatal("encoder mutated caller-owned bindings")
	}
	withoutKey := strings.Replace(validKagentBindingsYAML, "        key: api-key\n", "", 1)
	if _, err := ParseKagentBindings([]byte(withoutKey)); err == nil || !strings.Contains(err.Error(), "secretRef.key") {
		t.Fatalf("parser accepted an implicit key: %v", err)
	}
}

func TestEncodeKagentBindingsIsDeterministic(t *testing.T) {
	first, err := EncodeKagentBindings(validKagentBindings())
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeKagentBindings(validKagentBindings())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("Kagent bindings encoding is not deterministic:\n%s\n---\n%s", first, second)
	}
}

func TestValidateKagentBindingsRequiresClosedIdentity(t *testing.T) {
	bindings := validKagentBindings()
	if err := ValidateKagentBindings(bindings); err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("validator accepted an unstated document identity: %v", err)
	}
	bindings.APIVersion = KagentBindingsAPIVersion
	if err := ValidateKagentBindings(bindings); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("validator accepted an unstated kind: %v", err)
	}
	bindings.Kind = KagentBindingsKind
	if err := ValidateKagentBindings(bindings); err != nil {
		t.Fatalf("validator rejected a fully stated identity: %v", err)
	}
}

func TestKagentBindingsRejectUnknownAmbiguousAndIncompleteDocuments(t *testing.T) {
	for _, tc := range []struct{ name, doc string }{
		{"unknown", strings.Replace(validKagentBindingsYAML, "    provider: openai\n", "    provider: openai\n    value: forbidden\n", 1)},
		{"legacy value field", strings.Replace(validKagentBindingsYAML, "        key: api-key\n", "        key: api-key\n        value: forbidden\n", 1)},
		{"duplicate", strings.Replace(validKagentBindingsYAML, "    provider: openai\n", "    provider: openai\n    provider: anthropic\n", 1)},
		{"merge", strings.Replace(validKagentBindingsYAML, "    provider: openai\n", "    <<: {provider: anthropic}\n    provider: openai\n", 1)},
		{"alias", strings.Replace(validKagentBindingsYAML, "    provider: openai\n", "    provider: &provider openai\n    baseURL: *provider\n", 1)},
		{"extra document", validKagentBindingsYAML + "---\nkind: ConfigMap\n"},
		{"missing apiVersion", strings.Replace(validKagentBindingsYAML, "apiVersion: kmx.kaimahi.dev/v1alpha1\n", "", 1)},
		{"missing kind", strings.Replace(validKagentBindingsYAML, "kind: KagentBindings\n", "", 1)},
		{"missing namespace", strings.Replace(validKagentBindingsYAML, "namespace: agents\n", "", 1)},
		{"missing provider", strings.Replace(validKagentBindingsYAML, "    provider: openai\n", "", 1)},
		{"missing secret name", strings.Replace(validKagentBindingsYAML, "        name: review-key\n", "", 1)},
		{"sequence root", "- apiVersion: kmx.kaimahi.dev/v1alpha1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseKagentBindings([]byte(tc.doc)); err == nil {
				t.Fatal("accepted invalid Kagent bindings")
			}
		})
	}
}

func TestKagentBindingsValidateProviderURLAndNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*KagentBindings)
		want string
	}{
		{"provider alias", func(b *KagentBindings) { b.ModelConfig.Provider = "OpenAI" }, "openai or anthropic"},
		{"namespace", func(b *KagentBindings) { b.Namespace = "Agents" }, "namespace"},
		{"secret name", func(b *KagentBindings) { b.ModelConfig.SecretRef.Name = "review/key" }, "secretRef.name"},
		{"secret key", func(b *KagentBindings) { b.ModelConfig.SecretRef.Key = "review/key" }, "secretRef.key"},
		{"base URL", func(b *KagentBindings) { b.ModelConfig.BaseURL = "https://user:password@example.invalid" }, "baseURL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bindings := validKagentBindings()
			tc.edit(&bindings)
			_, err := EncodeKagentBindings(bindings)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid bindings encoded: %v", err)
			}
			if tc.name == "base URL" && strings.Contains(err.Error(), bindings.ModelConfig.BaseURL) {
				t.Fatal("base URL refusal echoed its value")
			}
		})
	}
}

func TestKagentBindingsRejectInvalidUTF8(t *testing.T) {
	if _, err := ParseKagentBindings(append([]byte(validKagentBindingsYAML), 0xff)); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid raw UTF-8 accepted: %v", err)
	}
	bindings := validKagentBindings()
	bindings.ModelConfig.SecretRef.Name = "review\xffkey"
	if _, err := EncodeKagentBindings(bindings); err == nil || !strings.Contains(err.Error(), "UTF-8") || strings.Contains(err.Error(), "\xffkey") {
		t.Fatalf("invalid decoded UTF-8 accepted or echoed: %v", err)
	}
	decoded := strings.Replace(validKagentBindingsYAML, "        key: api-key", "        key: !!binary /w==", 1)
	if _, err := ParseKagentBindings([]byte(decoded)); err == nil || !strings.Contains(err.Error(), "UTF-8") || strings.Contains(err.Error(), "\xff") {
		t.Fatalf("binary-tagged invalid UTF-8 accepted or echoed: %v", err)
	}
}

func TestKagentBindingsNeverAcceptCredentialValues(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			for _, doc := range []string{
				strings.Replace(validKagentBindingsYAML, "namespace: agents", "namespace: "+shape.Example, 1),
				strings.Replace(validKagentBindingsYAML, "    provider: openai", "    provider: "+shape.Example, 1),
				strings.Replace(validKagentBindingsYAML, "    baseURL: https://models.example.invalid/v1", "    baseURL: "+shape.Example, 1),
				strings.Replace(validKagentBindingsYAML, "        name: review-key", "        name: "+shape.Example, 1),
				strings.Replace(validKagentBindingsYAML, "        key: api-key", "        key: "+shape.Example, 1),
			} {
				_, err := ParseKagentBindings([]byte(doc))
				assertRefusedWithoutEcho(t, err, shape.Example)
			}
		})
	}
}

func TestKagentBindingsRefuseCredentialShapesAssembledByYAML(t *testing.T) {
	proven := 0
	for _, shape := range secretshapes.All() {
		if shape.Example == "" || shape.Example[0] >= 0x80 || strings.ContainsAny(shape.Example, "\"\\\n") {
			continue
		}
		for _, doc := range []string{
			strings.Replace(validKagentBindingsYAML, "        key: api-key", "        key: "+escapedPortableKey(shape.Example), 1),
			strings.Replace(validKagentBindingsYAML, "namespace: agents", escapedPortableKey(shape.Example)+": true\nnamespace: agents", 1),
		} {
			if secretshapes.Match(doc) != nil {
				continue
			}
			proven++
			_, err := ParseKagentBindings([]byte(doc))
			assertRefusedWithoutEcho(t, err, shape.Example)
		}
	}
	if proven == 0 {
		t.Fatal("decoded Kagent bindings credential scan was not exercised")
	}
}
