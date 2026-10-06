package agentsuite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestToolSchemaDefinesBundledImplementation(t *testing.T) {
	schema := compileReferenceSchema(t, "tool.schema.json")
	valid := `{
	  "schemaVersion":"1.0.0-draft",
	  "mediaType":"application/vnd.agentsuite.tool.v1+json",
	  "id":"datetime",
	  "version":"1.0.0",
	  "provider":{
	    "protocol":"mcp",
	    "revision":"2025-06-18",
	    "operations":[{
	      "name":"current_time",
	      "inputSchema":{"path":"schemas/input.json","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	      "outputSchema":{"path":"schemas/output.json","digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	      "effects":["reads-system-clock"]
	    }]
	  },
	  "variants":[{
	    "platform":{"os":"linux","architecture":"amd64"},
	    "variantDigest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	    "installRoot":"/opt/agentsuite/tools/datetime/1.0.0",
	    "relocatable":false,
	    "payloadRoot":"tools/datetime/1.0.0/linux-amd64",
	    "entrypoint":"/opt/agentsuite/tools/datetime/1.0.0/bin/datetime",
	    "arguments":[],
	    "searchPath":[],
	    "environment":[],
	    "writablePaths":[],
	    "network":[],
	    "runtime":{"abi":"static","cpuBaseline":"x86-64-v1"},
	    "files":[{
	      "path":"bin/datetime",
	      "type":"file",
	      "mode":493,
	      "uid":0,
	      "gid":0,
	      "size":123456,
	      "digest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	      "component":"datetime"
	    }],
	    "dependencies":[]
	  }],
	  "extensions":[]
	}`
	validateSchemaJSON(t, schema, valid, true)
	withoutFileDigest := strings.Replace(
		valid,
		`"digest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"`,
		`"notDigest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"`,
		1,
	)
	validateSchemaJSON(t, schema, withoutFileDigest, false)
}

func TestToolSchemaAcceptsMultiFileCLIBundles(t *testing.T) {
	schema := compileReferenceSchema(t, "tool.schema.json")
	for _, name := range []string{"kubectl-tool.json", "azure-cli-tool.json"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "tools", name))
			if err != nil {
				t.Fatal(err)
			}
			validateSchemaJSON(t, schema, string(data), true)
		})
	}
}

func TestAgentSchemaDefinesSharedSandboxMode(t *testing.T) {
	schema := compileReferenceSchema(t, "agent.schema.json")
	valid := `{
	  "schemaVersion":"1.0.0-draft",
	  "mediaType":"application/vnd.agentsuite.agent.v1+json",
	  "id":"writer",
	  "instructions":{
	    "path":"instructions/writer.md",
	    "digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	  },
	  "model":{"protocol":"openai-compatible","model":"example"},
	  "tools":[{
	    "id":"datetime",
	    "version":"1.0.0",
	    "executionMode":"shared-sandbox"
	  }]
	}`
	validateSchemaJSON(t, schema, valid, true)
	validateSchemaJSON(t, schema, strings.Replace(valid, `"shared-sandbox"`, `"unsupported"`, 1), false)
}

func TestCompositionSchemaDefinesSharedSandboxResolution(t *testing.T) {
	schema := compileReferenceSchema(t, "composition.schema.json")
	base := `{
	  "schemaVersion":"1.0.0-draft",
	  "mediaType":"application/vnd.agentsuite.composition.v1+json",
	  "agent":"writer",
	  "platform":{"os":"linux","architecture":"amd64"},
	  "buildProfile":"default",
	  "tools":[%s]
	}`
	resolved := `{
	  "id":"datetime",
	  "version":"1.0.0",
	  "manifestDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	  "variantDigest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	  "executionMode":"shared-sandbox"
	}`
	validateSchemaJSON(t, schema, strings.Replace(base, "%s", resolved, 1), true)
	validateSchemaJSON(t, schema, strings.Replace(base, "%s", strings.Replace(resolved, `"variantDigest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",`, "", 1), 1), false)
	validateSchemaJSON(t, schema, strings.Replace(base, "%s", strings.Replace(resolved, `"shared-sandbox"`, `"unsupported"`, 1), 1), false)
}

func compileReferenceSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("schema", name))
	if err != nil {
		t.Fatal(err)
	}
	var resource any
	if err := json.Unmarshal(data, &resource); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(name, resource); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(name)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func validateSchemaJSON(t *testing.T, schema *jsonschema.Schema, document string, valid bool) {
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
