//go:build !windows

package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
)

const (
	localAgentWorkerEnv   = "KMX_LOCAL_AGENT_WORKER"
	localAgentIPCLimit    = 1 << 20
	localAgentMaxSessions = 64
)

type localAgentIPCRequest struct {
	Op           string `json:"op"`
	EventID      string `json:"eventId"`
	Label        string `json:"label,omitempty"`
	SessionID    string `json:"sessionId,omitempty"`
	Input        string `json:"input,omitempty"`
	InvocationID string `json:"invocationId,omitempty"`
	Fresh        bool   `json:"fresh,omitempty"`
}

type localAgentIPCResponse struct {
	OK          bool                  `json:"ok"`
	Phase       string                `json:"phase,omitempty"`
	Error       string                `json:"error,omitempty"`
	EventID     string                `json:"eventId,omitempty"`
	Observation LocalAgentObservation `json:"observation,omitempty"`
	Session     LocalAgentSession     `json:"session,omitempty"`
	Answer      string                `json:"answer,omitempty"`
}

type residentSession struct {
	mu      sync.Mutex
	turn    sync.Mutex
	meta    LocalAgentSession
	history []localpreview.ConversationMessage
}

type residentWorker struct {
	record                LocalAgentInstance
	instructions          string
	listener              net.Listener
	started               time.Time
	mu                    sync.Mutex
	sessions              map[string]*residentSession
	lastActivity          *time.Time
	lastError             string
	closing               bool
	defaultSession        string
	invocations           map[string]context.CancelFunc
	explicitCredential    string
	explicitCredentialSet bool
}

type localAgentCredentialSnapshot struct {
	Set   bool   `json:"set"`
	Token string `json:"token"`
}

func init() {
	if os.Getenv(localAgentWorkerEnv) != "1" {
		return
	}
	credential := localAgentCredentialSnapshot{}
	if file := os.NewFile(4, "credential"); file != nil {
		raw, readErr := io.ReadAll(io.LimitReader(file, 128<<10+1))
		_ = file.Close()
		if readErr != nil || len(raw) > 128<<10 || json.Unmarshal(raw, &credential) != nil {
			os.Exit(1)
		}
	} else {
		os.Exit(1)
	}
	err := runLocalAgentWorker(context.Background(), os.Getenv("KMX_LOCAL_AGENT_ID"), os.Getenv("KMX_LOCAL_AGENT_EVENT"), credential, os.NewFile(3, "readiness"))
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func validLocalAgentSocketName(name string) bool {
	return strings.HasPrefix(name, "worker-") && strings.HasSuffix(name, ".sock") && validHexValue(strings.TrimSuffix(strings.TrimPrefix(name, "worker-"), ".sock"), 24)
}

func validLocalAgentLockName(name string) bool {
	return strings.HasPrefix(name, "active-") && strings.HasSuffix(name, ".lock") && validHexValue(strings.TrimSuffix(strings.TrimPrefix(name, "active-"), ".lock"), 24)
}

func localAgentSocket(record LocalAgentInstance) (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	managedBase := false
	if base == "" {
		base = filepath.Join(os.TempDir(), "kmx-runtime-"+strconv.Itoa(os.Getuid()))
		managedBase = true
	}
	runtimeDir := filepath.Join(base, "kmx")
	if len(filepath.Join(runtimeDir, record.InstanceID+".sock")) > 103 {
		shortRoot := "/tmp"
		if runtime.GOOS == "darwin" {
			shortRoot = "/private/tmp"
		}
		base = filepath.Join(shortRoot, "kmx-runtime-"+strconv.Itoa(os.Getuid()))
		runtimeDir = filepath.Join(base, "kmx")
		managedBase = true
	}
	if managedBase {
		if err := ensureLocalAgentRuntimeDirectory(base); err != nil {
			return "", err
		}
	}
	dir, err := fileaccess.EnsurePrivateDirectory(context.Background(), runtimeDir)
	if err != nil {
		return "", err
	}
	if err := fileaccess.CheckPrivate(context.Background(), dir, true); err != nil {
		_ = dir.Close()
		return "", err
	}
	if err := dir.Close(); err != nil {
		return "", err
	}
	return filepath.Join(runtimeDir, record.InstanceID+".sock"), nil
}

func ensureLocalAgentRuntimeDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("local agent runtime directory is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return errors.New("local agent runtime directory is unsafe")
	}
	return nil
}

