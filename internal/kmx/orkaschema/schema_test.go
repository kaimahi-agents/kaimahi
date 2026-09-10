package orkaschema_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

func testBundle(t *testing.T) *scaffold.OrkaBundle {
	t.Helper()
	b, err := scaffold.GenerateOrka(scaffold.OrkaSpec{
		Name: "sample", Namespace: "orka-system", ProviderType: "openai", Model: "test",
		SecretName: "model-key", TaskPrompt: "Say hello", Tools: []string{"web_search"}, Skills: []string{"research"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixtures(t *testing.T, target string) map[string][]byte {
	t.Helper()
	crds := make(map[string][]byte)
	for _, kind := range []string{"Agent", "Provider", "Task"} {
		data, err := os.ReadFile("fixtures/" + target + "/" + strings.ToLower(kind) + "s.yaml")
		if err != nil {
			t.Fatal(err)
		}
		crds[kind] = data
	}
	return crds
}

func mutateCRD(t *testing.T, crds map[string][]byte, kind string, mutate func(map[string]any)) {
	t.Helper()
	var crd map[string]any
	if err := yaml.Unmarshal(crds[kind], &crd); err != nil {
		t.Fatal(err)
	}
	mutate(crd)
	data, err := yaml.Marshal(crd)
	if err != nil {
		t.Fatal(err)
	}
	crds[kind] = data
}

func version(crd map[string]any) map[string]any {
	return crd["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)
}

func schema(crd map[string]any) map[string]any {
	return version(crd)["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
}

func specProperties(crd map[string]any) map[string]any {
	return schema(crd)["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)
}

func TestOfflineAndInstalledValidateOrdinaryWholeBundles(t *testing.T) {
	for target, pin := range map[string]string{
		"v0.1.3": "b07d42c0b9e52fe511b434827a342b4720f5d422",
		"main":   "7c4753c2c68a510112ea2bb25b60a406d9c45686",
	} {
		t.Run(target, func(t *testing.T) {
			offline, err := orkaschema.Offline(target)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(offline.Provenance(), pin) || !strings.Contains(offline.Provenance(), target) {
				t.Fatalf("missing immutable provenance: %s", offline.Provenance())
			}
			installed, err := orkaschema.Installed(fixtures(t, target))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.ToLower(installed.Provenance()), "installed") {
				t.Fatal("missing installed provenance")
			}
			b := testBundle(t)
			for _, v := range []*orkaschema.Validator{offline, installed} {
				for _, doc := range b.Documents()[1:] {
					if err := v.Validate(doc); err != nil {
						t.Fatalf("%s: %v", doc["kind"], err)
					}
				}
				if err := v.Validate(b.Secret); err == nil {
					t.Fatal("Secret is not an Orka CRD")
				}
			}
		})
	}
	v, err := orkaschema.Offline("")
	if err != nil || !strings.Contains(v.Provenance(), "v0.1.3") {
		t.Fatalf("default target: %v", err)
	}
	for _, target := range []string{"latest", "v0.1.2", "../main"} {
		if _, err := orkaschema.Offline(target); err == nil {
			t.Errorf("accepted unknown target %q", target)
		}
	}
}

func TestRateLimitDriftRefusesEachResourceByExactPath(t *testing.T) {
	release, err := orkaschema.Offline("v0.1.3")
	if err != nil {
		t.Fatal(err)
	}
	main, err := orkaschema.Offline("main")
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range []string{"Agent", "Provider"} {
		for _, field := range []string{"requestsPerMinute", "tokensPerMinute"} {
			t.Run(resource+"/"+field, func(t *testing.T) {
				b := testBundle(t)
				doc := b.Agent
				if resource == "Provider" {
					doc = b.Provider
				}
				doc["spec"].(map[string]any)["rateLimit"] = map[string]any{field: 12}
				if err := release.Validate(doc); err != nil {
					t.Fatal(err)
				}
				if err := main.Validate(doc); err == nil || !strings.Contains(err.Error(), resource) || !strings.Contains(err.Error(), "spec.rateLimit") {
					t.Fatalf("drift must name resource and path, got %v", err)
				}
			})
		}
	}
}

func TestWholeResourceValidationNotJustRateLimits(t *testing.T) {
	cases := []struct {
		name, resource, path string
		mutate               func(map[string]any)
	}{
		{"wrong kind", "Agent", "kind", func(d map[string]any) { d["kind"] = "Other" }},
		{"wrong version", "Agent", "apiVersion", func(d map[string]any) { d["apiVersion"] = "core.orka.ai/v2" }},
		{"top level unknown", "Agent", "invented", func(d map[string]any) { d["invented"] = true }},
		{"spec type", "Agent", "spec", func(d map[string]any) { d["spec"] = "wrong" }},
		{"nested type", "Agent", "spec.systemPrompt.inline", func(d map[string]any) { d["spec"].(map[string]any)["systemPrompt"] = map[string]any{"inline": 7} }},
		{"nested unknown", "Agent", "spec.providerRef.extra", func(d map[string]any) { d["spec"].(map[string]any)["providerRef"].(map[string]any)["extra"] = true }},
		{"array unknown", "Agent", "spec.tools.0.extra", func(d map[string]any) {
			d["spec"].(map[string]any)["tools"].([]any)[0].(map[string]any)["extra"] = true
		}},
		{"nested required", "Provider", "spec.secretRef.name", func(d map[string]any) { delete(d["spec"].(map[string]any)["secretRef"].(map[string]any), "name") }},
		{"required", "Provider", "spec.type", func(d map[string]any) { delete(d["spec"].(map[string]any), "type") }},
		{"enum", "Provider", "spec.type", func(d map[string]any) { d["spec"].(map[string]any)["type"] = "unsupported" }},
		{"Task enum", "Task", "spec.type", func(d map[string]any) { d["spec"].(map[string]any)["type"] = "unsupported" }},
		{"Task prompt type", "Task", "spec.prompt", func(d map[string]any) { d["spec"].(map[string]any)["prompt"] = false }},
	}
	for _, target := range []string{"v0.1.3", "main"} {
		v, err := orkaschema.Offline(target)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range cases {
			t.Run(target+"/"+tc.name, func(t *testing.T) {
				b := testBundle(t)
				doc := map[string]map[string]any{"Agent": b.Agent, "Provider": b.Provider, "Task": b.Task}[tc.resource]
				tc.mutate(doc)
				if err := v.Validate(doc); err == nil || !strings.Contains(err.Error(), tc.path) {
					t.Fatalf("expected failure at %s, got %v", tc.path, err)
				}
			})
		}
		if err := v.Validate(nil); err == nil {
			t.Fatal("nil resource admitted")
		}
	}
}

func TestInstalledRefusesMissingUnservedWrongAndInvalidCRDs(t *testing.T) {
	cases := map[string]func(map[string][]byte){
		"missing":            func(c map[string][]byte) { delete(c, "Agent") },
		"malformed YAML":     func(c map[string][]byte) { c["Agent"] = []byte("spec: [") },
		"multiple documents": func(c map[string][]byte) { c["Agent"] = append(c["Agent"], []byte("\n---\nkind: Other\n")...) },
		"wrong identity":     func(c map[string][]byte) { c["Agent"] = c["Provider"] },
		"wrong group": func(c map[string][]byte) {
			mutateCRD(t, c, "Agent", func(d map[string]any) { d["spec"].(map[string]any)["group"] = "other.test" })
		},
		"wrong scope": func(c map[string][]byte) {
			mutateCRD(t, c, "Agent", func(d map[string]any) { d["spec"].(map[string]any)["scope"] = "Cluster" })
		},
		"unserved": func(c map[string][]byte) {
			mutateCRD(t, c, "Agent", func(d map[string]any) { version(d)["served"] = false })
		},
		"missing version": func(c map[string][]byte) {
			mutateCRD(t, c, "Agent", func(d map[string]any) { version(d)["name"] = "v2" })
		},
		"missing schema": func(c map[string][]byte) {
			mutateCRD(t, c, "Agent", func(d map[string]any) { delete(version(d), "schema") })
		},
		"empty schema": func(c map[string][]byte) {
			mutateCRD(t, c, "Agent", func(d map[string]any) { version(d)["schema"] = map[string]any{"openAPIV3Schema": map[string]any{}} })
		},
		"invalid schema": func(c map[string][]byte) {
			mutateCRD(t, c, "Agent", func(d map[string]any) { schema(d)["type"] = "nonsense" })
		},
		"external HTTPS ref": func(c map[string][]byte) {
			mutateCRD(t, c, "Agent", func(d map[string]any) {
				specProperties(d)["external"] = map[string]any{"$ref": "https://example.invalid/schema.json"}
			})
		},
		"external file ref": func(c map[string][]byte) {
			mutateCRD(t, c, "Agent", func(d map[string]any) {
				specProperties(d)["external"] = map[string]any{"$ref": "file:///must-not-read.json"}
			})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			crds := fixtures(t, "v0.1.3")
			mutate(crds)
			if v, err := orkaschema.Installed(crds); err == nil || v != nil || !strings.Contains(err.Error(), "Agent") {
				t.Fatalf("bad CRD admitted or lacks resource context: %v", err)
			}
		})
	}
}

func TestSchemaNormalizationPreservesOpenMapsNullableAndNumericBounds(t *testing.T) {
	crds := fixtures(t, "v0.1.3")
	mutateCRD(t, crds, "Agent", func(d map[string]any) {
		p := specProperties(d)
		p["optionalText"] = map[string]any{"type": "string", "nullable": true}
		p["preserved"] = map[string]any{"type": "object", "properties": map[string]any{"known": map[string]any{"type": "string"}}, "x-kubernetes-preserve-unknown-fields": true}
		p["open"] = map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
		p["labels"] = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	})
	v, err := orkaschema.Installed(crds)
	if err != nil {
		t.Fatal(err)
	}
	doc := testBundle(t).Agent
	spec := doc["spec"].(map[string]any)
	spec["optionalText"] = nil
	spec["preserved"] = map[string]any{"unknown": []any{1, "value"}}
	spec["open"] = map[string]any{"anything": true}
	spec["labels"] = map[string]any{"custom": "value"}
	doc["metadata"].(map[string]any)["labels"] = map[string]any{"custom.test/name": "value"}
	if err := v.Validate(doc); err != nil {
		t.Fatal(err)
	}
	spec["preserved"].(map[string]any)["known"] = 1
	if err := v.Validate(doc); err == nil {
		t.Fatal("preserve-unknown disabled validation of known property")
	}
	delete(spec["preserved"].(map[string]any), "known")
	spec["labels"].(map[string]any)["custom"] = false
	if err := v.Validate(doc); err == nil {
		t.Fatal("additionalProperties schema not enforced")
	}
	delete(spec, "labels")
	for _, tc := range []struct {
		field     string
		good, bad any
	}{
		{"requestsPerMinute", int64(math.MaxInt32), int64(math.MaxInt32) + 1},
		{"tokensPerMinute", int64(math.MaxInt64), json.Number("9223372036854775808")},
	} {
		spec["rateLimit"] = map[string]any{tc.field: tc.good}
		if err := v.Validate(doc); err != nil {
			t.Fatal(err)
		}
		spec["rateLimit"] = map[string]any{tc.field: tc.bad}
		if err := v.Validate(doc); err == nil {
			t.Fatalf("%s overflow admitted", tc.field)
		}
	}
}

func TestFixtureBytesMatchImmutableSources(t *testing.T) {
	for path, want := range map[string]string{
		"v0.1.3/agents.yaml":    "d6b9123ea29d904846777b63c59e8f5c054ac851e63d6c1bf8177a4accc44f4d",
		"v0.1.3/providers.yaml": "2c9b4b25800a8d6a57494fc9e267d7ebd7b3cd52c9a2956ba87544a8c3388ff6",
		"v0.1.3/tasks.yaml":     "8672cf42f1b2dc17020df1fc539ebdfa5ac67604ecfb8a272cd6eac2a51c1d6d",
		"main/agents.yaml":      "9e7bc6252cdf45cdc9fc127a558b3b2f77eb4ff1a1388386996acbf1d58da43c",
		"main/providers.yaml":   "6185d760bd43d00a4594cfa8fd50ff86b269f952be6656884d000f67e64562f8",
		"main/tasks.yaml":       "e0c657f3a9c0878665e36ae18a3cfe62ecb563efb8b5141576b57024e8b52279",
	} {
		data, err := os.ReadFile("fixtures/" + path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
			t.Errorf("%s hash = %s", path, got)
		}
	}
}
