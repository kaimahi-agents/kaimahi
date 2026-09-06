// Package notify is the approval notifier: when an approval request
// is filed, the plane posts into the pinned Slack channel that a human
// decision is waiting, and the inbound bridge replies the outcome of a
// Slack command in the mention's thread. Both go THROUGH the plane's own
// MCP gateway, under the plane's own credential, so the message the
// plane sends is governed exactly as an agent's would be: authenticated
// (a kmh_ token the gateway knows by hash), allowlisted (the one posting
// tool), channel-pinned (the MCP server's own restriction, from the same
// Secret key the channel here is read from), and audited (a tool_audit
// row per attempt under the plane's credential).
//
// A notification is a convenience, never a gate: a post that fails
// never un-files a request, and the human can always run `make
// approvals`. Retries are bounded and deliberately narrow — see retry.
package notify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaimahi-agents/kaimahi/plane/internal/gateway"

	"github.com/kaimahi-agents/kaimahi/plane/internal/metrics"
)

// Deps configures the Poster.
type Deps struct {
	// GatewayURL is the plane's own MCP gateway origin (the loopback
	// listener); the post goes to {GatewayURL}/upstream/{Upstream}/mcp.
	GatewayURL string
	Upstream   string
	Tool       string
	// CredentialFile holds the plane's kmh_ token; ChannelFile the
	// channel id. Both read per post from plane custody.
	CredentialFile string
	ChannelFile    string
	// Client makes the gateway calls. Nil gets a default that never
	// follows a redirect and bounds one call at CallTimeout.
	Client      *http.Client
	CallTimeout time.Duration
	// Backoff between retry attempts; len(Backoff)+1 is the attempt
	// count. Nil takes the default (2s, 5s: three attempts).
	Backoff []time.Duration
	// QueueSize bounds posts waiting to be sent (default 32). A full
	// queue drops the post with a log line — a notification the plane
	// cannot get out is not worth holding a request handler for.
	QueueSize int
	// Sleep waits out a backoff, returning early when ctx is done (a
	// shutdown must not wait on a retry). Nil takes the default; tests
	// inject a recorder.
	Sleep func(context.Context, time.Duration)
}

const (
	defaultCallTimeout = 30 * time.Second
	defaultQueueSize   = 32
	maxBufferedResp    = 1 << 20
)

// Post is one message to send: text, into the pinned channel, in a
// thread when ThreadTS is set. Kind labels the log lines.
type Post struct {
	Kind     string
	Text     string
	ThreadTS string
}

// Poster sends posts asynchronously through the gateway.
type Poster struct {
	d        Deps
	jobs     chan Post
	inflight atomic.Bool
}

func New(d Deps) *Poster {
	if d.CallTimeout <= 0 {
		d.CallTimeout = defaultCallTimeout
	}
	if d.Backoff == nil {
		d.Backoff = []time.Duration{2 * time.Second, 5 * time.Second}
	}
	if d.QueueSize <= 0 {
		d.QueueSize = defaultQueueSize
	}
	if d.Sleep == nil {
		d.Sleep = func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}
	if d.Client == nil {
		d.Client = &http.Client{
			Timeout: d.CallTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	// Expose the queue series from the start (an idle replica shows 0,
	// not nothing).
	metrics.SetQueue(metrics.QueueNotifier, 0, d.QueueSize)
	return &Poster{d: d, jobs: make(chan Post, d.QueueSize)}
}

// Enqueue queues a post; it never blocks the caller. Reports whether the
// post was taken.
func (p *Poster) Enqueue(post Post) bool {
	select {
	case p.jobs <- post:
		depth := len(p.jobs)
		if p.inflight.Load() {
			depth++ // queued plus the post being sent right now
		}
		metrics.SetQueue(metrics.QueueNotifier, depth, cap(p.jobs))
		return true
	default:
		slog.Error("notify: queue full; post dropped (the request stays filed — run 'make approvals')",
			"kind", post.Kind)
		return false
	}
}

// Run sends queued posts one at a time until ctx is done. One worker:
// notifications are rare and ordering in the channel should follow
// filing order.
func (p *Poster) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case post := <-p.jobs:
			p.inflight.Store(true)
			metrics.SetQueue(metrics.QueueNotifier, len(p.jobs)+1, cap(p.jobs))
			p.send(ctx, post)
			p.inflight.Store(false)
			metrics.SetQueue(metrics.QueueNotifier, len(p.jobs), cap(p.jobs))
		}
	}
}

