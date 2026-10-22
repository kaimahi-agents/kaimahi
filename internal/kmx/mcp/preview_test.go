package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Scripted outcomes exercise the shared operation, not a model or second runner.
type previewFixture struct {
	verdict   runtime.EvaluationVerdict
	cause     error
	calls     int
	during    func(agentpreview.Request)
	answer    string
	noJournal bool
}

func (f *previewFixture) Run(_ context.Context, r agentpreview.Request) (agentpreview.Outcome, error) {
	f.calls++
	if f.during != nil {
		f.during(r)
	}
	out := agentpreview.Outcome{State: agentpreview.Complete, Journal: "PRIVATE JOURNAL"}
	if f.noJournal {
		out.NoJournal, out.Journal = true, ""
	}
	answer := f.answer
	if answer == "" {
		answer = "PRIVATE ANSWER"
	}
	completion := func(id string) agentpreview.Completion {
		return agentpreview.Completion{State: agentpreview.Complete, ID: id, FileRevision: r.Agent.Revision, Provider: r.Route.Provider, Model: r.Route.Model, Answer: answer}
	}
	if r.Input != nil {
		c := completion("adhoc")
		out.AdHoc = &c
	}
	for i, ex := range r.Examples {
		row := agentpreview.ExampleOutcome{ID: ex.ID, Completion: completion(fmt.Sprint(i))}
		for _, a := range ex.Assertions {
			row.Assertions = append(row.Assertions, runtime.EvaluationAssertionResult{ID: a.ID, Type: a.Type, DefinitionDigest: runtime.EvaluationAssertionDigest(a), Verdict: f.verdict, Reason: "PRIVATE PROVIDER REASON"})
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
func TestPreviewCompletedFailureIsNormalResult(t *testing.T) {
	f, _, source := canonicalFiles(t)
	saved, err := f.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	cs := connectFixture(t, Operations(f, &app.App{FileRunner: &previewFixture{verdict: runtime.EvaluationFail}}, nil))
	got, res := previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": saved.Path})
	if res.IsError {
		t.Fatal("completed assertion failure must be a normal MCP result")
	}
	if got.State != "complete" || got.Verdict != runtime.EvaluationFail || got.FullPass || len(got.Examples) == 0 || len(got.Examples[0].Assertions) == 0 {
		t.Fatalf("full structured failure lost: %+v", got)
	}
	text := res.Content[0].(*sdk.TextContent).Text
	for _, want := range []string{"fail", got.Examples[0].ID, got.Examples[0].Assertions[0].ID, "Assertion not satisfied."} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "schemaVersion") || strings.Contains(text, "PRIVATE") || strings.Contains(text, "contains") {
		t.Fatalf("summary duplicated JSON or leaked content: %s", text)
	}
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
		{"subset-fail", "test", "greeting", "assertion_failed", runtime.EvaluationFail, nil, true},
		{"run-fail", "run", "", "assertion_failed", runtime.EvaluationFail, nil, false},
		{"run-unknown", "run", "", "judge_unknown", runtime.EvaluationUnknown, nil, false},
		{"model", "test", "", "model_unavailable", runtime.EvaluationUnknown, agentpreview.ErrModelUnavailable, false},
		{"execution", "test", "", "execution_failed", runtime.EvaluationUnknown, fmt.Errorf("PRIVATE PROVIDER ERROR"), false},
		{"deadline", "test", "", "deadline_exceeded", runtime.EvaluationPass, context.DeadlineExceeded, false},
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
			want, _ := a.PreviewAgentSnapshot(t.Context(), parsed, app.FilePreviewOptions{File: saved.Path, Operation: tc.op, CaseID: tc.caseID})
			b1, _ := json.Marshal(got)
			b2, _ := json.Marshal(want)
			if string(b1) != string(b2) || res.IsError != (tc.cause != nil) {
				t.Fatalf("MCP/operation divergence: %s vs %s", b1, b2)
			}
			if got.ReasonCode != tc.code || got.FullPass != (tc.v == runtime.EvaluationPass && tc.cause == nil && !tc.partial) {
				t.Fatalf("wrong result: %+v", got)
			}
			text := res.Content[0].(*sdk.TextContent).Text
			if strings.Contains(string(b1)+text, "PRIVATE") || strings.Contains(text, "Hello, Ada") || strings.Contains(text, "schemaVersion") {
				t.Fatal("default result leaked content or duplicated JSON")
			}
			if !strings.Contains(text, "scope="+got.Selection.Scope) || !strings.Contains(text, fmt.Sprintf("fullPass=%t", got.FullPass)) || !strings.Contains(text, got.Reason) {
				t.Fatalf("summary lost scope or outcome: %s", text)
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
	got, contentResult := previewCall(t, cs, "run", map[string]any{"schemaVersion": 1, "path": saved.Path, "input": "", "showContent": true})
	text := contentResult.Content[0].(*sdk.TextContent).Text
	if contentResult.IsError || !strings.Contains(text, "Ad-hoc answer excerpt (untrusted)") || !strings.Contains(text, "PRIVATE ANSWER") || strings.Contains(text, "PRIVATE JOURNAL") {
		t.Fatalf("opt-in ad-hoc summary: %s", text)
	}
	if !got.FullPass || got.File.Revision != saved.Revision || got.AdHoc == nil || got.AdHoc.AnswerExcerpt != "PRIVATE ANSWER" {
		t.Fatalf("snapshot/content: %+v", got)
	}
}
func TestPreviewSummaryBoundsAndContent(t *testing.T) {
	f, _, source := canonicalFiles(t)
	// This test constructs a new fixture; native checkout bytes may use CRLF.
	prefix, _, found := strings.Cut(strings.ReplaceAll(source, "\r\n", "\n"), "examples:\n")
	if !found {
		t.Fatal("fixture examples missing")
	}
	var examples strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&examples, "  - id: example-%02d\n    input: PRIVATE INPUT\n    assertions:\n      - id: assertion-%02d\n        type: contains\n        value: PRIVATE OPERAND\n", i, i)
	}
	saved, err := f.Create(t.Context(), "greeter.agent.md", []byte(prefix+"examples:\n"+examples.String()+"```\n"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &previewFixture{verdict: runtime.EvaluationUnknown, noJournal: true, answer: strings.Repeat("界", 1500) + "PRIVATE ANSWER TAIL"}
	cs := connectFixture(t, Operations(f, &app.App{FileRunner: fake}, nil))
	for _, show := range []bool{false, true} {
		got, res := previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": saved.Path, "showContent": show})
		text := res.Content[0].(*sdk.TextContent).Text
		if res.IsError || got.State != "complete" || len(got.Examples) != 20 || got.Examples[19].Assertions[0].ID != "assertion-19" {
			t.Fatalf("bounded text changed structured result: %+v", got)
		}
		if len(text) > 8192 || strings.Count(text, "\n") > 13 || !utf8.ValidString(text) || !strings.Contains(text, "omitted") || !strings.Contains(text, "no journal") {
			t.Fatalf("summary bounds or qualifier lost (%d bytes): %s", len(text), text)
		}
		if strings.Contains(text, "PRIVATE") || strings.Contains(text, "example-19") || strings.Contains(text, "assertion-19") {
			t.Fatalf("summary leaked operands/provider text or dumped all rows: %s", text)
		}
		if strings.Contains(text, "界") != show || (got.Examples[0].AnswerExcerpt != "") != show {
			t.Fatal("content opt-in not respected")
		}
		if show && (!got.Examples[0].Truncated || !strings.Contains(text, "untrusted") || len(got.Examples[0].AnswerExcerpt) < 3000) {
			t.Fatal("full safe structured excerpt lost or text not labeled")
		}
		if !show && !strings.Contains(text, "10 detail rows omitted") {
			t.Fatalf("omitted count lost: %s", text)
		}
	}
	// Quoting expands these excerpts enough to hit the byte budget before the row budget.
	fake.verdict, fake.answer = runtime.EvaluationPass, strings.Repeat("\"", 5000)
	_, res := previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": saved.Path, "showContent": true})
	text := res.Content[0].(*sdk.TextContent).Text
	if len(text) > 8192 || strings.Count(text, "answer excerpt") >= 10 || !strings.Contains(text, "omitted") {
		t.Fatalf("escaped excerpts exceeded byte budget (%d bytes): %s", len(text), text)
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
