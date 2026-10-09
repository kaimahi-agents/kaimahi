package agentsuite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageSourceRequiresExactContractAndComposition(t *testing.T) {
	root := writeMinimalSuite(t)
	content, err := loadDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := content.data("agentsuite.json")
	if err != nil {
		t.Fatal(err)
	}
	var suite Suite
	if err := decodeStrict(raw, &suite); err != nil {
		t.Fatal(err)
	}
	raw, err = content.data(suite.BuildProfiles[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	var profile BuildProfile
	if err := decodeStrict(raw, &profile); err != nil {
		t.Fatal(err)
	}
	contract := ExecutionContract{Kind: ExecutionHTTPV1, Protocol: "openai-chat-v1", Port: 8080, HealthPath: "/healthz", Inputs: []ExecutionInput{}}
	profile.Execution = &contract
	suite.BuildProfiles[0].Digest = mustWriteJSON(t, root, suite.BuildProfiles[0].Path, profile)
	mustWriteJSON(t, root, "agentsuite.json", suite)
	digest := "sha256:" + strings.Repeat("a", 64)
	record := ImageDeployment{SchemaVersion: SpecVersion, MediaType: ImageDeploymentMediaType, SuiteReference: "registry.example/suite@" + digest, SuiteDigest: digest, Agent: "writer", Platform: Platform{OS: "linux", Architecture: "amd64"}, CompositionDigest: suite.Compositions[0].Digest, BuildProfile: "default", Execution: contract}
	if err := ValidateImageSource(root, record); err != nil {
		t.Fatal(err)
	}
	label, err := EncodeImageDeployment(record)
	if err != nil {
		t.Fatal(err)
	}
	validateSchemaJSON(t, compileReferenceSchema(t, "image-deployment.schema.json"), label, true)
	record.Execution.Port = 8081
	if err := ValidateImageSource(root, record); err == nil {
		t.Fatal("mismatched execution contract accepted")
	}
	record.Execution = contract
	record.CompositionDigest = digest
	if err := ValidateImageSource(root, record); err == nil {
		t.Fatal("stale composition accepted")
	}
}

func TestExecutionContractSchemaAndSemanticValidation(t *testing.T) {
	schema := compileReferenceSchema(t, "execution.schema.json")
	valid := `{"kind":"kubernetes-http-v1","protocol":"openai-chat-v1","port":8080,"healthPath":"/healthz","inputs":[]}`
	validateSchemaJSON(t, schema, valid, true)
	validateSchemaJSON(t, schema, strings.Replace(valid, "openai-chat-v1", "unknown", 1), false)
	var contract ExecutionContract
	if err := decodeStrict([]byte(valid), &contract); err != nil {
		t.Fatal(err)
	}
	contract.Inputs = []ExecutionInput{{Name: "inject", Environment: "LD_PRELOAD"}}
	if err := ValidateExecutionContract(contract); err == nil {
		t.Fatal("injecting input accepted")
	}
	contract.Inputs = nil
	contract.HealthPath = "/../healthz"
	if err := ValidateExecutionContract(contract); err == nil {
		t.Fatal("unclean health path accepted")
	}
	// Exercise the cross-file schema reference as an actual build profile.
	raw, err := os.ReadFile(filepath.Join("testdata", "minimal", "build-profiles", "default.json"))
	if err != nil {
		t.Fatal(err)
	}
	withExecution := strings.Replace(string(raw), `"sourceEpoch":`, `"execution":`+valid+`,"sourceEpoch":`, 1)
	validateSchemaJSON(t, compileReferenceSchema(t, "build-profile.schema.json"), withExecution, true)
}