func startLocalAgentWorker(ctx context.Context, executable string, record LocalAgentInstance) error {
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return errors.New("local agent executable is unavailable")
		}
	}
	readiness, child, err := os.Pipe()
	if err != nil {
		return errors.New("local agent readiness pipe is unavailable")
	}
	defer readiness.Close()
	cmd := exec.Command(executable)
	cmd.Env = localAgentWorkerEnvironment(record)
	credentialRead, credentialWrite, err := os.Pipe()
	if err != nil {
		child.Close()
		return errors.New("local agent credential pipe is unavailable")
	}
	defer credentialRead.Close()
	cmd.ExtraFiles = []*os.File{child, credentialRead}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		child.Close()
		credentialWrite.Close()
		return errors.New("local agent worker could not start")
	}
	if err := ctx.Err(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = child.Close()
		_ = credentialWrite.Close()
		return err
	}
	_ = child.Close()
	token, set := "", false
	if record.Provider == "copilot" {
		token, set = os.LookupEnv("COPILOT_GITHUB_TOKEN")
	}
	raw, marshalErr := json.Marshal(localAgentCredentialSnapshot{Set: set, Token: token})
	_, writeErr := credentialWrite.Write(append(raw, '\n'))
	closeErr := credentialWrite.Close()
	if marshalErr != nil || writeErr != nil || closeErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("local agent credential handoff failed")
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	_ = readiness.SetReadDeadline(time.Now().Add(10 * time.Second))
	var response localAgentIPCResponse
	readyCh := make(chan error, 1)
	go func() { readyCh <- decodeLocalAgentJSON(readiness, &response) }()
	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-waited
		return ctx.Err()
	case err := <-readyCh:
		if err == nil && response.OK && response.EventID == record.EventID {
			if ctx.Err() != nil {
				_ = cmd.Process.Kill()
				<-waited
				return ctx.Err()
			}
			return nil
		}
		_ = cmd.Process.Kill()
		if response.Error != "" {
			return errors.New(response.Error)
		}
		return errors.New("local agent worker did not become ready")
	}
}

