package runtime

import (
	"strings"
	"testing"
)

// The fixtures are Go constants rather than testdata files because the
// document IS the schema: a reader changing the struct should see the
// authored shape in the same package, and an editing case below should be a
// one-line change to a document already known to parse.

// portableCore is everything the closed schema requires above the extension.
const portableCore = `apiVersion: kmx.kaimahi.dev/v1alpha1
kind: PortableAgent
metadata:
  name: hello
spec:
  instructions: Do the thing.
  model:
    name: gpt-4o-mini
`

// minimalPortableYAML states only what is required: every optional field is
// absent, which is the shape a hand-authored document is most likely to take
// and the one an accidental required-field addition would break.
const minimalPortableYAML = portableCore + `extensions:
  orka:
    apiVersion: core.orka.ai/v1alpha1
    namespace: orka-system
    provider:
      type: openai
      secretRef:
        name: hello-key
`

// validPortableYAML states every field the closed schema models.
const validPortableYAML = `apiVersion: kmx.kaimahi.dev/v1alpha1
kind: PortableAgent
metadata:
  name: hello
spec:
  instructions: Answer briefly, and say plainly when you do not know.
  model:
    name: gpt-4o-mini
extensions:
  orka:
    apiVersion: core.orka.ai/v1alpha1
    namespace: orka-system
    provider:
      type: openai
      baseURL: https://models.example.invalid
      secretRef:
        name: hello-key
        key: api-key
      rateLimit:
        requestsPerMinute: 60
        tokensPerMinute: 100000
    agent:
      tools:
        - name: web-search
      skills:
        - name: triage
      rateLimit:
        requestsPerMinute: 30
`

func mustReplace(t *testing.T, doc, old, replacement string) string {
	t.Helper()
	if strings.Count(doc, old) != 1 {
		t.Fatalf("fixture does not contain %q exactly once", old)
	}
	return strings.Replace(doc, old, replacement, 1)
}

func mustNotParse(t *testing.T, doc, wantSubstring string) error {
	t.Helper()
	agent, err := ParsePortableAgent([]byte(doc))
	if err == nil {
		t.Fatal("a document that must be refused parsed")
	}
	if agent != nil {
		t.Fatal("a refused document still produced a PortableAgent")
	}
	if wantSubstring != "" && !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("error %q does not mention %q", err, wantSubstring)
	}
	return err
}

func TestParsePortableAgentAcceptsAFullyStatedDocument(t *testing.T) {
	agent, err := ParsePortableAgent([]byte(validPortableYAML))
	if err != nil {
		t.Fatalf("a valid document must parse: %v", err)
	}
	if agent.APIVersion != PortableAPIVersion || agent.Kind != PortableKind {
		t.Errorf("identity = %q/%q", agent.APIVersion, agent.Kind)
	}
	if agent.Metadata.Name != "hello" {
		t.Errorf("metadata.name = %q", agent.Metadata.Name)
	}
	if agent.Spec.Model.Name != "gpt-4o-mini" {
		t.Errorf("spec.model.name = %q", agent.Spec.Model.Name)
	}
	if !strings.HasPrefix(agent.Spec.Instructions, "Answer briefly") {
		t.Errorf("spec.instructions = %q", agent.Spec.Instructions)
	}
	orka := agent.Extensions.Orka
	if orka == nil {
		t.Fatal("extensions.orka is missing")
	}
	if orka.APIVersion != "core.orka.ai/v1alpha1" || orka.Namespace != "orka-system" || orka.Provider.Type != "openai" {
		t.Errorf("extensions.orka = %+v", *orka)
	}
	if orka.Provider.BaseURL != "https://models.example.invalid" {
		t.Errorf("provider.baseURL = %q", orka.Provider.BaseURL)
	}
	if orka.Provider.SecretRef.Name != "hello-key" || orka.Provider.SecretRef.Key != "api-key" {
		t.Errorf("provider.secretRef = %+v", orka.Provider.SecretRef)
	}
	if orka.Provider.RateLimit == nil || *orka.Provider.RateLimit.RequestsPerMinute != 60 ||
		*orka.Provider.RateLimit.TokensPerMinute != 100000 {
		t.Errorf("provider.rateLimit = %+v", orka.Provider.RateLimit)
	}
	if orka.Agent == nil || len(orka.Agent.Tools) != 1 || orka.Agent.Tools[0].Name != "web-search" {
		t.Fatalf("agent.tools = %+v", orka.Agent)
	}
	if len(orka.Agent.Skills) != 1 || orka.Agent.Skills[0].Name != "triage" {
		t.Errorf("agent.skills = %+v", orka.Agent.Skills)
	}
	if orka.Agent.RateLimit == nil || *orka.Agent.RateLimit.RequestsPerMinute != 30 ||
		orka.Agent.RateLimit.TokensPerMinute != nil {
		t.Errorf("agent.rateLimit = %+v", orka.Agent.RateLimit)
	}
}

