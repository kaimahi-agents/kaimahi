package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProtocolNativeVersionIsPerOperationAndClosed(t *testing.T) {
	ops := protocolFixture()
	op := ops["create"]
	op.V2 = &Operation{Schema: &jsonschema.Schema{Type: "object", Required: []string{"target"}, Properties: map[string]*jsonschema.Schema{"target": {Type: "string", Enum: []any{"squad-members"}}}, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}, Handler: func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{StructuredContent: map[string]any{"schemaVersion": 2, "kind": "native-fixture"}}, nil
	}}
	ops["create"] = op
	cs := connectFixture(t, ops)
	list, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(list.Tools) != 5 {
		t.Fatal("changed tool count")
	}
	for _, tool := range list.Tools {
		b, _ := json.Marshal(tool.InputSchema)
		var schema map[string]any
		if json.Unmarshal(b, &schema) != nil {
			t.Fatal("invalid discovery schema")
		}
		if schema["oneOf"] != nil || schema["anyOf"] != nil || schema["allOf"] != nil || schema["properties"] == nil {
			t.Fatalf("model-incompatible top-level union: %s", b)
		}
		versions := schema["properties"].(map[string]any)["schemaVersion"].(map[string]any)["enum"].([]any)
		if (tool.Name == "create") != (len(versions) == 2) {
			t.Fatalf("wrong per-operation version support: %s", b)
		}
	}
	result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "create", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-members"}})
	if err != nil || result.IsError {
		t.Fatalf("native branch unavailable: %+v %v", result, err)
	}
	b, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(b), `"kind":"native-fixture"`) {
		t.Fatal("native request reached canonical handler")
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"edit", map[string]any{"schemaVersion": 2, "target": "squad-members"}},
		{"create", map[string]any{"schemaVersion": 2, "target": "squad-members", "path": "mixed.agent.md"}},
		{"create", map[string]any{"schemaVersion": 2, "target": "unknown"}},
	} {
		res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err != nil || !res.IsError {
			t.Fatalf("unsupported/mixed native request accepted: %+v %v", res, err)
		}
	}
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "create", Arguments: map[string]any{"schemaVersion": 1}})
	if err != nil || res.IsError {
		t.Fatal("v1 behavior changed")
	}
}
