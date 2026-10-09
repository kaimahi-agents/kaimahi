package agentsuite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestSandboxBindingSchemaIsBuilderOwned(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("schema", "sandbox-binding.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	valid := `{
	  "schemaVersion":"1.0.0-draft",
	  "mediaType":"application/vnd.agentsuite.sandbox.binding.v1+json",
	  "suiteDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	  "agent":"writer",
	  "platform":{"os":"linux","architecture":"amd64"},
	  "buildProfile":"default",
	  "composition":{"mediaType":"application/vnd.agentsuite.composition.v1+json","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":1},
	  "inventory":{"mediaType":"application/vnd.kaimahi.sandbox.inventory.v1+json","digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":1}
	}`
	validateBuilderSchemaJSON(t, resolved, valid, true)
	validateBuilderSchemaJSON(t, resolved, strings.Replace(valid, `"application/vnd.agentsuite.sandbox.binding.v1+json"`, `"application/vnd.kaimahi.sandbox.binding.v1+json"`, 1), false)
}

func validateBuilderSchemaJSON(t *testing.T, schema *jsonschema.Resolved, document string, valid bool) {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(document), &value); err != nil {
		t.Fatal(err)
	}
	err := schema.Validate(value)
	if valid && err != nil {
		t.Fatalf("schema rejected valid document: %v", err)
	}
	if !valid && err == nil {
		t.Fatal("schema accepted invalid document")
	}
}
