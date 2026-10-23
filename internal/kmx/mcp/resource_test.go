package mcp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func resourceClient(t *testing.T, f *Files, application *app.App) *sdk.ClientSession {
	t.Helper()
	return connectFixture(t, Operations(f, application, nil), func(s *sdk.Server) { AddAgentResource(s, f) })
}
func readView(t *testing.T, cs *sdk.ClientSession, p string) AgentView {
	t.Helper()
	r, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: agentResourceURI(p)})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Contents) != 1 || r.Contents[0].MIMEType != "application/json" || len(r.Contents[0].Text) > 2<<20 {
		t.Fatal("invalid resource envelope")
	}
	var view AgentView
	if err := json.Unmarshal([]byte(r.Contents[0].Text), &view); err != nil {
		t.Fatal(err)
	}
	if view.Kind != "kmx.agent-view" || view.SchemaVersion != 1 || view.File.Path != p {
		t.Fatal("resource identity lost")
	}
	return view
}
func TestAgentResourceShapesAndConfinement(t *testing.T) {
	f, root, source := canonicalFiles(t)
	minimal := "# Greeter\n\nGreet the supplied name.\n\n## Model\n- **Preferred:** fake\n\n## Examples" + strings.Split(source, "## Examples")[1]
	if err := os.WriteFile(filepath.Join(root, "minimal.agent.md"), []byte(minimal), 0600); err != nil {
		t.Fatal(err)
	}
	saved, err := f.Create(t.Context(), ".squad/agents/greeter/charter.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	cs := resourceClient(t, f, &app.App{})
	templates, err := cs.ListResourceTemplates(t.Context(), nil)
	if err != nil || len(templates.ResourceTemplates) != 1 {
		t.Fatal("resource not discoverable", err)
	}
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 4 {
		t.Fatal("resource added a tool")
	}
	for _, p := range []string{"minimal.agent.md", saved.Path} {
		view := readView(t, cs, p)
		if view.Model != "fake" || len(view.Examples) != 1 || view.Examples[0].Assertions[0].Value != "Hello, Ada" || view.Latest.Status != "none" || view.Latest.Result != nil {
			t.Fatalf("incomplete authored view: %+v", view)
		}
		if !strings.HasPrefix(view.File.Revision, "sha256:") || strings.Contains(view.Instructions, "examples:") {
			t.Fatal("incoherent source view")
		}
	}
	for _, uri := range []string{"file:///etc/passwd", "kmx://other/minimal.agent.md", "kmx://agent/..%2Fminimal.agent.md", "kmx://agent/minimal.agent.md?showContent=true", "kmx://agent/%6Dinimal.agent.md", "kmx://agent/.squad/agents/greeter/charter.md", "kmx://agent/no.txt", "kmx://agent/minimal.agent.md#anything", "kmx://agent/%252E%252E%252Fminimal.agent.md"} {
		_, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri})
		if err == nil {
			t.Fatalf("unsafe URI accepted: %s", uri)
		}
		if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "Greet the supplied") {
			t.Fatal("resource error leaked source")
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.agent.md"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "outside.agent.md"), filepath.Join(root, "linked.agent.md")); err == nil {
		if _, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: agentResourceURI("linked.agent.md")}); err == nil {
			t.Fatal("symlink read accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(root, "large.agent.md"), []byte(strings.Repeat("x", maxFileBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: agentResourceURI("large.agent.md")}); err == nil {
		t.Fatal("oversized resource accepted")
	}
}
func TestAgentResourceLatestResult(t *testing.T) {
	f, root, source := canonicalFiles(t)
	saved, err := f.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	fake := &previewFixture{verdict: runtime.EvaluationPass, noJournal: true}
	cs := resourceClient(t, f, &app.App{FileRunner: fake})
	// Absolute tool paths and relative resource URIs must identify the same file.
	previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": filepath.Join(root, saved.Path)})
	current := readView(t, cs, saved.Path)
	if current.Latest.Status != "current" || current.Latest.Result == nil || !current.Latest.Result.FullPass {
		t.Fatal("latest outcome absent")
	}
	b, _ := json.Marshal(current)
	if strings.Contains(string(b), "PRIVATE") {
		t.Fatal("default answer content leaked")
	}
	previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": saved.Path, "showContent": true})
	shown := readView(t, cs, saved.Path)
	if shown.Latest.Result.Examples[0].AnswerExcerpt != "PRIVATE ANSWER" {
		t.Fatal("existing answer opt-in lost")
	}
	if fake.calls != 2 {
		t.Fatal("resource read executed inference")
	}
	if err := os.WriteFile(filepath.Join(root, saved.Path), []byte(strings.Replace(source, "Hello, ", "Goodbye, ", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	stale := readView(t, cs, saved.Path)
	if stale.Latest.Status != "stale" || stale.Latest.Result != nil {
		t.Fatal("stale result joined to new expected values")
	}
	reopened, err := OpenFiles(root, f.validate)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if v := readView(t, resourceClient(t, reopened, &app.App{}), saved.Path); v.Latest.Status != "none" {
		t.Fatal("fresh server invented history")
	}
}
func TestAgentResourceRefusalsAreNotOldRevisions(t *testing.T) {
	f, root, source := canonicalFiles(t)
	minimal := "# Minimal\n\nGreet a name.\n\n## Model\n- **Preferred:** fake\n\n## Examples" + strings.Split(source, "## Examples")[1]
	if err := os.WriteFile(filepath.Join(root, "minimal.agent.md"), []byte(minimal), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Create(t.Context(), "greeter.agent.md", []byte(source)); err != nil {
		t.Fatal(err)
	}
	cs := resourceClient(t, f, &app.App{FileRunner: &previewFixture{verdict: runtime.EvaluationPass}})
	refused, _ := previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": "minimal.agent.md"})
	if refused.ReasonCode != "invalid_file" {
		t.Fatal("strict minimal execution changed")
	}
	if got := readView(t, cs, "minimal.agent.md"); got.Latest.Status != "none" {
		t.Fatalf("missing snapshot is not an old revision: %+v", got.Latest)
	}
	previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": "greeter.agent.md", "caseId": "missing"})
	got := readView(t, cs, "greeter.agent.md")
	if got.Latest.Status != "current" || got.Latest.Result == nil || got.Latest.Result.FullPass || got.Latest.Result.ReasonCode != "invalid_selection" {
		t.Fatalf("same-revision refusal lost: %+v", got.Latest)
	}
}

// Catch snapshots that normalize source bytes or mistake display acceptance for
// strict authoring acceptance. Run/Test must keep their existing strict semantics.
func TestAgentResourceAuthoringSnapshot(t *testing.T) {
	f, root, source := canonicalFiles(t)
	minimal := "# Minimal\n\nGreet a name.\n\n## Model\n- **Preferred:** fake\n\n## Examples" + strings.Split(source, "## Examples")[1]
	cs := resourceClient(t, f, &app.App{})
	for _, row := range []struct {
		name, source string
		editable     bool
	}{
		{"canonical.agent.md", source, true},
		{"crlf.agent.md", strings.ReplaceAll(strings.ReplaceAll(source, "\r\n", "\n"), "\n", "\r\n"), true},
		{"lf.agent.md", strings.ReplaceAll(source, "\r\n", "\n"), true},
		{"minimal.agent.md", minimal, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, row.name), []byte(row.source), 0600); err != nil {
				t.Fatal(err)
			}
			r, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: agentResourceURI(row.name)})
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				File      File `json:"file"`
				Authoring *struct {
					Source   string `json:"source"`
					Editable bool   `json:"editable"`
				} `json:"authoring"`
			}
			if err := json.Unmarshal([]byte(r.Contents[0].Text), &got); err != nil {
				t.Fatal(err)
			}
			if got.Authoring == nil || got.Authoring.Source != row.source || got.Authoring.Editable != row.editable {
				t.Fatal("exact source or strict editability lost")
			}
			wantRevision := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(got.Authoring.Source)))
			if got.File.Revision != wantRevision {
				t.Fatal("authoring source differs from revision bytes")
			}
			strict, err := f.snapshot(t.Context(), row.name)
			if row.editable && (err != nil || strict.Revision != got.File.Revision) {
				t.Fatal("strict snapshot semantics changed", err)
			}
			if !row.editable && (err == nil || strict != nil) {
				t.Fatal("inspection widened Run/Test acceptance")
			}
		})
	}
}

