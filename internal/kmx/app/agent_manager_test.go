package app

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedManagerSessionsAreIndependent(t *testing.T) {
	a := mdFixture(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(a.Path)))
	if err := InitMarkdownAgent(root, "second"); err != nil {
		t.Fatal(err)
	}
	b, err := ReadMarkdownAgent(filepath.Join(root, ".github", "agents", "second.agent.md"))
	if err != nil {
		t.Fatal(err)
	}
	var executors []*mdFakeExecutor
	m := &localAgentManager{agents: map[string]*managedAgent{}, socket: "shared", lastActive: time.Now(), newExecutor: func() markdownExecutor {
		e := &mdFakeExecutor{answer: "hello"}
		executors = append(executors, e)
		return e
	}}
	defer m.close()
	start := func(a *MarkdownAgent) AgentInstance {
		t.Helper()
		reply := m.handle(t.Context(), agentWorkerRequest{Operation: "start", Source: a.Path, Version: a.Revision})
		if reply.Error != "" || reply.Instance == nil {
			t.Fatalf("%+v", reply)
		}
		return *reply.Instance
	}
	ar, br := start(a), start(b)
	if ar.PID != br.PID || ar.Socket != br.Socket || ar.ID == br.ID || len(executors) != 0 {
		t.Fatal("instances did not share manager with isolated executors")
	}
	call := func(r AgentInstance, op string) agentWorkerReply {
		return m.handle(context.Background(), agentWorkerRequest{AgentID: r.ID, Version: r.Version, Generation: r.Generation, Operation: op, Prompt: "hello"})
	}
	if reply := call(ar, "send"); reply.Error != "" {
		t.Fatal(reply.Error)
	}
	if len(executors) != 1 || executors[0].calls != 1 {
		t.Fatal("conversation crossed agents")
	}
	if reply := call(ar, "stop"); reply.Error != "" {
		t.Fatal(reply.Error)
	}
	if reply := call(br, "send"); reply.Error != "" {
		t.Fatal("stopping first agent affected second:", reply.Error)
	}
	if reply := call(ar, "send"); reply.Error == "" {
		t.Fatal("stopped session accepted prompt")
	}
	restarted := start(a)
	if restarted.Generation != 2 {
		t.Fatal("restart generation not incremented")
	}
	if reply := call(ar, "send"); reply.Error == "" {
		t.Fatal("old generation reached restarted session")
	}
}

type activityExecutor struct {
	started, release chan struct{}
	session          atomic.Value
}

func (e *activityExecutor) Start(context.Context) (string, error) { return "test", nil }
func (e *activityExecutor) Reset()                                { e.session.Store("") }
func (e *activityExecutor) Close()                                {}
func (e *activityExecutor) SessionIdentity() string {
	v := e.session.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}
func (e *activityExecutor) Answer(ctx context.Context, _ *MarkdownAgent, _ string, _ []MarkdownMock) (markdownAnswer, error) {
	e.session.Store("session-123456789")
	close(e.started)
	select {
	case <-e.release:
		return markdownAnswer{Text: "hello"}, nil
	case <-ctx.Done():
		return markdownAnswer{}, ctx.Err()
	}
}