func runLocalAgentWorker(ctx context.Context, id, event string, credential localAgentCredentialSnapshot, readiness *os.File) error {
	ready := func(response localAgentIPCResponse) {
		_ = writeLocalAgentJSON(readiness, response)
		_ = readiness.Close()
	}
	record, err := resolveLocalAgent("li-"+event, true)
	if err != nil || record.EventID != event || record.State == "stopped" {
		ready(localAgentIPCResponse{Error: "worker state is unavailable"})
		return errors.New("worker state is unavailable")
	}
	dir, _, err := localAgentInstanceDirectory(ctx, id, false)
	if err != nil {
		ready(localAgentIPCResponse{Error: "worker state is unavailable"})
		return err
	}
	defer dir.Close()
	lock, err := fileaccess.EnsurePrivateFileAt(ctx, dir, "active-"+event+".lock")
	if err != nil {
		ready(localAgentIPCResponse{Error: "worker lock is unavailable"})
		return err
	}
	defer lock.Close()
	lockDeadline := time.Now().Add(2 * time.Second)
	for {
		if err := fileaccess.TryLock(ctx, lock); err == nil {
			break
		} else if !errors.Is(err, fileaccess.ErrUnsafe) || time.Now().After(lockDeadline) {
			ready(localAgentIPCResponse{Error: "worker is already active"})
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	pair, err := LoadLocalAgentPair(ctx, record)
	if err != nil || len(pair.Agent.Tools) != 0 {
		ready(localAgentIPCResponse{Error: "cached agent is unavailable"})
		return errors.New("cached agent is unavailable")
	}
	if err := checkLocalAgentRoute(ctx, record, credential); err != nil {
		ready(localAgentIPCResponse{Error: "model route is unavailable"})
		return err
	}
	socket, err := localAgentSocket(record)
	if err != nil || len(socket) > 103 {
		ready(localAgentIPCResponse{Error: "worker socket is unavailable"})
		return errors.New("worker socket is unavailable")
	}
	// The process-held lock proves no other worker owns this instance. A killed
	// worker can leave only its socket inode behind, which is safe to remove now.
	if err := removeLocalAgentSocket(record); err != nil {
		ready(localAgentIPCResponse{Error: "worker socket is unavailable"})
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		ready(localAgentIPCResponse{Error: "worker socket is unavailable"})
		return err
	}
	defer func() { _ = listener.Close(); _ = os.Remove(socket) }()
	if err := os.Chmod(socket, 0600); err != nil {
		ready(localAgentIPCResponse{Error: "worker socket is unavailable"})
		return err
	}
	w := &residentWorker{record: record, instructions: pair.Agent.Instructions, listener: listener, started: time.Now().UTC(), sessions: map[string]*residentSession{}, invocations: map[string]context.CancelFunc{}, explicitCredential: credential.Token, explicitCredentialSet: credential.Set}
	ready(localAgentIPCResponse{OK: true, EventID: event})
	for {
		conn, err := listener.Accept()
		if err != nil {
			w.mu.Lock()
			closing := w.closing
			w.mu.Unlock()
			if closing {
				return nil
			}
			return err
		}
		go w.serve(conn)
	}
}

func localAgentWorkerEnvironment(record LocalAgentInstance) []string {
	allowed := map[string]bool{"HOME": true, "PATH": true, "XDG_CONFIG_HOME": true, "XDG_RUNTIME_DIR": true, "TMPDIR": true, "KMX_HOME": true, "GH_CONFIG_DIR": true, "LANG": true, "LC_ALL": true}
	env := []string{}
	for _, value := range os.Environ() {
		name, _, ok := strings.Cut(value, "=")
		if ok && allowed[name] {
			env = append(env, value)
		}
	}
	return append(env, localAgentWorkerEnv+"=1", "KMX_LOCAL_AGENT_ID="+record.ID, "KMX_LOCAL_AGENT_EVENT="+record.EventID)
}

func checkLocalAgentRoute(ctx context.Context, record LocalAgentInstance, credential localAgentCredentialSnapshot) error {
	if record.Provider == "fake" {
		return nil
	}
	route := modelaccess.Route{Provider: "copilot", Model: record.Model, Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
	provider := copilot.New(nil)
	if credential.Set {
		provider = copilot.NewExplicitCredential(credential.Token)
	}
	_, err := provider.PreviewClient(ctx, []modelaccess.Route{route})
	return err
}

func (w *residentWorker) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(9 * time.Minute))
	var request localAgentIPCRequest
	reader := bufio.NewReader(io.LimitReader(conn, localAgentIPCLimit+1))
	line, readErr := reader.ReadBytes('\n')
	if readErr != nil || len(line) > localAgentIPCLimit || decodeLocalAgentJSON(bytes.NewReader(line), &request) != nil || request.EventID != w.record.EventID {
		_ = writeLocalAgentJSON(conn, localAgentIPCResponse{Error: "invalid request"})
		return
	}
	if request.Op == "invoke" {
		w.serveInvoke(conn, request)
		return
	}
	requestCtx := context.Background()
	response := w.handle(requestCtx, request)
	_ = writeLocalAgentJSON(conn, response)
	if request.Op == "shutdown" && response.OK {
		w.mu.Lock()
		w.closing = true
		w.mu.Unlock()
		_ = w.listener.Close()
	}
}

func (w *residentWorker) handle(ctx context.Context, request localAgentIPCRequest) localAgentIPCResponse {
	switch request.Op {
	case "ping":
		return localAgentIPCResponse{OK: true, EventID: w.record.EventID}
	case "observe", "session-list":
		return localAgentIPCResponse{OK: true, EventID: w.record.EventID, Observation: w.observe()}
	case "session-open":
		if len(request.Label) > 256 {
			return localAgentIPCResponse{Error: "invalid session label"}
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if !request.Fresh && w.defaultSession != "" {
			if existing := w.sessions[w.defaultSession]; existing != nil {
				existing.mu.Lock()
				meta := existing.meta
				existing.mu.Unlock()
				return localAgentIPCResponse{OK: true, EventID: w.record.EventID, Session: meta}
			}
		}
		if len(w.sessions) >= localAgentMaxSessions {
			return localAgentIPCResponse{Error: "session limit reached"}
		}
		id, err := randomLocalSessionID()
		if err != nil {
			return localAgentIPCResponse{Error: "session could not be created"}
		}
		now := time.Now().UTC()
		s := &residentSession{meta: LocalAgentSession{ID: id, Label: request.Label, Created: now, LastActivity: now}}
		w.sessions[id] = s
		if !request.Fresh {
			w.defaultSession = id
		}
		return localAgentIPCResponse{OK: true, EventID: w.record.EventID, Session: s.meta}
	case "session-close":
		w.mu.Lock()
		s := w.sessions[request.SessionID]
		if s == nil {
			w.mu.Unlock()
			return localAgentIPCResponse{Error: "session not found"}
		}
		if w.defaultSession == request.SessionID {
			w.defaultSession = ""
		}
		delete(w.sessions, request.SessionID)
		w.mu.Unlock()
		s.turn.Lock()
		s.turn.Unlock()
		return localAgentIPCResponse{OK: true, EventID: w.record.EventID}
	case "shutdown":
		w.mu.Lock()
		for _, cancel := range w.invocations {
			cancel()
		}
		w.mu.Unlock()
		return localAgentIPCResponse{OK: true, EventID: w.record.EventID}
	default:
		return localAgentIPCResponse{Error: "unsupported operation"}
	}
}

func (w *residentWorker) serveInvoke(conn net.Conn, request localAgentIPCRequest) {
	if request.Input == "" || len(request.Input) > agentfile.MaxInputBytes || !utf8.ValidString(request.Input) || !validHexValue(request.InvocationID, 24) {
		_ = writeLocalAgentJSON(conn, localAgentIPCResponse{Error: "invalid invocation input"})
		return
	}
	w.mu.Lock()
	s := w.sessions[request.SessionID]
	if s == nil {
		w.mu.Unlock()
		_ = writeLocalAgentJSON(conn, localAgentIPCResponse{Error: "session not found"})
		return
	}
	if _, exists := w.invocations[request.InvocationID]; exists {
		w.mu.Unlock()
		_ = writeLocalAgentJSON(conn, localAgentIPCResponse{Error: "invocation already exists"})
		return
	}
	turnCtx, cancel := context.WithCancel(context.Background())
	w.invocations[request.InvocationID] = cancel
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.invocations, request.InvocationID); w.mu.Unlock(); cancel() }()
	reader := bufio.NewReader(io.LimitReader(conn, 4097))
	ackCh := make(chan []byte, 1)
	go func() {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			cancel()
			ackCh <- nil
			return
		}
		ackCh <- line
	}()
	answer, commit, abort, err := w.prepareInvoke(turnCtx, s, request.Input)
	if err != nil {
		_ = writeLocalAgentJSON(conn, localAgentIPCResponse{Error: "invocation failed"})
		return
	}
	committed := false
	defer func() {
		if !committed {
			abort()
		}
	}()
	if err := writeLocalAgentJSON(conn, localAgentIPCResponse{OK: true, Phase: "prepared", EventID: w.record.EventID, Answer: answer}); err != nil {
		return
	}
	line := <-ackCh
	var ack struct {
		InvocationID string `json:"invocationId"`
		Commit       bool   `json:"commit"`
	}
	if line == nil || json.Unmarshal(line, &ack) != nil || ack.InvocationID != request.InvocationID || !ack.Commit {
		return
	}
	commit()
	committed = true
	_ = writeLocalAgentJSON(conn, localAgentIPCResponse{OK: true, Phase: "committed", EventID: w.record.EventID})
}

