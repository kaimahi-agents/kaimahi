package agentsuite

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/google/jsonschema-go/jsonschema"
)

func TestImageDeclarationSchemaParity(t *testing.T) {
	raw, err := os.ReadFile("schema/image-deployment.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	d := "sha256:" + strings.Repeat("a", 64)
	r := ImageDeployment{SchemaVersion: suitecore.SpecVersion, MediaType: ImageDeploymentMediaType, SuiteDigest: d, Agent: "hello", Platform: suitecore.Platform{OS: "linux", Architecture: "amd64"}, CompositionDigest: d, BuildProfile: "default", BuildProfileDigest: d, Execution: suitecore.ExecutionContract{Kind: suitecore.ExecutionHTTPV1, Protocol: "openai-chat-v1", Port: 8080, HealthPath: "/healthz", Inputs: []suitecore.ExecutionInput{}}}
	base, err := EncodeImageDeployment(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		valid  bool
		change func(map[string]any)
	}{
		{"valid", true, func(map[string]any) {}},
		{"null inference", false, func(m map[string]any) { m["inference"] = nil }},
		{"null hint", false, func(m map[string]any) { m["suiteReference"] = nil }},
		{"missing execution", false, func(m map[string]any) { delete(m, "execution") }},
		{"missing inputs", false, func(m map[string]any) { delete(m["execution"].(map[string]any), "inputs") }},
		{"mounted without inference", false, func(m map[string]any) {
			m["execution"].(map[string]any)["configuration"] = suitecore.AgentKitMountedConfig
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]any
			_ = json.Unmarshal([]byte(base), &m)
			tc.change(m)
			wire, _ := json.Marshal(m)
			_, err := DecodeImageDeployment(wire)
			if (err == nil) != tc.valid {
				t.Fatalf("decoder: %v", err)
			}
			if err := resolved.Validate(m); (err == nil) != tc.valid {
				t.Fatalf("schema: %v", err)
			}
		})
	}
}

func TestImageDeclarationRoundTripAndBindingRefusal(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	r := ImageDeployment{SchemaVersion: suitecore.SpecVersion, MediaType: ImageDeploymentMediaType, SuiteReference: "registry.example/source@" + d, SuiteDigest: d, Agent: "hello", Platform: suitecore.Platform{OS: "linux", Architecture: "amd64"}, CompositionDigest: d, BuildProfile: "default", Execution: suitecore.ExecutionContract{Kind: suitecore.ExecutionHTTPV1, Protocol: "openai-chat-v1", Port: 8080, HealthPath: "/healthz", Inputs: []suitecore.ExecutionInput{}}}
	r.BuildProfileDigest = d
	label, err := EncodeImageDeployment(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeImageDeployment([]byte(label)); err != nil {
		t.Fatal(err)
	}
	r.SuiteReference = ""
	if _, err := EncodeImageDeployment(r); err != nil {
		t.Fatal("location-free declaration refused", err)
	}
	r.BuildProfileDigest = ""
	if _, err := EncodeImageDeployment(r); err == nil {
		t.Fatal("missing exact build-profile identity accepted")
	}
	r.BuildProfileDigest = d
	r.SuiteReference = "registry.example/source:latest"
	if _, err := EncodeImageDeployment(r); err == nil {
		t.Fatal("mutable source accepted")
	}
	if _, err := DecodeImageDeployment([]byte(strings.Replace(label, `"agent":"hello"`, `"agent":"hello","agent":"other"`, 1))); err == nil {
		t.Fatal("duplicate identity accepted")
	}
}