// An omitted optional field means absent, never a default filled in behind
// the author's back: the adapter that renders this document must be able to
// tell "no rate limit stated" from "a rate limit of zero".
func TestParsePortableAgentLeavesOmittedOptionalFieldsAbsent(t *testing.T) {
	agent, err := ParsePortableAgent([]byte(minimalPortableYAML))
	if err != nil {
		t.Fatalf("a minimal document must parse: %v", err)
	}
	orka := agent.Extensions.Orka
	if orka.Provider.BaseURL != "" || orka.Provider.SecretRef.Key != "" || orka.Provider.RateLimit != nil {
		t.Errorf("an omitted provider field was filled in: %+v", orka.Provider)
	}
	if orka.Agent != nil {
		t.Errorf("an omitted agent block was filled in: %+v", orka.Agent)
	}
}

// The authored bytes are the portable identity, so they are retained exactly
// and handed out defensively: neither the caller's original slice nor any
// returned copy can change what this document will be digested as.
func TestPortableSourceRetainsExactBytesDefensively(t *testing.T) {
	input := []byte(validPortableYAML)
	agent, err := ParsePortableAgent(input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if string(agent.Source()) != validPortableYAML {
		t.Fatal("Source() is not the exact authored bytes")
	}
	input[0] = 'X'
	if string(agent.Source()) != validPortableYAML {
		t.Error("mutating the caller's input changed the retained source")
	}
	first := agent.Source()
	first[0] = 'X'
	if string(agent.Source()) != validPortableYAML {
		t.Error("mutating a returned copy changed the retained source")
	}
}

// The portable digest is framed over these exact bytes through the existing
// digest API, which is why the source is retained rather than reserialized.
func TestPortableSourceFeedsThePortableBundleDigest(t *testing.T) {
	agent, err := ParsePortableAgent([]byte(validPortableYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := PortableBundleDigest(agent.Source()), PortableBundleDigest([]byte(validPortableYAML)); got != want {
		t.Errorf("digest over Source() = %q, over the authored bytes = %q", got, want)
	}
}

// A nil receiver has no source and must say so rather than panic.
func TestPortableSourceOnNilAgent(t *testing.T) {
	var agent *PortableAgent
	if agent.Source() != nil {
		t.Error("a nil PortableAgent reported source bytes")
	}
}

func TestParsePortableAgentRequiresExactIdentity(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement, want string }{
		{"wrong apiVersion", "apiVersion: kmx.kaimahi.dev/v1alpha1\n", "apiVersion: kmx.kaimahi.dev/v1\n", "apiVersion must be"},
		{"wrong kind", "kind: PortableAgent\n", "kind: Agent\n", "kind must be"},
		{"missing apiVersion", "apiVersion: kmx.kaimahi.dev/v1alpha1\n", "", "apiVersion must be"},
		{"missing kind", "kind: PortableAgent\n", "", "kind must be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustNotParse(t, mustReplace(t, minimalPortableYAML, tc.old, tc.replacement), tc.want)
		})
	}
}

func TestParsePortableAgentRequiresExactlyOneYAMLMapping(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"empty", "", "empty or not valid YAML"},
		{"comments only", "# nothing here\n", "empty or not valid YAML"},
		{"unparseable", "apiVersion: [\nkind: PortableAgent\n", "empty or not valid YAML"},
		{"two documents", minimalPortableYAML + "---\n" + minimalPortableYAML, "exactly one YAML document"},
		{"trailing document", minimalPortableYAML + "---\nalso: true\n", "exactly one YAML document"},
		{"sequence root", "- apiVersion: kmx.kaimahi.dev/v1alpha1\n", "single YAML mapping"},
		{"scalar root", "just a string\n", "single YAML mapping"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustNotParse(t, tc.doc, tc.want)
		})
	}
}