func (w *residentWorker) prepareInvoke(ctx context.Context, s *residentSession, input string) (string, func(), func(), error) {
	s.turn.Lock()
	release := true
	defer func() {
		if release {
			s.turn.Unlock()
		}
	}()
	now := time.Now().UTC()
	s.mu.Lock()
	s.meta.Active, s.meta.LastActivity = true, now
	history := append([]localpreview.ConversationMessage(nil), s.history...)
	s.mu.Unlock()
	var transport http.RoundTripper
	var check func(...string) error
	base := ""
	if w.record.Provider == "copilot" {
		route := modelaccess.Route{Provider: "copilot", Model: w.record.Model, Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
		provider := copilot.New(nil)
		if w.explicitCredentialSet {
			provider = copilot.NewExplicitCredential(w.explicitCredential)
		}
		access, err := provider.PreviewClient(ctx, []modelaccess.Route{route})
		if err != nil {
			s.mu.Lock()
			s.meta.Active = false
			s.meta.LastError = "invocation failed"
			s.mu.Unlock()
			return "", nil, nil, err
		}
		client, err := access.Client(route)
		if err != nil {
			s.mu.Lock()
			s.meta.Active = false
			s.meta.LastError = "invocation failed"
			s.mu.Unlock()
			return "", nil, nil, err
		}
		transport, check, base = client, access.CheckContent, modelaccess.CopilotEndpoint
	}
	answer, err := localpreview.SendConversation(ctx, w.record.Provider, w.record.Model, base, transport, w.instructions, history, input, check)
	now = time.Now().UTC()
	s.mu.Lock()
	s.meta.Active, s.meta.LastActivity = false, now
	s.mu.Unlock()
	w.mu.Lock()
	w.lastActivity = &now
	if err != nil || ctx.Err() != nil {
		s.mu.Lock()
		s.meta.LastError = "invocation failed"
		s.mu.Unlock()
		w.lastError = "invocation failed"
		w.mu.Unlock()
		if ctx.Err() != nil {
			return "", nil, nil, ctx.Err()
		}
		return "", nil, nil, err
	}
	w.mu.Unlock()
	release = false
	var finish sync.Once
	commit := func() {
		finish.Do(func() {
			now := time.Now().UTC()
			w.mu.Lock()
			s.mu.Lock()
			s.meta.LastError, w.lastError = "", ""
			w.lastActivity = &now
			s.history = append(s.history, localpreview.ConversationMessage{Role: "user", Content: input}, localpreview.ConversationMessage{Role: "assistant", Content: answer})
			s.meta.Messages = len(s.history)
			s.meta.LastActivity = now
			s.mu.Unlock()
			w.mu.Unlock()
			s.turn.Unlock()
		})
	}
	abort := func() { finish.Do(func() { s.mu.Lock(); s.meta.Active = false; s.mu.Unlock(); s.turn.Unlock() }) }
	return answer, commit, abort, nil
}

func (w *residentWorker) observe() LocalAgentObservation {
	w.mu.Lock()
	sessions := make([]*residentSession, 0, len(w.sessions))
	for _, session := range w.sessions {
		sessions = append(sessions, session)
	}
	activity, lastError := w.lastActivity, w.lastError
	w.mu.Unlock()
	result := LocalAgentObservation{Ready: true, Started: w.started, LastActivity: activity, LastError: lastError}
	for _, session := range sessions {
		session.mu.Lock()
		result.Sessions = append(result.Sessions, session.meta)
		session.mu.Unlock()
	}
	sort.Slice(result.Sessions, func(i, j int) bool { return result.Sessions[i].Created.Before(result.Sessions[j].Created) })
	return result
}

func randomLocalSessionID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "session-" + hex.EncodeToString(raw), nil
}