func TestAgentResourceOversizedAuthoringRemainsReadable(t *testing.T) {
	f, _, source := canonicalFiles(t)
	source = strings.ReplaceAll(source, "\r\n", "\n")
	heading := "## Accountable Responsibilities\n"
	start, end := strings.Index(source, heading), strings.Index(source, "## Non-Responsibilities\n")
	if start < 0 || end <= start {
		t.Fatal("canonical section fixture missing")
	}
	large := source[:start] + heading + strings.Repeat("&", 190000) + "\n\n" + source[end:]
	saved, err := f.Create(t.Context(), "large.agent.md", []byte(large))
	if err != nil {
		t.Fatal(err)
	}
	got := readView(t, resourceClient(t, f, &app.App{}), saved.Path)
	if got.Authoring != nil || got.File.Revision != saved.Revision || len(got.Examples) != 1 || !strings.Contains(got.Instructions, strings.Repeat("&", 190000)) {
		t.Fatal("oversized optional snapshot lost read-only projection")
	}
	got.Authoring = &AgentViewAuthoring{Source: large, Editable: true}
	encoded, err := json.Marshal(got)
	if err != nil || len(encoded) <= 2<<20 {
		t.Fatal("fixture no longer exceeds the encoded resource budget")
	}
}

func TestAgentResourceLateObservation(t *testing.T) {
	f, _, _ := canonicalFiles(t)
	first := f.beginPreview("greeter.agent.md")
	second := f.beginPreview("greeter.agent.md")
	newer := app.FilePreviewResult{State: "error", ReasonCode: "execution_failed"}
	f.finishPreview(second, "newer.agent.md", newer)
	f.finishPreview(first, "older.agent.md", app.FilePreviewResult{State: "complete", FullPass: true})
	f.viewMu.Lock()
	defer f.viewMu.Unlock()
	if f.latest.path != "newer.agent.md" || f.latest.result == nil || f.latest.result.ReasonCode != "execution_failed" {
		t.Fatal("late completion overwrote newer operation")
	}
}
