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

// A "!!binary" scalar decodes to whatever bytes its base64 payload holds,
// so a document whose raw bytes are valid UTF-8 (the base64 text itself is
// plain ASCII) can still decode into a field that is not: the raw-byte scan
// in ParsePortableAgent cannot see this, because it runs before decoding.
func TestParsePortableAgentRejectsInvalidUTF8InADecodedField(t *testing.T) {
	doc := mustReplace(t, minimalPortableYAML, "  instructions: Do the thing.\n", "  instructions: !!binary /w==\n")
	err := mustNotParse(t, doc, "spec.instructions")
	if !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("error %q does not name the encoding", err)
	}
	if strings.Contains(err.Error(), "\xff") {
		t.Errorf("the refusal echoed the decoded value: %v", err)
	}
}

// spec.instructions is rendered as a literal block scalar and spec.model.name
// as a single-line one, so each is held here to exactly the renderer's
// control-character policy: a document that validated at authoring time and
// then failed to render would be a refusal with no authoring gate behind it.
// The refusal names the field and never the value — the value is what was
// refused, and a control character is not something to print at a terminal.
func TestParsePortableAgentRejectsControlCharactersRenderingWouldRefuse(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement, want, value string }{
		{"instructions carrying a control character", "  instructions: Do the thing.\n",
			"  instructions: \"Do\\u0001the thing.\"\n", "spec.instructions must not contain control characters", "Do\x01the thing."},
		{"instructions carrying a delete", "  instructions: Do the thing.\n",
			"  instructions: \"Do\\u007fthe thing.\"\n", "spec.instructions must not contain control characters", "Do\x7fthe thing."},
		{"a model name spanning lines", "    name: gpt-4o-mini\n",
			"    name: \"gpt-4o\\nmini\"\n", "spec.model.name must not contain line breaks", "gpt-4o\nmini"},
		{"a model name carrying a control character", "    name: gpt-4o-mini\n",
			"    name: \"gpt-4o\\u0001mini\"\n", "spec.model.name must not contain line breaks", "gpt-4o\x01mini"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := mustNotParse(t, mustReplace(t, minimalPortableYAML, tc.old, tc.replacement), tc.want)
			if strings.Contains(err.Error(), tc.value) {
				t.Errorf("the refusal echoed the value: %q", err)
			}
		})
	}
}

