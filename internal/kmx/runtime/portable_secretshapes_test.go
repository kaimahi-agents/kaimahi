package runtime

import (
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// Credential scanning must be the FIRST thing either portable entry point
// does, because every gate after it quotes what it refused: yaml.v3 quotes
// the token it choked on, the duplicate/merge-key walk names the key at its
// path, and field validation prints the offending value. A credential pasted
// into a field that is also invalid would otherwise be echoed back by
// whichever gate failed first — into a terminal, a CI log and, for the flag
// shorthand, whatever captured the command's stderr.
//
// Both halves are asserted on every declared shape, in every field an author
// can reach: the refusal names the shape CLASS, and the value itself appears
// nowhere in the message.

// assertRefusedWithoutEcho proves a refusal happened, that it named a
// credential shape rather than the field's own validation complaint, and
// that it did not carry the value.
func assertRefusedWithoutEcho(t *testing.T, err error, value string) {
	t.Helper()
	if err == nil {
		t.Fatal("a credential-shaped value was accepted")
	}
	if strings.Contains(err.Error(), value) {
		t.Fatalf("the refusal echoed the credential-shaped value: %v", err)
	}
	if !strings.Contains(err.Error(), "shaped like") {
		t.Fatalf("the refusal does not name the shape class: %v", err)
	}
}

// The shorthand is flag input, so every flag is covered: namespace, Secret
// name and key, tools, skills, model, base URL, name, provider type and
// instructions. Each case ALSO leaves the rest of the shorthand incomplete
// or the field itself invalid, so a scan running after validation would be
// visible as the validation error instead.
func TestEncodeOrkaShorthandRefusesCredentialShapesBeforeValidation(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			for _, tc := range []struct {
				field string
				build func(string) OrkaShorthand
			}{
				{"namespace", func(v string) OrkaShorthand { s := validShorthand(); s.Namespace = v; return s }},
				{"secret name", func(v string) OrkaShorthand { s := validShorthand(); s.SecretName = v; return s }},
				{"secret key", func(v string) OrkaShorthand { s := validShorthand(); s.SecretKey = v; return s }},
				{"tools", func(v string) OrkaShorthand { s := validShorthand(); s.Tools = []string{v}; return s }},
				{"skills", func(v string) OrkaShorthand { s := validShorthand(); s.Skills = []string{v}; return s }},
				{"model", func(v string) OrkaShorthand { s := validShorthand(); s.Model = v; return s }},
				{"base URL", func(v string) OrkaShorthand { s := validShorthand(); s.BaseURL = v; return s }},
				{"name", func(v string) OrkaShorthand { s := validShorthand(); s.Name = v; return s }},
				{"provider type", func(v string) OrkaShorthand { s := validShorthand(); s.ProviderType = v; return s }},
				{"instructions", func(v string) OrkaShorthand { s := validShorthand(); s.Instructions = v; return s }},
			} {
				t.Run(tc.field, func(t *testing.T) {
					agent, err := EncodeOrkaShorthand(tc.build(shape.Example))
					assertRefusedWithoutEcho(t, err, shape.Example)
					if agent != nil {
						t.Fatal("a refused shorthand still produced a document")
					}
				})
			}
		})
	}
}

// A shorthand that is credential-shaped AND independently invalid is still
// refused by shape: the scan cannot be reached only when everything else
// passes, or it would be a gate that stops working exactly when a document
// is malformed enough to quote.
func TestEncodeOrkaShorthandScansBeforeAnInvalidFieldCanEcho(t *testing.T) {
	shape := secretshapes.All()[0]
	s := validShorthand()
	// Not a valid namespace by any rule, so ValidateNamespace would refuse
	// it — and would print it — if the scan ran second.
	s.Namespace = "NOT A NAMESPACE " + shape.Example
	_, err := EncodeOrkaShorthand(s)
	assertRefusedWithoutEcho(t, err, shape.Example)
}

