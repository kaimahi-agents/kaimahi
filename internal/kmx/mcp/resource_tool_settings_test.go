package mcp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestResourceToolSettingsRefusalBecomesStaleAfterConfigRepair(t *testing.T) {
	files, root, source := canonicalFiles(t)
	source = strings.Replace(source, "## Examples", "## Tools\n- ping\n\n## Examples", 1)
	if _, err := files.Create(t.Context(), "greeter.agent.md", []byte(source)); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "kmx.yaml")
	if err := os.WriteFile(config, []byte("tools: {ping: Ask}"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &previewFixture{verdict: runtime.EvaluationPass}
	cs := resourceClient(t, files, &app.App{FileRunner: runner})
	out, call := previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": "greeter.agent.md"})
	if !call.IsError || out.ReasonCode != "invalid_tool_settings" || runner.calls != 0 {
		t.Fatalf("not a config refusal: %+v", out)
	}
	if err := os.WriteFile(config, []byte("tools: {ping: Disabled}"), 0600); err != nil {
		t.Fatal(err)
	}
	view := readView(t, cs, "greeter.agent.md")
	if view.Latest.Status != "stale" || view.Latest.Result != nil {
		t.Fatalf("repaired settings still carry their old error: %+v", view.Latest)
	}
}

func TestResourceToolSettingsDoNotHidePreExecutionRefusals(t *testing.T) {
	for _, reason := range []string{"no_tests", "model_unavailable", "invalid_selection"} {
		t.Run(reason, func(t *testing.T) {
			files, root, source := canonicalFiles(t)
			source = strings.Replace(source, "## Examples", "## Tools\n- ping\n\n## Examples", 1)
			if reason == "no_tests" {
				source = strings.Split(source, "## Examples")[0] + "## Examples\n```yaml\nexamples: []\n```\n"
			}
			if _, err := files.Create(t.Context(), "greeter.agent.md", []byte(source)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("tools: {ping: Disabled}"), 0600); err != nil {
				t.Fatal(err)
			}
			runner := &previewFixture{verdict: runtime.EvaluationPass}
			var route *modelaccess.Route
			if reason == "model_unavailable" {
				route = &modelaccess.Route{Provider: "fake", Model: "other", Auth: "none"}
			}
			cs := connectFixture(t, Operations(files, &app.App{FileRunner: runner}, route), func(s *sdk.Server) { AddAgentResource(s, files) })
			args := map[string]any{"schemaVersion": 1, "path": "greeter.agent.md"}
			if reason == "invalid_selection" {
				args["caseId"] = "absent"
			}
			out, call := previewCall(t, cs, "test", args)
			view := readView(t, cs, "greeter.agent.md")
			if !call.IsError || out.ReasonCode != reason || runner.calls != 0 || len(out.ToolSettings) != 0 {
				t.Fatalf("not a pre-execution refusal: %+v", out)
			}
			if view.Latest.Status != "current" || view.Latest.Result == nil || view.Latest.Result.ReasonCode != reason {
				t.Fatalf("refusal incorrectly stale: %+v", view.Latest)
			}
		})
	}
}

func TestResourceToolSettingsFollowPhysicalAgentTarget(t *testing.T) {
	files, root, source := canonicalFiles(t)
	unbound, err := OpenFiles("", files.validate)
	if err != nil {
		t.Fatal(err)
	}
	defer unbound.Close()
	folder := filepath.Join(root, "actual")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(folder, "greeter.agent.md")
	source = strings.Replace(source, "## Examples", "## Tools\n- ping\n\n## Examples", 1)
	if _, err := unbound.Create(t.Context(), target, []byte(source)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("tools: {ping: Enabled}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "kmx.yaml"), []byte("tools: {ping: Disabled}"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias.agent.md")
	if err := os.Symlink(target, alias); err != nil {
		t.Skip("symlink fixture unavailable")
	}
	cs := resourceClient(t, unbound, &app.App{FileRunner: &previewFixture{verdict: runtime.EvaluationPass, noJournal: true}})
	out, call := previewCall(t, cs, "test", map[string]any{"schemaVersion": 1, "path": alias})
	view := readView(t, cs, alias)
	if call.IsError || len(out.ToolSettings) != 1 || out.ToolSettings[0].State != localtools.Disabled || !reflect.DeepEqual(out.ToolSettings, view.ToolSettings) {
		t.Fatalf("alias folder applied: %+v %+v", out, view)
	}
}

func TestResourceToolSettingsFolderAndConfigOnlyStaleness(t *testing.T) {
	for _, unbound := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "unbound"}[unbound], func(t *testing.T) {
			files, root, source := canonicalFiles(t)
			folder := filepath.Join(root, "nested")
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			selectedRoot := folder
			if unbound {
				selectedRoot = ""
			}
			files, err := OpenFiles(selectedRoot, files.validate)
			if err != nil {
				t.Fatal(err)
			}
			defer files.Close()
			path := "greeter.agent.md"
			if unbound {
				path = filepath.Join(folder, path)
			}
			source = strings.Replace(source, "## Examples", "## Tools\n- ping\n\n## Examples", 1)
			if _, err := files.Create(t.Context(), path, []byte(source)); err != nil {
				t.Fatal(err)
			}
			// Parent config must not override the agent's own folder.
			if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("tools: {ping: Enabled}"), 0600); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(root, "nested", "kmx.yaml")
			if err := os.WriteFile(config, []byte("tools: {ping: Disabled}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			want := []localtools.ToolSetting{{Name: "ping", State: localtools.Disabled}}
			runner := &previewFixture{verdict: runtime.EvaluationPass, noJournal: true, during: func(r agentpreview.Request) {
				if !reflect.DeepEqual(r.ToolSettings, want) {
					t.Fatalf("wrong execution folder: %+v", r.ToolSettings)
				}
			}}
			cs := resourceClient(t, files, &app.App{FileRunner: runner})
			before := readView(t, cs, path)
			if !reflect.DeepEqual(before.ToolSettings, want) {
				t.Fatalf("resource settings: %+v", before)
			}
			for _, op := range []string{"run", "test"} {
				out, call := previewCall(t, cs, op, map[string]any{"schemaVersion": 1, "path": path})
				if call.IsError || !reflect.DeepEqual(out.ToolSettings, want) {
					t.Fatalf("run settings: %+v", out)
				}
			}
			if current := readView(t, cs, path); current.Latest.Status != "current" {
				t.Fatal("current result lost")
			}
			if err := os.WriteFile(config, []byte("# same state, other keys\ntools: {ping: Disabled}\nother: true\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if current := readView(t, cs, path); current.Latest.Status != "current" {
				t.Fatal("unrelated config edit staled result")
			}
			if err := os.WriteFile(config, []byte("tools: {ping: Enabled}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			stale := readView(t, cs, path)
			if stale.File.Revision != before.File.Revision || stale.Latest.Status != "stale" || stale.Latest.Result != nil || stale.ToolSettings[0].State != localtools.Enabled {
				t.Fatalf("config-only change kept old pass: %+v", stale)
			}
			if err := os.WriteFile(config, []byte("tools: {ping: Ask}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			refused, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: agentResourceURI(path)})
			if err != nil || refused == nil || len(refused.Contents) != 1 || refused.Contents[0].Text != `{"schemaVersion":1,"kind":"kmx.agent-view-error","reasonCode":"invalid_tool_settings"}` {
				t.Fatalf("invalid config must return only its fixed classification: %v", err)
			}
		})
	}
}
