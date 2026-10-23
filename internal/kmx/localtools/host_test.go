package localtools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
)

func validCall() api.ToolCall {
	return api.ToolCall{ID: "call-1", Tool: "github_list_merged_prs", Mediation: api.MediationControllerMediated, IdempotencyKey: "run-call-1"}
}
func TestHostProjectionAndCorrelation(t *testing.T) {
	calls := 0
	host, err := NewHost(testBinding(t), "credential-canary", func(context.Context) ([]PullRequest, error) {
		calls++
		return []PullRequest{{Number: 7, Title: "Fix parser", Repository: "owner/repo", Merged: true}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{nil, {}} {
		call := validCall()
		call.Args = args
		result, err := host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, call)
		if err != nil {
			t.Fatal(err)
		}
		if result.ID != "call-1" || result.IsError || result.OutputURI != "" || len(result.Content) != 0 {
			t.Fatalf("invalid correlation: %+v", result)
		}
		data, err := json.Marshal(result.Output)
		if err != nil || string(data) != `{"merged":true,"pullRequests":[{"number":7,"title":"Fix parser"}],"repository":"owner/repo"}` {
			t.Fatalf("unsafe/wrong projection: %s %v", data, err)
		}
	}
	if calls != 2 {
		t.Fatalf("read calls = %d", calls)
	}
	data, err := json.Marshal(struct {
		Host any `json:"host"`
	}{host})
	if err != nil || string(data) != `{"host":{}}` {
		t.Fatal("host leaks private fields")
	}
	definition := Definition()
	definition.InputSchema["properties"].(map[string]any)["url"] = "unsafe"
	if !reflect.DeepEqual(Definition().InputSchema, map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}) {
		t.Fatal("definition is not fixed/fresh")
	}
}

func TestHostRejectsUnmediatedOrParameterizedCallsBeforeRead(t *testing.T) {
	host, err := NewHost(testBinding(t), "credential-canary", func(context.Context) ([]PullRequest, error) { t.Fatal("invalid call reached read"); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*api.ToolCall){
		func(c *api.ToolCall) { c.Tool = "write" }, func(c *api.ToolCall) { c.ID = "" }, func(c *api.ToolCall) { c.IdempotencyKey = "" },
		func(c *api.ToolCall) { c.ID = "credential-canary" }, func(c *api.ToolCall) { c.IdempotencyKey = "credential-canary" },
		func(c *api.ToolCall) { c.Mediation = "" }, func(c *api.ToolCall) { c.Mediation = api.MediationRequiresApproval }, func(c *api.ToolCall) { c.Mediation = api.MediationInHarnessReported },
		func(c *api.ToolCall) { c.Args = map[string]any{"repository": "other/repo"} },
	} {
		call := validCall()
		mutate(&call)
		if _, err := host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, call); err == nil || strings.Contains(err.Error(), "credential-canary") {
			t.Fatal("unsafe call accepted/error leaked", err)
		}
	}
	if _, err := host.Execute(t.Context(), controller.ToolCallContext{}, validCall()); err == nil {
		t.Fatal("unscoped call accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := host.Execute(ctx, controller.ToolCallContext{SessionUID: "session"}, validCall()); err == nil {
		t.Fatal("cancelled call accepted")
	}
}

func TestHostRejectsUnsafeResultsAndReadErrors(t *testing.T) {
	valid := PullRequest{Number: 1, Title: "safe", Repository: "owner/repo", Merged: true}
	for _, mutate := range []func(*PullRequest){
		func(p *PullRequest) { p.Number = 0 }, func(p *PullRequest) { p.Number = -1 }, func(p *PullRequest) { p.Merged = false }, func(p *PullRequest) { p.Repository = "other/repo" },
		func(p *PullRequest) { p.Title = strings.Repeat("a", 513) }, func(p *PullRequest) { p.Title = "\xff" }, func(p *PullRequest) { p.Title = "credential-canary" }, func(p *PullRequest) { p.Title = "ghp_" + strings.Repeat("a", 36) },
	} {
		p := valid
		mutate(&p)
		host, err := NewHost(testBinding(t), "credential-canary", func(context.Context) ([]PullRequest, error) { return []PullRequest{p}, nil })
		if err != nil {
			t.Fatal(err)
		}
		result, err := host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, validCall())
		if err == nil || strings.Contains(err.Error(), "credential-canary") || result.ID != "" || result.Output != nil {
			t.Fatal("unsafe result accepted/leaked", err)
		}
	}
	for _, count := range []int{0, 20, 21} {
		host, err := NewHost(testBinding(t), "credential-canary", func(context.Context) ([]PullRequest, error) {
			rows := make([]PullRequest, count)
			for i := range rows {
				rows[i] = valid
			}
			return rows, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, validCall())
		if (err != nil) != (count > 20) {
			t.Fatalf("count %d: %v", count, err)
		}
	}
	host, err := NewHost(testBinding(t), "credential-canary", func(context.Context) ([]PullRequest, error) {
		return nil, errors.New("raw credential-canary remote error")
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "session"}, validCall())
	if err == nil || strings.Contains(err.Error(), "raw") || strings.Contains(err.Error(), "credential-canary") {
		t.Fatal("remote error leaked", err)
	}
}

func TestHostCredentialGuardsAndConstruction(t *testing.T) {
	read := func(context.Context) ([]PullRequest, error) { return nil, nil }
	for _, credential := range []string{"x", "credential-canary"} {
		host, err := NewHost(testBinding(t), credential, read)
		if err != nil {
			t.Fatal(err)
		}
		if host.Check("prefix"+credential+"suffix") == nil || host.Check("ghp_"+strings.Repeat("a", 36)) == nil {
			t.Fatal("credential guard missed exact/known shape")
		}
		if err := host.Check("safe"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewHost(Binding{}, "canary", read); err == nil {
		t.Fatal("invalid binding accepted")
	}
	if _, err := NewHost(testBinding(t), "", read); err == nil {
		t.Fatal("empty credential accepted")
	}
	if _, err := NewHost(testBinding(t), "canary", nil); err == nil {
		t.Fatal("nil read accepted")
	}
	var host *Host
	if host.Check("safe") == nil {
		t.Fatal("nil host accepted")
	}
}
