package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type agentWorkerConfig struct {
	Record     AgentInstance `json:"record"`
	RecordPath string        `json:"recordPath"`
}
type agentWorkerRequest struct {
	BindingDigest string `json:"bindingDigest,omitempty"`
	NewVersion    string `json:"newVersion,omitempty"`
	Session       string `json:"session,omitempty"`
	Label         string `json:"label,omitempty"`
	AgentID       string `json:"agentID,omitempty"`
	Source        string `json:"source,omitempty"`
	Model         string `json:"model,omitempty"`
	Operation     string `json:"operation"`
	Prompt        string `json:"prompt,omitempty"`
	Version       string `json:"version"`
	Generation    int    `json:"generation"`
}
type agentWorkerReply struct {
	Sessions      []AgentSessionInfo `json:"sessions,omitempty"`
	SessionsKnown bool               `json:"sessionsKnown,omitempty"`
	Conversation  *AgentSessionInfo  `json:"conversation,omitempty"`
	Activity      string             `json:"activity,omitempty"`
	Session       string             `json:"session,omitempty"`
	Instance      *AgentInstance     `json:"instance,omitempty"`
	Socket        string             `json:"socket,omitempty"`
	Ready         bool               `json:"ready,omitempty"`
	Answer        string             `json:"answer,omitempty"`
	Error         string             `json:"error,omitempty"`
}

// RunAgentWorker is an internal child entry point, not a user-facing daemon setup.
func RunAgentWorker() error {
	leaseFile := os.NewFile(3, "agent-lease")
	ready := os.NewFile(4, "agent-ready")
	if leaseFile == nil || ready == nil {
		return errors.New("worker requires inherited descriptors")
	}
	defer ready.Close()
	sendReady := func(err error) {
		reply := agentWorkerReply{Ready: err == nil}
		if err != nil {
			reply.Error = err.Error()
		}
		_ = json.NewEncoder(ready).Encode(reply)
	}
	var cfg agentWorkerConfig
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 32<<10)).Decode(&cfg); err != nil {
		sendReady(errors.New("invalid worker configuration"))
		return err
	}
	root, err := agentStateRoot()
	if err != nil {
		sendReady(err)
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		sendReady(err)
		return err
	}
	expected := filepath.Join(root, "instances", cfg.Record.ID, "instance.json")
	recordPath, _ := filepath.Abs(cfg.RecordPath)
	if cfg.Record.ID != agentInstanceID(cfg.Record.Source) || recordPath != expected || !agentVersionDigest.MatchString(cfg.Record.Version) {
		err = errors.New("worker identity mismatch")
		sendReady(err)
		return err
	}
	info, err := leaseFile.Stat()
	if err != nil {
		sendReady(err)
		return err
	}
	expectedInfo, err := os.Lstat(filepath.Join(filepath.Dir(recordPath), "active.lock"))
	if err != nil || !os.SameFile(info, expectedInfo) {
		err = errors.New("worker lease identity mismatch")
		sendReady(err)
		return err
	}
	if err = mdLock(leaseFile); err != nil {
		sendReady(err)
		return err
	}
	lease := &agentLease{file: leaseFile, path: recordPath, record: cfg.Record}
	defer lease.finish(nil)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a, err := loadAgentVersion(cfg.Record)
	if err != nil {
		sendReady(err)
		return err
	}
	executor := &markdownCopilot{}
	defer executor.Close()
	if _, err = executor.Start(ctx); err != nil {
		sendReady(err)
		return err
	}
	// Keep Unix socket paths below platform limits regardless of KMX_HOME length.
	socketDir, err := os.MkdirTemp("", "kmx-agent-")
	if err != nil {
		sendReady(err)
		return err
	}
	defer os.RemoveAll(socketDir)
	socket := filepath.Join(socketDir, "rpc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		sendReady(err)
		return err
	}
	defer listener.Close()
	if err = os.Chmod(socket, 0600); err != nil {
		sendReady(err)
		return err
	}
	lease.record.PID = os.Getpid()
	lease.record.Socket = socket
	if err = lease.save(); err != nil {
		sendReady(err)
		return err
	}
	sendReady(nil)
	_ = ready.Close()
	go func() { <-ctx.Done(); _ = listener.Close() }()
	var jobs sync.WaitGroup
	defer func() { cancel(); jobs.Wait() }()
	// One shared conversation, one in-flight turn. Other clients get a bounded
	// busy response rather than accidentally interleaving their prompts.
	var turn sync.Mutex
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
			_ = conn.SetDeadline(time.Now().Add(6 * time.Minute))
			var request agentWorkerRequest
			if json.NewDecoder(io.LimitReader(conn, 128<<10)).Decode(&request) != nil {
				_ = json.NewEncoder(conn).Encode(agentWorkerReply{Error: "invalid agent request"})
				return
			}
			reply := agentWorkerReply{}
			if request.Version != lease.record.Version || request.Generation != lease.record.Generation {
				reply.Error = "agent version changed; reconnect"
			} else if request.Operation == "stop" {
				reply.Ready = true
				_ = json.NewEncoder(conn).Encode(reply)
				cancel()
				return
			} else if request.Operation == "ping" {
				reply.Ready = true
			} else if request.Operation == "send" {
				if strings.TrimSpace(request.Prompt) == "" || len(request.Prompt) > 64<<10 {
					reply.Error = "provide a nonempty prompt of at most 64 KiB"
				} else if !turn.TryLock() {
					reply.Error = "agent is busy with another turn"
				} else {
					answer, e := executor.Answer(ctx, a, request.Prompt, nil)
					turn.Unlock()
					if e != nil {
						reply.Error = e.Error()
					} else {
						reply.Answer = answer.Text
						reply.Ready = true
					}
				}
			} else {
				reply.Error = "unsupported agent operation"
			}
			_ = json.NewEncoder(conn).Encode(reply)
		}()
	}
}

