package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const localAgentManagerVersion = "copilot-sessions/v4"

var ErrAgentVersionChanged = errors.New("agent is running a different version; use --update to update the agent definition")

type agentManagerRecord struct {
	Version string `json:"version"`
	PID     int    `json:"pid"`
	Socket  string `json:"socket"`
}
type managedAgent struct {
	lease          *agentLease
	definition     *MarkdownAgent
	ctx            context.Context
	cancel         context.CancelFunc
	sessions       map[string]*managedConversation
	defaultSession string
	stopping       bool
}
type AgentSessionInfo struct {
	ID              string `json:"id"`
	Label           string `json:"label"`
	State           string `json:"state"`
	Activity        string `json:"activity"`
	Version         string `json:"version"`
	ProviderSession string `json:"providerSession,omitempty"`
}
type managedConversation struct {
	id, label, version string
	executor           markdownExecutor
	turn               sync.Mutex
	responding         atomic.Bool
	failed             atomic.Bool
}

func (m *localAgentManager) newConversation(a *managedAgent, label string) (*managedConversation, error) {
	if len(a.sessions) >= 64 {
		return nil, errors.New("agent has 64 sessions; stop/restart to release them")
	}
	if label == "" {
		label = "conversation"
	}
	if len(label) > 64 || !markdownAgentName.MatchString(label) {
		return nil, errors.New("session label must be at most 64 printable characters")
	}
	idBytes := make([]byte, 12)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	s := &managedConversation{id: fmt.Sprintf("s-%x", idBytes), label: label, version: a.definition.Revision, executor: m.newExecutor()}
	a.sessions[s.id] = s
	return s, nil
}
func sessionInfo(s *managedConversation) AgentSessionInfo {
	activity := "idle"
	if s.responding.Load() {
		activity = "responding"
	} else if s.failed.Load() {
		activity = "error"
	}
	providerID := ""
	if observer, ok := s.executor.(interface{ SessionIdentity() string }); ok {
		providerID = observer.SessionIdentity()
	}
	return AgentSessionInfo{ID: s.id, Label: s.label, State: "open", Activity: activity, Version: s.version, ProviderSession: providerID}
}
func agentSessions(a *managedAgent) []AgentSessionInfo {
	rows := make([]AgentSessionInfo, 0, len(a.sessions))
	for _, s := range a.sessions {
		rows = append(rows, sessionInfo(s))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

type localAgentManager struct {
	mu          sync.Mutex
	agents      map[string]*managedAgent
	newExecutor func() markdownExecutor
	socket      string
	lastActive  time.Time
	closing     bool
}

func (s *MarkdownSession) StartBackgroundAgent(ctx context.Context, a *MarkdownAgent) (AgentInstance, error) {
	if err := attachApprovedTools(a); err != nil {
		return AgentInstance{}, err
	}
	if err := s.checkSelection(a); err != nil {
		return AgentInstance{}, err
	}
	if existing, err := FindAgentInstance(a.Path); err == nil && existing.State == "running" {
		if existing.Manager != localAgentManagerVersion {
			return AgentInstance{}, fmt.Errorf("%s uses an older manager protocol; stop it and run again to use scoped tool bindings", existing.Name)
		}
		if existing.Version != a.Revision || existing.Model != s.effective(a).Model || existing.BindingDigest != a.BindingDigest {
			return AgentInstance{}, fmt.Errorf("%s: %w", existing.Name, ErrAgentVersionChanged)
		}
		if _, err = agentWorkerCall(ctx, existing, "ping", ""); err != nil {
			return AgentInstance{}, err
		}
		return existing, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return AgentInstance{}, err
	}
	if _, err := CacheMarkdownVersion(a); err != nil {
		return AgentInstance{}, err
	}
	manager, err := ensureAgentManager(ctx)
	if err != nil {
		return AgentInstance{}, err
	}
	// Source identity is canonical and version is checked again by the manager.
	source, err := filepath.EvalSymlinks(a.Path)
	if err != nil {
		return AgentInstance{}, err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return AgentInstance{}, err
	}
	reply, err := agentManagerCall(ctx, manager.Socket, agentWorkerRequest{Operation: "start", Source: source, Version: a.Revision, Model: s.effective(a).Model, BindingDigest: a.BindingDigest})
	if err != nil {
		return AgentInstance{}, err
	}
	if reply.Instance == nil {
		return AgentInstance{}, errors.New("manager returned no instance")
	}
	return *reply.Instance, nil
}

func ensureAgentManager(ctx context.Context) (agentManagerRecord, error) {
	root, err := agentStateRoot()
	if err != nil {
		return agentManagerRecord{}, err
	}
	dir, err := filepath.Abs(filepath.Join(root, "manager"))
	if err != nil {
		return agentManagerRecord{}, err
	}
	if err = mdPrivateDir(dir); err != nil {
		return agentManagerRecord{}, err
	}
	lease, err := openAgentLease(filepath.Join(dir, "active.lock"), true)
	if err != nil {
		return agentManagerRecord{}, err
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		err = mdLock(lease)
		if err == nil {
			break
		}
		if !mdLockBusy(err) {
			lease.Close()
			return agentManagerRecord{}, err
		}
		var record agentManagerRecord
		if e := mdStateRead(filepath.Join(dir, "manager.json"), &record); e == nil && record.Version == localAgentManagerVersion {
			probe, cancel := context.WithTimeout(ctx, time.Second)
			_, e = agentManagerCall(probe, record.Socket, agentWorkerRequest{Operation: "manager-ping"})
			cancel()
			if e == nil {
				lease.Close()
				return record, nil
			}
		}
		select {
		case <-ctx.Done():
			lease.Close()
			return agentManagerRecord{}, ctx.Err()
		case <-deadline.C:
			lease.Close()
			return agentManagerRecord{}, errors.New("local agent manager is unavailable; existing agent state was preserved")
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer lease.Close()
	exe, err := os.Executable()
	if err != nil {
		return agentManagerRecord{}, err
	}
	read, write, err := os.Pipe()
	if err != nil {
		return agentManagerRecord{}, err
	}
	defer read.Close()
	defer write.Close()
	cmd := exec.Command(exe, "__agent-manager")
	if err = mdDetachProcess(cmd); err != nil {
		return agentManagerRecord{}, err
	}
	cmd.ExtraFiles = []*os.File{lease, write}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	// State paths passed as data, never shell interpolation; no credentials copied.
	input, err := cmd.StdinPipe()
	if err != nil {
		return agentManagerRecord{}, err
	}
	if err = cmd.Start(); err != nil {
		input.Close()
		return agentManagerRecord{}, err
	}
	write.Close()
	if err = json.NewEncoder(input).Encode(dir); err != nil {
		input.Close()
		cmd.Process.Kill()
		cmd.Wait()
		return agentManagerRecord{}, err
	}
	input.Close()
	replies := make(chan agentWorkerReply, 1)
	go func() {
		var r agentWorkerReply
		if json.NewDecoder(io.LimitReader(read, 8192)).Decode(&r) != nil {
			r.Error = "manager exited before readiness"
		}
		replies <- r
	}()
	var reply agentWorkerReply
	select {
	case reply = <-replies:
	case <-ctx.Done():
		reply.Error = "manager startup cancelled"
	case <-deadline.C:
		reply.Error = "manager startup timed out"
	}
	if !reply.Ready {
		cmd.Process.Kill()
		cmd.Wait()
		return agentManagerRecord{}, errors.New(reply.Error)
	}
	go func() { _ = cmd.Wait() }()
	var record agentManagerRecord
	if err = mdStateRead(filepath.Join(dir, "manager.json"), &record); err != nil {
		return record, err
	}
	return record, nil
}

func agentManagerCall(ctx context.Context, socket string, request agentWorkerRequest) (agentWorkerReply, error) {
	// Reuse the same socket validation and deadlines as per-agent attachment.
	return agentSocketCall(ctx, socket, request)
}

func RunAgentManager() error {
	lease := os.NewFile(3, "manager-lease")
	ready := os.NewFile(4, "manager-ready")
	defer lease.Close()
	defer ready.Close()
	notify := func(err error) {
		reply := agentWorkerReply{Ready: err == nil}
		if err != nil {
			reply.Error = err.Error()
		}
		_ = json.NewEncoder(ready).Encode(reply)
	}
	var dir string
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 8192)).Decode(&dir); err != nil {
		notify(err)
		return err
	}
	root, err := agentStateRoot()
	if err != nil {
		notify(err)
		return err
	}
	expected, err := filepath.Abs(filepath.Join(root, "manager"))
	if err != nil || dir != expected {
		err = errors.New("manager state identity mismatch")
		notify(err)
		return err
	}
	info, err := lease.Stat()
	if err != nil {
		notify(err)
		return err
	}
	disk, err := os.Lstat(filepath.Join(dir, "active.lock"))
	if err != nil || !os.SameFile(info, disk) {
		err = errors.New("manager lease mismatch")
		notify(err)
		return err
	}
	if err = mdLock(lease); err != nil {
		notify(err)
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	provider := &markdownCopilot{}
	defer provider.Close()
	if _, err = provider.Start(ctx); err != nil {
		notify(err)
		return err
	}
	socketDir, err := os.MkdirTemp("", "kmx-manager-")
	if err != nil {
		notify(err)
		return err
	}
	defer os.RemoveAll(socketDir)
	socket := filepath.Join(socketDir, "rpc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		notify(err)
		return err
	}
	defer listener.Close()
	if err = os.Chmod(socket, 0600); err != nil {
		notify(err)
		return err
	}
	manager := &localAgentManager{agents: map[string]*managedAgent{}, socket: socket, lastActive: time.Now(), newExecutor: func() markdownExecutor {
		return &markdownCopilot{client: provider.client, dir: provider.dir, version: provider.version, borrowed: true}
	}}
	defer manager.close()
	record := agentManagerRecord{Version: localAgentManagerVersion, PID: os.Getpid(), Socket: socket}
	if err = mdStateUpdate(filepath.Join(dir, "manager.json"), func() ([]byte, error) { return json.Marshal(record) }); err != nil {
		notify(err)
		return err
	}
	notify(nil)
	ready.Close()
	go func() { <-ctx.Done(); listener.Close() }()
	// Release the shared provider when the final agent has stopped for 30 seconds.
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				manager.mu.Lock()
				idle := len(manager.agents) == 0 && time.Since(manager.lastActive) > 30*time.Second
				if idle {
					manager.closing = true
				}
				manager.mu.Unlock()
				if idle {
					cancel()
					return
				}
			}
		}
	}()
	var jobs sync.WaitGroup
	defer func() { cancel(); jobs.Wait() }()
	for {
		conn, e := listener.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var req agentWorkerRequest
			if json.NewDecoder(io.LimitReader(conn, 128<<10)).Decode(&req) != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(6 * time.Minute))
			reply := manager.handle(ctx, req)
			_ = json.NewEncoder(conn).Encode(reply)
		}()
	}
}