// yaml.v3 silently keeps the last occurrence of a repeated key, so a
// document could quietly say two different things about one field. Every
// mapping is walked, including mappings reached only through a sequence.
func TestParsePortableAgentRejectsDuplicateKeysAtEveryLevel(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement, where string }{
		{"top level", "kind: PortableAgent\n", "kind: PortableAgent\nkind: PortableAgent\n", "kind"},
		{"metadata", "  name: hello\n", "  name: hello\n  name: hello\n", "metadata.name"},
		{"spec", "  instructions: Answer", "  instructions: Twice.\n  instructions: Answer", "spec.instructions"},
		{"spec.model", "    name: gpt-4o-mini\n", "    name: gpt-4o-mini\n    name: gpt-4o-mini\n", "spec.model.name"},
		{"extensions", "extensions:\n  orka:\n", "extensions:\n  orka: {}\n  orka:\n", "extensions.orka"},
		{"extension", "    namespace: orka-system\n", "    namespace: orka-system\n    namespace: orka-system\n", "extensions.orka.namespace"},
		{"provider", "      type: openai\n", "      type: openai\n      type: openai\n", "extensions.orka.provider.type"},
		{"provider.secretRef", "        name: hello-key\n", "        name: hello-key\n        name: hello-key\n", "extensions.orka.provider.secretRef.name"},
		{"provider.rateLimit", "        requestsPerMinute: 60\n", "        requestsPerMinute: 60\n        requestsPerMinute: 60\n", "extensions.orka.provider.rateLimit.requestsPerMinute"},
		{"agent", "      skills:\n", "      skills: []\n      skills:\n", "extensions.orka.agent.skills"},
		{"sequence entry", "        - name: web-search\n", "        - name: web-search\n          name: web-search\n", "extensions.orka.agent.tools[0].name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := mustNotParse(t, mustReplace(t, validPortableYAML, tc.old, tc.replacement), "duplicate key")
			if !strings.Contains(err.Error(), tc.where) {
				t.Errorf("error %q does not name the path %q", err, tc.where)
			}
		})
	}
}

// A merge key is resolved by yaml.v3 before the strict decode sees the
// mapping, so neither the duplicate walk nor KnownFields can see what it
// supplied. It is refused wherever it appears rather than resolved.
func TestParsePortableAgentRejectsMergeKeysAnywhere(t *testing.T) {
	for _, tc := range []struct{ name, doc, where string }{
		{
			"top level",
			"apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata: &meta\n  name: hello\n<<: *meta\n",
			"<<",
		},
		{
			"supplying a modeled field under spec",
			"apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nspec:\n  <<: &base\n    instructions: From the anchor.\n  instructions: Stated here.\n",
			"spec.<<",
		},
		{
			"inside the extension",
			"apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nextensions:\n  orka:\n    provider: &p\n      type: openai\n    agent:\n      <<: *p\n",
			"extensions.orka.agent.<<",
		},
		{
			"inside a sequence entry",
			"apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nextensions:\n  orka:\n    provider:\n      secretRef: &ref\n        name: hello-key\n    agent:\n      tools:\n        - <<: *ref\n",
			"extensions.orka.agent.tools[0].<<",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := mustNotParse(t, tc.doc, "merge key")
			if !strings.Contains(err.Error(), tc.where) {
				t.Errorf("error %q does not name the path %q", err, tc.where)
			}
		})
	}
}

