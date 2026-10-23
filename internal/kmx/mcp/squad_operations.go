package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type squadMemberInput struct {
	ID               string `json:"memberId"`
	Role             string `json:"role"`
	DisplayRole      string `json:"displayRole"`
	TaskInstructions string `json:"taskInstructions"`
	Model            string `json:"model"`
	Examples         string `json:"examples"`
}

func (m squadMemberInput) member() SquadMember {
	return SquadMember(m)
}
func nativeString(max int) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", MinLength: new(1), MaxLength: new(max)}
}
func nativeSchema(target string, properties map[string]*jsonschema.Schema, required ...string) *jsonschema.Schema {
	properties["target"] = &jsonschema.Schema{Type: "string", Enum: []any{target}}
	return &jsonschema.Schema{Type: "object", Properties: properties, Required: append(required, "target"), AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
}
func nativeMemberSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"memberId": nativeString(63), "role": nativeString(160), "displayRole": nativeString(160), "taskInstructions": nativeString(agentfile.MaxFileBytes), "model": nativeString(256), "examples": nativeString(agentfile.MaxFileBytes)}, Required: []string{"memberId", "role", "displayRole", "taskInstructions", "model", "examples"}, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
}

type NativeMemberResult struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Kind          string                 `json:"kind"`
	Target        string                 `json:"target"`
	MemberID      string                 `json:"memberId"`
	State         string                 `json:"state"`
	Label         string                 `json:"label"`
	File          *File                  `json:"file,omitempty"`
	Profile       *NativeMemberProfile   `json:"profile,omitempty"`
	Preview       *app.FilePreviewResult `json:"preview,omitempty"`
}
type NativeMemberProfile struct {
	Release  string `json:"release"`
	MemberID string `json:"memberId"`
	Role     string `json:"role"`
}

func nativeMemberResult(id string, d *agentfile.SquadCharter) NativeMemberResult {
	r := NativeMemberResult{SchemaVersion: 2, Kind: "kmx.squad-member-operation", Target: "squad-member", State: "not_setup", Label: "not set up by kmx"}
	if d != nil {
		r.MemberID = id
		r.State = "ready"
		r.Label = "native state validated; origin not inferred"
		r.File = &File{Path: squadPath(SquadMember{ID: id}), Revision: d.Revision}
		r.Profile = &NativeMemberProfile{d.Profile.Release, id, d.Profile.Role}
	}
	return r
}