func loadAgentVersion(r AgentInstance) (*MarkdownAgent, error) {
	if !agentVersionDigest.MatchString(r.Version) {
		return nil, errors.New("invalid cached version digest")
	}
	root, err := agentStateRoot()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, "versions", strings.TrimPrefix(r.Version, "sha256:"), "agent.md")
	raw, err := mdRead(path, markdownAgentLimit)
	if err != nil || mdHash(raw) != r.Version {
		return nil, errors.New("cached agent version unavailable or corrupt")
	}
	a, err := parseMarkdownAgent(r.Source, raw)
	if err != nil {
		return nil, err
	}
	a.Model = r.Model
	if err = loadToolSnapshot(a, r.BindingDigest); err != nil {
		return nil, err
	}
	a.PairRevision = r.Version
	return a, nil
}
func FindAgentInstance(selector string) (AgentInstance, error) {
	rows, err := ReadAgentInstances("")
	if err != nil {
		return AgentInstance{}, err
	}
	return resolveAgentInstance(rows, selector)
}
func agentWorkerCall(ctx context.Context, r AgentInstance, operation, prompt string) (agentWorkerReply, error) {
	if r.State != "running" || r.Socket == "" {
		return agentWorkerReply{}, errors.New("agent is not a running background instance; start it with kmx run FILE")
	}
	return agentSocketCall(ctx, r.Socket, agentWorkerRequest{AgentID: r.ID, Operation: operation, Prompt: prompt, Version: r.Version, Generation: r.Generation})
}
func agentSocketCall(ctx context.Context, socket string, request agentWorkerRequest) (agentWorkerReply, error) {
	info, err := os.Lstat(filepath.Dir(socket))
	if err != nil {
		return agentWorkerReply{}, errors.New("agent socket unavailable")
	}
	if !mdOwned(info, false) || !info.IsDir() || info.Mode().Perm() != 0700 {
		return agentWorkerReply{}, errors.New("unsafe agent socket directory")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return agentWorkerReply{}, errors.New("agent worker unavailable; check status")
	}
	defer conn.Close()
	deadline := time.Now().Add(6 * time.Minute)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err = json.NewEncoder(conn).Encode(request); err != nil {
		return agentWorkerReply{}, err
	}
	var reply agentWorkerReply
	if err = json.NewDecoder(io.LimitReader(conn, 128<<10)).Decode(&reply); err != nil {
		if ctx.Err() != nil {
			return reply, ctx.Err()
		}
		return reply, errors.New("agent response unavailable; the turn may still be running")
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}
func StopAgentInstance(ctx context.Context, selector string) error {
	r, err := FindAgentInstance(selector)
	if err != nil {
		return err
	}
	if r.State == "stopped" {
		return nil
	}
	if _, err = agentWorkerCall(ctx, r, "stop", ""); err != nil {
		return err
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		current, e := FindAgentInstance(r.ID)
		if e != nil {
			return e
		}
		if current.State == "stopped" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("stop requested; agent has not finished shutting down")
		case <-tick.C:
		}
	}
}

