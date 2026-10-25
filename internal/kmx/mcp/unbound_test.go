package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func unboundFiles(t *testing.T) (*Files, string) {
	t.Helper()
	bound, _, source := canonicalFiles(t)
	f, err := OpenFiles("", bound.validate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, source
}

func authoringCall(t *testing.T, cs *sdk.ClientSession, operation, requested, content, expected string) File {
	t.Helper()
	args := map[string]any{"schemaVersion": 1, "path": requested, "content": content}
	if operation == "edit" {
		args["expectedRevision"] = expected
	}
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: operation, Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("%s: %+v, %v", operation, res, err)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct{ File File }
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.File.Path != requested || saved.File.Revision == "" {
		t.Fatalf("requested identity lost: %+v", saved.File)
	}
	return saved.File
}

// Catch root binding, filename-ID restrictions and path-schema limits leaking
// into unbound access. Canonical content validation still owns authored identity.
func TestUnboundFilesPathForms(t *testing.T) {
	f, source := unboundFiles(t)
	t.Chdir(t.TempDir())
	cs := resourceClient(t, f, &app.App{FileRunner: &previewFixture{verdict: runtime.EvaluationPass}})
	for _, requested := range []string{
		"./relative folder/Agent café #1.agent.md",
		filepath.Join(t.TempDir(), "ordinary folder", "nested", "CON.agent.md"),
		filepath.Join(t.TempDir(), "项目 Team Member", ".squad", "agents", "greeter", "charter.md"),
		filepath.Join(t.TempDir(), strings.Repeat("nested-", 30), "another folder", "Agent 2.agent.md"),
	} {
		t.Run(requested, func(t *testing.T) {
			saved := authoringCall(t, cs, "create", requested, source, "")
			view := readView(t, cs, requested)
			if view.Authoring == nil || !view.Authoring.Editable || view.Authoring.Source != source || view.File.Revision != saved.Revision {
				t.Fatal("resource did not read the exact created source")
			}
			for _, operation := range []string{"run", "test"} {
				got, res := previewCall(t, cs, operation, map[string]any{"schemaVersion": 1, "path": requested})
				if res.IsError || !got.FullPass || got.File.Path != requested || got.File.Revision != saved.Revision {
					t.Fatalf("%s: %+v", operation, got)
				}
			}
			edited := authoringCall(t, cs, "edit", requested, source+"\n", saved.Revision)
			if edited.Revision == saved.Revision {
				t.Fatal("edit lost exact-byte revision")
			}
			_, err := f.Edit(t.Context(), requested, saved.Revision, []byte(source))
			requireCode(t, err, "stale_revision")
			_, err = f.Edit(t.Context(), requested, edited.Revision, []byte("invalid"))
			requireCode(t, err, "invalid_file")
			content, err := os.ReadFile(requested)
			if err != nil || string(content) != source+"\n" {
				t.Fatalf("saved content: %q, %v", content, err)
			}
		})
	}
}

// Create through a parent alias, preview through a file alias, read through both
// spellings and edit through the alias without replacing either link.
func TestUnboundMCPAliasesKeepIdentityAndLinks(t *testing.T) {
	t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
	f, source := unboundFiles(t)
	realParent, aliases := t.TempDir(), t.TempDir()
	parentAlias := filepath.Join(aliases, "repo link")
	if err := os.Symlink(realParent, parentAlias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	requested := parentAlias + "/unused/../new folder/Greeter café.agent.md"
	target := filepath.Join(realParent, "new folder", "Greeter café.agent.md")
	cs := resourceClient(t, f, &app.App{FileRunner: &previewFixture{verdict: runtime.EvaluationPass}})
	saved := authoringCall(t, cs, "create", requested, source, "")
	if got := readView(t, cs, requested); got.File.Revision != saved.Revision || got.Latest.Status != "none" {
		t.Fatal("created resource identity lost")
	}
	charterAlias := filepath.Join(aliases, ".squad", "agents", "alias", "charter.md")
	if err := os.MkdirAll(filepath.Dir(charterAlias), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, charterAlias); err != nil {
		t.Fatal(err)
	}
	if view := readView(t, cs, charterAlias); view.Authoring == nil || !view.Authoring.Editable {
		t.Fatal("alias spelling changed canonical target editability")
	}
	fileAlias := filepath.Join(aliases, "different name.agent.md")
	if err := os.Symlink(target, fileAlias); err != nil {
		t.Fatal(err)
	}
	got, res := previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": fileAlias})
	if res.IsError || !got.FullPass || got.File.Path != fileAlias || got.File.Revision != saved.Revision {
		t.Fatalf("aliased preview: %+v", got)
	}
	for _, p := range []string{requested, target, fileAlias} {
		view := readView(t, cs, p)
		if view.Latest.Status != "current" || view.Latest.Result == nil || view.Latest.Result.File.Path != fileAlias || !view.Latest.Result.FullPass {
			t.Fatalf("canonical latest matching lost: %+v", view.Latest)
		}
	}
	edited := authoringCall(t, cs, "edit", fileAlias, source+"\n", saved.Revision)
	for _, p := range []string{parentAlias, fileAlias} {
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("edit replaced link %q: %v", p, err)
		}
	}
	if view := readView(t, cs, requested); view.File.Revision != edited.Revision || view.Latest.Status != "stale" || view.Latest.Result != nil {
		t.Fatal("edit did not invalidate the prior alias preview")
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != source+"\n" {
		t.Fatal("edit did not change the agent target", err)
	}
}

// Canvas uses encodeURIComponent, which leaves apostrophes, parentheses and !
// unescaped unlike Go's PathEscape. Both spellings must read the same resource.
func TestUnboundResourceCanvasURIEncoding(t *testing.T) {
	f, source := unboundFiles(t)
	root := t.TempDir()
	requested := filepath.Join(root, "Bob's (agents) + notes", "Agent!.agent.md")
	cs := resourceClient(t, f, &app.App{})
	saved := authoringCall(t, cs, "create", requested, source, "")
	uri := strings.NewReplacer("%27", "'", "%28", "(", "%29", ")", "%21", "!", "+", "%2B").Replace(agentResourceURI(requested))
	res, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatal(err)
	}
	var view AgentView
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &view); err != nil {
		t.Fatal(err)
	}
	if view.File != saved || view.Authoring == nil || view.Authoring.Source != source {
		t.Fatal("canvas URI did not return the requested agent")
	}
	readView(t, cs, requested)
}

