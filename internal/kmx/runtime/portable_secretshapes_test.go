package runtime

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// Credential scanning must be the FIRST thing either portable entry point
// does, because every gate after it quotes what it refused: yaml.v3 quotes
// the token it choked on, the duplicate/merge/alias walk names the key at
// its path, and field validation prints the offending value. A credential
// pasted into a field that is ALSO malformed would otherwise be echoed by
// whichever gate failed first — into a terminal, a CI log, and whatever
// captured the command's stderr.
//
// The shapes themselves are never written here. Every case takes its value
// from secretshapes.All(), which assembles each example from parts at run
// time, so this file carries no credential-shaped literal for the tree scan
// (scripts/check-secret-shapes.py) to find — and a shape added to
// shapes.json is covered here without editing this file.

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

// Every field an author can reach in an authored document, for every
// declared shape. Each replacement also leaves the field independently
// invalid or the document unparseable, so a scan running after any other
// gate would show up as that gate's error instead.
func TestParsePortableAgentRefusesCredentialShapesBeforeAnythingCanEchoThem(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			for _, tc := range []struct{ field, old, replacement string }{
				{"metadata.name", "  name: hello\n", "  name: %s\n"},
				{"spec.instructions", "  instructions: Do the thing.\n", "  instructions: %s\n"},
				{"spec.model.name", "    name: gpt-4o-mini\n", "    name: %s\n"},
				{"extension namespace", "    namespace: orka-system\n", "    namespace: %s\n"},
				{"provider.type", "      type: openai\n", "      type: %s\n"},
				{"provider.baseURL", "      type: openai\n", "      type: openai\n      baseURL: https://models.example.invalid/%s\n"},
				{"provider.secretRef.name", "        name: hello-key\n", "        name: %s\n"},
				{"provider.secretRef.key", "        name: hello-key\n", "        name: hello-key\n        key: %s\n"},
				{"agent.tools[0].name", "        name: hello-key\n", "        name: hello-key\n    agent:\n      tools:\n        - name: %s\n"},
				{"agent.skills[0].name", "        name: hello-key\n", "        name: hello-key\n    agent:\n      skills:\n        - name: %s\n"},
				{"an unknown field", "kind: PortableAgent\n", "kind: PortableAgent\nbogus: %s\n"},
			} {
				t.Run(tc.field, func(t *testing.T) {
					doc := mustReplace(t, minimalPortableYAML, tc.old,
						strings.Replace(tc.replacement, "%s", shape.Example, 1))
					agent, err := ParsePortableAgent([]byte(doc))
					assertRefusedWithoutEcho(t, err, shape.Example)
					if agent != nil {
						t.Fatal("a refused document still parsed")
					}
				})
			}
		})
	}
}

// The scan must beat the YAML parser and the node walk. A document that is
// malformed AND credential-shaped is exactly the case where the value would
// otherwise reach a log inside another gate's quoted error.
func TestParsePortableAgentScansRawBytesBeforeEveryStructuralGate(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			for _, tc := range []struct{ name, doc string }{
				{"unparseable YAML", "apiVersion: [\nkey: " + shape.Example},
				{"duplicate key", "kind: PortableAgent\nmetadata:\n  name: hello\n  name: " + shape.Example + "\n"},
				{"merge key", "kind: PortableAgent\nspec:\n  <<: &base\n    instructions: " + shape.Example + "\n"},
				{"alias", "kind: &k " + shape.Example + "\nmetadata:\n  name: *k\n"},
				{"two documents", "kind: PortableAgent\n---\nkind: " + shape.Example + "\n"},
				{"not a mapping", "- " + shape.Example + "\n"},
				{"invalid UTF-8", "instructions: " + shape.Example + "\n\xff"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					_, err := ParsePortableAgent([]byte(tc.doc))
					assertRefusedWithoutEcho(t, err, shape.Example)
				})
			}
		})
	}
}

// A double-quoted scalar can spell a credential across an escaped line
// break: the authored bytes never contain the shape, but the decoded value
// does. The raw scan cannot see this, which is why the scan also runs over
// the decoded values — and still before any check that would quote them.
func TestParsePortableAgentRefusesCredentialShapesSpelledAcrossLineBreaks(t *testing.T) {
	proven := 0
	for _, shape := range secretshapes.All() {
		// Only shapes that survive double quoting unescaped can be folded
		// this way; the rest are covered by the field cases above.
		if strings.ContainsAny(shape.Example, "\"\\\n") || len(shape.Example) < 4 {
			continue
		}
		half := len(shape.Example) / 2
		doc := mustReplace(t, minimalPortableYAML, "  instructions: Do the thing.\n",
			"  instructions: \""+shape.Example[:half]+"\\\n    "+shape.Example[half:]+"\"\n")
		if secretshapes.Match(doc) != nil {
			continue // The raw bytes still carry it; proves nothing here.
		}
		proven++
		t.Run(shape.Name, func(t *testing.T) {
			_, err := ParsePortableAgent([]byte(doc))
			assertRefusedWithoutEcho(t, err, shape.Example)
		})
	}
	if proven == 0 {
		t.Fatal("no declared shape could be hidden from the raw scan; this gate is unproven")
	}
}

// A `!!binary` tag is the other way authored bytes can hide a credential
// the decoded document carries: the raw scan sees base64, the string field
// gets the decoded value.
func TestParsePortableAgentRefusesBase64TaggedCredentialShapes(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			encoded := base64.StdEncoding.EncodeToString([]byte(shape.Example))
			doc := mustReplace(t, minimalPortableYAML, "  instructions: Do the thing.\n",
				"  instructions: !!binary "+encoded+"\n")
			if secretshapes.Match(doc) != nil {
				t.Skip("the raw bytes still carry the shape; proves nothing here")
			}
			_, err := ParsePortableAgent([]byte(doc))
			assertRefusedWithoutEcho(t, err, shape.Example)
		})
	}
}

