package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func pairProtocolBranch(version int, calls *atomic.Int32) Operation {
	required, field, value := []string{"path", "canonical"}, "canonical", "agent"
	if version == 2 {
		required, field, value = []string{"target", "native"}, "native", "squad"
	}
	if version == 3 {
		required, field, value = []string{"path", "sourceVersion", "paired"}, "paired", "tests"
	}
	props := map[string]*jsonschema.Schema{field: {Type: "string", Enum: []any{value}}}
	minLength := 1
	for _, name := range required {
		if props[name] == nil {
			props[name] = &jsonschema.Schema{Type: "string", MinLength: &minLength}
		}
	}
	return Operation{
		Schema: &jsonschema.Schema{Type: "object", Required: required, Properties: props, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}},
		Handler: func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			calls.Add(1)
			return &sdk.CallToolResult{StructuredContent: map[string]any{"branch": version}}, nil
		},
	}
}

func TestProtocolPairedIndependentBranches(t *testing.T) {
	ops := protocolFixture()
	var calls atomic.Int32
	for _, name := range []string{"create", "edit", "run", "test"} {
		op, native, paired := pairProtocolBranch(1, &calls), pairProtocolBranch(2, &calls), pairProtocolBranch(3, &calls)
		op.V2, op.V3 = &native, &paired
		ops[name] = op
	}
	cs := connectFixture(t, ops)
	list, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(list.Tools) != 5 {
		t.Fatalf("original tools: %+v %v", list, err)
	}
	for _, tool := range list.Tools {
		b, _ := json.Marshal(tool.InputSchema)
		var schema jsonschema.Schema
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.OneOf != nil || schema.AnyOf != nil || schema.AllOf != nil {
			t.Fatalf("top-level combinator: %s", b)
		}
		if tool.Name == "set_tool_state" {
			continue
		}
		// Path is shared by v1/v3 but not required by native v2.
		if !slices.Equal(schema.Required, []string{"schemaVersion"}) {
			t.Fatalf("required fields are not the three-way intersection: %s", b)
		}
		if !slices.Equal(schema.Properties["schemaVersion"].Enum, []any{float64(1), float64(2), float64(3)}) || schema.Properties["canonical"] == nil || schema.Properties["native"] == nil || schema.Properties["paired"] == nil {
			t.Fatalf("incomplete discovery union: %s", b)
		}
		for _, tc := range []struct {
			version int
			args    map[string]any
		}{
			{1, map[string]any{"schemaVersion": 1, "path": "x.agent.md", "canonical": "agent"}},
			{2, map[string]any{"schemaVersion": 2, "target": "squad-members", "native": "squad"}},
			{3, map[string]any{"schemaVersion": 3, "path": "x.agent.md", "sourceVersion": "opaque", "paired": "tests"}},
		} {
			r, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: tool.Name, Arguments: tc.args})
			if err != nil || r.IsError {
				t.Fatalf("branch %d: %+v %v", tc.version, r, err)
			}
			b, _ := json.Marshal(r.StructuredContent)
			if string(b) != map[int]string{1: `{"branch":1}`, 2: `{"branch":2}`, 3: `{"branch":3}`}[tc.version] {
				t.Fatalf("wrong handler: %s", b)
			}
		}
		for _, args := range []map[string]any{
			{"schemaVersion": 1, "canonical": "agent"},
			{"schemaVersion": 1, "path": "x.agent.md", "canonical": "agent", "paired": "tests"},
			{"schemaVersion": 2, "target": "squad-members", "native": "squad", "sourceVersion": "opaque"},
			{"schemaVersion": 2, "target": "squad-members", "native": "squad", "path": "x.agent.md"},
			{"schemaVersion": 3, "path": "x.agent.md", "paired": "tests"},
			{"schemaVersion": 3, "path": "x.agent.md", "sourceVersion": "", "paired": "tests"},
			{"schemaVersion": 3, "path": "x.agent.md", "sourceVersion": "opaque", "paired": "wrong"},
			{"schemaVersion": 3, "path": "x.agent.md", "sourceVersion": "opaque", "paired": "tests", "target": "squad-members"},
			{"schemaVersion": 3, "path": "x.agent.md", "sourceVersion": "opaque", "paired": "tests", "command": "private"},
			{"schemaVersion": 9}, {"schemaVersion": "3"}, {},
		} {
			before := calls.Load()
			r, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: tool.Name, Arguments: args})
			if err != nil || !r.IsError || calls.Load() != before {
				t.Fatalf("bad operands reached handler: %+v %v", r, err)
			}
		}
	}
	// Caller-owned schema and required slices must not acquire discovery fields.
	if ops["create"].Schema.Properties["schemaVersion"] != nil || len(ops["create"].V2.Schema.Required) != 2 || len(ops["create"].Schema.Required) != 2 {
		t.Fatal("registration mutated caller schema")
	}
}

