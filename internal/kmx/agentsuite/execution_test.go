package agentsuite

import (
	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"strings"
	"testing"
)

func TestImageDeclarationRoundTripAndBindingRefusal(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	r := ImageDeployment{SchemaVersion: spec.SpecVersion, MediaType: ImageDeploymentMediaType, SuiteReference: "registry.example/source@" + d, SuiteDigest: d, Agent: "hello", Platform: spec.Platform{OS: "linux", Architecture: "amd64"}, CompositionDigest: d, BuildProfile: "default", Execution: spec.ExecutionContract{Kind: spec.ExecutionHTTPV1, Protocol: "openai-chat-v1", Port: 8080, HealthPath: "/healthz", Inputs: []spec.ExecutionInput{}}}
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
