package runtime

import (
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const validOrkaBindingsYAML = `# Creation target only; other targets supply their own bindings.
apiVersion: kmx.kaimahi.dev/v1alpha1
kind: OrkaBindings
namespace: orka-system
provider:
  type: openai
  baseURL: https://models.example.invalid
  secretRef:
    name: hello-key
    key: api-key
`

func TestOrkaBindingsStrictlyDecodeAndRoundTrip(t *testing.T) {
	bindings, err := ParseOrkaBindings([]byte(validOrkaBindingsYAML))
	if err != nil {
		t.Fatal(err)
	}
	if bindings.Namespace != "orka-system" || bindings.Provider.Type != "openai" || bindings.Provider.BaseURL != "https://models.example.invalid" || bindings.Provider.SecretRef.Name != "hello-key" || bindings.Provider.SecretRef.Key != "api-key" {
		t.Fatalf("bindings lost target fields: %+v", bindings)
	}
	encoded, err := EncodeOrkaBindings(bindings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseOrkaBindings(encoded); err != nil {
		t.Fatalf("encoded bindings did not parse: %v", err)
	}
}

func TestOrkaBindingsRejectUnknownAndAmbiguousFields(t *testing.T) {
	for _, tc := range []struct{ name, doc string }{
		{"unknown", strings.Replace(validOrkaBindingsYAML, "  type: openai\n", "  type: openai\n  credential: not-a-reference\n", 1)},
		{"duplicate", strings.Replace(validOrkaBindingsYAML, "  type: openai\n", "  type: openai\n  type: anthropic\n", 1)},
		{"merge", strings.Replace(validOrkaBindingsYAML, "  type: openai\n", "  <<: {type: anthropic}\n  type: openai\n", 1)},
		{"alias", strings.Replace(validOrkaBindingsYAML, "  type: openai\n", "  type: &model openai\n  baseURL: *model\n", 1)},
		{"extra document", validOrkaBindingsYAML + "---\nkind: ConfigMap\n"},
		{"missing namespace", strings.Replace(validOrkaBindingsYAML, "namespace: orka-system\n", "", 1)},
		{"missing kind", strings.Replace(validOrkaBindingsYAML, "kind: OrkaBindings\n", "", 1)},
		{"missing version", strings.Replace(validOrkaBindingsYAML, "apiVersion: kmx.kaimahi.dev/v1alpha1\n", "", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseOrkaBindings([]byte(tc.doc)); err == nil {
				t.Fatal("accepted invalid target bindings")
			}
		})
	}
}

func TestOrkaBindingsRefuseCredentialShapeBeforeEchoingInvalidInput(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			for _, doc := range []string{
				strings.Replace(validOrkaBindingsYAML, "namespace: orka-system", "namespace: "+shape.Example, 1),
				strings.Replace(validOrkaBindingsYAML, "  type: openai", "  type: "+shape.Example, 1),
				strings.Replace(validOrkaBindingsYAML, "  baseURL: https://models.example.invalid", "  baseURL: "+shape.Example, 1),
				strings.Replace(validOrkaBindingsYAML, "    name: hello-key", "    name: "+shape.Example, 1),
				strings.Replace(validOrkaBindingsYAML, "    key: api-key", "    key: "+shape.Example, 1),
				strings.Replace(validOrkaBindingsYAML, "kind: OrkaBindings", "kind: OrkaBindings\n"+shape.Example+": true", 1),
			} {
				_, err := ParseOrkaBindings([]byte(doc))
				assertRefusedWithoutEcho(t, err, shape.Example)
			}
		})
	}
}

func TestOrkaBindingsRefuseDecodedCredentialShapes(t *testing.T) {
	proven := 0
	for _, shape := range secretshapes.All() {
		if shape.Example == "" || strings.ContainsAny(shape.Example, "\"\\\n") || shape.Example[0] >= 0x80 {
			continue
		}
		for _, doc := range []string{
			strings.Replace(validOrkaBindingsYAML, "    key: api-key", "    key: "+escapedPortableKey(shape.Example), 1),
			strings.Replace(validOrkaBindingsYAML, "namespace: orka-system", escapedPortableKey(shape.Example)+": true\nnamespace: orka-system", 1),
		} {
			if secretshapes.Match(doc) != nil {
				continue
			}
			proven++
			_, err := ParseOrkaBindings([]byte(doc))
			assertRefusedWithoutEcho(t, err, shape.Example)
		}
	}
	if proven == 0 {
		t.Fatal("decoded bindings credential scan was not exercised")
	}
}

func TestOrkaBindingsValidatedBeforeEncoding(t *testing.T) {
	_, err := EncodeOrkaBindings(OrkaBindings{Namespace: "Orka_System"})
	if err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("encoded malformed bindings: %v", err)
	}
}