// Do not replace the shared App's explicit withheld-file marker with a path
// after unsafe tool content has caused it to clear the result identity.
func TestUnboundPreviewRetainsWithheldIdentity(t *testing.T) {
	f, source := unboundFiles(t)
	requested := filepath.Join(t.TempDir(), "greeter.agent.md")
	cs := resourceClient(t, f, &app.App{FileRunner: &previewFixture{cause: agentpreview.ErrUnsafeToolContent}})
	authoringCall(t, cs, "create", requested, source, "")
	got, result := previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": requested})
	if !result.IsError || got.ReasonCode != "unsafe_tool_content" || got.File.Path != "[agent file]" || got.File.Revision != "" {
		t.Fatalf("shared App redaction overwritten: %+v", got)
	}
}

// Full paths are operational identities, but preview results must retain the
// App's credential withholding for both basenames and parent directories.
func TestUnboundPreviewWithholdsSensitivePaths(t *testing.T) {
	canary := "ghp_" + strings.Repeat("a", 36)
	for _, placement := range []string{"basename", "parent"} {
		t.Run(placement, func(t *testing.T) {
			f, source := unboundFiles(t)
			requested := filepath.Join(t.TempDir(), canary+".agent.md")
			if placement == "parent" {
				requested = filepath.Join(t.TempDir(), canary, "greeter.agent.md")
			}
			cs := resourceClient(t, f, &app.App{FileRunner: &previewFixture{verdict: runtime.EvaluationPass}})
			authoringCall(t, cs, "create", requested, source, "")
			got, result := previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": requested})
			if result.IsError || !got.FullPass || got.File.Path != "withheld" {
				t.Fatal("preview did not preserve credential withholding")
			}
			view := readView(t, cs, requested)
			if view.Latest.Status != "current" || view.Latest.Result == nil || view.Latest.Result.File.Path != "withheld" {
				t.Fatal("latest preview did not retain credential withholding")
			}
			for _, projection := range []any{result.StructuredContent, result.Content, view.Latest, f.latest.result} {
				encoded, err := json.Marshal(projection)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), canary) {
					t.Fatal("credential-shaped path reached a preview projection")
				}
			}
		})
	}
}

