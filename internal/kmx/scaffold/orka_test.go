package scaffold

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"math"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

func orkaSpec() OrkaSpec {
	return OrkaSpec{Name: "sample", Namespace: "orka-system", ProviderType: "openai", Model: "test", SecretName: "model-key"}
}

func orkaBundle(t *testing.T, spec OrkaSpec) *OrkaBundle {
	t.Helper()
	b, err := GenerateOrka(spec)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOrkaBundleShapeAndReferences(t *testing.T) {
	spec := orkaSpec()
	spec.Tools, spec.Skills = []string{"web_search", "fetch-page"}, []string{"research"}
	spec.Description = "A researcher"
	b := orkaBundle(t, spec)
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	if b.Task != nil || len(b.Documents()) != 3 {
		t.Fatal("Task must be opt-in")
	}
	wantSecret := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "model-key", "namespace": "orka-system"}}
	if !reflect.DeepEqual(b.Secret, wantSecret) {
		t.Fatalf("Secret must be metadata-only: %#v", b.Secret)
	}
	provider := b.Provider["spec"].(map[string]any)
	wantProvider := map[string]any{"type": "openai", "defaultModel": "test", "secretRef": map[string]any{"name": "model-key", "key": "api-key"}}
	if !reflect.DeepEqual(provider, wantProvider) {
		t.Fatalf("Provider: %#v", provider)
	}
	agent := b.Agent["spec"].(map[string]any)
	if !reflect.DeepEqual(agent["providerRef"], map[string]any{"name": "sample", "namespace": "orka-system"}) {
		t.Fatalf("Provider reference: %#v", agent)
	}
	for key, want := range map[string]any{
		"tools":  []any{map[string]any{"name": "web_search"}, map[string]any{"name": "fetch-page"}},
		"skills": []any{map[string]any{"name": "research"}},
	} {
		if !reflect.DeepEqual(agent[key], want) {
			t.Errorf("%s: %#v", key, agent[key])
		}
	}
	if got := b.Agent["metadata"].(map[string]any)["annotations"].(map[string]any)["kaimahi.dev/description"]; got != spec.Description {
		t.Errorf("description annotation: %v", got)
	}
	out, err := b.YAML("test provenance")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"kagent", "ModelConfig", "podTemplate", "\nstringData:", "\ndata:"} {
		if strings.Contains(out, bad) {
			t.Errorf("unexpected %q in output", bad)
		}
	}
	for _, want := range []string{"Orka", "test provenance", "watches", "Provider", "Ready", "Agent", "Secret", "CEL", "admission"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing guidance %q", want)
		}
	}
}

func TestOrkaRejectsIncompleteOrUnsafeInputs(t *testing.T) {
	cases := map[string]func(*OrkaSpec){
		"name":                         func(s *OrkaSpec) { s.Name = "" },
		"invalid name":                 func(s *OrkaSpec) { s.Name = "Bad/name" },
		"namespace":                    func(s *OrkaSpec) { s.Namespace = "" },
		"long namespace":               func(s *OrkaSpec) { s.Namespace = strings.Repeat("a", 64) },
		"provider type":                func(s *OrkaSpec) { s.ProviderType = "" },
		"unknown provider":             func(s *OrkaSpec) { s.ProviderType = "local" },
		"azure deployment unsupported": func(s *OrkaSpec) { s.ProviderType = "azure-openai" },
		"model":                        func(s *OrkaSpec) { s.Model = " \t" },
		"model newline":                func(s *OrkaSpec) { s.Model = "test\nother: value" },
		"secret":                       func(s *OrkaSpec) { s.SecretName = "" },
		"bad secret":                   func(s *OrkaSpec) { s.SecretName = "invalid/name" },
		"bad key":                      func(s *OrkaSpec) { s.SecretKey = "invalid/key" },
		"long key":                     func(s *OrkaSpec) { s.SecretKey = strings.Repeat("a", 254) },
		"legacy tools":                 func(s *OrkaSpec) { s.Tools = []string{"server:tool"} },
		"empty tool":                   func(s *OrkaSpec) { s.Tools = []string{""} },
		"injected skill":               func(s *OrkaSpec) { s.Skills = []string{"skill\n- other"} },
		"description newline":          func(s *OrkaSpec) { s.Description = "hello\nkind: Secret" },
		"instructions control":         func(s *OrkaSpec) { s.Instructions = "hello\x00" },
		"task control":                 func(s *OrkaSpec) { s.TaskPrompt = "hello\rworld" },
		"blank task":                   func(s *OrkaSpec) { s.TaskPrompt = " \n\t" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := orkaSpec()
			mutate(&s)
			if b, err := GenerateOrka(s); err == nil || b != nil {
				t.Fatalf("unsafe spec produced bundle: %v, %v", b, err)
			}
		})
	}
}