// WithSquadOperations adds only implemented v2 branches. No program, root,
// release or route is accepted from tool input. Canonical branches are retained.
func WithSquadOperations(ops map[string]Operation, c *SquadCreator, a *app.App, route *modelaccess.Route) map[string]Operation {
	if c == nil {
		return ops
	}
	bound := make(map[string]Operation, len(ops))
	for name, op := range ops {
		bound[name] = op
	}
	var selected *modelaccess.Route
	if route != nil {
		frozen := *route
		selected = &frozen
	}
	create := bound["create"]
	create.Description += " Native v2 target squad-members conditionally initializes, casts and enriches an approved frozen proposal; no member/team execution."
	leadSchema := nativeMemberSchema()
	leadSchema.Properties["role"] = &jsonschema.Schema{Type: "string", Enum: []any{"lead"}}
	leadSchema.Properties["displayRole"] = &jsonschema.Schema{Type: "string", Enum: []any{"Lead / Architect"}}
	create.V2 = &Operation{Schema: nativeSchema("squad-members", map[string]*jsonschema.Schema{"lead": leadSchema, "specialists": {Type: "array", Items: nativeMemberSchema(), MaxItems: new(7)}}, "lead", "specialists"), Handler: func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var input struct {
			Lead        squadMemberInput   `json:"lead"`
			Specialists []squadMemberInput `json:"specialists"`
		}
		if json.Unmarshal(req.Params.Arguments, &input) != nil {
			return nil, refusal("invalid_options")
		}
		plan := SquadPlan{Lead: input.Lead.member()}
		for _, m := range input.Specialists {
			if strings.EqualFold(m.Role, "lead") || strings.EqualFold(m.DisplayRole, "Lead / Architect") {
				return nil, refusal("invalid_squad_members")
			}
			plan.Specialists = append(plan.Specialists, m.member())
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		result, err := c.Create(ctx, plan)
		if err != nil {
			failure := versionedError(err, 2)
			data := failure.StructuredContent.(map[string]any)
			data["kind"] = "kmx.squad-create"
			data["target"] = "squad-members"
			data["result"] = result
			return failure, nil
		}
		return &sdk.CallToolResult{StructuredContent: map[string]any{"schemaVersion": 2, "kind": "kmx.squad-create", "target": "squad-members", "result": result}, Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("Native squad authoring: setup=%s; %d requested members. Inspect each member state in structuredContent; no member/team execution or package pass is claimed.", result.Setup, len(result.Members))}}}, nil
	}}
	bound["create"] = create
	for _, name := range []string{"edit", "run", "test"} {
		properties := map[string]*jsonschema.Schema{"memberId": nativeString(63)}
		required := []string{"memberId"}
		if name == "edit" {
			properties["expectedRevision"] = nativeString(80)
			properties["taskInstructions"] = nativeString(agentfile.MaxFileBytes)
			properties["model"] = nativeString(256)
			properties["examples"] = nativeString(agentfile.MaxFileBytes)
			required = append(required, "expectedRevision", "taskInstructions", "model", "examples")
		} else {
			properties["caseId"] = nativeString(agentfile.MaxInputBytes)
			properties["showContent"] = &jsonschema.Schema{Type: "boolean"}
			if name == "run" {
				properties["input"] = &jsonschema.Schema{Type: "string", MaxLength: new(agentfile.MaxInputBytes)}
			}
		}
		op := bound[name]
		op.Description += " Native v2 target squad-member revalidates native casting state; read kmx://squad-member/{memberId} for current owned fields/revision. Not package/team evidence."
		op.V2 = &Operation{Schema: nativeSchema("squad-member", properties, required...), Handler: func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			var input struct {
				ID       string  `json:"memberId"`
				Expected string  `json:"expectedRevision"`
				Task     string  `json:"taskInstructions"`
				Model    string  `json:"model"`
				Examples string  `json:"examples"`
				CaseID   string  `json:"caseId"`
				Input    *string `json:"input"`
				Show     bool    `json:"showContent"`
			}
			if json.Unmarshal(req.Params.Arguments, &input) != nil {
				return nil, refusal("invalid_options")
			}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			d, err := c.Member(ctx, input.ID)
			if err != nil {
				return nativeMemberFailure(err, input.ID), nil
			}
			result := nativeMemberResult(input.ID, d)
			if name == "edit" {
				file, err := c.EditMember(ctx, input.ID, input.Expected, SquadMember{TaskInstructions: input.Task, Model: input.Model, Examples: input.Examples})
				if err != nil {
					return nativeMemberFailure(err, input.ID), nil
				}
				result.File = &file
			} else {
				preview, err := a.PreviewSquadSnapshot(ctx, d, app.FilePreviewOptions{File: result.File.Path, Operation: name, CaseID: input.CaseID, Input: input.Input, ShowContent: input.Show, Route: selected})
				result.Preview = &preview
				var failure *app.FilePreviewError
				completed := errors.As(err, &failure) && failure.ExitCode() == 2 && preview.State == "complete"
				return &sdk.CallToolResult{IsError: err != nil && !completed, StructuredContent: result, Content: []sdk.Content{&sdk.TextContent{Text: previewSummary(preview) + "Local member preview only; not a package/team pass."}}}, nil
			}
			return &sdk.CallToolResult{StructuredContent: result, Content: []sdk.Content{&sdk.TextContent{Text: "Native owned fields saved; preview results must be rerun at the new revision."}}}, nil
		}}
		bound[name] = op
	}
	return bound
}

func nativeMemberFailure(err error, id string) *sdk.CallToolResult {
	failure := versionedError(err, 2)
	var detail *Error
	if errors.As(err, &detail) && detail.Code == "squad_not_setup" {
		data := failure.StructuredContent.(map[string]any)
		data["member"] = nativeMemberResult(id, nil)
		data["state"] = "not_setup"
	}
	return failure
}
