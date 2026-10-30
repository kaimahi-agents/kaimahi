package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypedInventoryIncludesMDAndUsesSharedActions(t *testing.T) {
	a := mdFixture(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(a.Path)))
	app := &App{}
	rows, err := app.agentTUIInventory(context.Background(), agentTUIEnvironment{Name: "files", Directory: root, Local: true}, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	row := rows[0]
	if row.Runtime != "md" || row.DefinitionPath != a.Path || row.SystemPrompt != a.Instructions {
		t.Fatalf("row=%+v", row)
	}
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	m.columns[0].Agents = append([]agentTUIAgent{row}, m.columns[0].Agents...)
	view := strings.Join(m.agentDetailsLines(row, 100), "\n")
	if !strings.Contains(view, "Definition") || !strings.Contains(view, "Test status") {
		t.Fatal(view)
	}
	for _, action := range m.agentActions(row) {
		if action.key == "f" || action.key == "R" {
			t.Fatalf("md acquired cluster action: %+v", action)
		}
	}
	updated, _ := m.execute("/chat hello")
	model := updated.(agentTUIModel)
	if model.action != nil {
		t.Fatal("demo must not execute")
	}
	m.opt.Demo = false
	updated, _ = m.execute("/chat hello")
	model = updated.(agentTUIModel)
	if model.action == nil || model.action.agent.Runtime != "md" {
		t.Fatal("chat did not retain md type")
	}
}

func TestLocalStatusFirstRunDoesNotCreateState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	t.Setenv("KMX_HOME", home)
	root := t.TempDir()
	var out bytes.Buffer
	err := localStatus(t.Context(), &out, root, "json", func() string { return "" }, func(context.Context, string) string { t.Fatal("auth without CLI"); return "" })
	if err != nil {
		t.Fatal(err)
	}
	var report LocalEnvironmentStatus
	if err = json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Environment != "local" || report.LocalInference != "not configured" || report.CopilotCLI != "not installed" || len(report.Agents) != 0 {
		t.Fatalf("%+v", report)
	}
	if _, err = os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("status wrote state: %v", err)
	}
}

func TestLocalStatusReportsSavedProviderAndAgents(t *testing.T) {
	a := mdFixture(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(a.Path)))
	if err := SetMarkdownProvider("accepted"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"logged in", "not logged in", "status unavailable"} {
		t.Run(status, func(t *testing.T) {
			var out bytes.Buffer
			calls := 0
			err := localStatus(t.Context(), &out, root, "json", func() string { return "/copilot" }, func(context.Context, string) string { calls++; return status })
			if (err != nil) != (status == "status unavailable") {
				t.Fatalf("err=%v", err)
			}
			var report LocalEnvironmentStatus
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || report.LocalInference != "copilot (saved default)" || len(report.Agents) != 1 || report.Agents[0].Status != "UNTESTED" {
				t.Fatalf("%+v", report)
			}
		})
	}
}