func decodeLocalAgentJSON(reader io.Reader, target any) error {
	limited := &io.LimitedReader{R: bufio.NewReader(reader), N: localAgentIPCLimit + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if limited.N == 0 {
		return errors.New("message too large")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return errors.New("trailing message")
	}
	return nil
}

func writeLocalAgentJSON(writer io.Writer, value localAgentIPCResponse) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw)+1 > localAgentIPCLimit {
		raw, _ = json.Marshal(localAgentIPCResponse{Error: "response exceeds limit"})
	}
	raw = append(raw, '\n')
	_, err = writer.Write(raw)
	return err
}

func readLocalAgentFrame(reader *bufio.Reader, target any) error {
	line, err := reader.ReadBytes('\n')
	if err != nil || len(line) > localAgentIPCLimit {
		return errors.New("invalid local agent frame")
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return errors.New("invalid local agent frame")
	}
	return nil
}

func probeLocalAgent(ctx context.Context, record LocalAgentInstance, op string, request localAgentIPCRequest) (localAgentIPCResponse, error) {
	socket, err := localAgentSocket(record)
	if err != nil {
		return localAgentIPCResponse{}, errors.New("local agent is unavailable")
	}
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return localAgentIPCResponse{}, errors.New("local agent is unavailable")
	}
	defer conn.Close()
	deadline := time.Now().Add(5 * time.Minute)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	request.Op, request.EventID = op, record.EventID
	raw, err := json.Marshal(request)
	if err != nil || len(raw)+1 > localAgentIPCLimit {
		return localAgentIPCResponse{}, errors.New("local agent request failed")
	}
	raw = append(raw, '\n')
	if _, err := conn.Write(raw); err != nil {
		return localAgentIPCResponse{}, errors.New("local agent request failed")
	}
	if unix, ok := conn.(*net.UnixConn); ok {
		if err := unix.CloseWrite(); err != nil {
			return localAgentIPCResponse{}, errors.New("local agent request failed")
		}
	}
	var response localAgentIPCResponse
	if decodeLocalAgentJSON(conn, &response) != nil || !response.OK || response.EventID != record.EventID {
		if response.Error != "" {
			return response, errors.New(response.Error)
		}
		return response, errors.New("local agent response failed")
	}
	return response, nil
}