func (m *localAgentManager) handle(ctx context.Context, req agentWorkerRequest) agentWorkerReply {
	fail := func(err error) agentWorkerReply { return agentWorkerReply{Error: err.Error()} }
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return fail(errors.New("manager is stopping; run again"))
	}
	m.lastActive = time.Now()
	if req.Operation == "manager-ping" {
		m.mu.Unlock()
		return agentWorkerReply{Ready: true}
	}
	if req.Operation == "start" {
		defer m.mu.Unlock()
		if !agentVersionDigest.MatchString(req.Version) || !filepath.IsAbs(req.Source) || (req.Model != "" && !localModelName.MatchString(req.Model)) {
			return fail(errors.New("invalid agent start request"))
		}
		id := agentInstanceID(req.Source)
		if active := m.agents[id]; active != nil {
			r := active.lease.record
			if r.Version != req.Version || r.Model != req.Model || r.BindingDigest != req.BindingDigest {
				return fail(errors.New("agent is running a different version; use --update to update the agent definition"))
			}
			return agentWorkerReply{Ready: true, Instance: &r}
		}
		a, err := LoadMarkdownAgent(req.Source, "")
		if err != nil {
			return fail(err)
		}
		if a.Revision != req.Version {
			return fail(errors.New("source changed before manager start; run again"))
		}
		if err = attachApprovedTools(a); err != nil {
			return fail(err)
		}
		if a.BindingDigest != req.BindingDigest {
			return fail(errors.New("tool binding changed before start; run again"))
		}
		if err = validateActivatedTools(ctx, a); err != nil {
			return fail(err)
		}
		lease, frozen, err := beginAgentInstance(a, req.Model)
		if err != nil {
			return fail(err)
		}
		agentCtx, cancel := context.WithCancel(ctx)
		lease.record.Socket = m.socket
		lease.record.Manager = localAgentManagerVersion
		if err = lease.save(); err != nil {
			cancel()
			lease.finish(err)
			return fail(err)
		}
		m.agents[id] = &managedAgent{lease: lease, definition: frozen, ctx: agentCtx, cancel: cancel, sessions: map[string]*managedConversation{}}
		r := lease.record
		return agentWorkerReply{Ready: true, Instance: &r}
	}
	agent := m.agents[req.AgentID]
	if agent == nil {
		m.mu.Unlock()
		return fail(errors.New("agent session is stopped or unknown"))
	}
	r := agent.lease.record
	if agent.stopping {
		m.mu.Unlock()
		return fail(errors.New("agent is stopping"))
	}
	if req.Version != r.Version || req.Generation != r.Generation {
		m.mu.Unlock()
		return fail(errors.New("agent version changed; reconnect"))
	}
	if req.Operation == "observe" {
		activity := "idle"
		sessions := agentSessions(agent)
		for _, s := range sessions {
			if s.Activity == "responding" {
				activity = "responding"
				break
			}
			if s.Activity == "error" {
				activity = "error"
			}
		}
		m.mu.Unlock()
		return agentWorkerReply{Ready: true, Activity: activity, Sessions: sessions, SessionsKnown: true}
	}
	if req.Operation == "stop" {
		agent.stopping = true
		agent.cancel()
		m.mu.Unlock()
		for _, s := range agent.sessions {
			s.turn.Lock()
			s.executor.Close()
			s.turn.Unlock()
		}
		m.mu.Lock()
		agent.lease.record.Sessions = agentSessions(agent)
		for i := range agent.lease.record.Sessions {
			agent.lease.record.Sessions[i].State = "closed"
			agent.lease.record.Sessions[i].Activity = ""
		}
		err := agent.lease.finish(nil)
		delete(m.agents, req.AgentID)
		m.lastActive = time.Now()
		m.mu.Unlock()
		if err != nil {
			return fail(err)
		}
		return agentWorkerReply{Ready: true}
	}
	if req.Operation == "update" {
		// Complete source/package validation precedes cancelling any old session.
		if !agentVersionDigest.MatchString(req.NewVersion) || (req.Model != "" && !localModelName.MatchString(req.Model)) {
			m.mu.Unlock()
			return fail(errors.New("invalid new version/model"))
		}
		a, err := LoadMarkdownAgent(r.Source, req.Source)
		if err != nil {
			m.mu.Unlock()
			return fail(err)
		}
		if a.Revision != req.NewVersion {
			m.mu.Unlock()
			return fail(errors.New("source changed or tools unsupported; running agent unchanged"))
		}
		if err = attachApprovedTools(a); err != nil {
			m.mu.Unlock()
			return fail(err)
		}
		if a.BindingDigest != req.BindingDigest {
			m.mu.Unlock()
			return fail(errors.New("tool bindings changed after review; running agent unchanged"))
		}
		if err = validateActivatedTools(ctx, a); err != nil {
			m.mu.Unlock()
			return fail(err)
		}
		frozen, err := CacheMarkdownVersion(a)
		if err != nil {
			m.mu.Unlock()
			return fail(err)
		}
		frozen.Model = req.Model
		if r.Version == a.Revision && r.Model == req.Model && r.BindingDigest == a.BindingDigest {
			m.mu.Unlock()
			return agentWorkerReply{Ready: true, Instance: &r}
		}
		agent.stopping = true
		agent.cancel()
		m.mu.Unlock()
		for _, s := range agent.sessions {
			s.turn.Lock()
			s.executor.Close()
			s.turn.Unlock()
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		next := r
		next.PreviousVersion = r.Version
		next.Version = a.Revision
		next.Name = a.Name
		next.Model = req.Model
		next.BindingDigest = a.BindingDigest
		next.Tools = nil
		if a.Grant != nil {
			next.Tools = append([]string{}, a.Grant.Tools...)
		}
		next.Generation++
		next.Started = time.Now().UTC()
		next.Finished = nil
		next.Reason = ""
		next.Sessions = nil
		next.SessionCount = nil
		next.Session = ""
		next.Activity = ""
		agent.lease.record = next
		if err = agent.lease.save(); err != nil {
			_ = agent.lease.finish(err)
			delete(m.agents, req.AgentID)
			return fail(errors.New("old sessions closed but saving replacement failed; inspect agent state"))
		}
		newCtx, cancel := context.WithCancel(ctx)
		m.agents[req.AgentID] = &managedAgent{lease: agent.lease, definition: frozen, ctx: newCtx, cancel: cancel, sessions: map[string]*managedConversation{}}
		return agentWorkerReply{Ready: true, Instance: &next}
	}
	if req.Operation == "ping" {
		m.mu.Unlock()
		return agentWorkerReply{Ready: true}
	}
	if req.Operation == "session-open" || req.Operation == "session-new" {
		defer m.mu.Unlock()
		var s *managedConversation
		if req.Operation == "session-new" {
			var err error
			s, err = m.newConversation(agent, req.Label)
			if err != nil {
				return fail(err)
			}
		} else if req.Session != "" {
			s = agent.sessions[req.Session]
			if s == nil {
				return fail(errors.New("session not found on this agent; inspect the agent for session IDs"))
			}
		} else {
			s = agent.sessions[agent.defaultSession]
			if s == nil {
				var err error
				s, err = m.newConversation(agent, "default")
				if err != nil {
					return fail(err)
				}
				agent.defaultSession = s.id
			}
		}
		info := sessionInfo(s)
		return agentWorkerReply{Ready: true, Session: s.id, Conversation: &info}
	}
	if req.Operation != "send" {
		m.mu.Unlock()
		return fail(errors.New("unsupported manager operation"))
	}
	if len(req.Prompt) > 64<<10 || req.Prompt == "" {
		m.mu.Unlock()
		return fail(errors.New("prompt must contain 1-65536 bytes"))
	}
	s := agent.sessions[req.Session]
	if req.Session == "" {
		s = agent.sessions[agent.defaultSession]
		if s == nil {
			var err error
			s, err = m.newConversation(agent, "default")
			if err != nil {
				m.mu.Unlock()
				return fail(err)
			}
			agent.defaultSession = s.id
		}
	}
	if s == nil {
		m.mu.Unlock()
		return fail(errors.New("session not found on this agent"))
	}
	if !s.turn.TryLock() {
		m.mu.Unlock()
		return fail(errors.New("session is busy with another turn"))
	}
	if s.failed.Load() {
		s.turn.Unlock()
		m.mu.Unlock()
		return fail(errors.New("session failed; start a new conversation with /reset or chat --new"))
	}
	s.responding.Store(true)
	m.mu.Unlock()
	defer func() { s.responding.Store(false); s.turn.Unlock() }()
	answer, err := s.executor.Answer(agent.ctx, agent.definition, req.Prompt, nil)
	if err != nil {
		s.failed.Store(true)
		return fail(err)
	}
	return agentWorkerReply{Ready: true, Answer: answer.Text, Session: s.id}
}
func (m *localAgentManager) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closing = true
	for id, a := range m.agents {
		a.cancel()
		for _, s := range a.sessions {
			s.turn.Lock()
			s.executor.Close()
			s.turn.Unlock()
		}
		a.lease.record.Sessions = agentSessions(a)
		for i := range a.lease.record.Sessions {
			a.lease.record.Sessions[i].State = "closed"
			a.lease.record.Sessions[i].Activity = ""
		}
		_ = a.lease.finish(nil)
		delete(m.agents, id)
	}
}