func TestManagerReportsActivityIndependentlyOfLifecycle(t *testing.T) {
	a := mdFixture(t)
	executor := &activityExecutor{started: make(chan struct{}), release: make(chan struct{})}
	m := &localAgentManager{agents: map[string]*managedAgent{}, socket: "test", newExecutor: func() markdownExecutor { return executor }}
	defer m.close()
	r := m.handle(t.Context(), agentWorkerRequest{Operation: "start", Source: a.Path, Version: a.Revision})
	if r.Instance == nil {
		t.Fatal(r.Error)
	}
	req := agentWorkerRequest{AgentID: r.Instance.ID, Version: r.Instance.Version, Generation: r.Instance.Generation, Operation: "observe"}
	before := m.handle(t.Context(), req)
	if before.Activity != "idle" || len(before.Sessions) != 0 || !before.SessionsKnown {
		t.Fatalf("before=%+v", before)
	}
	done := make(chan agentWorkerReply, 1)
	go func() {
		send := req
		send.Operation = "send"
		send.Prompt = "hello"
		done <- m.handle(t.Context(), send)
	}()
	<-executor.started
	during := m.handle(t.Context(), req)
	if during.Activity != "responding" || len(during.Sessions) != 1 || during.Sessions[0].ProviderSession != "session-123456789" {
		t.Errorf("during=%+v", during)
	}
	close(executor.release)
	if reply := <-done; reply.Error != "" {
		t.Fatal(reply.Error)
	}
	after := m.handle(t.Context(), req)
	if after.Activity != "idle" || len(after.Sessions) != 1 || after.Sessions[0].ID != during.Sessions[0].ID {
		t.Fatalf("after=%+v", after)
	}
	rows, err := ReadAgentInstances("")
	if err != nil || rows[0].State != "running" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestBorrowedCopilotCloseDoesNotDeleteSharedDirectory(t *testing.T) {
	dir := t.TempDir()
	borrowed := &markdownCopilot{dir: dir, borrowed: true}
	borrowed.Close()
	// A new source can still be created under the shared directory after closing
	// one logical executor. The owning provider is responsible for cleanup.
	if err := InitMarkdownAgent(dir, "still-here"); err != nil {
		t.Fatal(err)
	}
}

func TestMultipleConversationsDoNotRestartAgent(t *testing.T) {
	a := mdFixture(t)
	var executors []*mdFakeExecutor
	m := &localAgentManager{agents: map[string]*managedAgent{}, socket: "shared", newExecutor: func() markdownExecutor {
		e := &mdFakeExecutor{answer: "reply"}
		executors = append(executors, e)
		return e
	}}
	defer m.close()
	start := m.handle(t.Context(), agentWorkerRequest{Operation: "start", Source: a.Path, Version: a.Revision})
	if start.Instance == nil {
		t.Fatal(start.Error)
	}
	r := *start.Instance
	call := func(op, session, label string) agentWorkerReply {
		return m.handle(t.Context(), agentWorkerRequest{AgentID: r.ID, Version: r.Version, Generation: r.Generation, Operation: op, Session: session, Label: label, Prompt: "hello"})
	}
	first := call("session-open", "", "")
	if first.Session == "" || first.Conversation.Label != "default" {
		t.Fatalf("%+v", first)
	}
	if again := call("session-open", "", ""); again.Session != first.Session {
		t.Fatal("default session not reused")
	}
	second := call("session-new", "", "research")
	if second.Session == first.Session || second.Session == "" {
		t.Fatal("new session not distinct")
	}
	if reply := call("send", first.Session, ""); reply.Error != "" {
		t.Fatal(reply.Error)
	}
	if executors[0].calls != 1 || executors[1].calls != 0 {
		t.Fatal("sessions shared conversation")
	}
	if selected := call("session-open", second.Session, ""); selected.Session != second.Session {
		t.Fatal("explicit session selection failed")
	}
	if reply := call("send", "s-foreign", ""); reply.Error == "" {
		t.Fatal("foreign session accepted")
	}
	observed := call("observe", "", "")
	if len(observed.Sessions) != 2 {
		t.Fatalf("%+v", observed)
	}
	rows, err := ReadAgentInstances("")
	if err != nil || len(rows) != 1 || rows[0].Generation != 1 || rows[0].Version != a.Revision {
		t.Fatal("conversation creation changed agent lifecycle")
	}
	if reply := call("stop", "", ""); reply.Error != "" {
		t.Fatal(reply.Error)
	}
	rows, err = ReadAgentInstances("")
	if err != nil || len(rows[0].Sessions) != 2 {
		t.Fatalf("closed session metadata missing: %+v %v", rows, err)
	}
	for _, session := range rows[0].Sessions {
		if session.State != "closed" {
			t.Fatal("session left open after stop")
		}
	}
}