func TestOrkaSecretKeyRejectsDirectoryPrefixes(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		valid     bool
	}{
		{"dot", ".", false},
		{"parent", "..", false},
		{"parent prefix", "..api-key", false},
		{"single dot prefix", ".api-key", true},
	} {
		t.Run(tc.name+"/generation", func(t *testing.T) {
			s := orkaSpec()
			s.SecretKey = tc.key
			b, err := GenerateOrka(s)
			if !tc.valid {
				if err == nil || b != nil {
					t.Fatalf("generation accepted invalid Secret key %q", tc.key)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := b.Provider["spec"].(map[string]any)["secretRef"].(map[string]any)["key"]; got != tc.key {
				t.Fatalf("Secret key changed: got %v, want %q", got, tc.key)
			}
		})
		t.Run(tc.name+"/mutated bundle", func(t *testing.T) {
			b := orkaBundle(t, orkaSpec())
			b.Provider["spec"].(map[string]any)["secretRef"].(map[string]any)["key"] = tc.key
			if err := b.Validate(); (err == nil) != tc.valid {
				t.Fatalf("mutated Secret key %q: valid=%t, error=%v", tc.key, tc.valid, err)
			}
		})
	}
}

func TestOrkaBaseURLBoundaries(t *testing.T) {
	for _, good := range []string{"", "https://api.example.test/v1", "http://localhost:11434/v1", "http://[::1]:11434/v1"} {
		s := orkaSpec()
		s.BaseURL, s.ProviderType = good, "anthropic"
		b := orkaBundle(t, s)
		if got := b.Provider["spec"].(map[string]any)["baseURL"]; good != "" && got != good {
			t.Errorf("baseURL changed: %v", got)
		}
	}
	for _, bad := range []string{"api.example.test/v1", "ftp://example.test", "https:///v1", "http://:8080", "https://user:password@example.test/v1", "https://user@example.test", "https://example.test?token=value", "https://example.test?", "https://example.test#token", "https://example.test#"} {
		s := orkaSpec()
		s.BaseURL = bad
		if b, err := GenerateOrka(s); err == nil || b != nil {
			t.Errorf("accepted unsafe URL %q", bad)
		} else if strings.Contains(err.Error(), bad) {
			t.Errorf("URL refusal echoes possibly sensitive URL")
		}
	}
}

func TestOrkaPairAndValueFreeSecretRemainMandatory(t *testing.T) {
	cases := map[string]func(*OrkaBundle){
		"missing Provider":   func(b *OrkaBundle) { b.Provider = nil },
		"missing Agent":      func(b *OrkaBundle) { b.Agent = nil },
		"missing Secret":     func(b *OrkaBundle) { b.Secret = nil },
		"wrong kind":         func(b *OrkaBundle) { b.Provider["kind"] = "Agent" },
		"wrong version":      func(b *OrkaBundle) { b.Agent["apiVersion"] = "other/v1" },
		"name mismatch":      func(b *OrkaBundle) { b.Provider["metadata"].(map[string]any)["name"] = "other" },
		"namespace mismatch": func(b *OrkaBundle) { b.Agent["metadata"].(map[string]any)["namespace"] = "other" },
		"missing namespace":  func(b *OrkaBundle) { delete(b.Provider["metadata"].(map[string]any), "namespace") },
		"Provider ref": func(b *OrkaBundle) {
			b.Agent["spec"].(map[string]any)["providerRef"] = map[string]any{"name": "other", "namespace": "orka-system"}
		},
		"Provider ref namespace": func(b *OrkaBundle) {
			b.Agent["spec"].(map[string]any)["providerRef"].(map[string]any)["namespace"] = "other"
		},
		"Secret ref": func(b *OrkaBundle) {
			b.Provider["spec"].(map[string]any)["secretRef"].(map[string]any)["name"] = "other"
		},
		"Secret key":                 func(b *OrkaBundle) { delete(b.Provider["spec"].(map[string]any)["secretRef"].(map[string]any), "key") },
		"Secret data even empty":     func(b *OrkaBundle) { b.Secret["data"] = map[string]any{} },
		"Secret stringData even nil": func(b *OrkaBundle) { b.Secret["stringData"] = nil },
		"Secret extra field":         func(b *OrkaBundle) { b.Secret["value"] = "not-a-credential" },
		"Task ref":                   func(b *OrkaBundle) { b.Task["spec"].(map[string]any)["agentRef"].(map[string]any)["name"] = "other" },
		"Task namespace":             func(b *OrkaBundle) { b.Task["metadata"].(map[string]any)["namespace"] = "other" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := orkaSpec()
			s.TaskPrompt = "Say hello"
			b := orkaBundle(t, s)
			mutate(b)
			if err := b.Validate(); err == nil {
				t.Fatal("invalid bundle admitted")
			}
			if text, err := b.YAML("test"); err == nil || text != "" {
				t.Fatal("invalid bundle rendered")
			}
		})
	}
	var nilBundle *OrkaBundle
	if err := nilBundle.Validate(); err == nil {
		t.Fatal("nil bundle admitted")
	}
}

