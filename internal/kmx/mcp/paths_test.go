package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPPathForms(t *testing.T) {
	for _, relative := range []string{"greeter.agent.md", ".squad/agents/greeter/charter.md"} {
		for _, absolute := range []bool{false, true} {
			name := "relative/" + relative
			if absolute {
				name = "absolute/" + relative
			}
			t.Run(name, func(t *testing.T) {
				f, root, source := canonicalFiles(t)
				requested := relative
				if absolute {
					// Keep the unclean spelling in the actual protocol request.
					requested = root + string(filepath.Separator) + "unused" + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.FromSlash(relative)
				}
				cs := connectFixture(t, Operations(f, &app.App{FileRunner: &previewFixture{verdict: runtime.EvaluationPass}}, nil))
				var saved struct{ File File }
				call := func(name string, args map[string]any) {
					t.Helper()
					res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
					if err != nil || res.IsError {
						t.Fatalf("%s: %+v, %v", name, res, err)
					}
					b, err := json.Marshal(res.StructuredContent)
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(b, &saved); err != nil {
						t.Fatal(err)
					}
					if saved.File.Path != relative || saved.File.Revision == "" {
						t.Fatalf("saved identity: %+v", saved.File)
					}
				}
				call("create", map[string]any{"schemaVersion": 1, "path": requested, "content": source})
				created := saved.File.Revision
				call("edit", map[string]any{"schemaVersion": 1, "path": requested, "content": source + "\n", "expectedRevision": created})
				if saved.File.Revision == created {
					t.Fatal("Edit did not change the exact-byte revision")
				}
				content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
				if err != nil || string(content) != source+"\n" {
					t.Fatalf("saved content: %q, %v", content, err)
				}
				for _, operation := range []string{"run", "test"} {
					got, res := previewCall(t, cs, operation, map[string]any{"schemaVersion": 1, "path": requested})
					if res.IsError || !got.FullPass || got.File.Revision != saved.File.Revision {
						t.Fatalf("%s did not consume the saved snapshot: %+v", operation, got)
					}
				}
			})
		}
	}
}

func TestMCPAbsolutePathConfinement(t *testing.T) {
	f, root, source := canonicalFiles(t)
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "greeter.agent.md")
	if err := os.WriteFile(outsideFile, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		"outside-existing": outsideFile,
		"outside-missing":  filepath.Join(outside, ".squad", "agents", "greeter", "charter.md"),
		"sibling-prefix":   filepath.Join(root+"-other", "greeter.agent.md"),
		"root-itself":      root,
		"invalid-layout":   filepath.Join(root, "notes.md"),
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "escape.agent.md")); err == nil {
		paths["symlink-escape"] = filepath.Join(root, "escape.agent.md")
		if err := os.Symlink(filepath.Join(outside, "absent.agent.md"), filepath.Join(root, "dangling.agent.md")); err != nil {
			t.Fatal(err)
		}
		paths["dangling-symlink"] = filepath.Join(root, "dangling.agent.md")
		if err := os.Symlink(outside, filepath.Join(root, ".squad")); err != nil {
			t.Fatal(err)
		}
		paths["parent-symlink-escape"] = filepath.Join(root, ".squad", "agents", "greeter", "charter.md")
	} else {
		t.Logf("symlink cases unavailable: %v", err)
	}
	runner := &previewFixture{verdict: runtime.EvaluationPass}
	cs := connectFixture(t, Operations(f, &app.App{FileRunner: runner}, nil))
	for label, requested := range paths {
		for _, operation := range []string{"create", "edit", "run", "test"} {
			t.Run(label+"/"+operation, func(t *testing.T) {
				args := map[string]any{"schemaVersion": 1, "path": requested}
				if operation == "create" || operation == "edit" {
					args["content"] = source
				}
				if operation == "edit" {
					args["expectedRevision"] = revision([]byte(source))
				}
				res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: operation, Arguments: args})
				if err != nil {
					t.Fatal(err)
				}
				b, err := json.Marshal(res.StructuredContent)
				if err != nil {
					t.Fatal(err)
				}
				var refusal struct{ ReasonCode string }
				if err := json.Unmarshal(b, &refusal); err != nil {
					t.Fatal(err)
				}
				if !res.IsError || refusal.ReasonCode != "unsafe_path" {
					t.Fatalf("%s: expected unsafe_path, got %s", operation, b)
				}
			})
		}
	}
	if runner.calls != 0 {
		t.Fatal("refused paths reached the runner")
	}
	b, err := os.ReadFile(outsideFile)
	if err != nil || string(b) != source {
		t.Fatal("outside file changed")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 1 || entries[0].Name() != "greeter.agent.md" {
		t.Fatalf("refused Create changed outside entries: %v, %v", entries, err)
	}
	if _, err := os.Stat(root + "-other"); !os.IsNotExist(err) {
		t.Fatalf("refused Create made a sibling: %v", err)
	}
}

func TestFilesAbsoluteSymlinkAliases(t *testing.T) {
	f, root, source := canonicalFiles(t)
	alias := filepath.Join(t.TempDir(), "root-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	viaAlias, err := OpenFiles(alias, f.validate)
	if err != nil {
		t.Fatal(err)
	}
	defer viaAlias.Close()
	f = viaAlias
	created, err := f.Create(t.Context(), filepath.Join(alias, ".squad", "agents", "greeter", "charter.md"), []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if created.Path != ".squad/agents/greeter/charter.md" {
		t.Fatalf("noncanonical saved path: %+v", created)
	}
	fileAlias := filepath.Join(root, "alias.agent.md")
	if err := os.Symlink(filepath.Join(root, filepath.FromSlash(created.Path)), fileAlias); err != nil {
		t.Fatal(err)
	}
	_, err = f.Edit(t.Context(), "alias.agent.md", created.Revision, []byte(source+"\n"))
	requireCode(t, err, "unsafe_path")
	edited, err := f.Edit(t.Context(), fileAlias, created.Revision, []byte(source+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if edited.Path != created.Path {
		t.Fatalf("alias replaced canonical identity: %+v", edited)
	}
	parsed, err := f.snapshot(t.Context(), fileAlias)
	if err != nil || parsed.Revision != edited.Revision {
		t.Fatalf("alias snapshot: %+v, %v", parsed, err)
	}
	if info, err := os.Lstat(fileAlias); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("Edit replaced the alias instead of the rooted target")
	}
}

func TestFilesAbsolutePathsRefuseRootReplacement(t *testing.T) {
	f, root, source := canonicalFiles(t)
	moved := filepath.Join(t.TempDir(), "original")
	if err := os.Rename(root, moved); err != nil {
		t.Skipf("open-root rename unavailable: %v", err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := f.Create(t.Context(), filepath.Join(root, "greeter.agent.md"), []byte(source))
	requireCode(t, err, "unsafe_path")
	for _, dir := range []string{root, moved} {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("root replacement produced effects: %v, %v", entries, err)
		}
	}
}
