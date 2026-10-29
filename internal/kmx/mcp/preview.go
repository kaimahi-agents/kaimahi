package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Operations binds original-server authoring, settings and execution to canonical parsing and the shared
// snapshot operation. An explicit route is trusted startup configuration, never
// tool input. Without one, unbound real-model previews use Copilot; fake and
// legacy root-bound previews retain their existing route behavior.
// A nil runner retains the application's honest runner_unavailable result.
func Operations(files *Files, application *app.App, route *modelaccess.Route) map[string]Operation {
	ops := AuthoringOperations(files)
	ops["set_tool_state"] = ToolStateOperation(files)
	var selected *modelaccess.Route
	if route != nil {
		frozen := *route
		selected = &frozen
	}
	for _, name := range []string{"run", "test"} {
		schema := &jsonschema.Schema{Type: "object", Required: []string{"path"}, Properties: map[string]*jsonschema.Schema{
			"path": {Type: "string"}, "model": {Type: "string"}, "caseId": {Type: "string", MinLength: new(1), MaxLength: new(agentfile.MaxInputBytes)}, "showContent": {Type: "boolean"}, "showFailedContent": {Type: "boolean"},
		}, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
		if files != nil && files.root != nil {
			schema.Properties["path"].MaxLength = new(256)
		}
		schema.Properties["expectedRevision"] = &jsonschema.Schema{Type: "string", Pattern: `^sha256:[a-f0-9]{64}$`}
		schema.Properties["acknowledgeCopilotTests"] = &jsonschema.Schema{Type: "boolean", Description: copilotTestsNotice + " Ask before capturing tool recordings in a Copilot file; set true only after go-ahead."}
		if name == "test" {
			schema.Properties["recordedReplayOnly"] = &jsonschema.Schema{Type: "boolean"}
			schema.Properties["expectedToolSettings"] = &jsonschema.Schema{Type: "array", MaxItems: new(32), Items: &jsonschema.Schema{Type: "object", Required: []string{"name", "state"}, Properties: map[string]*jsonschema.Schema{"name": {Type: "string"}, "state": {Type: "string", Enum: []any{"Enabled", "Disabled"}}}, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}}
		}
		if name == "run" {
			schema.Properties["input"] = &jsonschema.Schema{Type: "string", MaxLength: new(agentfile.MaxInputBytes)}
		}
		description := "Test this charter's saved tests in a kmx local preview, optionally one case (always partial). "
		if name == "run" {
			description = "Run this charter in a kmx local preview with optional ad-hoc input and saved examples, optionally one case (always partial). "
		}
		ops[name] = Operation{Schema: schema, Description: description + "Returns the shared revision, route, state and verdict; never deployment evidence. V1 only: A successful first live call for a pending Recorded tool saves its result in this file. Version 3 saves only the selected source after journal close and retains pair history. Version 3 requires expectedSourceVersion: Run answers without checks; Test evaluates. Inspect per-source partial/uncertain acknowledgements; do not retry automatically.", Handler: func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			var input struct {
				Path                    string                    `json:"path"`
				Model                   string                    `json:"model"`
				CaseID                  string                    `json:"caseId"`
				Input                   *string                   `json:"input"`
				ShowContent             bool                      `json:"showContent"`
				ShowFailedContent       bool                      `json:"showFailedContent"`
				ExpectedRevision        string                    `json:"expectedRevision"`
				RecordedReplayOnly      bool                      `json:"recordedReplayOnly"`
				AcknowledgeCopilotTests bool                      `json:"acknowledgeCopilotTests"`
				ExpectedToolSettings    *[]localtools.ToolSetting `json:"expectedToolSettings"`
			}
			if json.Unmarshal(req.Params.Arguments, &input) != nil {
				return nil, refusal("invalid_options")
			}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			sequence := files.beginPreview(input.Path)
			path, pathErr := files.rootRelative(input.Path)
			if pathErr != nil {
				return nil, pathErr
			}
			sourcePath := path
			if files.root != nil {
				sourcePath = filepath.Join(files.root.Name(), filepath.FromSlash(path))
			}
			retentionPath := sourcePath
			if name != "test" {
				retentionPath = ""
			}
			parsed, parseErr := files.snapshot(ctx, path)
			var detail *agentfile.Error
			if parseErr != nil && !errors.As(parseErr, &detail) {
				return nil, parseErr
			}
			if input.ExpectedRevision != "" && (parsed == nil || parsed.Revision != input.ExpectedRevision) {
				return nil, refusal("stale_revision")
			}
			confirmationRequired := false
			executionRoute := previewExecutionRoute(files, selected, parsed, input.Model)
			ctx = withHostProgress(ctx, name, req)
			options := app.FilePreviewOptions{File: input.Path, SourceFile: sourcePath, FullPath: files.root == nil, Operation: name, CaseID: input.CaseID, Input: input.Input, ShowContent: input.ShowContent, ShowFailedContent: input.ShowFailedContent, Model: input.Model, Route: executionRoute, CaseFinished: caseProgressNotifier(ctx)}
			if retentionPath != "" && parsed != nil {
				options = app.WithFilePreviewRetention(options, retentionPath, parsed.Revision)
			}
			options.RecordedReplayOnly, options.ExpectedToolSettings = input.RecordedReplayOnly, input.ExpectedToolSettings
			options.PrepareRecordingSave = func(ctx context.Context, revision string) (app.RecordingSaver, error) {
				if err := copilotCaptureConsent(parsed, input.RecordedReplayOnly, input.AcknowledgeCopilotTests); err != nil {
					confirmationRequired = true
					return nil, err
				}
				save, err := files.RecordingSaver(ctx, path, revision)
				return app.RecordingSaver(save), err
			}
			result, err := application.PreviewAgentSnapshot(ctx, parsed, options)
			if confirmationRequired {
				return nil, refusal("confirmation_required")
			}
			if detail != nil && result.ReasonCode == "invalid_file" {
				result.Diagnostic = &app.FilePreviewDiagnostic{Field: detail.Field, Line: detail.Location.Line, Column: detail.Location.Column, Repair: detail.Problem}
			}
			files.finishPreview(sequence, path, result)
			// Keep the complete safe result even when the application returns an error.
			// Returning that error to the generic protocol wrapper would discard it.
			var previewErr *app.FilePreviewError
			completedNonpass := errors.As(err, &previewErr) && previewErr.ExitCode() == 2 && result.State == "complete"
			return &sdk.CallToolResult{IsError: err != nil && !completedNonpass, StructuredContent: result, Content: []sdk.Content{&sdk.TextContent{Text: previewSummary(result)}}}, nil
		}}
	}
	for _, name := range []string{"run", "test"} {
		op := ops[name]
		op.V3 = pairPreviewOperation(files, application, selected, name, op)
		ops[name] = op
	}
	for _, name := range []string{"create", "edit"} {
		op := ops[name]
		op.V3 = pairAuthoringOperation(files, name)
		ops[name] = op
	}
	if files != nil {
		files.viewMu.Lock()
		files.pairWrites = true
		files.pairExecute = true
		files.viewMu.Unlock()
	}
	return ops
}

