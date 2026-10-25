package localpreview

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

func TestFakeBoundToolFollowupCompletes(t *testing.T) {
	client := newModelClient(route{model: FakeModel, baseURL: "http://fake.invalid", transport: fakeTransport{}})
	request := api.ModelRequest{Model: FakeModel, Tools: []api.ToolDefinition{localtools.Definition()}, Messages: []api.Message{*api.TextMessage("system", "Call the tool `github_list_merged_prs`. Reply with exactly `done`."), *api.TextMessage("user", "input")}}
	first, err := client.ToolModel(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	call := first.Message.Parts[0].ToolCall
	request.Messages = append(request.Messages, first.Message, api.Message{Role: "tool", Parts: []api.Part{{ToolResult: &api.ToolResult{ID: call.ID, Output: map[string]any{"ok": true}}}}})
	final, err := client.ToolModel(context.Background(), request)
	if err != nil || final.Message.Text() != "done" {
		t.Fatalf("follow-up = %q, %v", final.Message.Text(), err)
	}
	request.Messages = request.Messages[:2]
	request.Messages[0] = *api.TextMessage("system", strings.ReplaceAll("Call the tool `absent`. Reply with exactly `fallback`.", "unused", ""))
	final, err = client.ToolModel(context.Background(), request)
	if err != nil || final.Message.Text() != "fallback" {
		t.Fatalf("unoffered name selected: %q %v", final.Message.Text(), err)
	}
}