// Identical basenames and bytes in unrelated repos must not attribute one
// preview to the other file. Relative resource layouts remain usable too.
func TestUnboundMCPUnrelatedRepositories(t *testing.T) {
	f, source := unboundFiles(t)
	first, second := filepath.Join(t.TempDir(), "greeter.agent.md"), filepath.Join(t.TempDir(), "greeter.agent.md")
	cs := resourceClient(t, f, &app.App{FileRunner: &previewFixture{verdict: runtime.EvaluationPass}})
	for _, p := range []string{first, second} {
		authoringCall(t, cs, "create", p, source, "")
	}
	previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": first})
	if got := readView(t, cs, second); got.Latest.Status != "none" || got.Latest.Result != nil {
		t.Fatal("same-name agent inherited another repo's preview")
	}
	t.Chdir(filepath.Dir(first))
	if got := readView(t, cs, "greeter.agent.md"); got.Latest.Status != "current" || got.Latest.Result.File.Path != first {
		t.Fatal("relative resource did not match absolute preview")
	}
	previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": second, "caseId": "missing"})
	if got := readView(t, cs, first); got.Latest.Result != nil && got.Latest.Result.ReasonCode == "invalid_selection" {
		t.Fatal("second repo's result leaked onto first repo")
	}
	if got := readView(t, cs, second); got.Latest.Status != "current" || got.Latest.Result == nil || got.Latest.Result.ReasonCode != "invalid_selection" {
		t.Fatal("second repo's latest result absent")
	}
}

// Catch checking only the requested suffix (alias.agent.md -> README.md), or
// only the resolved suffix (notes.md -> actual.agent.md), before reads or writes.
func TestUnboundMCPRejectsNonAgentAliases(t *testing.T) {
	f, source := unboundFiles(t)
	root := t.TempDir()
	agent, notes := filepath.Join(root, "actual.agent.md"), filepath.Join(root, "README.md")
	for _, p := range []string{agent, notes} {
		if err := os.WriteFile(p, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	badAgent, badNotes := filepath.Join(root, "alias.agent.md"), filepath.Join(root, "notes.md")
	if err := os.Symlink(notes, badAgent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(agent, badNotes); err != nil {
		t.Fatal(err)
	}
	runner := &previewFixture{verdict: runtime.EvaluationPass}
	cs := resourceClient(t, f, &app.App{FileRunner: runner})
	for _, requested := range []string{badAgent, badNotes, notes, filepath.Join(root, ".agent.md"), filepath.Join(root, "ordinary", "charter.md")} {
		for _, operation := range []string{"create", "edit", "run", "test"} {
			args := map[string]any{"schemaVersion": 1, "path": requested}
			if operation == "create" || operation == "edit" {
				args["content"] = source
			}
			if operation == "edit" {
				args["expectedRevision"] = revision([]byte(source))
			}
			res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: operation, Arguments: args})
			if err != nil || !res.IsError {
				t.Fatalf("%s accepted non-agent alias %q: %+v, %v", operation, requested, res, err)
			}
			encoded, err := json.Marshal(res.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var refused struct{ ReasonCode string }
			if err := json.Unmarshal(encoded, &refused); err != nil {
				t.Fatal(err)
			}
			if refused.ReasonCode != "unsafe_path" {
				t.Fatalf("%s refused for an unrelated reason: %s", operation, encoded)
			}
		}
		if _, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: agentResourceURI(requested)}); err == nil {
			t.Fatalf("resource accepted non-agent path %q", requested)
		}
	}
	if runner.calls != 0 {
		t.Fatal("non-agent path reached preview runner")
	}
	for _, p := range []string{agent, notes} {
		content, err := os.ReadFile(p)
		if err != nil || string(content) != source {
			t.Fatal("refused operation changed source", err)
		}
	}
}
