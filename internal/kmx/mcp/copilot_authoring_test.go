package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const copilotPrompt = "---\nname: Reviewer\ndescription: Review changes\nmodel: fake\nfuture: {keep: 'as written'} # comment\n---\nReview the changes.\n"
const copilotExamples = "\n## Examples\n```yaml\nexamples:\n  - id: review\n    input: a change\n    assertions:\n      - id: text\n        type: contains\n        value: reviewed\n```\n"

func TestCopilotResourceWithoutExamples(t *testing.T) {
	f, root, _ := canonicalFiles(t)
	if err := os.WriteFile(filepath.Join(root, "review.agent.md"), []byte(copilotPrompt), 0600); err != nil {
		t.Fatal(err)
	}
	cs := resourceClient(t, f, &app.App{})
	r, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: agentResourceURI("review.agent.md")})
	if err != nil || len(r.Contents) != 1 {
		t.Fatal(err)
	}
	var view map[string]any
	if err := json.Unmarshal([]byte(r.Contents[0].Text), &view); err != nil {
		t.Fatal(err)
	}
	authoring, ok := view["authoring"].(map[string]any)
	if !ok || authoring["editable"] != true || authoring["source"] != copilotPrompt {
		t.Fatalf("missing editable snapshot: %s", r.Contents[0].Text)
	}
	if view["copilotToolsOmitted"] != true || !strings.Contains(view["frontmatter"].(string), "future: {keep: 'as written'} # comment") {
		t.Fatal("preserved settings missing")
	}
}

func TestCopilotTestWriteConfirmation(t *testing.T) {
	const warning = "Tests added here become part of this agent's prompt in Copilot."
	f, root, _ := canonicalFiles(t)
	cs := resourceClient(t, f, &app.App{})
	call := func(op string, args map[string]any) *sdk.CallToolResult {
		t.Helper()
		args["schemaVersion"] = 1
		r, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: op, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	create := map[string]any{"path": "review.agent.md", "content": copilotPrompt + copilotExamples}
	refused := call("create", create)
	encoded, _ := json.Marshal(refused)
	if !refused.IsError || !strings.Contains(string(encoded), warning) {
		t.Fatalf("missing confirmation: %s", encoded)
	}
	if _, err := os.Stat(filepath.Join(root, "review.agent.md")); !os.IsNotExist(err) {
		t.Fatal("unconfirmed Create wrote source")
	}
	create["acknowledgeCopilotTests"] = true
	if r := call("create", create); r.IsError {
		t.Fatalf("acknowledged Create: %+v", r)
	}
	original := copilotPrompt + copilotExamples
	// Prompt-only changes preserve tests, so no additional confirmation is needed.
	next := strings.Replace(original, "Review the changes.", "Review carefully.", 1)
	if r := call("edit", map[string]any{"path": "review.agent.md", "content": next, "expectedRevision": revision([]byte(original))}); r.IsError {
		t.Fatalf("prompt-only Edit: %+v", r)
	}
	edits := []any{map[string]any{"beforeId": "review", "input": "another change"}}
	args := map[string]any{"path": "review.agent.md", "content": next, "expectedRevision": revision([]byte(next)), "exampleEdits": edits}
	if r := call("edit", args); !r.IsError || r.StructuredContent.(map[string]any)["reasonCode"] != "confirmation_required" {
		t.Fatalf("unconfirmed sparse Edit: %+v", r)
	}
	saved, err := os.ReadFile(filepath.Join(root, "review.agent.md"))
	if err != nil || string(saved) != next {
		t.Fatal("unconfirmed Edit changed source", err)
	}
	args["acknowledgeCopilotTests"] = true
	if r := call("edit", args); r.IsError {
		t.Fatalf("acknowledged sparse Edit: %+v", r)
	}
}

func TestCopilotRecordingCaptureConfirmation(t *testing.T) {
	f, root, _ := canonicalFiles(t)
	source := strings.Replace(copilotPrompt, "model: fake", "model: fake\ntools: [ping]", 1) + strings.Replace(copilotExamples, "    assertions:", "    mocks:\n      tools:\n        - name: ping\n          mode: recorded\n          calls: []\n    assertions:", 1)
	if err := os.WriteFile(filepath.Join(root, "review.agent.md"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &previewFixture{verdict: runtime.EvaluationPass}
	cs := resourceClient(t, f, &app.App{FileRunner: runner})
	for _, operation := range []string{"run", "test"} {
		runner.calls = 0
		args := map[string]any{"schemaVersion": 1, "path": "review.agent.md"}
		r, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: operation, Arguments: args})
		if err != nil || !r.IsError || r.StructuredContent.(map[string]any)["reasonCode"] != "confirmation_required" || runner.calls != 0 {
			t.Fatalf("%s capture without acknowledgement: %+v %v sends=%d", operation, r, err, runner.calls)
		}
		args["acknowledgeCopilotTests"] = true
		r, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: operation, Arguments: args})
		if err != nil || runner.calls != 1 {
			t.Fatalf("acknowledged capture didn't reach runner: %+v %v", r, err)
		}
	}
}

func TestCopilotAddFirstTestConfirmation(t *testing.T) {
	f, root, _ := canonicalFiles(t)
	if err := os.WriteFile(filepath.Join(root, "review.agent.md"), []byte(copilotPrompt), 0600); err != nil {
		t.Fatal(err)
	}
	cs := resourceClient(t, f, &app.App{})
	args := map[string]any{"schemaVersion": 1, "path": "review.agent.md", "content": copilotPrompt, "expectedRevision": revision([]byte(copilotPrompt)), "exampleEdits": []any{map[string]any{"id": "first", "input": "a change", "assertions": []any{map[string]any{"id": "text", "type": "contains", "value": "reviewed"}}}}}
	r, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: args})
	if err != nil || !r.IsError || r.StructuredContent.(map[string]any)["reasonCode"] != "confirmation_required" {
		t.Fatalf("missing confirmation: %+v %v", r, err)
	}
	args["acknowledgeCopilotTests"] = true
	r, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: args})
	if err != nil || r.IsError {
		t.Fatalf("first test: %+v %v", r, err)
	}
	saved, err := os.ReadFile(filepath.Join(root, "review.agent.md"))
	if err != nil || !strings.HasPrefix(string(saved), copilotPrompt) || !strings.Contains(string(saved), "## Examples") {
		t.Fatal("first-test save changed prefix", err)
	}
}
