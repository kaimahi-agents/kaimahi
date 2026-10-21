package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Operations binds the four tools directly to canonical parsing and the shared
// snapshot operation. The route is trusted startup configuration, never tool input.
// A nil runner retains the application's honest runner_unavailable result.
func Operations(files *Files, application *app.App, route *modelaccess.Route) map[string]Operation {
	ops := AuthoringOperations(files)
	var selected *modelaccess.Route
	if route != nil {
		frozen := *route
		selected = &frozen
	}
	for _, name := range []string{"run", "test"} {
		schema := &jsonschema.Schema{Type: "object", Required: []string{"path"}, Properties: map[string]*jsonschema.Schema{
			"path": {Type: "string", MaxLength: new(256)}, "caseId": {Type: "string", MinLength: new(1), MaxLength: new(agentfile.MaxInputBytes)}, "showContent": {Type: "boolean"},
		}, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
		if name == "run" {
			schema.Properties["input"] = &jsonschema.Schema{Type: "string", MaxLength: new(agentfile.MaxInputBytes)}
		}
		ops[name] = Operation{Schema: schema, Description: "kmx local preview of this charter; saved examples, optionally one case (always partial). Returns the shared revision, route, state and verdict; never deployment evidence.", Handler: func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			var input struct {
				Path        string  `json:"path"`
				CaseID      string  `json:"caseId"`
				Input       *string `json:"input"`
				ShowContent bool    `json:"showContent"`
			}
			if json.Unmarshal(req.Params.Arguments, &input) != nil {
				return nil, refusal("invalid_options")
			}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			parsed, parseErr := files.snapshot(ctx, input.Path)
			var detail *agentfile.Error
			if parseErr != nil && !errors.As(parseErr, &detail) {
				return nil, parseErr
			}
			result, err := application.PreviewAgentSnapshot(ctx, parsed, app.FilePreviewOptions{File: input.Path, Operation: name, CaseID: input.CaseID, Input: input.Input, ShowContent: input.ShowContent, Route: selected})
			if detail != nil && result.ReasonCode == "invalid_file" {
				result.Diagnostic = &app.FilePreviewDiagnostic{Field: detail.Field, Line: detail.Location.Line, Column: detail.Location.Column, Repair: detail.Problem}
			}
			// Keep the complete safe result even when the application returns an error.
			// Returning that error to the generic protocol wrapper would discard it.
			b, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				return nil, refusal("operation_failed")
			}
			return &sdk.CallToolResult{IsError: err != nil, StructuredContent: result, Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}}, nil
		}}
	}
	return ops
}
func (f *Files) snapshot(ctx context.Context, p string) (*agentfile.Agent, error) {
	if err := cancelled(ctx); err != nil {
		return nil, err
	}
	if !safePath(p) {
		return nil, refusal("unsafe_path")
	}
	parent, err := f.parent(p, false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	file, b, _, err := readTarget(parent, path.Base(p))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return agentfile.Parse(p, b)
}
