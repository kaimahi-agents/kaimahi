package app

import (
	tea "charm.land/bubbletea/v2"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceConsoleUsesSharedReviewPane(t *testing.T) {
	m := newAgentTUIModel(AgentTUIOptions{Demo: true, Workspace: "/authored/suite"})
	m.columns[0].Env = agentTUIEnvironment{Name: "kind-test", Local: true}
	m.columns[0].Loading = false
	next, _ := m.createAgent()
	p := next.(agentTUIModel).creation
	if p == nil || p.input.Value() != "/authored/suite" || p.review != nil || p.prepared != nil {
		t.Fatal("workspace selection bypassed the shared source/case review")
	}
}

func TestSharedConsoleAuthorsImageWorkspaceOffline(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	instructions := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(instructions, []byte("Answer briefly."), 0600); err != nil {
		t.Fatal(err)
	}
	m := newAgentTUIModel(AgentTUIOptions{})
	m.columns[0].Env = agentTUIEnvironment{Name: "kind-test", Local: true}
	m.columns[0].Loading = false
	next, _ := m.createAgent()
	m = next.(agentTUIModel)
	for _, value := range []string{"new " + root, "hello", "image-http", instructions, "capabilities"} {
		m.creation.input.SetValue(value)
		next, _ = m.updateCreatePane(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = next.(agentTUIModel)
	}
	summary, err := workspace.Describe(root)
	if err != nil || summary.Execution != workspace.ImageExecution {
		t.Fatalf("offline shared authoring: %+v %v", summary, err)
	}
}