// Drain waits until the queue is empty and nothing is in flight, or ctx
// is done; it reports how many posts were still queued. Shutdown calls
// it BEFORE cancelling Run, so a post filed during the servers' drain
// still goes out within the shutdown budget.
func (p *Poster) Drain(ctx context.Context) (queued int) {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		if len(p.jobs) == 0 && !p.inflight.Load() {
			return 0
		}
		select {
		case <-ctx.Done():
			return len(p.jobs)
		case <-t.C:
		}
	}
}

// outcome classifies one attempt. Only "refused" is retried: it means
// the message is KNOWN not to have reached Slack (the gateway refused
// before forwarding or could not be dialled, or Slack said no).
// "ambiguous" means the request may have gone out and the answer was
// lost (timeout, reset, EOF, an unparseable success, a 5xx from beyond
// the gateway — including the gateway's own 502, which it answers for
// ANY failure on its hop to the MCP server, a dial refusal and a reset
// after the post was delivered alike) — the post may have landed, and
// posting again is the double-post the slack-post retry rule exists to
// prevent, so it is recorded and left.
type outcome int

const (
	posted outcome = iota
	refused
	ambiguous
)

func (o outcome) String() string {
	switch o {
	case posted:
		return "posted"
	case refused:
		return "refused"
	}
	return "ambiguous"
}

// send runs one post through the bounded retry loop.
func (p *Poster) send(ctx context.Context, post Post) {
	for attempt := 0; ; attempt++ {
		o, err := p.attempt(ctx, post)
		switch {
		case o == posted:
			slog.Info("notify: posted", "kind", post.Kind, "attempt", attempt+1)
			return
		case o == ambiguous:
			slog.Error("notify: post outcome unknown; NOT retried (a second post could be a double-post)",
				"kind", post.Kind, "attempt", attempt+1, "err", err)
			return
		case attempt >= len(p.d.Backoff):
			slog.Error("notify: post refused; giving up (the request stays filed — run 'make approvals')",
				"kind", post.Kind, "attempts", attempt+1, "err", err)
			return
		}
		slog.Warn("notify: post refused; retrying", "kind", post.Kind, "attempt", attempt+1, "err", err)
		p.d.Sleep(ctx, p.d.Backoff[attempt])
		if ctx.Err() != nil {
			slog.Warn("notify: shutting down mid-retry; post not sent (the request stays filed)", "kind", post.Kind)
			return
		}
	}
}

// attempt makes one post: initialize (best effort — the session id is
// relayed when the upstream assigns one), notifications/initialized,
// then tools/call. The tools/call is sent whatever the handshake
// answered, so every attempt leaves its own tool_audit row under the
// plane's credential: what the governed path recorded IS the record of
// whether the human was told.
func (p *Poster) attempt(ctx context.Context, post Post) (outcome, error) {
	token, err := readFile(p.d.CredentialFile)
	if err != nil {
		return refused, fmt.Errorf("plane credential unavailable: %w", err)
	}
	channel, err := readFile(p.d.ChannelFile)
	if err != nil {
		return refused, fmt.Errorf("channel unavailable: %w", err)
	}
	if strings.ContainsAny(channel, ", \n") || channel == "true" || strings.HasPrefix(channel, "!") {
		// The MCP server's grammar admits "true" (post anywhere), "!C…"
		// (everywhere but) and lists; the notifier posts to ONE room or
		// none. Same refusal the inbound channel allowlist makes.
		return refused, fmt.Errorf("channel file does not name exactly one channel")
	}
	ctx, cancel := context.WithTimeout(ctx, p.d.CallTimeout)
	defer cancel()

	session := ""
	init := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-03-26", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "kaimahi-plane", "version": "p8b"}}}
	if resp, body, err := p.call(ctx, token, session, init); err == nil {
		if resp.StatusCode == http.StatusOK {
			session = resp.Header.Get("Mcp-Session-Id")
			_, _, _ = p.call(ctx, token, session, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
		} else {
			slog.Warn("notify: initialize answered non-200; sending tools/call regardless",
				"status", resp.StatusCode, "body", strings.TrimSpace(string(body)))
		}
	} else {
		slog.Warn("notify: initialize failed; sending tools/call regardless", "err", err)
	}

	// A handshake that ate the whole budget (an upstream holding a
	// stream open) means the post was never sent: refused, not
	// ambiguous — the deadline error the call below would return says
	// nothing about which.
	if ctx.Err() != nil {
		return refused, fmt.Errorf("handshake exhausted the call budget before tools/call: %w", ctx.Err())
	}
	args := map[string]any{"channel_id": channel, "payload": post.Text}
	if post.ThreadTS != "" {
		args["thread_ts"] = post.ThreadTS
	}
	callMsg := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": p.d.Tool, "arguments": args}}
	resp, body, err := p.call(ctx, token, session, callMsg)
	if err != nil {
		return classifyErr(err), err
	}
	return classifyResponse(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}

func readFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(raw))
	if v == "" {
		return "", errors.New("empty")
	}
	return v, nil
}

