package mcp

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExampleEditSDKCheckedPatch(t *testing.T) {
	files, root, source := canonicalFiles(t)
	created, err := files.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	client := connectFixture(t, Operations(files, nil, nil))
	result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: map[string]any{"schemaVersion": 1, "path": created.Path, "expectedRevision": created.Revision, "content": source, "exampleEdits": []any{map[string]any{"beforeId": "greeting", "input": "different"}}}})
	if err != nil || result.IsError {
		t.Fatalf("patch rejected: %+v %v", result, err)
	}
	got, err := os.ReadFile(filepath.Join(root, "greeter.agent.md"))
	if err != nil || !bytes.Contains(got, []byte(`"different"`)) {
		t.Fatalf("not patched %s %v", got, err)
	}
	for _, edits := range []any{[]any{map[string]any{"beforeId": "greeting", "calls": []any{}}}, []any{map[string]any{"beforeId": "missing", "input": "bad"}}} {
		response, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: map[string]any{"schemaVersion": 1, "path": created.Path, "expectedRevision": revision(got), "content": string(got), "exampleEdits": edits}})
		if err == nil && !response.IsError {
			t.Fatal("invalid mutation accepted")
		}
		after, _ := os.ReadFile(filepath.Join(root, "greeter.agent.md"))
		if !bytes.Equal(got, after) {
			t.Fatal("refusal changed file")
		}
	}
}
