package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type pairPreviewInput struct {
	Path                    string                    `json:"path"`
	Tests                   string                    `json:"tests"`
	ExpectedSourceVersion   string                    `json:"expectedSourceVersion"`
	Model                   string                    `json:"model"`
	CaseID                  string                    `json:"caseId"`
	Input                   *string                   `json:"input"`
	ShowContent             bool                      `json:"showContent"`
	ShowFailedContent       bool                      `json:"showFailedContent"`
	RecordedReplayOnly      bool                      `json:"recordedReplayOnly"`
	AcknowledgeCopilotTests bool                      `json:"acknowledgeCopilotTests"`
	ExpectedToolSettings    *[]localtools.ToolSetting `json:"expectedToolSettings"`
}
type pairPreviewResult struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Kind          string                `json:"kind"`
	SourceVersion string                `json:"sourceVersion"`
	Preview       app.FilePreviewResult `json:"preview"`
}

func pairPreviewOperation(files *Files, application *app.App, selected *modelaccess.Route, name string, legacy Operation) *Operation {
	schema := legacy.Schema.CloneSchemas()
	delete(schema.Properties, "expectedRevision")
	schema.Required = append(schema.Required, "expectedSourceVersion")
	schema.Properties["expectedSourceVersion"] = &jsonschema.Schema{Type: "string", MinLength: new(1), MaxLength: new(128)}
	schema.Properties["tests"] = &jsonschema.Schema{Type: "string", MinLength: new(1)}
	return &Operation{Schema: schema, Description: "Preview one checked selected pair at its resource sourceVersion. Run answers without assertions; Test evaluates. Source-only context is not deployment authority.", Handler: func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var input pairPreviewInput
		if json.Unmarshal(req.Params.Arguments, &input) != nil {
			return nil, refusal("invalid_options")
		}
		if !strings.HasSuffix(filepath.Base(input.Path), ".agent.md") {
			return nil, refusal("invalid_options")
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		bound, err := files.bindPair(ctx, input.Path, input.Tests)
		if err != nil {
			return nil, err
		}
		defer bound.close()
		if _, ok := agentfile.DefaultTestsPath(bound.pair.AgentPath); !ok {
			return nil, refusal("invalid_options")
		}
		if err := bound.requireVersion(input.ExpectedSourceVersion); err != nil {
			return nil, err
		}
		pair := bound.pair
		if pair.Tests.State == "inline" && pair.Agent.HasFrontmatter && !input.RecordedReplayOnly && !input.AcknowledgeCopilotTests {
			settings, e := localtools.ReadSettingsSnapshot(ctx, pair.AgentPath, pair.Agent.Tools)
			for _, example := range pair.Tests.Suite.Examples {
				if input.CaseID != "" && example.ID != input.CaseID || example.Mocks == nil {
					continue
				}
				for _, tool := range example.Mocks.Tools {
					if e == nil && tool.Mode == "recorded" && len(tool.Calls) == 0 && !localtools.IsDisabled(settings.Settings, tool.Name) {
						return nil, refusal("confirmation_required")
					}
				}
			}
		}
		executionRoute := selected
		if executionRoute == nil && files.root == nil {
			model := pair.Agent.Model
			if input.Model != "" {
				model = input.Model
			}
			if model != "fake" {
				executionRoute = &modelaccess.Route{Provider: "copilot", Model: model, Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
			}
		}
		ctx = withHostProgress(ctx, name, req)
		opt := app.FilePreviewOptions{File: input.Path, SourceFile: pair.AgentPath, FullPath: files.root == nil, Operation: name, CaseID: input.CaseID, Input: input.Input, ShowContent: input.ShowContent, ShowFailedContent: input.ShowFailedContent, Model: input.Model, Route: executionRoute, SkipAssertions: name == "run", RecordedReplayOnly: input.RecordedReplayOnly, ExpectedToolSettings: input.ExpectedToolSettings, ValidateSource: bound.check, CaseFinished: caseProgressNotifier(ctx)}
		if name == "test" {
			opt = app.WithPairFilePreviewRetention(opt, pair)
		}
		result, err := application.PreviewAgentPairSnapshot(ctx, pair, opt)
		var previewErr *app.FilePreviewError
		completedNonpass := errors.As(err, &previewErr) && previewErr.ExitCode() == 2 && result.State == "complete"
		// Never publish paired results into the agent-only legacy latest cache.
		return &sdk.CallToolResult{IsError: err != nil && !completedNonpass, StructuredContent: pairPreviewResult{3, "kmx.agent-pair-preview", input.ExpectedSourceVersion, result}, Content: []sdk.Content{&sdk.TextContent{Text: previewSummary(result)}}}, nil
	}}
}
