package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
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

func TestSquadMCPPreviewRevisionPair(t *testing.T) {
	t.Setenv("KMX_HOME", previewStateHome(t))
	root, program := squadSetupRoot(t)
	c, err := OpenSquadSetup(root, program)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lead, brief := squadLead(), "Reviewed team context."
	lead.Model = "fake"
	created, err := c.Create(t.Context(), SquadPlan{Lead: lead, Brief: &brief})
	if err != nil {
		t.Fatal(err)
	}
	memberRevision, briefRevision := created.Members[0].File.Revision, created.Brief.Revision
	files, err := OpenFiles(root, func(string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	runner := &previewFixture{verdict: runtime.EvaluationPass, during: func(r agentpreview.Request) {
		if r.Agent.Revision != memberRevision || !strings.Contains(r.Agent.Instructions, brief) || r.Agent.Model != "fake" {
			t.Error("preview did not consume the reviewed member and brief")
		}
	}}
	a := &app.App{FileRunner: runner}
	client := connectFixture(t, WithSquadOperations(Operations(files, a, nil), c, a, nil))
	for _, operation := range []string{"run", "test"} {
		for _, tc := range []struct {
			name, member, brief string
			allowed             bool
		}{
			{"matching", memberRevision, briefRevision, true},
			{"legacy", "", "", true},
			{"stale-member", "sha256:" + strings.Repeat("0", 64), briefRevision, false},
			{"stale-brief", memberRevision, "none", false},
			{"member-only", memberRevision, "", false},
			{"brief-only", "", briefRevision, false},
			{"malformed-member", "invalid", briefRevision, false},
			{"malformed-brief", memberRevision, "invalid", false},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				args := map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": lead.ID}
				if tc.member != "" {
					args["expectedRevision"] = tc.member
				}
				if tc.brief != "" {
					args["expectedBriefRevision"] = tc.brief
				}
				before := runner.calls
				result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: operation, Arguments: args})
				if err != nil || result.IsError == tc.allowed {
					t.Fatalf("preview allowed=%v: %+v %v", tc.allowed, result, err)
				}
				want := before
				if tc.allowed {
					want++
				}
				if runner.calls != want {
					t.Fatalf("runner calls=%d, want %d", runner.calls, want)
				}
			})
		}
	}
	changedBrief, err := c.EditBrief(t.Context(), briefRevision, "Context changed while approval was pending.")
	if err != nil {
		t.Fatal(err)
	}
	before := runner.calls
	result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": lead.ID, "expectedRevision": memberRevision, "expectedBriefRevision": briefRevision}})
	if err != nil || !result.IsError || runner.calls != before {
		t.Fatal("changed brief reached preview", err)
	}
	lead.Model = "unselected-model"
	if _, err := c.EditMember(t.Context(), lead.ID, memberRevision, lead); err != nil {
		t.Fatal(err)
	}
	result, err = client.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": lead.ID, "expectedRevision": memberRevision, "expectedBriefRevision": changedBrief.Revision}})
	encoded, _ := json.Marshal(result.StructuredContent)
	if err != nil || !result.IsError || runner.calls != before || !strings.Contains(string(encoded), `"reasonCode":"stale_revision"`) {
		t.Fatal("changed model was not refused at the source precondition", err)
	}
}

func TestSquadMCPEmptyTestsRemainEditable(t *testing.T) {
	t.Setenv("KMX_HOME", previewStateHome(t))
	root, program := squadSetupRoot(t)
	c, err := OpenSquadSetup(root, program)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lead := squadLead()
	lead.Model = "fake"
	examples := lead.Examples
	lead.Examples = "```yaml\nexamples: []\n```"
	created, err := c.Create(t.Context(), SquadPlan{Lead: lead})
	if err != nil {
		t.Fatal(err)
	}
	files, err := OpenFiles(root, func(string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	runner := &previewFixture{verdict: runtime.EvaluationPass}
	a := &app.App{FileRunner: runner}
	client := connectFixture(t, WithSquadOperations(Operations(files, a, nil), c, a, nil), func(s *sdk.Server) { AddSquadResource(s, c) })
	view, err := client.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "kmx://squad-member/" + lead.ID})
	if err != nil || !strings.Contains(view.Contents[0].Text, `"tests":[]`) || !strings.Contains(view.Contents[0].Text, `"state":"ready"`) {
		t.Fatal("empty member is not readable", err)
	}
	result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": lead.ID, "expectedRevision": created.Members[0].File.Revision, "expectedBriefRevision": "none"}})
	if err != nil || !result.IsError || runner.calls != 0 {
		t.Fatal("empty tests executed", err)
	}
	result, err = client.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": lead.ID, "expectedRevision": created.Members[0].File.Revision, "taskInstructions": lead.TaskInstructions, "model": lead.Model, "examples": examples}})
	if err != nil || result.IsError {
		t.Fatal("first test could not be saved", err)
	}
	doc, err := c.Member(t.Context(), lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err = client.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": lead.ID, "expectedRevision": doc.Revision, "expectedBriefRevision": "none"}})
	if err != nil || result.IsError || runner.calls != 1 {
		t.Fatal("newly added test did not execute", err)
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
	cs, notifications := connectProgressFixture(t, WithSquadOperations(Operations(files, a, nil), c, a, nil), func(s *sdk.Server) { AddSquadResource(s, c) })
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
		params := &sdk.CallToolParams{Name: operation, Arguments: map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": "leader"}}
		params.SetProgressToken("native-cases")
		res, err = cs.CallTool(t.Context(), params)
		if err != nil || res.IsError {
			t.Fatalf("native %s failed: %+v %v", operation, res, err)
		}
		b, _ = json.Marshal(res.StructuredContent)
		if !strings.Contains(string(b), `"fullPass":true`) || !strings.Contains(string(b), doc.File.Revision) {
			t.Fatalf("native preview not revision bound: %s", b)
		}
		if operation == "test" {
			select {
			case n := <-notifications:
				if n.ProgressToken != "native-cases" || n.Progress != 1 || !strings.Contains(n.Message, `"verdict":"pass"`) {
					t.Fatalf("native progress: %+v", n)
				}
			default:
				t.Fatal("missing native case progress")
			}
		}
		select {
		case n := <-notifications:
			t.Fatalf("unexpected native progress: %+v", n)
		default:
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
