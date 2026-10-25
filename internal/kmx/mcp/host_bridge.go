package mcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var errHostBridge = errors.New("host tools unavailable")

type HostBridgeDescriptor struct {
	URL       string `json:"url"`
	PublicKey string `json:"publicKey"`
}

// HostRuntime carries trusted process identity, never credentials or arbitrary argv.
type HostRuntime struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Kind          string                `json:"kind"`
	Executable    string                `json:"executable"`
	CWD           string                `json:"cwd"`
	GOOS          string                `json:"goos"`
	Distro        string                `json:"distro"`
	Args          []string              `json:"args"`
	Broker        *HostBridgeDescriptor `json:"broker,omitempty"`
}

func NewHostRuntime(root, provider, endpoint string, bridge *HostBridge) (*HostRuntime, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, errHostBridge
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, errHostBridge
	}
	value := &HostRuntime{SchemaVersion: 1, Kind: "kmx.host-runtime", Executable: executable, CWD: cwd, GOOS: goruntime.GOOS, Distro: os.Getenv("WSL_DISTRO_NAME"), Args: []string{"mcp"}}
	for _, flag := range [][2]string{{"--root", root}, {"--provider", provider}, {"--endpoint", endpoint}} {
		if flag[1] != "" {
			value.Args = append(value.Args, flag[0], flag[1])
		}
	}
	if bridge != nil {
		descriptor := bridge.Descriptor()
		value.Broker = &descriptor
	}
	return value, nil
}
func AddHostRuntimeResource(server *sdk.Server, value *HostRuntime) {
	raw, _ := json.Marshal(value)
	server.AddResource(&sdk.Resource{URI: "kmx://host-runtime", Name: "host-runtime", MIMEType: "application/json", Description: "Trusted process launch identity; public relay keys grant no execution authority."}, func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, MIMEType: "application/json", Text: string(raw)}}}, nil
	})
}