func previewExecutionRoute(files *Files, selected *modelaccess.Route, parsed *agentfile.Agent, override string) *modelaccess.Route {
	if selected != nil || files.root != nil || parsed == nil {
		return selected
	}
	model := parsed.Model
	if override != "" {
		model = override
	}
	if model == "fake" {
		return nil
	}
	return &modelaccess.Route{Provider: "copilot", Model: model, Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
}

// Summarize only the App's safe projection, never runner output or authored operands.
// StructuredContent retains all metadata; chat text has independent row/byte limits.
func previewSummary(r app.FilePreviewResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: state=%s verdict=%s; scope=%s; fullPass=%t. %s\n", r.Operation, r.State, r.Verdict, r.Selection.Scope, r.FullPass, r.Reason)
	if r.Runner != "" {
		fmt.Fprintf(&b, "%s\n", r.Runner)
	}
	rows, omitted := 0, 0
	add := func(line string) {
		if rows >= 10 || b.Len()+len(line) > 8000 {
			omitted++
			return
		}
		b.WriteString(line)
		rows++
	}
	for _, ex := range r.Examples {
		for _, assertion := range ex.Assertions {
			if assertion.Verdict != runtime.EvaluationPass {
				add(fmt.Sprintf("Example %s / assertion %s: %s. %s\n", previewSummaryField(ex.ID, 160), previewSummaryField(assertion.ID, 160), assertion.Verdict, assertion.Reason))
			}
		}
		if ex.AnswerExcerpt != "" {
			add(fmt.Sprintf("Example %s answer excerpt (untrusted): %s\n", previewSummaryField(ex.ID, 160), previewSummaryField(ex.AnswerExcerpt, 512)))
		}
	}
	if r.AdHoc != nil && r.AdHoc.AnswerExcerpt != "" {
		add(fmt.Sprintf("Ad-hoc answer excerpt (untrusted): %s\n", previewSummaryField(r.AdHoc.AnswerExcerpt, 512)))
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "%d detail rows omitted; see structuredContent.\n", omitted)
	}
	return b.String()
}

func previewSummaryField(s string, limit int) string {
	if len(s) > limit {
		s = s[:limit]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "…"
	}
	return strconv.Quote(s)
}

func (f *Files) snapshot(ctx context.Context, p string) (*agentfile.Agent, error) {
	agent, _, err := f.readSnapshot(ctx, p, false)
	return agent, err
}

func (f *Files) inspect(ctx context.Context, p string) (*agentfile.Agent, []byte, error) {
	return f.readSnapshot(ctx, p, true)
}

// Retain the same bounded descriptor bytes used by parsing and revision hashing.
// The resource must never reread a possibly changed path for authoring content.
func (f *Files) readSnapshot(ctx context.Context, p string, inspection bool) (*agentfile.Agent, []byte, error) {
	if err := cancelled(ctx); err != nil {
		return nil, nil, err
	}
	p, err := f.rootRelative(p)
	if err != nil {
		return nil, nil, err
	}
	if !f.allowedPath(p) {
		return nil, nil, refusal("unsafe_path")
	}
	if err := f.requireSingleSource(p); err != nil {
		return nil, nil, err
	}
	parent, err := f.parent(p, false)
	if err != nil {
		return nil, nil, err
	}
	defer parent.Close()
	file, b, _, err := readTarget(parent, filepath.Base(p))
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	var agent *agentfile.Agent
	if inspection {
		agent, err = agentfile.Inspect(p, b)
	} else {
		agent, err = agentfile.Parse(p, b)
	}
	return agent, b, err
}