type attachedAgentExecutor struct {
	record         AgentInstance
	session, label string
}

func (e *attachedAgentExecutor) Start(ctx context.Context) (string, error) {
	_, err := agentWorkerCall(ctx, e.record, "ping", "")
	return "background-worker", err
}
func (e *attachedAgentExecutor) Answer(ctx context.Context, _ *MarkdownAgent, input string, _ []MarkdownMock) (markdownAnswer, error) {
	reply, err := agentSocketCall(ctx, e.record.Socket, agentWorkerRequest{AgentID: e.record.ID, Version: e.record.Version, Generation: e.record.Generation, Operation: "send", Prompt: input, Session: e.session})
	if err == nil && reply.Session != "" {
		e.session = reply.Session
	}
	return markdownAnswer{Text: reply.Answer}, err
}
func (e *attachedAgentExecutor) selectSession(ctx context.Context, selected string, fresh bool, label string) error {
	if e.record.Manager != localAgentManagerVersion {
		return errors.New("agent manager predates multiple sessions; stop and rerun the agent")
	}
	op := "session-open"
	if fresh {
		op = "session-new"
	}
	reply, err := agentSocketCall(ctx, e.record.Socket, agentWorkerRequest{AgentID: e.record.ID, Version: e.record.Version, Generation: e.record.Generation, Operation: op, Session: selected, Label: label})
	if err != nil {
		return err
	}
	if reply.Conversation == nil || reply.Session == "" {
		return errors.New("manager returned no conversation")
	}
	e.session, e.label = reply.Session, reply.Conversation.Label
	return nil
}
func (e *attachedAgentExecutor) Reset() {}
func (e *attachedAgentExecutor) Close() {}
func NewAttachedAgentSession(ctx context.Context, selector string, out, diagnostics io.Writer) (*MarkdownSession, *MarkdownAgent, error) {
	return NewSelectedAgentSession(ctx, selector, "", false, "", out, diagnostics)
}
func NewSelectedAgentSession(ctx context.Context, selector, session string, fresh bool, label string, out, diagnostics io.Writer) (*MarkdownSession, *MarkdownAgent, error) {
	r, err := FindAgentInstance(selector)
	if err != nil {
		return nil, nil, err
	}
	a, err := loadAgentVersion(r)
	if err != nil {
		return nil, nil, err
	}
	s := NewMarkdownSession(out, diagnostics)
	s.executor = &attachedAgentExecutor{record: r}
	s.Provider = "copilot"
	s.Model = r.Model
	if _, err = s.executor.Start(ctx); err != nil {
		return nil, nil, err
	}
	if r.Manager == localAgentManagerVersion || session != "" || fresh {
		if err := s.executor.(*attachedAgentExecutor).selectSession(ctx, session, fresh, label); err != nil {
			return nil, nil, err
		}
	}
	return s, a, nil
}