func TestOrkaYAMLPreservesInputAndTaskName(t *testing.T) {
	s := orkaSpec()
	s.Name = strings.Repeat("a", 29) + "-" + strings.Repeat("b", 33)
	s.Description = `A "quoted" description: yes # still text`
	s.Model = `org/model: "yes" # not a comment`
	s.Instructions = "    initially indented\n---\nkind: Secret\n\n  trailing space  \n"
	s.TaskPrompt = "Say: \"hello\"\n---\nkind: Provider\n"
	b := orkaBundle(t, s)
	name := b.Task["metadata"].(map[string]any)["name"].(string)
	if !regexp.MustCompile(`^a{29}-[0-9a-f]{32}$`).MatchString(name) {
		t.Fatalf("Task name lacks bounded prefix and 128-bit suffix: %s", name)
	}
	if name == orkaBundle(t, s).Task["metadata"].(map[string]any)["name"] {
		t.Fatal("new invocation reused Task name")
	}
	for range 2 {
		out, err := b.YAML("source\n---\nkind: ConfigMap")
		if err != nil {
			t.Fatal(err)
		}
		dec := yaml.NewDecoder(strings.NewReader(out))
		var parsed []map[string]any
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("YAML parse: %v\n%s", err, out)
			}
			parsed = append(parsed, doc)
		}
		want, err := json.Marshal(b.Documents())
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("YAML changed bundle:\ngot %s\nwant %s", got, want)
		}
		if parsed[3]["metadata"].(map[string]any)["name"] != name {
			t.Fatal("render regenerated Task name")
		}
	}
	if got := b.Agent["spec"].(map[string]any)["systemPrompt"].(map[string]any)["inline"]; got != s.Instructions {
		t.Fatal("instructions were changed")
	}
}