// A plain alias copies a value in from elsewhere, so the bytes where a field
// takes effect are not what it means. Nothing here validates an expanded
// alias, so none is accepted.
func TestParsePortableAgentRejectsAliasesAnywhere(t *testing.T) {
	for _, tc := range []struct{ name, doc, where string }{
		{
			"as a mapping value",
			"apiVersion: &v kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata:\n  name: *v\n",
			"metadata.name",
		},
		{
			"as a whole block",
			"apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata: &meta\n  name: hello\nspec: *meta\n",
			"spec",
		},
		{
			"inside a sequence entry",
			"apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata: &meta\n  name: hello\nextensions:\n  orka:\n    agent:\n      tools:\n        - *meta\n",
			"extensions.orka.agent.tools[0]",
		},
		{
			"as a key",
			"apiVersion: &v kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\n*v: hello\n",
			"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := mustNotParse(t, tc.doc, "alias")
			if tc.where != "" && !strings.Contains(err.Error(), tc.where) {
				t.Errorf("error %q does not name the path %q", err, tc.where)
			}
		})
	}
}

// The closed schema has only plain-name keys, so a complex key is neither
// modeled nor comparable for the duplicate walk.
func TestParsePortableAgentRejectsNonScalarKeys(t *testing.T) {
	mustNotParse(t, "apiVersion: kmx.kaimahi.dev/v1alpha1\n? [a, b]\n: c\n", "plain name")
}

func TestParsePortableAgentRejectsUnknownFieldsAtEveryLevel(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{"top level", "kind: PortableAgent\n", "kind: PortableAgent\nbogus: true\n"},
		{"metadata", "  name: hello\n", "  name: hello\n  bogus: true\n"},
		{"spec", "  instructions:", "  bogus: true\n  instructions:"},
		{"spec.model", "    name: gpt-4o-mini\n", "    name: gpt-4o-mini\n    bogus: true\n"},
		{"extensions", "extensions:\n", "extensions:\n  bogus: true\n"},
		{"extension", "    namespace: orka-system\n", "    namespace: orka-system\n    bogus: true\n"},
		{"provider", "      type: openai\n", "      type: openai\n      bogus: true\n"},
		{"provider.secretRef", "        name: hello-key\n", "        name: hello-key\n        bogus: true\n"},
		{"provider.rateLimit", "        requestsPerMinute: 60\n", "        requestsPerMinute: 60\n        bogus: true\n"},
		{"agent", "      skills:\n", "      bogus: true\n      skills:\n"},
		{"agent.tools entry", "        - name: web-search\n", "        - name: web-search\n          bogus: true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustNotParse(t, mustReplace(t, validPortableYAML, tc.old, tc.replacement), "field bogus not found")
		})
	}
}

// The Orka extension is the only extension this schema models and the only
// thing that gives a document a lifecycle target, so exactly it is required.
func TestParsePortableAgentRequiresExactlyTheOrkaExtension(t *testing.T) {
	for _, tc := range []struct{ name, doc, want string }{
		{"no extensions block", portableCore, "extensions.orka is required"},
		{"empty extensions block", portableCore + "extensions: {}\n", "extensions.orka is required"},
		{"null orka extension", portableCore + "extensions:\n  orka:\n", "extensions.orka is required"},
		{"a kagent extension", portableCore + "extensions:\n  kagent:\n    namespace: kagent\n", "field kagent not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustNotParse(t, tc.doc, tc.want)
		})
	}
}

// Every reference this document carries names something a renderer must be
// able to address. A malformed name is refused at authoring time rather than
// becoming an unexplained failure at apply time.
func TestParsePortableAgentRejectsMalformedNames(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement, want string }{
		{"blank metadata.name", "  name: hello\n", "  name: \"\"\n", "metadata.name"},
		{"uppercase metadata.name", "  name: hello\n", "  name: Hello\n", "metadata.name"},
		{"reserved metadata.name", "  name: hello\n", "  name: hello-world\n", "metadata.name"},
		{"blank instructions", "  instructions: Do the thing.\n", "  instructions: \"   \"\n", "spec.instructions is required"},
		{"blank model name", "    name: gpt-4o-mini\n", "    name: \"\"\n", "spec.model.name is required"},
		{"wrong extension apiVersion", "    apiVersion: core.orka.ai/v1alpha1\n", "    apiVersion: core.orka.ai/v1\n", "apiVersion must be"},
		{"malformed namespace", "    namespace: orka-system\n", "    namespace: Orka_System\n", "namespace"},
		{"unknown provider type", "      type: openai\n", "      type: llama\n", "openai or anthropic"},
		{"unscaffoldable provider type", "      type: openai\n", "      type: azure-openai\n", "azure-openai"},
		{"malformed secretRef name", "        name: hello-key\n", "        name: Hello_Key\n", "secretRef.name"},
		{"malformed secretRef key", "        name: hello-key\n", "        name: hello-key\n        key: \"a/b\"\n", "secretRef.key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustNotParse(t, mustReplace(t, minimalPortableYAML, tc.old, tc.replacement), tc.want)
		})
	}
}

