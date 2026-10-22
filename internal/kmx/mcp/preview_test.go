package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Scripted outcomes exercise the shared operation, not a model or second runner.
type previewFixture struct {
	verdict runtime.EvaluationVerdict
	cause   error
	calls   int
	during  func(agentpreview.Request)
}

func (f *previewFixture) Run(_ context.Context, r agentpreview.Request) (agentpreview.Outcome, error) {
	f.calls++
	if f.during != nil {
		f.during(r)
	}
	out := agentpreview.Outcome{State: agentpreview.Complete, Journal: "PRIVATE JOURNAL"}
	completion := func(id string) agentpreview.Completion {
		return agentpreview.Completion{State: agentpreview.Complete, ID: id, FileRevision: r.Agent.Revision, Provider: r.Route.Provider, Model: r.Route.Model, Answer: "PRIVATE ANSWER"}
	}
	if r.Input != nil {
		c := completion("adhoc")
		out.AdHoc = &c
	}
	for i, ex := range r.Examples {
		row := agentpreview.ExampleOutcome{ID: ex.ID, Completion: completion(fmt.Sprint(i))}
		for _, a := range ex.Assertions {
			row.Assertions = append(row.Assertions, runtime.EvaluationAssertionResult{ID: a.ID, Type: a.Type, DefinitionDigest: runtime.EvaluationAssertionDigest(a), Verdict: f.verdict})
		}
		out.Examples = append(out.Examples, row)
	}
	return out, f.cause
}
func canonicalFiles(t *testing.T) (*Files, string, string) {
	t.Helper()
	b, err := os.ReadFile("../agentfile/testdata/greeter.agent.md")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	f, err := OpenFiles(root, func(p string, b []byte) error { _, err := agentfile.Parse(p, b); return err })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, root, string(b)
}
func previewCall(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (app.FilePreviewResult, *sdk.CallToolResult) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var r app.FilePreviewResult
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.Kind != "kmx.agent-file-preview" || r.SchemaVersion != 1 {
		t.Fatalf("shared result lost: %s", b)
	}
	return r, res
}
func TestPreviewToolsSharedResults(t *testing.T) {
	f, root, source := canonicalFiles(t)
	saved, err := f.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, op, caseID, code string
		v                      runtime.EvaluationVerdict
		cause                  error
		partial                bool
	}{
		{"pass", "test", "", "checks_passed", runtime.EvaluationPass, nil, false},
		{"fail", "test", "", "assertion_failed", runtime.EvaluationFail, nil, false},
		{"unknown", "test", "", "judge_unknown", runtime.EvaluationUnknown, nil, false},
		{"subset", "test", "greeting", "checks_passed", runtime.EvaluationPass, nil, true},
		{"journal", "run", "", "journal_failed", runtime.EvaluationPass, agentpreview.ErrJournalFailed, false},
		{"cancel", "run", "", "cancelled", runtime.EvaluationPass, context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &previewFixture{verdict: tc.v, cause: tc.cause}
			a := &app.App{FileRunner: fake}
			cs := connectFixture(t, Operations(f, a, nil))
			args := map[string]any{"schemaVersion": 1, "path": saved.Path}
			if tc.caseID != "" {
				args["caseId"] = tc.caseID
			}
			got, res := previewCall(t, cs, tc.op, args)
			parsed, err := agentfile.Parse(saved.Path, []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			want, wantErr := a.PreviewAgentSnapshot(t.Context(), parsed, app.FilePreviewOptions{File: saved.Path, Operation: tc.op, CaseID: tc.caseID})
			b1, _ := json.Marshal(got)
			b2, _ := json.Marshal(want)
			if string(b1) != string(b2) || res.IsError != (wantErr != nil) {
				t.Fatalf("MCP/operation divergence: %s vs %s", b1, b2)
			}
			if got.ReasonCode != tc.code || got.FullPass != (tc.v == runtime.EvaluationPass && tc.cause == nil && !tc.partial) {
				t.Fatalf("wrong result: %+v", got)
			}
			if strings.Contains(string(b1), "PRIVATE") {
				t.Fatal("default result leaked content")
			}
		})
	}
	// Snapshot execution is independent of later source replacement/removal.
	fake := &previewFixture{verdict: runtime.EvaluationPass, during: func(r agentpreview.Request) {
		if r.Agent.Revision != saved.Revision {
			t.Fatal("wrong snapshot")
		}
		if err := os.Remove(filepath.Join(root, saved.Path)); err != nil {
			t.Fatal(err)
		}
	}}
	cs := connectFixture(t, Operations(f, &app.App{FileRunner: fake}, nil))
	got, _ := previewCall(t, cs, "run", map[string]any{"schemaVersion": 1, "path": saved.Path, "input": "", "showContent": true})
	if !got.FullPass || got.File.Revision != saved.Revision || got.AdHoc == nil || got.AdHoc.AnswerExcerpt != "PRIVATE ANSWER" {
		t.Fatalf("snapshot/content: %+v", got)
	}
}
func TestPreviewToolsRefusals(t *testing.T) {
	f, root, source := canonicalFiles(t)
	fake := &previewFixture{verdict: runtime.EvaluationPass}
	a := &app.App{FileRunner: fake}
	cs := connectFixture(t, Operations(f, a, nil))
	if err := os.WriteFile(filepath.Join(root, "greeter.agent.md"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	invalidOptions, _ := previewCall(t, cs, "run", map[string]any{"schemaVersion": 1, "path": "greeter.agent.md", "input": strings.Repeat("界", 6000)})
	if invalidOptions.ReasonCode != "invalid_options" || invalidOptions.Diagnostic != nil {
		t.Fatalf("option failure inherited parser diagnostic: %+v", invalidOptions)
	}
	for _, tc := range []struct{ source, code string }{{"invalid", "invalid_file"}, {strings.Replace(source, "**Preferred:** fake", "**Preferred:** local-model", 1), "model_unavailable"}} {
		if err := os.WriteFile(filepath.Join(root, "greeter.agent.md"), []byte(tc.source), 0600); err != nil {
			t.Fatal(err)
		}
		r, res := previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": "greeter.agent.md"})
		if !res.IsError || r.ReasonCode != tc.code || r.FullPass || fake.calls != 0 {
			t.Fatalf("refusal: %+v", r)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "greeter.agent.md"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{
		{"schemaVersion": 1, "path": "greeter.agent.md", "input": "not on Test"},
		{"schemaVersion": 1, "path": "greeter.agent.md", "endpoint": "http://private-endpoint"},
		{"schemaVersion": 1, "path": "../greeter.agent.md"},
	} {
		res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: args})
		if err != nil || !res.IsError || fake.calls != 0 {
			t.Fatalf("unsafe args: %+v %v", res, err)
		}
	}
	nilCS := connectFixture(t, Operations(f, &app.App{}, nil))
	r, _ := previewCall(t, nilCS, "run", map[string]any{"schemaVersion": 1, "path": "greeter.agent.md"})
	if r.ReasonCode != "runner_unavailable" {
		t.Fatal("production runner refusal lost")
	}
	route := &modelaccess.Route{Provider: "compatible", Endpoint: "https://trusted.invalid/v1", Auth: "none"}
	frozen := Operations(f, a, route)
	route.Endpoint = "https://changed.invalid/v1"
	fake.during = func(req agentpreview.Request) {
		if req.Route.Endpoint != "https://trusted.invalid/v1" {
			t.Fatal("trusted route was not frozen")
		}
	}
	if err := os.WriteFile(filepath.Join(root, "greeter.agent.md"), []byte(strings.Replace(source, "**Preferred:** fake", "**Preferred:** local-model", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	routeCS := connectFixture(t, frozen)
	r, _ = previewCall(t, routeCS, "run", map[string]any{"schemaVersion": 1, "path": "greeter.agent.md"})
	if !r.FullPass {
		t.Fatalf("explicit trusted route: %+v", r)
	}
}