func TestProtocolPairedPerOperationAndRoles(t *testing.T) {
	for _, role := range []ServerRole{OriginalServer, PreviewServer} {
		ops := protocolFixture()
		if role == PreviewServer {
			delete(ops, "set_tool_state")
		}
		var calls atomic.Int32
		op, paired := pairProtocolBranch(1, &calls), pairProtocolBranch(3, &calls)
		op.V3 = &paired
		ops["edit"] = op
		server, err := NewServer("test", ops, role)
		if err != nil {
			t.Fatal(err)
		}
		a, b := sdk.NewInMemoryTransports()
		ss, err := server.Connect(t.Context(), a, nil)
		if err != nil {
			t.Fatal(err)
		}
		client := sdk.NewClient(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
		cs, err := client.Connect(t.Context(), b, nil)
		if err != nil {
			ss.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { cs.Close(); ss.Close() })
		list, err := cs.ListTools(t.Context(), nil)
		want := 5
		if role == PreviewServer {
			want = 4
		}
		if err != nil || len(list.Tools) != want {
			t.Fatalf("role tools: %+v %v", list, err)
		}
		for _, tool := range list.Tools {
			if tool.Name != "edit" {
				continue
			}
			b, _ := json.Marshal(tool.InputSchema)
			var schema jsonschema.Schema
			if err := json.Unmarshal(b, &schema); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(schema.Required, []string{"path", "schemaVersion"}) || !slices.Equal(schema.Properties["schemaVersion"].Enum, []any{float64(1), float64(3)}) {
				t.Fatalf("v1/v3 discovery lost common required field or advertised native: %s", b)
			}
		}
		r, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: map[string]any{"schemaVersion": 3, "path": "x.agent.md", "sourceVersion": "opaque", "paired": "tests"}})
		if err != nil || r.IsError || calls.Load() != 1 {
			t.Fatalf("paired-only branch unavailable for role %d: %+v %v", role, r, err)
		}
		for _, name := range []string{"create", "run", "test", "set_tool_state"} {
			r, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: map[string]any{"schemaVersion": 3}})
			if role == PreviewServer && name == "set_tool_state" {
				if err == nil {
					t.Fatal("preview exposed settings writer")
				}
			} else if err != nil || !r.IsError {
				t.Fatalf("unadvertised v3 accepted: %+v %v", r, err)
			}
		}
	}
	for _, version := range []int{2, 3} {
		ops := protocolFixture()
		op, branch := ops["set_tool_state"], ops["run"]
		if version == 2 {
			op.V2 = &branch
		} else {
			op.V3 = &branch
		}
		ops["set_tool_state"] = op
		if _, err := NewServer("test", ops, OriginalServer); err == nil {
			t.Fatalf("settings registered v%d", version)
		}
	}
}

func TestProtocolPairedRegistrationAndInstructions(t *testing.T) {
	for _, versions := range [][]int{nil, {2}, {3}, {2, 3}} {
		ops := protocolFixture()
		op := ops["run"]
		for _, version := range versions {
			branch := ops["edit"]
			if version == 2 {
				op.V2 = &branch
			} else {
				op.V3 = &branch
			}
		}
		ops["run"] = op
		cs := connectFixture(t, ops)
		for _, version := range []int{2, 3} {
			token := map[int]string{2: "schemaVersion=2", 3: "schemaVersion=3"}[version]
			if strings.Contains(cs.InitializeResult().Instructions, token) != slices.Contains(versions, version) {
				t.Fatalf("misleading instructions: %s", cs.InitializeResult().Instructions)
			}
		}
	}
	for _, kind := range []string{"nil-schema", "wrong-type", "nil-handler", "unresolvable"} {
		ops := protocolFixture()
		op, branch := ops["run"], ops["edit"]
		switch kind {
		case "nil-schema":
			branch.Schema = nil
		case "wrong-type":
			branch.Schema = &jsonschema.Schema{Type: "string"}
		case "nil-handler":
			branch.Handler = nil
		case "unresolvable":
			branch.Schema = &jsonschema.Schema{Type: "object", Ref: "#/$defs/missing"}
		}
		op.V3 = &branch
		ops["run"] = op
		if _, err := NewServer("test", ops, OriginalServer); err == nil {
			t.Fatalf("accepted v3 %s", kind)
		}
	}
}

func TestProtocolPairedErrorsKeepVersionWithoutNativeDispatch(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{refusal("stale_revision"), "stale_revision"},
		{refusal("squad_setup_failed"), "operation_failed"},
		{errors.New("private credential endpoint"), "operation_failed"},
		{context.Canceled, "cancelled"},
		{context.DeadlineExceeded, "deadline_exceeded"},
	} {
		result := versionedError(tc.err, 3)
		data := result.StructuredContent.(map[string]any)
		if data["schemaVersion"] != 3 || data["reasonCode"] != tc.code {
			t.Fatalf("v3 error: %+v", data)
		}
		b, _ := json.Marshal(result)
		if strings.Contains(string(b), "private credential") {
			t.Fatalf("unsafe error: %s", b)
		}
	}
	native := versionedError(refusal("squad_setup_failed"), 2).StructuredContent.(map[string]any)
	if native["schemaVersion"] != 2 || native["reasonCode"] != "squad_setup_failed" {
		t.Fatalf("native error changed: %+v", native)
	}
	for _, version := range []int{0, 1, 4} {
		data := versionedError(refusal("stale_revision"), version).StructuredContent.(map[string]any)
		if data["schemaVersion"] != 1 {
			t.Fatalf("unknown version changed: %+v", data)
		}
	}
	ops := protocolFixture()
	op := ops["run"]
	branch := ops["edit"]
	branch.Handler = func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return nil, refusal("squad_setup_failed")
	}
	op.V3 = &branch
	ops["run"] = op
	cs := connectFixture(t, ops)
	for _, tc := range []struct {
		args    map[string]any
		version float64
		code    string
	}{
		{map[string]any{"schemaVersion": 3}, 3, "operation_failed"},
		{map[string]any{"schemaVersion": 99}, 1, "incompatible_version"},
		{map[string]any{"schemaVersion": "3"}, 1, "incompatible_version"},
	} {
		r, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "run", Arguments: tc.args})
		if err != nil || !r.IsError {
			t.Fatalf("error call: %+v %v", r, err)
		}
		b, _ := json.Marshal(r.StructuredContent)
		var data map[string]any
		if err := json.Unmarshal(b, &data); err != nil {
			t.Fatal(err)
		}
		if data["schemaVersion"] != tc.version || data["reasonCode"] != tc.code {
			t.Fatalf("wire error: %s", b)
		}
	}
}
