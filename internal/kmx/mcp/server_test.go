package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func protocolFixture() map[string]Operation {
	ops := map[string]Operation{}
	for _, name := range []string{"create", "edit", "run", "test"} {
		ops[name] = Operation{Schema: &jsonschema.Schema{Type: "object"}, Handler: func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "fixture"}}}, nil
		}}
	}
	return ops
}
func connectFixture(t *testing.T, ops map[string]Operation) *sdk.ClientSession {
	t.Helper()
	s, err := NewServer("test-engine", ops)
	if err != nil {
		t.Fatal(err)
	}
	a, b := sdk.NewInMemoryTransports()
	ss, err := s.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := sdk.NewClient(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	cs, err := c.Connect(t.Context(), b, nil)
	if err != nil {
		ss.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); ss.Close() })
	return cs
}
func TestProtocolDiscoveryAndVersion(t *testing.T) {
	cs := connectFixture(t, protocolFixture())
	info := cs.InitializeResult()
	if info.ServerInfo.Version != "test-engine" || info.ProtocolVersion == "" || !strings.Contains(info.Instructions, "schemaVersion=1") {
		t.Fatalf("initialize: %+v", info)
	}
	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "create,edit,run,test" {
		t.Fatalf("tools: %v", names)
	}
	_, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "secret-tool-value", Arguments: map[string]any{"schemaVersion": 1}})
	if err == nil || strings.Contains(err.Error(), "secret-tool-value") {
		t.Fatalf("unknown tool error: %v", err)
	}
	_, resourceErr := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "private-resource-value"})
	var protocolErr *jsonrpc.Error
	if !errors.As(resourceErr, &protocolErr) || protocolErr.Code != jsonrpc.CodeInvalidParams || strings.Contains(resourceErr.Error(), "private-resource-value") {
		t.Fatalf("SDK error code/privacy changed: %v", resourceErr)
	}
	for _, args := range []map[string]any{{}, {"schemaVersion": 2}, {"schemaVersion": "1"}} {
		res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "create", Arguments: args})
		if err != nil || !res.IsError {
			t.Fatalf("version refusal: %+v %v", res, err)
		}
	}
	for _, name := range []string{"run", "test"} {
		res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: map[string]any{"schemaVersion": 1}})
		if err != nil || res.IsError {
			t.Fatalf("contract fixture: %+v %v", res, err)
		}
	}
}
func TestProtocolRegistrationAndSafeErrors(t *testing.T) {
	versioned := protocolFixture()
	op := versioned["run"]
	op.Schema.Required = []string{"schemaVersion"}
	op.Schema.Properties = map[string]*jsonschema.Schema{"schemaVersion": {Type: "integer"}}
	versioned["run"] = op
	versionClient := connectFixture(t, versioned)
	versionTools, err := versionClient.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range versionTools.Tools {
		if tool.Name != "run" {
			continue
		}
		b, _ := json.Marshal(tool.InputSchema)
		var schema jsonschema.Schema
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatal(err)
		}
		if len(schema.Required) != 1 {
			t.Fatalf("duplicate version requirement: %v", schema.Required)
		}
	}
	if len(op.Schema.Required) != 1 {
		t.Fatal("caller schema mutated")
	}
	for _, kind := range []string{"missing", "extra", "nil-handler", "nil-schema"} {
		ops := protocolFixture()
		switch kind {
		case "missing":
			delete(ops, "test")
		case "extra":
			ops["explain"] = ops["test"]
		case "nil-handler":
			op := ops["run"]
			op.Handler = nil
			ops["run"] = op
		case "nil-schema":
			op := ops["run"]
			op.Schema = nil
			ops["run"] = op
		}
		if _, err := NewServer("1", ops); err == nil {
			t.Fatalf("accepted %s", kind)
		}
	}
	ops := protocolFixture()
	ops["run"] = Operation{Schema: &jsonschema.Schema{Type: "object"}, Handler: func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return nil, errors.New("private endpoint credential")
	}}
	cs := connectFixture(t, ops)
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "run", Arguments: map[string]any{"schemaVersion": 1}})
	if err != nil || !res.IsError {
		t.Fatalf("error: %+v %v", res, err)
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "private") || strings.Contains(string(b), "credential") {
		t.Fatalf("unsafe: %s", b)
	}
}
func TestProtocolDeadlineClassification(t *testing.T) {
	res := errorResult(context.DeadlineExceeded)
	b, _ := json.Marshal(res.StructuredContent)
	if !bytes.Contains(b, []byte(`"reasonCode":"deadline_exceeded"`)) {
		t.Fatalf("deadline collapsed into cancellation: %s", b)
	}
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	f, _ := filesFixture(t)
	_, err := f.Create(ctx, "x.agent.md", []byte("valid:x"))
	requireCode(t, err, "deadline_exceeded")
}

func TestProtocolCancellation(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	ops := protocolFixture()
	ops["run"] = Operation{Schema: &jsonschema.Schema{Type: "object"}, Handler: func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}}
	cs := connectFixture(t, ops)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "run", Arguments: map[string]any{"schemaVersion": 1}})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler not started")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("handler not cancelled")
	}
	if err := <-done; err == nil {
		t.Fatal("cancelled call succeeded")
	}
}

type protocolOutput struct{ bytes.Buffer }

func (*protocolOutput) Close() error { return nil }

func TestProtocolOversizedFrame(t *testing.T) {
	s, err := NewServer("1", protocolFixture())
	if err != nil {
		t.Fatal(err)
	}
	out := &protocolOutput{}
	err = Serve(t.Context(), s, io.NopCloser(strings.NewReader(strings.Repeat(" ", (2<<20)+1))), out)
	if err == nil {
		t.Fatal("oversized frame accepted")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
		if len(line) > 0 && !json.Valid(line) {
			t.Fatal("non-protocol stdout")
		}
	}
}

func TestProtocolAuthoringOverPipes(t *testing.T) {
	f, _ := filesFixture(t)
	ops := protocolFixture()
	for name, op := range AuthoringOperations(f) {
		ops[name] = op
	}
	s, err := NewServer("1", ops)
	if err != nil {
		t.Fatal(err)
	}
	sr, cw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cr, sw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer sr.Close()
	defer cw.Close()
	defer cr.Close()
	defer sw.Close()
	done := make(chan error, 1)
	go func() { done <- Serve(t.Context(), s, sr, sw) }()
	c := sdk.NewClient(&sdk.Implementation{Name: "pipe-client", Version: "1"}, nil)
	cs, err := c.Connect(t.Context(), &sdk.IOTransport{Reader: cr, Writer: cw}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	args := map[string]any{"schemaVersion": 1, "path": "x.agent.md", "content": "valid:old"}
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "create", Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("create: %+v %v", res, err)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var saved struct{ File File }
	if err := json.Unmarshal(raw, &saved); err != nil || saved.File.Revision == "" {
		t.Fatalf("identity: %s %v", raw, err)
	}
	args["expectedRevision"] = saved.File.Revision
	args["content"] = "valid:new"
	res, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("edit: %+v %v", res, err)
	}
	args["command"] = "private-shell-command"
	res, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: args})
	if err != nil || !res.IsError {
		t.Fatalf("unknown field: %+v %v", res, err)
	}
	cs.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit")
	}
}
