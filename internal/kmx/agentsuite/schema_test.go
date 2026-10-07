package agentsuite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestToolProviderSchemaDefinesBundledImplementation(t *testing.T) {
	schema := compileReferenceSchema(t, "tool-provider.schema.json")
	valid := `{
	  "schemaVersion":"1.0.0-draft",
	  "mediaType":"application/vnd.agentsuite.tool.provider.v1+json",
	  "id":"datetime",
	  "version":"1.0.0",
	  "protocol":"mcp",
	  "revision":"2025-06-18",
	  "tools":[{
	      "name":"current_time",
	      "inputSchema":{"path":"schemas/input.json","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	      "outputSchema":{"path":"schemas/output.json","digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	      "effects":["reads-system-clock"]
	    }],
	  "variants":[{
	    "platform":{"os":"linux","architecture":"amd64"},
	    "variantDigest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	    "installRoot":"/opt/agentsuite/tool-providers/datetime/1.0.0",
	    "relocatable":false,
	    "payloadRoot":"tool-providers/datetime/1.0.0/linux-amd64",
	    "entrypoint":"/opt/agentsuite/tool-providers/datetime/1.0.0/bin/datetime",
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
	retained := strings.Replace(valid, `"protocol":`, `"retained":true,"protocol":`, 1)
	validateSchemaJSON(t, schema, retained, true)
	validateSchemaJSON(t, schema, strings.Replace(retained, `"retained":true`, `"retained":false`, 1), false)
	withRemote := strings.Replace(valid, `"extensions":[]`, `"remote":{
	    "transport":"streamable-http",
	    "endpointRef":"datetime-mcp-endpoint",
	    "network":[{"destinationRef":"datetime-mcp-egress"}],
	    "timeouts":{"connectMilliseconds":5000,"requestMilliseconds":60000},
	    "cancellation":"propagate",
	    "connection":"session-aware"
	  },
	  "extensions":[]`, 1)
	validateSchemaJSON(t, schema, withRemote, true)
	withoutFileDigest := strings.Replace(
		valid,
		`"digest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"`,
		`"notDigest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"`,
		1,
	)
	validateSchemaJSON(t, schema, withoutFileDigest, false)
}

func TestToolProviderSchemaAcceptsMultiFileCLIBundles(t *testing.T) {
	schema := compileReferenceSchema(t, "tool-provider.schema.json")
	for _, name := range []string{"kubectl-tool-provider.json", "azure-cli-tool-provider.json", "opa-tool-provider.json"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "tool-providers", name))
			if err != nil {
				t.Fatal(err)
			}
			validateSchemaJSON(t, schema, string(data), true)
		})
	}
}

func TestToolProviderSchemaDefinesRemoteMCPImplementation(t *testing.T) {
	schema := compileReferenceSchema(t, "tool-provider.schema.json")
	data, err := os.ReadFile(filepath.Join("testdata", "remote-mcp", "tool-provider.json"))
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	validateSchemaJSON(t, schema, valid, true)
	validateSchemaJSON(t, schema, strings.Replace(valid, `"streamable-http"`, `"sse"`, 1), false)
	validateSchemaJSON(t, schema, strings.Replace(valid, `"search-mcp-endpoint"`, `"https://example.test/mcp"`, 1), false)
	for _, header := range []string{"Authorization", "Cookie", "X-API-Key"} {
		t.Run(header, func(t *testing.T) {
			withHeader := strings.Replace(valid, `"Authorization"`, `"`+header+`"`, 1)
			plaintext := strings.Replace(
				withHeader,
				`"secretRef": {
          "name": "search-mcp-auth",
          "key": "token"
        }`,
				`"value": "plaintext-secret"`,
				1,
			)
			validateSchemaJSON(t, schema, plaintext, false)
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
	  "toolProviders":[{
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
	  "toolProviders":[%s]
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

func TestToolProviderCompositionSchemaDefinesStandaloneSandboxInput(t *testing.T) {
	schema := compileReferenceSchema(t, "tool-provider-composition.schema.json")
	data, err := os.ReadFile(filepath.Join("testdata", "tool-providers", "opa-tool-provider-composition.json"))
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	validateSchemaJSON(t, schema, valid, true)
	validateSchemaJSON(t, schema, strings.Replace(valid, `"variantDigest"`, `"notVariantDigest"`, 1), false)
}

func TestProviderTerminologyRejectsEarlierDraftFields(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		fixture string
		current string
		legacy  string
		target  any
	}{
		{"suite catalog", "suite.schema.json", "minimal/agentsuite.json", "toolProviderCatalog", "toolCatalog", &Suite{}},
		{"agent requirements", "agent.schema.json", "minimal/agents/writer.json", "toolProviders", "tools", &Agent{}},
		{"composition resolutions", "composition.schema.json", "minimal/compositions/writer-linux-amd64.json", "toolProviders", "tools", &Composition{}},
		{"callable tools", "tool-provider.schema.json", "remote-mcp/tool-provider.json", "tools", "operations", &ToolProvider{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			schema := compileReferenceSchema(t, test.schema)
			validateSchemaJSON(t, schema, string(raw), true)
			if err := decodeStrict(raw, test.target); err != nil {
				t.Fatalf("current field rejected: %v", err)
			}
			legacy := strings.Replace(string(raw), `"`+test.current+`"`, `"`+test.legacy+`"`, 1)
			validateSchemaJSON(t, schema, legacy, false)
			if err := decodeStrict([]byte(legacy), test.target); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("legacy field must fail closed, got %v", err)
			}
		})
	}

	for _, test := range []struct {
		name   string
		raw    string
		target any
	}{
		{"nested provider", `{"provider":{"protocol":"mcp","revision":"2025-06-18","operations":[]}}`, &ToolProvider{}},
		{"catalog entries", `{"tools":[]}`, &ToolProviderCatalog{}},
		{"suite compositions", `{"toolCompositions":[]}`, &Suite{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := decodeStrict([]byte(test.raw), test.target); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("legacy field must fail closed, got %v", err)
			}
		})
	}
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
