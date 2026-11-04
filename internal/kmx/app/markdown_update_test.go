package app

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestManagerUpdateIsReplacementAndNoopIsStable(t *testing.T) {
	a := mdFixture(t)
	m := &localAgentManager{agents: map[string]*managedAgent{}, socket: "shared", newExecutor: func() markdownExecutor { return &mdFakeExecutor{answer: "hello"} }}
	defer m.close()
	started := m.handle(t.Context(), agentWorkerRequest{Operation: "start", Source: a.Path, Version: a.Revision})
	if started.Instance == nil {
		t.Fatal(started.Error)
	}
	r := *started.Instance
	opened := m.handle(t.Context(), agentWorkerRequest{AgentID: r.ID, Version: r.Version, Generation: r.Generation, Operation: "session-open"})
	if opened.Session == "" {
		t.Fatal(opened.Error)
	}
	update := agentWorkerRequest{AgentID: r.ID, Version: r.Version, Generation: r.Generation, Operation: "update", NewVersion: a.Revision}
	noop := m.handle(t.Context(), update)
	if noop.Instance == nil || !noop.Instance.Started.Equal(r.Started) || noop.Instance.Generation != 1 || len(m.agents[r.ID].sessions) != 1 {
		t.Fatalf("no-op=%+v", noop)
	}
	if err := os.WriteFile(a.Path, []byte(strings.Replace(string(a.Source), "Greet the user.", "Say Goodbye.", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := ReadMarkdownAgent(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	update.NewVersion = b.Revision
	replaced := m.handle(t.Context(), update)
	if replaced.Instance == nil {
		t.Fatal(replaced.Error)
	}
	next := *replaced.Instance
	if next.ID != r.ID || next.Generation != 2 || next.Version != b.Revision || next.PreviousVersion != r.Version || !next.Started.After(r.Started) || len(m.agents[r.ID].sessions) != 0 {
		t.Fatalf("replacement=%+v", next)
	}
	if m.agents[r.ID].definition.Instructions != "Say Goodbye." {
		t.Fatal("replacement instructions wrong")
	}
	old := m.handle(t.Context(), agentWorkerRequest{AgentID: r.ID, Version: r.Version, Generation: r.Generation, Operation: "send", Session: opened.Session, Prompt: "hi"})
	if old.Error == "" {
		t.Fatal("old session accepted")
	}
	if again := m.handle(t.Context(), update); again.Error == "" {
		t.Fatal("stale update applied")
	}
}

func TestManagerUpdateInvalidInputsKeepSessions(t *testing.T) {
	a := mdFixture(t)
	m := &localAgentManager{agents: map[string]*managedAgent{}, socket: "shared", newExecutor: func() markdownExecutor { return &mdFakeExecutor{} }}
	defer m.close()
	r := m.handle(t.Context(), agentWorkerRequest{Operation: "start", Source: a.Path, Version: a.Revision}).Instance
	opened := m.handle(t.Context(), agentWorkerRequest{AgentID: r.ID, Version: r.Version, Generation: r.Generation, Operation: "session-open"})
	if err := os.WriteFile(a.Path, []byte("---\nbroken"), 0600); err != nil {
		t.Fatal(err)
	}
	reply := m.handle(t.Context(), agentWorkerRequest{AgentID: r.ID, Version: r.Version, Generation: r.Generation, Operation: "update", NewVersion: mdHash([]byte("---\nbroken"))})
	if reply.Error == "" || m.agents[r.ID].sessions[opened.Session] == nil || m.agents[r.ID].stopping {
		t.Fatal("invalid replacement disrupted instance")
	}
}

func TestUpdateConfirmationDeclineAndScript(t *testing.T) {
	p := MarkdownUpdatePlan{Instance: AgentInstance{Name: "frog", Version: "old"}, Definition: &MarkdownAgent{Revision: "new"}}
	var out bytes.Buffer
	if err := ConfirmAgentUpdate(t.Context(), &out, nil, p, false); err == nil {
		t.Fatal("script consent missing")
	}
	if err := ConfirmAgentUpdate(t.Context(), io.Discard, strings.NewReader("n\n"), p, false); err == nil {
		t.Fatal("decline ignored")
	}
	if err := ConfirmAgentUpdate(t.Context(), io.Discard, strings.NewReader("\n"), p, false); err != nil {
		t.Fatal(err)
	}
	if err := ConfirmAgentUpdate(t.Context(), io.Discard, nil, p, true); err != nil {
		t.Fatal(err)
	}
}
