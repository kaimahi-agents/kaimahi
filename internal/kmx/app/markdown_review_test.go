package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMDReviewFullStringToolPattern(t *testing.T) {
	path, _ := cliFixture(t)
	b, err := os.ReadFile(MarkdownToolsPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(MarkdownToolsPath(path), []byte(strings.Replace(string(b), "^dg-[a-z0-9-]+$", "^dg-prod|staging$", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := ReadMarkdownAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"dg-prod-other-team", "x-staging"} {
		if _, err := cliInvocationArgs(a.CLITools[0], map[string]any{"cluster": value}); err == nil {
			t.Fatal("out-of-scope operand accepted", value)
		}
	}
	for _, value := range []string{"dg-prod", "staging"} {
		if _, err := cliInvocationArgs(a.CLITools[0], map[string]any{"cluster": value}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMDReviewParentAliasGrantAndToolRemoval(t *testing.T) {
	path, bin := cliFixture(t)
	fakeTool(t, bin, "fixture-cli", `printf 'fixture 2.4\n'`)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Dir(path), alias); err != nil {
		t.Fatal(err)
	}
	aliased := filepath.Join(alias, filepath.Base(path))
	plan, err := PrepareCLIToolApproval(aliased)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyCLIToolApproval(t.Context(), &plan); err != nil {
		t.Fatal(err)
	}
	a, err := ReadMarkdownAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := attachApprovedTools(a); err != nil || a.Grant == nil {
		t.Fatal("canonical source lost grant", err)
	}
	if g, _, err := readCurrentToolGrant(aliased); err != nil || g == nil {
		t.Fatal("alias lost grant", err)
	}
	if err := os.WriteFile(path, []byte(markdownTestAgent), 0600); err != nil {
		t.Fatal(err)
	}
	a, err = ReadMarkdownAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := attachApprovedTools(a); err != nil || a.Grant != nil {
		t.Fatal("stale grant blocked tool removal", err)
	}
	leaf := filepath.Join(filepath.Dir(path), "leaf.agent.md")
	if err := os.Symlink(path, leaf); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMarkdownAgent(leaf); err == nil {
		t.Fatal("symlink leaf accepted")
	}
}

func TestMDReviewStateParentAlias(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if err := mdPrivateDir(filepath.Join(alias, "private")); err != nil {
		t.Fatal(err)
	}
	if err := mdPrivateDir(alias); err == nil {
		t.Fatal("managed symlink leaf accepted")
	}
}

func TestMDReviewApprovedMockSubset(t *testing.T) {
	a, p, g := toolProposalFixture(t)
	g.Tools = []string{"github_pr_read"}
	if err := ApplyToolGrant(p, g); err != nil {
		t.Fatal(err)
	}
	base := MarkdownMock{Description: "fixture", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}, Calls: []MarkdownMockCall{{Arguments: map[string]any{}, Result: "ok"}}}
	read, list := base, base
	read.Name = "github_pr_read"
	list.Name = "github_pr_list"
	a.Mocks["sam"] = []MarkdownMock{read, list}
	s := NewMarkdownSession(io.Discard, io.Discard)
	s.Provider = "copilot"
	s.executor = &mdFakeExecutor{answer: "Hello"}
	result, err := s.Evaluate(t.Context(), a, "", nil)
	if err != nil || result.Status != "passed" {
		t.Fatalf("approved subset refused: %v %+v", err, result)
	}
	if len(a.Mocks["sam"]) != 2 {
		t.Fatal("evaluation mutated authored mocks")
	}
}

func TestMDReviewEscapedSocketEnvelope(t *testing.T) {
	dir, err := os.MkdirTemp("", "kmx-wire-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "rpc.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	want := strings.Repeat("<", 64<<10)
	done := make(chan error, 1)
	go func() {
		c, e := l.Accept()
		if e != nil {
			done <- e
			return
		}
		defer c.Close()
		var req agentWorkerRequest
		e = json.NewDecoder(io.LimitReader(c, agentWireLimit)).Decode(&req)
		if e == nil && req.Prompt != want {
			e = io.ErrUnexpectedEOF
		}
		if e == nil {
			e = json.NewEncoder(c).Encode(agentWorkerReply{Ready: true, Answer: want})
		}
		done <- e
	}()
	reply, err := agentSocketCall(t.Context(), socket, agentWorkerRequest{Operation: "send", Prompt: want})
	if err != nil || reply.Answer != want {
		t.Fatal("escaped answer lost", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMDReviewAttachedPollingKeepsFrozenExecution(t *testing.T) {
	a := mdFixture(t)
	lease, cached, err := beginAgentInstance(a, "")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.finish(nil)
	frozen, err := loadAgentVersion(lease.record)
	if err != nil {
		t.Fatal(err)
	}
	s := NewMarkdownSession(io.Discard, io.Discard)
	s.setExecutionState(&attachedAgentExecutor{record: lease.record}, "")
	m := markdownTUI{session: s, loaded: frozen, disk: frozen, attached: true, chatTimelineModel: newChatTimeline(a.Name, "local", "", false)}
	_, cmd := m.Update(mdPoll{})
	event := cmd().(mdDisk)
	if event.err != nil || len(event.agent.Examples) != 1 || event.agent.TestsPath == "" {
		t.Fatal("attachment lost paired cases", event.err)
	}
	if frozen.TestsPath != "" || frozen.Revision != cached.Revision {
		t.Fatal("poll changed frozen version")
	}
}

func TestMDReviewHeaderDoesNotRaceWorkerState(t *testing.T) {
	a := mdFixture(t)
	s := NewMarkdownSession(io.Discard, io.Discard)
	m := markdownTUI{session: s, loaded: a, disk: a, chatTimelineModel: newChatTimeline(a.Name, "local", "", false)}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 1000 {
			s.setExecutionState(&markdownCopilot{}, "")
			s.setExecutionState(&attachedAgentExecutor{}, "owned")
		}
	}()
	for range 1000 {
		m.refreshHeader()
	}
	wg.Wait()
}

func TestMDReviewProbeDoesNotHoldManagerLock(t *testing.T) {
	path, bin := cliFixture(t)
	started := filepath.Join(t.TempDir(), "started")
	release := filepath.Join(t.TempDir(), "release")
	t.Setenv("KMX_PROBE_STARTED", started)
	t.Setenv("KMX_PROBE_RELEASE", release)
	fakeTool(t, bin, "fixture-cli", `printf started > "$KMX_PROBE_STARTED"; while [ ! -f "$KMX_PROBE_RELEASE" ]; do :; done; printf 'fixture 2.4\n'`)
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareCLIToolApproval(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyCLIToolApproval(t.Context(), &plan); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(started)
	_ = os.Remove(release)
	a, err := ReadMarkdownAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := attachApprovedTools(a); err != nil {
		t.Fatal(err)
	}
	m := &localAgentManager{agents: map[string]*managedAgent{}, newExecutor: func() markdownExecutor { return &mdFakeExecutor{answer: "ok"} }}
	defer m.close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan agentWorkerReply, 1)
	go func() {
		done <- m.handle(ctx, agentWorkerRequest{Operation: "start", Source: a.Path, Version: a.Revision, BindingDigest: a.BindingDigest})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("probe not started")
		}
		time.Sleep(time.Millisecond)
	}
	ping := make(chan agentWorkerReply, 1)
	go func() { ping <- m.handle(ctx, agentWorkerRequest{Operation: "manager-ping"}) }()
	select {
	case r := <-ping:
		if !r.Ready {
			t.Fatal(r.Error)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("probe held manager lock")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.Error != "" {
		t.Fatal(r.Error)
	}
}