// A mapping KEY can spell a credential the authored bytes do not contain:
// "\x67hp_..." is nothing in the file and a token once decoded. Keys are
// exactly where that matters, because the gates that refuse a key name it —
// the duplicate/merge/alias walk quotes the key and the path it sits at,
// and the strict decoder's "field X not found" quotes the unknown field —
// and a key is never a value the closed schema models, so the scan over the
// decoded struct cannot see one. The scan therefore also runs over every
// decoded scalar node, before either gate.
func TestParsePortableAgentRefusesCredentialShapedKeysAssembledAtDecodeTime(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(*testing.T, string) string
	}{
		{"an unknown field", func(t *testing.T, key string) string {
			return mustReplace(t, minimalPortableYAML, "kind: PortableAgent\n", "kind: PortableAgent\n"+key+": true\n")
		}},
		{"a duplicate key", func(t *testing.T, key string) string {
			return mustReplace(t, minimalPortableYAML, "  name: hello\n",
				"  name: hello\n  "+key+": one\n  "+key+": two\n")
		}},
		{"the path a refused key is named at", func(t *testing.T, key string) string {
			return mustReplace(t, minimalPortableYAML, "  instructions: Do the thing.\n",
				"  instructions: Do the thing.\n  "+key+":\n    <<: &base\n      a: b\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proven := 0
			for _, shape := range secretshapes.All() {
				// Only a shape that survives double quoting unescaped can
				// be written as a key this way.
				if shape.Example == "" || shape.Example[0] >= 0x80 || strings.ContainsAny(shape.Example, "\"\\\n") {
					continue
				}
				doc := tc.build(t, escapedPortableKey(shape.Example))
				if secretshapes.Match(doc) != nil {
					continue // The raw bytes still carry it; proves nothing here.
				}
				proven++
				t.Run(shape.Name, func(t *testing.T) {
					_, err := ParsePortableAgent([]byte(doc))
					assertRefusedWithoutEcho(t, err, shape.Example)
				})
			}
			if proven == 0 {
				t.Fatal("no declared shape could be hidden from the raw scan; this gate is unproven")
			}
		})
	}
}

// escapedPortableKey renders value as a double-quoted YAML key whose first
// byte is written as a \xNN escape, so the authored bytes carry no shape and
// the decoder assembles one. The credential is never written here: it comes
// from secretshapes.All(), which joins it from parts at run time.
func escapedPortableKey(value string) string {
	return fmt.Sprintf("\"\\x%02x%s\"", value[0], value[1:])
}

// The same argument for a "!!binary" key: the raw scan sees base64, and the
// strict decoder resolves the payload before naming the field it did not
// find.
func TestParsePortableAgentRefusesBase64TaggedCredentialShapedKeys(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			encoded := base64.StdEncoding.EncodeToString([]byte(shape.Example))
			doc := mustReplace(t, minimalPortableYAML, "kind: PortableAgent\n",
				"kind: PortableAgent\n!!binary "+encoded+": true\n")
			if secretshapes.Match(doc) != nil {
				t.Skip("the raw bytes still carry the shape; proves nothing here")
			}
			_, err := ParsePortableAgent([]byte(doc))
			assertRefusedWithoutEcho(t, err, shape.Example)
		})
	}
}

// The shorthand half. Each case also leaves the shorthand independently
// invalid, so a scan running after validation would be visible as the
// validation error instead.
func TestEncodeOrkaShorthandRefusesCredentialShapesBeforeValidation(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			for _, tc := range []struct {
				field string
				build func(string) OrkaShorthand
			}{
				{"name", func(v string) OrkaShorthand { s := validShorthand(); s.Name = v; return s }},
				{"namespace", func(v string) OrkaShorthand { s := validShorthand(); s.Namespace = v; return s }},
				{"instructions", func(v string) OrkaShorthand { s := validShorthand(); s.Instructions = v; return s }},
				{"provider type", func(v string) OrkaShorthand { s := validShorthand(); s.ProviderType = v; return s }},
				{"model", func(v string) OrkaShorthand { s := validShorthand(); s.Model = v; return s }},
				{"base URL", func(v string) OrkaShorthand { s := validShorthand(); s.BaseURL = v; return s }},
				{"secret name", func(v string) OrkaShorthand { s := validShorthand(); s.SecretName = v; return s }},
				{"secret key", func(v string) OrkaShorthand { s := validShorthand(); s.SecretKey = v; return s }},
				{"tools", func(v string) OrkaShorthand { s := validShorthand(); s.Tools = []string{v}; return s }},
				{"skills", func(v string) OrkaShorthand { s := validShorthand(); s.Skills = []string{v}; return s }},
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

// A shorthand that is credential-shaped AND independently invalid by another
// rule is still refused by shape: the scan cannot be a gate that stops
// working exactly when the rest of the input is malformed enough to quote.
func TestEncodeOrkaShorthandScansBeforeAnInvalidFieldCanEcho(t *testing.T) {
	shape := secretshapes.All()[0]
	s := validShorthand()
	// Not a namespace by any rule, so ValidateNamespace would refuse it —
	// and would print it — if the scan ran second.
	s.Namespace = "NOT A NAMESPACE " + shape.Example
	_, err := EncodeOrkaShorthand(s)
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
