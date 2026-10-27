package mcp

import (
	"context"
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// AuthoringOperations binds only Create/Edit. The production caller must add
// the shared Run/Test operations before NewServer will accept the bindings.
func AuthoringOperations(files *Files) map[string]Operation {
	ops := map[string]Operation{}
	for _, name := range []string{"create", "edit"} {
		schema := &jsonschema.Schema{Type: "object", Required: []string{"path", "content"}, Properties: map[string]*jsonschema.Schema{
			"path": {Type: "string"}, "content": {Type: "string", MaxLength: new(maxFileBytes)},
		}, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
		if files != nil && files.root != nil {
			schema.Properties["path"].MaxLength = new(256)
		}
		description := "Create a validated agent file at an absolute path or a path relative to the server cwd (or explicit --root); never overwrite an existing target."
		if name == "edit" {
			schema.Properties["expectedRevision"] = &jsonschema.Schema{Type: "string", Pattern: `^sha256:[a-f0-9]{64}$`}
			schema.Required = append(schema.Required, "expectedRevision")
			schema.Properties["exampleEdits"] = exampleEditsSchema()
			description = "Replace one validated charter at its expected revision, preserving mode bits, not ownership, ACLs or extended attributes. Revision is checked just before rename under an OS lock; outside editors ignoring that lock can still race."
		}
		ops[name] = Operation{Schema: schema, Description: description, Handler: func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			if files == nil {
				return nil, refusal("invalid_root")
			}
			var input struct {
				Path             string                  `json:"path"`
				Content          string                  `json:"content"`
				ExpectedRevision string                  `json:"expectedRevision"`
				ExampleEdits     []agentfile.ExampleEdit `json:"exampleEdits"`
			}
			if json.Unmarshal(req.Params.Arguments, &input) != nil {
				return nil, refusal("invalid_options")
			}
			var file File
			var err error
			if name == "create" {
				file, err = files.Create(ctx, input.Path, []byte(input.Content))
			} else if len(input.ExampleEdits) > 0 {
				file, err = files.editExamples(ctx, input.Path, input.ExpectedRevision, []byte(input.Content), input.ExampleEdits)
			} else {
				file, err = files.Edit(ctx, input.Path, input.ExpectedRevision, []byte(input.Content))
			}
			if err != nil {
				return nil, err
			}
			result := struct {
				SchemaVersion int    `json:"schemaVersion"`
				Operation     string `json:"operation"`
				File          File   `json:"file"`
			}{1, name, file}
			b, err := json.Marshal(result)
			if err != nil {
				return nil, refusal("operation_failed")
			}
			return &sdk.CallToolResult{StructuredContent: result, Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}}, nil
		}}
	}
	return ops
}