// A baseURL is the one reference that could carry a credential inside it, so
// the refusal must never quote it.
func TestParsePortableAgentRejectsMalformedBaseURLsWithoutEchoingThem(t *testing.T) {
	for _, tc := range []struct{ name, baseURL string }{
		{"not a URL", "models.example.invalid"},
		{"not http", "ftp://models.example.invalid"},
		{"no host", "https://"},
		{"embedded credentials", "https://someone:a-passphrase@models.example.invalid"},
		{"query string", "https://models.example.invalid/?token=opaque"},
		{"fragment", "https://models.example.invalid/#frag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, minimalPortableYAML, "      type: openai\n",
				"      type: openai\n      baseURL: \""+tc.baseURL+"\"\n")
			err := mustNotParse(t, doc, "baseURL")
			if strings.Contains(err.Error(), tc.baseURL) {
				t.Errorf("the refusal echoed the URL: %v", err)
			}
		})
	}
}

func TestParsePortableAgentRejectsMalformedToolAndSkillReferences(t *testing.T) {
	for _, tc := range []struct{ name, block, want string }{
		{"server:tool tool syntax", "    agent:\n      tools:\n        - name: files:read\n", "tools"},
		{"blank tool name", "    agent:\n      tools:\n        - name: \"\"\n", "tools"},
		{"server:tool skill syntax", "    agent:\n      skills:\n        - name: pack:triage\n", "skills"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustNotParse(t, minimalPortableYAML+tc.block, tc.want)
		})
	}
}

// A stated rate limit of zero or less is not a limit; it is a document whose
// author meant something the schema cannot express.
func TestParsePortableAgentRejectsNonPositiveRateLimits(t *testing.T) {
	for _, tc := range []struct{ name, block, want string }{
		{"provider requests", "      rateLimit:\n        requestsPerMinute: 0\n", "provider.rateLimit.requestsPerMinute"},
		{"provider tokens", "      rateLimit:\n        tokensPerMinute: -1\n", "provider.rateLimit.tokensPerMinute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, minimalPortableYAML, "      type: openai\n", "      type: openai\n"+tc.block)
			mustNotParse(t, doc, tc.want)
		})
	}
	mustNotParse(t, minimalPortableYAML+"    agent:\n      rateLimit:\n        requestsPerMinute: -5\n",
		"agent.rateLimit.requestsPerMinute")
}

func TestParsePortableAgentRejectsInvalidUTF8(t *testing.T) {
	agent, err := ParsePortableAgent(append([]byte(minimalPortableYAML), 0xff))
	if err == nil || agent != nil {
		t.Fatalf("invalid UTF-8 was accepted: %v", err)
	}
	if !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("error %q does not name the encoding", err)
	}
}

func validShorthand() OrkaShorthand {
	return OrkaShorthand{
		Name:         "hello",
		Namespace:    "orka-system",
		Instructions: "Answer briefly, and say plainly when you do not know.",
		ProviderType: "openai",
		Model:        "gpt-4o-mini",
		SecretName:   "hello-key",
		SecretKey:    "api-key",
		Tools:        []string{"web-search"},
		Skills:       []string{"triage"},
	}
}

