package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSquadBriefSaveCASAndNativePreservation(t *testing.T) {
	root, p := squadSetupRoot(t)
	c, err := OpenSquadSetup(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	brief := "Preserve offline edits."
	result, err := c.Create(t.Context(), SquadPlan{Lead: squadLead(), Brief: &brief})
	if err != nil || result.Brief == nil {
		t.Fatalf("create: %v %+v", err, result)
	}
	charter := filepath.Join(root, result.Members[0].File.Path)
	before, _ := os.ReadFile(charter)
	saved, err := c.EditBrief(t.Context(), result.Brief.Revision, "Preserve both edits on conflict.")
	if err != nil || saved.Revision == result.Brief.Revision {
		t.Fatal("brief was not revised", err)
	}
	if _, err := c.EditBrief(t.Context(), result.Brief.Revision, "Stale overwrite"); err == nil {
		t.Fatal("stale write accepted")
	}
	after, _ := os.ReadFile(charter)
	if string(before) != string(after) {
		t.Fatal("brief edit rewrote charter")
	}
	got, err := c.brief(t.Context())
	if err != nil || got.Revision != saved.Revision || got.Text != "Preserve both edits on conflict." {
		t.Fatal("brief readback", err)
	}
	if _, err := c.EditBrief(t.Context(), saved.Revision, " "); err == nil {
		t.Fatal("blank brief accepted")
	}
}

func TestSquadBriefMCPBranchIsClosed(t *testing.T) {
	t.Setenv("KMX_HOME", previewStateHome(t))
	root, p := squadSetupRoot(t)
	c, err := OpenSquadSetup(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Create(t.Context(), SquadPlan{Lead: squadLead()}); err != nil {
		t.Fatal(err)
	}
	files, err := OpenFiles(root, func(string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	a := &app.App{FileRunner: app.NewLocalFileRunner()}
	cs := connectFixture(t, WithSquadOperations(Operations(files, a, nil), c, a, nil), func(s *sdk.Server) { AddSquadResource(s, c) })
	args := map[string]any{"schemaVersion": 2, "target": "squad-brief", "expectedRevision": "none", "text": "Keep offline writes."}
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("brief edit: %v %+v", err, res)
	}
	args["memberId"] = "leader"
	res, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "edit", Arguments: args})
	if err != nil || !res.IsError {
		t.Fatal("mixed brief/member operands accepted")
	}
	view, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "kmx://squad"})
	if err != nil || len(view.Contents) != 1 {
		t.Fatal("team read", err)
	}
}