// The policy shared for instructions is the BLOCK-scalar one, not the
// single-line one: instructions are multi-line text by nature, and a gate
// that refused a line break would refuse the ordinary case.
func TestParsePortableAgentAcceptsMultiLineInstructions(t *testing.T) {
	doc := mustReplace(t, minimalPortableYAML, "  instructions: Do the thing.\n",
		"  instructions: |\n    Do the thing.\n    \tThen say so.\n")
	agent, err := ParsePortableAgent([]byte(doc))
	if err != nil {
		t.Fatalf("multi-line instructions must parse: %v", err)
	}
	if agent.Spec.Instructions != "Do the thing.\n\tThen say so.\n" {
		t.Errorf("spec.instructions = %q", agent.Spec.Instructions)
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

// Shorthand reaches the same two fields, so it meets the same renderer
// policy before it is ever marshaled: there is no weaker way to encode text
// a renderer would then refuse.
func TestEncodeOrkaShorthandRejectsControlCharactersRenderingWouldRefuse(t *testing.T) {
	for _, tc := range []struct {
		name, want, value string
		build             func(OrkaShorthand) OrkaShorthand
	}{
		{"instructions", "spec.instructions must not contain control characters", "Answer\x01briefly.",
			func(s OrkaShorthand) OrkaShorthand { s.Instructions = "Answer\x01briefly."; return s }},
		{"model", "spec.model.name must not contain line breaks", "gpt-4o\nmini",
			func(s OrkaShorthand) OrkaShorthand { s.Model = "gpt-4o\nmini"; return s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent, err := EncodeOrkaShorthand(tc.build(validShorthand()))
			if err == nil {
				t.Fatal("shorthand a renderer would refuse encoded a document")
			}
			if agent != nil {
				t.Fatal("refused shorthand still produced a document")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Errorf("the refusal echoed the value: %q", err)
			}
		})
	}
	// ...and multi-line instructions still encode and parse back.
	s := validShorthand()
	s.Instructions = "Answer briefly.\nSay plainly when you do not know."
	agent, err := EncodeOrkaShorthand(s)
	if err != nil {
		t.Fatalf("multi-line shorthand instructions must encode: %v", err)
	}
	reparsed, err := ParsePortableAgent(agent.Source())
	if err != nil {
		t.Fatalf("an encoded document must parse: %v", err)
	}
	if reparsed.Spec.Instructions != s.Instructions {
		t.Errorf("round-tripped instructions = %q", reparsed.Spec.Instructions)
	}
}

// Shorthand input reaches the same string fields validate() checks, so the
// same UTF-8 gate applies before this shorthand is ever marshaled: an
// encoded document must never carry a field that cannot round-trip as text.
func TestEncodeOrkaShorthandRejectsInvalidUTF8(t *testing.T) {
	s := validShorthand()
	s.Instructions = "Answer\xffbriefly."
	agent, err := EncodeOrkaShorthand(s)
	if err == nil {
		t.Fatal("shorthand with invalid UTF-8 encoded a document")
	}
	if agent != nil {
		t.Fatal("refused shorthand still produced a document")
	}
	if !strings.Contains(err.Error(), "spec.instructions") || !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("error %q does not name the field and the encoding", err)
	}
	if strings.Contains(err.Error(), "\xffbriefly") {
		t.Errorf("the refusal echoed the invalid value: %v", err)
	}
}

// EncodeOrkaShorthand must not alias the caller's rate limit structs or the
// pointed-to limit values: mutating any of them after encoding must never
// change the returned document, its source bytes, or its digest, because a
// portable identity fixed at encode time cannot depend on what a caller does
// afterward.
func TestEncodeOrkaShorthandCopiesRateLimitsDefensively(t *testing.T) {
	rpm := int32(10)
	tpm := int64(1000)
	s := validShorthand()
	s.ProviderRateLimit = &OrkaRateLimit{RequestsPerMinute: &rpm}
	s.AgentRateLimit = &OrkaRateLimit{TokensPerMinute: &tpm}

	agent, err := EncodeOrkaShorthand(s)
	if err != nil {
		t.Fatalf("valid shorthand must encode: %v", err)
	}
	sourceBefore := string(agent.Source())
	digestBefore := PortableBundleDigest(agent.Source())
	providerRPMBefore := *agent.Extensions.Orka.Provider.RateLimit.RequestsPerMinute
	agentTPMBefore := *agent.Extensions.Orka.Agent.RateLimit.TokensPerMinute

	// Mutate the caller's structs and the values their pointers reach.
	rpm = 999
	tpm = 999999
	s.ProviderRateLimit.RequestsPerMinute = nil
	s.AgentRateLimit.TokensPerMinute = nil
	s.ProviderRateLimit = &OrkaRateLimit{}
	s.AgentRateLimit = &OrkaRateLimit{}

	if got := string(agent.Source()); got != sourceBefore {
		t.Errorf("mutating the caller's rate limits changed Source():\n%s\n---\n%s", got, sourceBefore)
	}
	if got := PortableBundleDigest(agent.Source()); got != digestBefore {
		t.Errorf("mutating the caller's rate limits changed the digest: %s != %s", got, digestBefore)
	}
	if got := *agent.Extensions.Orka.Provider.RateLimit.RequestsPerMinute; got != providerRPMBefore {
		t.Errorf("mutating the caller's rpm value changed the encoded field: %d != %d", got, providerRPMBefore)
	}
	if got := *agent.Extensions.Orka.Agent.RateLimit.TokensPerMinute; got != agentTPMBefore {
		t.Errorf("mutating the caller's tpm value changed the encoded field: %d != %d", got, agentTPMBefore)
	}
}

// A nil rate limit, or one with only one of its two fields set, must encode
// (and clone) without panicking or fabricating the field the caller omitted.
func TestEncodeOrkaShorthandClonesNilAndPartialRateLimits(t *testing.T) {
	rpm := int32(5)
	for _, tc := range []struct {
		name              string
		providerRateLimit *OrkaRateLimit
		agentRateLimit    *OrkaRateLimit
	}{
		{"both nil", nil, nil},
		{"provider partial, agent nil", &OrkaRateLimit{RequestsPerMinute: &rpm}, nil},
		{"provider empty", &OrkaRateLimit{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validShorthand()
			s.ProviderRateLimit = tc.providerRateLimit
			s.AgentRateLimit = tc.agentRateLimit
			agent, err := EncodeOrkaShorthand(s)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if tc.providerRateLimit == nil {
				if agent.Extensions.Orka.Provider.RateLimit != nil {
					t.Errorf("nil provider rate limit was encoded as non-nil")
				}
			} else if agent.Extensions.Orka.Provider.RateLimit == tc.providerRateLimit {
				t.Errorf("provider rate limit was aliased, not cloned")
			}
		})
	}
}