type hostJob struct {
	ID        string          `json:"id"`
	RunID     string          `json:"runId"`
	Kind      string          `json:"kind"`
	Names     []string        `json:"names,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments *map[string]any `json:"arguments,omitempty"`
	CallID    string          `json:"callId,omitempty"`
}
type hostPending struct {
	ctx     context.Context
	result  chan []byte
	kind    string
	claimed bool
}

// HostBridge is a bounded invocation relay pinned to one runtime's signing key.
// Private signing material stays only in memory; public discovery grants no authority.
type HostBridge struct {
	descriptor HostBridgeDescriptor
	public     ed25519.PublicKey
	private    ed25519.PrivateKey
	server     *http.Server
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	nonces     map[string]int64
	pending    map[string]*hostPending
	queue      chan hostJob
	runs       int
}

func OpenHostBridge(ctx context.Context, publicKey string) (*HostBridge, error) {
	der, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		return nil, errHostBridge
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, errHostBridge
	}
	public, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, errHostBridge
	}
	signer, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, errHostBridge
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errHostBridge
	}
	signerDER, _ := x509.MarshalPKIXPublicKey(signer)
	ctx, cancel := context.WithCancel(ctx)
	b := &HostBridge{descriptor: HostBridgeDescriptor{URL: "http://" + listener.Addr().String(), PublicKey: base64.StdEncoding.EncodeToString(signerDER)}, public: public, private: private, ctx: ctx, cancel: cancel, nonces: map[string]int64{}, pending: map[string]*hostPending{}, queue: make(chan hostJob, 128)}
	b.server = &http.Server{Handler: http.HandlerFunc(b.serve), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 4096}
	go func() { _ = b.server.Serve(listener); cancel() }()
	context.AfterFunc(ctx, func() { _ = b.server.Close() })
	return b, nil
}
func (b *HostBridge) Descriptor() HostBridgeDescriptor { return b.descriptor }
func (b *HostBridge) Close() error                     { b.cancel(); return b.server.Close() }
func hostID() string                                   { return hex.EncodeToString(makeRandomID()) }
func makeRandomID() []byte                             { id := make([]byte, 16); _, _ = rand.Read(id); return id }

func (b *HostBridge) authenticated(r *http.Request, body []byte) bool {
	if _, present := r.Header["Origin"]; present || r.Host != strings.TrimPrefix(b.descriptor.URL, "http://") || r.RequestURI != r.URL.Path {
		return false
	}
	stamp, nonce := r.Header.Get("X-Kmx-Time"), r.Header.Get("X-Kmx-Nonce")
	seconds, err := strconv.ParseInt(stamp, 10, 64)
	now := time.Now().Unix()
	decoded, nonceErr := hex.DecodeString(nonce)
	if err != nil || seconds < now-30 || seconds > now+30 || nonceErr != nil || len(decoded) != 16 || strings.ToLower(nonce) != nonce {
		return false
	}
	signature, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Kmx-Signature"))
	canonical := r.Method + "\n" + r.URL.Path + "\n" + stamp + "\n" + nonce + "\n" + string(body)
	if err != nil || !ed25519.Verify(b.public, []byte(canonical), signature) {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, expiry := range b.nonces {
		if expiry < now {
			delete(b.nonces, key)
		}
	}
	if _, used := b.nonces[nonce]; used || len(b.nonces) >= 4096 {
		return false
	}
	b.nonces[nonce] = now + 61
	return true
}
func (b *HostBridge) respond(w http.ResponseWriter, r *http.Request, status int, value any) {
	body, _ := json.Marshal(value)
	canonical := strconv.Itoa(status) + "\n" + r.Header.Get("X-Kmx-Nonce") + "\n" + string(body)
	w.Header().Set("X-Kmx-Signature", base64.StdEncoding.EncodeToString(ed25519.Sign(b.private, []byte(canonical))))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
func (b *HostBridge) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil {
		b.respond(w, r, 413, nil)
		return
	}
	if !b.authenticated(r, body) {
		b.respond(w, r, 401, nil)
		return
	}
	if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/claim/") {
		if len(body) != 0 {
			b.respond(w, r, 400, nil)
			return
		}
		b.mu.Lock()
		pending := b.pending[strings.TrimPrefix(r.URL.Path, "/claim/")]
		status := 410
		if pending != nil && pending.kind == "call" && !pending.claimed && pending.ctx.Err() == nil && b.ctx.Err() == nil {
			pending.claimed = true
			status = 200
		}
		b.mu.Unlock()
		b.respond(w, r, status, nil)
		return
	}
	if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/result/") {
		if !runtime.JSONKeysUnique(body) || !runtime.JSONNumbersExact(body) {
			b.respond(w, r, 400, nil)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/result/")
		b.mu.Lock()
		pending := b.pending[id]
		if pending != nil && pending.kind == "call" && !pending.claimed {
			pending = nil
		}
		if pending != nil {
			delete(b.pending, id)
		}
		if pending != nil && pending.ctx.Err() == nil && b.ctx.Err() == nil {
			pending.result <- body
		} else {
			pending = nil
		}
		b.mu.Unlock()
		if pending == nil {
			b.respond(w, r, 410, nil)
		} else {
			b.respond(w, r, 200, nil)
		}
		return
	}
	if r.Method != "GET" || r.URL.Path != "/next" || len(body) != 0 {
		b.respond(w, r, 404, nil)
		return
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case job := <-b.queue:
			b.mu.Lock()
			pending := b.pending[job.ID]
			b.mu.Unlock()
			if job.Kind != "close" && (pending == nil || pending.ctx.Err() != nil) {
				continue
			}
			b.respond(w, r, 200, job)
			return
		case <-timer.C:
			b.respond(w, r, 200, nil)
			return
		case <-r.Context().Done():
			return
		case <-b.ctx.Done():
			return
		}
	}
}
func (b *HostBridge) rpc(ctx context.Context, job hostJob, result any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if ctx.Err() != nil || b.ctx.Err() != nil {
		return errHostBridge
	}
	job.ID = hostID()
	pending := &hostPending{ctx: ctx, result: make(chan []byte, 1), kind: job.Kind}
	b.mu.Lock()
	if len(b.pending) >= 128 {
		b.mu.Unlock()
		return errHostBridge
	}
	b.pending[job.ID] = pending
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.pending, job.ID); b.mu.Unlock() }()
	select {
	case b.queue <- job:
	default:
		return errHostBridge
	}
	select {
	case raw := <-pending.result:
		var failure struct {
			Error bool `json:"error"`
		}
		if json.Unmarshal(raw, &failure) != nil || failure.Error || json.Unmarshal(raw, result) != nil || ctx.Err() != nil || b.ctx.Err() != nil {
			return errHostBridge
		}
		return nil
	case <-ctx.Done():
		return errHostBridge
	case <-b.ctx.Done():
		return errHostBridge
	}
}
func (b *HostBridge) ResolveTools(ctx context.Context, names []string) (*localtools.Host, error) {
	if len(names) == 0 || len(names) > 32 {
		return nil, errHostBridge
	}
	seen := map[string]bool{}
	for _, name := range names {
		if !localtools.ValidToolName(name) || seen[name] {
			return nil, errHostBridge
		}
		seen[name] = true
	}
	b.mu.Lock()
	if b.runs >= 64 || ctx.Err() != nil || b.ctx.Err() != nil {
		b.mu.Unlock()
		return nil, errHostBridge
	}
	b.runs++
	b.mu.Unlock()
	runID := hostID()
	var once sync.Once
	release := func() {
		once.Do(func() {
			b.mu.Lock()
			b.runs--
			b.mu.Unlock()
			select {
			case b.queue <- hostJob{ID: hostID(), RunID: runID, Kind: "close"}:
			default:
			}
		})
	}
	stop := context.AfterFunc(ctx, release)
	var response struct {
		Definitions []api.ToolDefinition `json:"definitions"`
	}
	if b.rpc(ctx, hostJob{RunID: runID, Kind: "resolve", Names: append([]string(nil), names...)}, &response) != nil {
		stop()
		release()
		return nil, errHostBridge
	}
	execute := func(callCtx context.Context, _ controller.ToolCallContext, call api.ToolCall) (api.ToolResult, error) {
		callCtx, cancel := context.WithCancel(callCtx)
		defer cancel()
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		if ctx.Err() != nil {
			return api.ToolResult{}, errHostBridge
		}
		args := call.Args
		if args == nil {
			args = map[string]any{}
		}
		var response struct {
			Output map[string]any `json:"output"`
		}
		err := b.rpc(callCtx, hostJob{RunID: runID, Kind: "call", Name: call.Tool, Arguments: &args, CallID: call.ID}, &response)
		if ctx.Err() != nil {
			return api.ToolResult{}, errHostBridge
		}
		return api.ToolResult{ID: call.ID, Output: response.Output}, err
	}
	host, err := localtools.NewTrustedHost(response.Definitions, execute, func(values ...string) error {
		for _, value := range values {
			if secretshapes.Match(value) != nil {
				return errHostBridge
			}
		}
		return nil
	})
	if err != nil || !host.Matches(names) || ctx.Err() != nil {
		stop()
		release()
		return nil, errHostBridge
	}
	return host, nil
}