func TestOrkaTaskEmitsCanonicalEmptyResources(t *testing.T) {
	s := orkaSpec()
	s.TaskPrompt = "Say hello"
	b := orkaBundle(t, s)
	resources, ok := b.Task["spec"].(map[string]any)["resources"].(map[string]any)
	if !ok || resources == nil || len(resources) != 0 {
		t.Fatal("Task must explicitly serialize the controller's empty resources default")
	}
	body, err := json.Marshal(b.Task)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"resources":{}`) {
		t.Fatal("Task create JSON omits canonical resources")
	}
	text, err := b.YAML("test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "resources: {}") {
		t.Fatal("review artifact omits canonical resources")
	}
}

func TestOrkaSecretShapesAcrossEveryInputAndFinalOutput(t *testing.T) {
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			for _, field := range []string{"Name", "Namespace", "Description", "ProviderType", "Model", "BaseURL", "SecretName", "SecretKey", "Instructions", "TaskPrompt", "Tools", "Skills"} {
				s := orkaSpec()
				value := reflect.ValueOf(&s).Elem().FieldByName(field)
				if value.Kind() == reflect.Slice {
					value.Set(reflect.ValueOf([]string{shape.Example}))
				} else {
					value.SetString(shape.Example)
				}
				if _, err := GenerateOrka(s); err == nil {
					t.Errorf("%s accepted key-shaped input", field)
				} else if strings.Contains(err.Error(), shape.Example) || strings.Contains(err.Error(), "kagent") {
					t.Errorf("%s refusal leaks input or legacy instructions", field)
				}
			}
			b := orkaBundle(t, orkaSpec())
			b.Agent["spec"].(map[string]any)["systemPrompt"].(map[string]any)["inline"] = shape.Example
			if out, err := b.YAML("test"); err == nil || out != "" {
				t.Fatal("mutated credential-shaped value rendered")
			}
			if out, err := orkaBundle(t, orkaSpec()).YAML(shape.Example); err == nil || out != "" {
				t.Fatal("credential-shaped provenance rendered")
			}
		})
	}
}

func TestOrkaTaskEntropyFailure(t *testing.T) {
	original := rand.Reader
	rand.Reader = strings.NewReader("short")
	t.Cleanup(func() { rand.Reader = original })
	s := orkaSpec()
	s.TaskPrompt = "Say hello"
	if b, err := GenerateOrka(s); b != nil || err == nil || !strings.Contains(err.Error(), "random") {
		t.Fatalf("entropy failure must refuse a Task: bundle=%v, error=%v", b, err)
	}
}

func TestOrkaRateLimitsAreExplicitPositiveAndLossless(t *testing.T) {
	var rpm int32 = math.MaxInt32
	var tpm int64 = math.MaxInt64
	s := orkaSpec()
	s.AgentRateLimit = &OrkaRateLimit{RequestsPerMinute: &rpm}
	s.ProviderRateLimit = &OrkaRateLimit{TokensPerMinute: &tpm}
	b := orkaBundle(t, s)
	for doc, want := range map[string]map[string]any{"Agent": {"requestsPerMinute": rpm}, "Provider": {"tokensPerMinute": tpm}} {
		resource := b.Agent
		if doc == "Provider" {
			resource = b.Provider
		}
		if got := resource["spec"].(map[string]any)["rateLimit"]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s limits: %#v", doc, got)
		}
	}
	for _, value := range []int64{0, -1} {
		r, tok := int32(value), value
		for _, limit := range []*OrkaRateLimit{{RequestsPerMinute: &r}, {TokensPerMinute: &tok}} {
			for _, agent := range []bool{true, false} {
				s := orkaSpec()
				if agent {
					s.AgentRateLimit = limit
				} else {
					s.ProviderRateLimit = limit
				}
				if _, err := GenerateOrka(s); err == nil {
					t.Fatal("nonpositive limit accepted")
				}
			}
		}
	}
	s = orkaSpec()
	s.AgentRateLimit = &OrkaRateLimit{}
	if _, exists := orkaBundle(t, s).Agent["spec"].(map[string]any)["rateLimit"]; exists {
		t.Fatal("empty limit should be omitted")
	}
}
