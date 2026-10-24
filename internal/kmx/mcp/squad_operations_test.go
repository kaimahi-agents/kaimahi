package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func squadToolMember(m SquadMember) map[string]any {
	return map[string]any{"memberId": m.ID, "role": m.Role, "displayRole": m.DisplayRole, "taskInstructions": m.TaskInstructions, "model": m.Model, "examples": m.Examples}
}
func TestSquadMCPRejectsSecondLeadBeforeSetup(t *testing.T) {
	t.Setenv("KMX_HOME", previewStateHome(t))
	root, p := squadSetupRoot(t)
	c, err := OpenSquadSetup(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	files, err := OpenFiles(root, func(string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	a := &app.App{FileRunner: app.NewLocalFileRunner()}
	cs := connectFixture(t, WithSquadOperations(Operations(files, a, nil), c, a, nil))
	extra := squadLead()
	extra.ID = "other-lead"
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "create", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-members", "lead": squadToolMember(squadLead()), "specialists": []any{squadToolMember(extra)}}})
	if err != nil || !res.IsError {
		t.Fatal("two Leads accepted")
	}
	if _, err := os.Stat(filepath.Join(root, ".init-called")); !os.IsNotExist(err) {
		t.Fatal("team failed only after native setup effects")
	}
}
func TestSquadMCPChatCreateMapEditAndPreview(t *testing.T) {
	t.Setenv("KMX_HOME", previewStateHome(t))
	root, p := squadSetupRoot(t)
	c, err := OpenSquadSetup(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	files, err := OpenFiles(root, func(p string, b []byte) error { _, err := agentfile.Parse(p, b); return err })
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	a := &app.App{FileRunner: app.NewLocalFileRunner()}
	cs := connectFixture(t, WithSquadOperations(Operations(files, a, nil), c, a, nil), func(s *sdk.Server) { AddSquadResource(s, c) })
	lead := squadLead()
	lead.Model = "fake"
	lead.TaskInstructions = "Reply with exactly `Hello, ` followed by the input name."
	lead.Examples = strings.Replace(lead.Examples, "input: Hello", "input: Ada", 1)
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "create", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-members", "lead": squadToolMember(lead), "specialists": []any{}}})
	if err != nil || res.IsError {
		t.Fatalf("native chat create refused: %+v %v", res, err)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), `"setup":"initialized"`) {
		t.Fatal("lost setup observation")
	}
	view, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "kmx://squad-member/leader"})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SchemaVersion int    `json:"schemaVersion"`
		State         string `json:"state"`
		File          File   `json:"file"`
	}
	if json.Unmarshal([]byte(view.Contents[0].Text), &doc) != nil || doc.State != "ready" || doc.SchemaVersion != 2 {
		t.Fatal("lost reopened native view")
	}
	for _, operation := range []string{"run", "test"} {
		res, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: operation, Arguments: map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": "leader"}})
		if err != nil || res.IsError {
			t.Fatalf("native %s failed: %+v %v", operation, res, err)
		}
		b, _ = json.Marshal(res.StructuredContent)
		if !strings.Contains(string(b), `"fullPass":true`) || !strings.Contains(string(b), doc.File.Revision) {
			t.Fatalf("native preview not revision bound: %s", b)
		}
	}
	res, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": "leader", "expectedRevision": doc.File.Revision, "taskInstructions": "Reply with exactly `Goodbye, ` followed by the input name.", "model": "fake", "examples": lead.Examples}})
	if err != nil || res.IsError {
		t.Fatalf("native edit failed: %+v %v", res, err)
	}
	res, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": "leader"}})
	b, _ = json.Marshal(res.StructuredContent)
	if err != nil || res.IsError || !strings.Contains(string(b), `"fullPass":false`) || !strings.Contains(string(b), `"verdict":"fail"`) {
		t.Fatalf("native failing assertion was not normal completion: %s %v", b, err)
	}
	marker := filepath.Join(root, ".squad/casting/registry-history.transaction.json")
	if err := os.WriteFile(marker, []byte("pending"), 0600); err != nil {
		t.Fatal(err)
	}
	charterPath := filepath.Join(root, ".squad/agents/leader/charter.md")
	before, _ := os.ReadFile(charterPath)
	for _, operation := range []string{"edit", "run", "test"} {
		args := map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": "leader"}
		if operation == "edit" {
			args["expectedRevision"] = doc.File.Revision
			args["taskInstructions"] = lead.TaskInstructions
			args["model"] = "fake"
			args["examples"] = lead.Examples
		}
		res, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: operation, Arguments: args})
		b, _ = json.Marshal(res.StructuredContent)
		if err != nil || !res.IsError || !strings.Contains(string(b), `"state":"not_setup"`) {
			t.Fatalf("pending native recovery allowed %s: %s %v", operation, b, err)
		}
	}
	view, err = cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "kmx://squad-member/leader"})
	if err != nil || !strings.Contains(view.Contents[0].Text, "not set up by kmx") {
		t.Fatal("pending recovery reported ready")
	}
	after, _ := os.ReadFile(charterPath)
	pending, _ := os.ReadFile(marker)
	if string(before) != string(after) || string(pending) != "pending" {
		t.Fatal("recovery refusal changed native state")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	} // Test models external manual reconciliation, never automatic recovery.
	os.WriteFile(filepath.Join(root, ".squad/casting/history.json"), []byte(`{}`), 0644)
	res, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": "leader"}})
	b, _ = json.Marshal(res.StructuredContent)
	if err != nil || !res.IsError || !strings.Contains(string(b), `"state":"not_setup"`) || !strings.Contains(string(b), "not set up by kmx") {
		t.Fatalf("native mapping failure guessed origin: %s %v", b, err)
	}
	view, err = cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "kmx://squad-member/leader"})
	if err != nil || !strings.Contains(view.Contents[0].Text, "not set up by kmx") {
		t.Fatal("view did not revalidate native state")
	}
}