// call posts one JSON-RPC message to the gateway and returns the
// response with its (bounded) body read.
func (p *Poster) call(ctx context.Context, token, session string, msg any) (*http.Response, []byte, error) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return nil, nil, err
	}
	u := strings.TrimSuffix(p.d.GatewayURL, "/") + "/upstream/" + p.d.Upstream + "/mcp"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := p.d.Client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBufferedResp))
	if err != nil {
		return resp, body, err
	}
	return resp, body, nil
}

// classifyErr: a dial failure is a refusal (nothing was sent); anything
// else — a timeout, a reset, an EOF — happened after the request may
// have gone out, and is ambiguous.
func classifyErr(err error) outcome {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Op == "dial" {
		return refused
	}
	return ambiguous
}

// classifyResponse reads the gateway's answer to tools/call.
//   - 200 with a JSON-RPC result and no isError: posted.
//   - 200 with a JSON-RPC error or isError: the gateway denied it, or
//     Slack (or the server) refused the post — nothing landed; refused.
//   - 4xx and 503: the gateway refused before forwarding, or the
//     upstream's credential was unreadable — before Slack saw anything;
//     refused. So is the one 502 the gateway issues itself before any
//     bytes reach the server: a redirect it refused to follow.
//   - any other 502 (the gateway's "unreachable", which it answers for a
//     dial failure AND for a reset after the post was delivered), a
//     relayed 5xx, a 200 that does not parse: the post may have reached
//     the server; ambiguous. On a kind cluster, where no Slack upstream
//     exists, that means one audited attempt per notification, not
//     three.
func classifyResponse(status int, contentType string, body []byte) (outcome, error) {
	switch {
	case status == http.StatusOK:
	case status >= 400 && status < 500, status == http.StatusServiceUnavailable,
		status == http.StatusBadGateway && strings.TrimSpace(string(body)) == gateway.MsgUpstreamRedirected:
		return refused, fmt.Errorf("gateway answered HTTP %d: %s", status, strings.TrimSpace(string(body)))
	default:
		return ambiguous, fmt.Errorf("gateway answered HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	payload := body
	if strings.HasPrefix(contentType, "text/event-stream") {
		payload = responseFrame(body, 2)
	}
	var rpc struct {
		ID     json.RawMessage `json:"id"`
		Error  *struct{ Message string }
		Result *struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &rpc); err != nil || (rpc.Error == nil && rpc.Result == nil) {
		return ambiguous, fmt.Errorf("gateway answered 200 with an unparseable body: %s", strings.TrimSpace(string(body)))
	}
	if rpc.Error != nil {
		return refused, fmt.Errorf("tools/call error: %s", rpc.Error.Message)
	}
	if rpc.Result.IsError {
		text := ""
		for _, c := range rpc.Result.Content {
			text += c.Text
		}
		return refused, fmt.Errorf("tool refused the post: %s", strings.TrimSpace(text))
	}
	return posted, nil
}

// responseFrame finds, in an SSE-framed body, the data event that is the
// JSON-RPC response with the given id (log notifications and the like
// are skipped). Returns nil when absent.
func responseFrame(raw []byte, id int) []byte {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), maxBufferedResp)
	for sc.Scan() {
		payload, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		payload = strings.TrimPrefix(payload, " ")
		var m struct {
			ID *int `json:"id"`
		}
		if json.Unmarshal([]byte(payload), &m) == nil && m.ID != nil && *m.ID == id {
			return []byte(payload)
		}
	}
	return nil
}