// The --file half: representative fields across the document, including the
// nested Orka extension an author is most likely to paste a key into.
func TestParsePortableAgentRefusesCredentialShapesBeforeParsing(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			for _, tc := range []struct {
				field, placeholder string
			}{
				{"metadata.name", "name: hello"},
				{"spec.model.name", "name: gpt-4o-mini"},
				{"orka namespace", "namespace: orka-system"},
				{"orka secretRef.name", "name: hello-key"},
				{"orka secretRef.key", "key: api-key"},
				{"orka tool name", "- name: web-search"},
				{"orka skill name", "- name: triage"},
				{"kagent modelConfigRef.name", "name: local-ollama"},
			} {
				t.Run(tc.field, func(t *testing.T) {
					field, _, _ := strings.Cut(tc.placeholder, ":")
					doc := mustReplace(t, validPortableYAML(t), tc.placeholder, field+": "+shape.Example)
					agent, err := ParsePortableAgent([]byte(doc))
					assertRefusedWithoutEcho(t, err, shape.Example)
					if agent != nil {
						t.Fatal("a refused document still parsed")
					}
				})
			}

			// A baseURL is not in the fixture; state it, which also proves a
			// field the document may add is covered rather than a fixed list.
			t.Run("orka provider.baseURL", func(t *testing.T) {
				doc := mustReplace(t, validPortableYAML(t), "      type: openai",
					"      type: openai\n      baseURL: https://example.invalid/"+shape.Example)
				assertRefusedWithoutEcho(t, parseError(t, doc), shape.Example)
			})
		})
	}
}

// The scan must beat the YAML parser itself. A document that is malformed
// AND credential-shaped is the case where the value would otherwise reach a
// log inside yaml.v3's own quoted error.
func TestParsePortableAgentScansRawBytesBeforeYAMLAndNodeGates(t *testing.T) {
	shape := secretshapes.All()[0]
	for _, tc := range []struct {
		name, doc string
	}{
		{"unparseable YAML", "apiVersion: [\nkey: " + shape.Example},
		{"duplicate key", mustDuplicate(shape.Example)},
		{"merge key", "apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nspec:\n  <<: &base\n    instructions: " + shape.Example + "\n"},
		{"unknown field", "apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nnotAField: " + shape.Example + "\n"},
		{"two documents", "apiVersion: a\n---\napiVersion: " + shape.Example + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePortableAgent([]byte(tc.doc))
			assertRefusedWithoutEcho(t, err, shape.Example)
		})
	}
}

// Invalid UTF-8 is refused too, but the scan still runs first: a document
// can carry both a credential and a stray byte.
func TestParsePortableAgentScansBeforeTheUTF8Gate(t *testing.T) {
	shape := secretshapes.All()[0]
	_, err := ParsePortableAgent(append([]byte("instructions: "+shape.Example+"\n"), 0xff))
	assertRefusedWithoutEcho(t, err, shape.Example)
}

// Every declared shape's counterexample must still be accepted: a scan that
// refused ordinary values would be a gate nobody could author past.
func TestPortableCredentialScanAcceptsCounterexamples(t *testing.T) {
	for _, shape := range secretshapes.All() {
		if shape.Counterexample == "" {
			continue
		}
		t.Run(shape.Name, func(t *testing.T) {
			s := validShorthand()
			s.Instructions = "Answer using " + shape.Counterexample
			if _, err := EncodeOrkaShorthand(s); err != nil {
				t.Fatalf("a counterexample was refused as a credential: %v", err)
			}
		})
	}
}

// The committed valid fixtures keep parsing: the scan is a refusal for
// credential shapes, not a new restriction on authored documents.
func TestPortableValidFixturesStillParseAfterTheRawScan(t *testing.T) {
	for _, doc := range []string{validPortableYAML(t), minimalPortableYAML()} {
		if _, err := ParsePortableAgent([]byte(doc)); err != nil {
			t.Fatalf("a valid fixture was refused: %v", err)
		}
	}
	if _, err := EncodeOrkaShorthand(validShorthand()); err != nil {
		t.Fatalf("valid shorthand was refused: %v", err)
	}
}

func validShorthand() OrkaShorthand {
	return OrkaShorthand{
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
}

func parseError(t *testing.T, doc string) error {
	t.Helper()
	_, err := ParsePortableAgent([]byte(doc))
	return err
}

func mustDuplicate(value string) string {
	return "apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata:\n  name: hello\n  name: " + value + "\n"
}