func invokeLocalAgentIPC(ctx context.Context, record LocalAgentInstance, sessionID, input string) (localAgentIPCResponse, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return localAgentIPCResponse{}, err
	}
	id := hex.EncodeToString(raw)
	socket, err := localAgentSocket(record)
	if err != nil {
		return localAgentIPCResponse{}, errors.New("local agent is unavailable")
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return localAgentIPCResponse{}, errors.New("local agent is unavailable")
	}
	defer conn.Close()
	request := localAgentIPCRequest{Op: "invoke", EventID: record.EventID, SessionID: sessionID, Input: input, InvocationID: id}
	encoded, _ := json.Marshal(request)
	if _, err := conn.Write(append(encoded, '\n')); err != nil {
		return localAgentIPCResponse{}, errors.New("local agent request failed")
	}
	prepared := make(chan struct {
		response localAgentIPCResponse
		err      error
	}, 1)
	responseReader := bufio.NewReader(io.LimitReader(conn, localAgentIPCLimit*2+2))
	go func() {
		var response localAgentIPCResponse
		err := readLocalAgentFrame(responseReader, &response)
		if err == nil && (!response.OK || response.EventID != record.EventID || response.Phase != "prepared") {
			err = errors.New(response.Error)
		}
		prepared <- struct {
			response localAgentIPCResponse
			err      error
		}{response, err}
	}()
	select {
	case result := <-prepared:
		if result.err != nil {
			return result.response, result.err
		}
		select {
		case <-ctx.Done():
			_ = conn.Close()
			return localAgentIPCResponse{}, ctx.Err()
		default:
		}
		ack, _ := json.Marshal(map[string]any{"invocationId": id, "commit": true})
		if _, err := conn.Write(append(ack, '\n')); err != nil {
			return localAgentIPCResponse{}, errors.New("local agent commit failed before acknowledgment")
		}
		var committed localAgentIPCResponse
		deadline := time.Now().Add(5 * time.Second)
		if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
			deadline = requested
		}
		_ = conn.SetReadDeadline(deadline)
		if err := readLocalAgentFrame(responseReader, &committed); err != nil || !committed.OK || committed.EventID != record.EventID || committed.Phase != "committed" {
			return localAgentIPCResponse{}, errors.New("local agent invocation outcome unknown; it may have committed, so inspect the session before retrying")
		}
		return result.response, nil
	case <-ctx.Done():
		_ = conn.Close()
		return localAgentIPCResponse{}, ctx.Err()
	}
}

func waitLocalAgentStopped(ctx context.Context, record LocalAgentInstance) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := probeLocalAgent(ctx, record, "ping", localAgentIPCRequest{}); err != nil {
			held, lockErr := localAgentWorkerLeaseHeld(ctx, record)
			if lockErr != nil {
				return lockErr
			}
			if !held {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return errors.New("local agent did not stop")
}

func removeLocalAgentSocket(record LocalAgentInstance) error {
	socket, err := localAgentSocket(record)
	if err != nil {
		return errors.New("local agent socket is unavailable")
	}
	info, err := os.Lstat(socket)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return errors.New("local agent socket is unsafe")
	}
	if err := os.Remove(socket); err != nil {
		return errors.New("local agent socket could not be removed")
	}
	return nil
}

func localAgentWorkerLeaseHeld(ctx context.Context, record LocalAgentInstance) (bool, error) {
	dir, _, err := localAgentInstanceDirectory(ctx, record.ID, false)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	lock, err := fileaccess.EnsurePrivateFileAt(ctx, dir, "active-"+record.EventID+".lock")
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err := fileaccess.TryLock(ctx, lock); err == nil {
		return false, nil
	} else if errors.Is(err, fileaccess.ErrUnsafe) {
		return true, nil
	} else {
		return false, err
	}
}
