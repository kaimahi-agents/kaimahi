package app

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestConsoleLiftMovedWithoutDispatch(t *testing.T) {
	want := "Deploy moved: kmx deploy <ref>@<digest> --to-context … --evidence …"
	for _, entry := range []string{"key", "/lift", "/lift sample new"} {
		t.Run(entry, func(t *testing.T) {
			m := newAgentTUIModel(AgentTUIOptions{Demo: true})
			m.opt.Demo = false
			if entry == "key" {
				m = tuiKey(m, 'L', "L")
			} else {
				next, _ := m.execute(entry)
				m = next.(agentTUIModel)
			}
			if m.status != want || m.action != nil || m.command {
				t.Fatalf("Lift still dispatches: status=%q action=%v", m.status, m.action)
			}
		})
	}
	m := newAgentTUIModel(AgentTUIOptions{Demo: true})
	for _, choice := range m.agentActions(agentTUIAgent{Name: "sample", Namespace: "agents"}) {
		if choice.key == "L" {
			t.Fatal("retired Lift is still an action")
		}
	}
}

func TestConsoleMutationMenusRefuseWithoutDispatch(t *testing.T) {
	for _, key := range []rune{'f', 't'} {
		m := newAgentTUIModel(AgentTUIOptions{Demo: true})
		m.opt.Demo = false
		m = tuiKey(m, tea.KeyEnter, "")
		m = tuiKey(m, key, string(key))
		if m.action != nil || m.inference != nil {
			t.Fatalf("mutation editor dispatched for %c", key)
		}
		if strings.Contains(m.status, "lift is not yet available") {
			t.Fatal("retirement remedy denies the implemented suite lift")
		}
		for _, want := range []string{"authored", "workspace", "test", "push", "deploy"} {
			if !strings.Contains(m.status, want) {
				t.Errorf("missing remedy %q: %s", want, m.status)
			}
		}
	}
}

func TestConsoleInferenceSaveRefusesBeforeReadsAuthOrWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KMX_HOME", home)
	for _, kind := range []string{"cluster", "ollama", "foundry-cluster", "foundry", "copilot"} {
		t.Run(kind, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			err := f.app.consoleSaveInference(t.Context(), agentTUIEnvironment{Name: "local", Local: true}, agentTUIAgent{Name: "sample", Namespace: "agents"}, consoleInferenceSnapshot{}, consoleInferenceSource{Kind: kind}, "")
			if err == nil || !strings.Contains(err.Error(), "authored") || !strings.Contains(err.Error(), "test") {
				t.Errorf("wrong refusal: %v", err)
			}
			if calls := f.calls(t); calls != "" {
				t.Fatalf("commands before refusal: %s", calls)
			}
		})
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("local routing writes: %v %v", entries, err)
	}
}

func TestConsoleToolActionRefusesBeforeMutation(t *testing.T) {
	f := newOrkaFixture(t, nil)
	_, err := f.app.runAgentTUIAction(agentTUIAction{kind: "tools", source: agentTUIEnvironment{Name: "local", Local: true}, agent: agentTUIAgent{Name: "sample", Namespace: "agents"}})
	if err == nil || !strings.Contains(err.Error(), "authored") {
		t.Errorf("wrong refusal: %v", err)
	}
	if calls := f.calls(t); calls != "" {
		t.Fatalf("commands before refusal: %s", calls)
	}
}
