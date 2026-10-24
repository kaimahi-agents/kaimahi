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

// The portable normalizer tests need no mounted drive; this additionally exercises
// real WSL IO, URI parsing and history with the same input the Windows host sends.
func TestWSLDrivePathsUseMountedFiles(t *testing.T) {
	if info, err := os.Stat("/mnt/c"); err != nil || !info.IsDir() {
		t.Skip("WSL C drive is not mounted")
	}
	workspace, err := os.MkdirTemp("/mnt/c", "kmx-path-test-")
	if err != nil {
		t.Skipf("mounted drive is not writable: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(workspace) })
	_, _, source := canonicalFiles(t)
	history, _ := historyPaths(t)
	t.Chdir(t.TempDir())
	f := historyFiles(t, "", history)
	cs := connectFixture(t, Operations(f, &app.App{FileRunner: &previewFixture{verdict: runtime.EvaluationPass}}, nil), func(s *sdk.Server) {
		AddAgentResource(s, f)
		AddAgentVersionResource(s, f)
	})
	for _, suffix := range []string{"Team café/release-notes.agent.md", ".squad/agents/greeter/charter.md"} {
		t.Run(suffix, func(t *testing.T) {
			target := filepath.Join(workspace, filepath.FromSlash(suffix))
			requested := "C:" + strings.ReplaceAll(strings.TrimPrefix(target, "/mnt/c"), "/", `\`)
			saved := authoringCall(t, cs, "create", requested, source, "")
			if got, err := os.ReadFile(target); err != nil || string(got) != source {
				t.Fatalf("Windows path did not create the mounted target: %v", err)
			}
			if _, err := os.Lstat(requested); !os.IsNotExist(err) {
				t.Fatalf("literal Windows filename exists in cwd: %v", err)
			}
			view := readView(t, cs, requested)
			if view.File.Revision != saved.Revision || view.Authoring.Source != source {
				t.Fatal("Windows resource lost requested identity or source")
			}
			if _, err := f.Create(t.Context(), requested, []byte(source+"\n")); err == nil {
				t.Fatal("duplicate Create overwrote the target")
			} else {
				requireCode(t, err, "already_exists")
			}
			updated := authoringCall(t, cs, "edit", requested, source+"\n", saved.Revision)
			_, err := f.Edit(t.Context(), requested, saved.Revision, []byte(source))
			requireCode(t, err, "stale_revision")
			for _, operation := range []string{"test", "run"} {
				result, response := previewCall(t, cs, operation, map[string]any{"schemaVersion": 1, "path": requested})
				if response.IsError || !result.FullPass || result.File.Path != requested || result.File.Revision != updated.Revision {
					t.Fatalf("Windows %s lost its target: %+v", operation, result)
				}
			}
			view = readView(t, cs, requested)
			if view.History.Status != "available" || len(view.History.Versions) != 1 {
				t.Fatalf("Windows history unavailable: %+v", view.History)
			}
			uri := view.History.Versions[0].URI
			parsed, _, err := agentVersionPath(uri)
			if err != nil || parsed != requested {
				t.Fatalf("Windows version URI parser: %q, %v", parsed, err)
			}
			old, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri})
			if err != nil {
				t.Fatal("Windows agent-version resource refused", err)
			}
			var snapshot struct{ Source string }
			if json.Unmarshal([]byte(old.Contents[0].Text), &snapshot) != nil || snapshot.Source != source {
				t.Fatal("Windows version source changed")
			}
		})
	}
	inside := filepath.Join(workspace, "inside")
	if err := os.Mkdir(inside, 0700); err != nil {
		t.Fatal(err)
	}
	bound := historyFiles(t, inside, history)
	outside := "C:" + strings.ReplaceAll(strings.TrimPrefix(filepath.Join(workspace, "outside.agent.md"), "/mnt/c"), "/", `\`)
	_, err = bound.Create(t.Context(), outside, []byte(source))
	requireCode(t, err, "unsafe_path")
}
