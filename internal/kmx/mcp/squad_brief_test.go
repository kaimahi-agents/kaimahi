package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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

type squadGateWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *squadGateWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestSquadCreateRechecksBriefInsideGate(t *testing.T) {
	root, _, c := createdSquadMember(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	waiting := &squadGateWaitContext{Context: ctx, entered: make(chan struct{})}
	c.gate <- struct{}{}
	held := true
	defer func() {
		if held {
			<-c.gate
		}
	}()
	brief, lead := "Queued proposal brief", squadLead()
	lead.ID = "queued-lead"
	type outcome struct {
		result SquadResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := c.Create(waiting, SquadPlan{Lead: lead, Brief: &brief})
		done <- outcome{result, err}
	}()
	select {
	case <-waiting.entered:
	case <-ctx.Done():
		t.Fatal("Create did not reach the held gate")
	}
	// This test holds the same gate as the concurrent brief writer.
	if _, err := c.saveBrief(ctx, "none", "Brief saved by the gate owner"); err != nil {
		t.Fatal(err)
	}
	before, err := c.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	<-c.gate
	held = false
	select {
	case got := <-done:
		var refusal *Error
		if !errors.As(got.err, &refusal) || refusal.Code != "already_exists" || got.result.EffectsUnknown || len(got.result.ChangedPaths) != 0 {
			t.Fatalf("queued Create did not refuse before effects: %+v %v", got.result, got.err)
		}
		for _, member := range got.result.Members {
			if member.State != "not_attempted" {
				t.Fatalf("unexpected cast: %+v", member)
			}
		}
	case <-ctx.Done():
		t.Fatal("queued Create did not finish")
	}
	after, err := c.snapshot()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("refused Create changed native files", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".squad/agents/queued-lead")); !os.IsNotExist(err) {
		t.Fatal("queued member was cast")
	}
}

func TestSquadInvalidBriefRefusalsAreActionable(t *testing.T) {
	for _, raw := range [][]byte{[]byte(" "), {0xff}, []byte("private\x00content"), []byte(strings.Repeat("x", 16385))} {
		err := validateSquadBrief(squadBriefPath, raw)
		if err == nil {
			t.Fatal("invalid brief accepted")
		}
		for _, version := range []int{1, 2} {
			result := versionedError(err, version)
			data := result.StructuredContent.(map[string]any)
			if data["reasonCode"] != "invalid_squad_brief" || data["schemaVersion"] != version {
				t.Fatalf("lost brief classification: %+v", data)
			}
			reason := data["reason"].(string)
			for _, part := range []string{"UTF-8", "16 KiB", "repair or remove", ".squad/kmx-brief.md"} {
				if !strings.Contains(reason, part) {
					t.Fatalf("missing repair guidance %q: %s", part, reason)
				}
			}
			if strings.Contains(reason, "private") {
				t.Fatal("brief content reached the error")
			}
		}
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
	runner := &previewFixture{}
	a := &app.App{FileRunner: runner}
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
	if err := os.WriteFile(filepath.Join(root, squadBriefPath), []byte("private\x00content"), 0600); err != nil {
		t.Fatal(err)
	}
	res, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "test", Arguments: map[string]any{"schemaVersion": 2, "target": "squad-member", "memberId": squadLead().ID}})
	if err != nil || !res.IsError || runner.calls != 0 {
		t.Fatal("invalid brief reached preview", err)
	}
	if res.StructuredContent.(map[string]any)["reasonCode"] != "invalid_squad_brief" {
		t.Fatal("native preview lost the brief refusal code")
	}
	if _, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "kmx://squad"}); err == nil || !strings.Contains(err.Error(), "protocol_failed") || strings.Contains(err.Error(), "private") {
		t.Fatal("resource did not preserve the payload-free refusal", err)
	}
}