// Shorthand is the only other way a portable document comes into existence,
// so it must produce the same kind of artifact: exact bytes that parse back
// to the same document and digest identically every time.
func TestEncodeOrkaShorthandIsDeterministicAndRoundTrips(t *testing.T) {
	first, err := EncodeOrkaShorthand(validShorthand())
	if err != nil {
		t.Fatalf("valid shorthand must encode: %v", err)
	}
	second, err := EncodeOrkaShorthand(validShorthand())
	if err != nil {
		t.Fatalf("valid shorthand must encode: %v", err)
	}
	if string(first.Source()) != string(second.Source()) {
		t.Fatalf("shorthand encoding is not deterministic:\n%s\n---\n%s", first.Source(), second.Source())
	}
	if PortableBundleDigest(first.Source()) != PortableBundleDigest(second.Source()) {
		t.Error("two encodings of the same shorthand have different portable identities")
	}
	reparsed, err := ParsePortableAgent(first.Source())
	if err != nil {
		t.Fatalf("encoded shorthand must parse as a portable document: %v", err)
	}
	if string(reparsed.Source()) != string(first.Source()) {
		t.Error("re-parsing an encoded document changed its source bytes")
	}
	orka := reparsed.Extensions.Orka
	if orka.Namespace != "orka-system" || orka.Provider.SecretRef.Key != "api-key" {
		t.Errorf("round-tripped extension = %+v", *orka)
	}
	if orka.Agent == nil || len(orka.Agent.Tools) != 1 || orka.Agent.Tools[0].Name != "web-search" {
		t.Errorf("round-tripped tools = %+v", orka.Agent)
	}
}

// Nothing the caller did not state appears in the encoded document: an
// omitted tool list must not become an empty agent block a renderer would
// then have to interpret.
func TestEncodeOrkaShorthandOmitsUnstatedOptionalFields(t *testing.T) {
	s := validShorthand()
	s.Tools, s.Skills, s.SecretKey = nil, nil, ""
	agent, err := EncodeOrkaShorthand(s)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if agent.Extensions.Orka.Agent != nil {
		t.Errorf("an unstated agent block was encoded: %+v", agent.Extensions.Orka.Agent)
	}
	for _, unwanted := range []string{"agent:", "tools:", "skills:", "key:", "baseURL:", "rateLimit:"} {
		if strings.Contains(string(agent.Source()), unwanted) {
			t.Errorf("encoded document states %q, which the caller did not", unwanted)
		}
	}
}

// Shorthand is validated by exactly the same rules as an authored document:
// there is no second, weaker way in.
func TestEncodeOrkaShorthandRefusesWhatAnAuthoredDocumentWouldFail(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(OrkaShorthand) OrkaShorthand
		want  string
	}{
		{"name", func(s OrkaShorthand) OrkaShorthand { s.Name = "Hello"; return s }, "metadata.name"},
		{"namespace", func(s OrkaShorthand) OrkaShorthand { s.Namespace = "Orka_System"; return s }, "namespace"},
		{"provider type", func(s OrkaShorthand) OrkaShorthand { s.ProviderType = "llama"; return s }, "openai or anthropic"},
		{"model", func(s OrkaShorthand) OrkaShorthand { s.Model = ""; return s }, "spec.model.name is required"},
		{"instructions", func(s OrkaShorthand) OrkaShorthand { s.Instructions = " "; return s }, "spec.instructions is required"},
		{"secret name", func(s OrkaShorthand) OrkaShorthand { s.SecretName = "Hello_Key"; return s }, "secretRef.name"},
		{"secret key", func(s OrkaShorthand) OrkaShorthand { s.SecretKey = "a/b"; return s }, "secretRef.key"},
		{"tool", func(s OrkaShorthand) OrkaShorthand { s.Tools = []string{"files:read"}; return s }, "tools"},
		{"base URL", func(s OrkaShorthand) OrkaShorthand { s.BaseURL = "ftp://models.example.invalid"; return s }, "baseURL"},
		{"rate limit", func(s OrkaShorthand) OrkaShorthand {
			zero := int32(0)
			s.ProviderRateLimit = &OrkaRateLimit{RequestsPerMinute: &zero}
			return s
		}, "rateLimit.requestsPerMinute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent, err := EncodeOrkaShorthand(tc.build(validShorthand()))
			if err == nil {
				t.Fatal("invalid shorthand encoded a document")
			}
			if agent != nil {
				t.Fatal("refused shorthand still produced a document")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}
