package kagentcompat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The exact bytes the PINNED kagent CLI v0.10.1 puts on the wire, captured
// from the binary itself against a recording loopback server:
//
//	kagent --kagent-url http://127.0.0.1:<port> invoke --agent hello --task hi
//	kagent --kagent-url http://127.0.0.1:<port> invoke --stream --agent hello --task hi --session sess1
//
// `messageId` is PRESENT and EMPTY, not absent — the CLI marshals a
// protocol.Message whose MessageID field was never set. The direct agent A2A
// endpoint answers -32602 `message ID is required` for exactly this, and the
// CLI's error decoder then masks it as an unmarshal failure. Anything that
// only looked for a missing key would not fix a single real call.
const (
	cliSendBody   = `{"jsonrpc":"2.0","id":"0f620574-548e-48a5-a90e-1614cbfb82bd","method":"message/send","params":{"message":{"kind":"message","messageId":"","parts":[{"kind":"text","text":"hi"}],"role":"user"}}}`
	cliStreamBody = `{"jsonrpc":"2.0","id":"bb9ef975-1ada-4d6b-8ca2-ef4feb6de051","method":"message/stream","params":{"message":{"contextId":"sess1","kind":"message","messageId":"","parts":[{"kind":"text","text":"hi"}],"role":"user"}}}`

	// kmx's own HITL continuation already carries a messageId, a taskId and a
	// contextId. It must come through byte-identical: a rewritten ID there
	// would be a new message rather than the decision the controller is
	// waiting on.
	hitlBody = `{"jsonrpc":"2.0","id":"kmx-1","method":"message/stream","params":{"message":{"kind":"message","role":"user","messageId":"kmx-1","taskId":"task-1","contextId":"ctx-1","parts":[{"kind":"data","data":{"decision_type":"approve"},"metadata":{}}]}}}`
)

func fixedID(id string) func() string { return func() string { return id } }

// Only a message/send or message/stream whose message has no usable ID is
// rewritten, and the rewrite touches nothing else.
func TestMessageIDIsInjectedOnlyWhereItIsMissing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		changed bool
	}{
		{"the CLI's nonstream send", cliSendBody, true},
		{"the CLI's stream send", cliStreamBody, true},
		{"a message with no messageId key at all", `{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"message":{"kind":"message","role":"user","parts":[]}}}`, true},
		{"a messageId that is only whitespace", `{"jsonrpc":"2.0","id":"1","method":"message/stream","params":{"message":{"messageId":"  ","role":"user","parts":[]}}}`, true},
		{"kmx's own HITL decision", hitlBody, false},
		{"a message that already has an ID", `{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"message":{"messageId":"theirs","role":"user"}}}`, false},
		{"a method that carries no message", `{"jsonrpc":"2.0","id":"1","method":"tasks/get","params":{"id":"task-1"}}`, false},
		{"no method at all", `{"jsonrpc":"2.0","id":"1","result":{}}`, false},
		{"params that are not an object", `{"jsonrpc":"2.0","id":"1","method":"message/send","params":[1,2]}`, false},
		{"a message that is not an object", `{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"message":"nope"}}`, false},
		{"a messageId that is not a string", `{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"message":{"messageId":7}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := InjectMessageID([]byte(tc.body), fixedID("kmx-test-id"))
			if err != nil {
				t.Fatalf("InjectMessageID: %v", err)
			}
			if changed != tc.changed {
				t.Fatalf("changed=%v, want %v (%s)", changed, tc.changed, got)
			}
			if !tc.changed {
				if !bytes.Equal(got, []byte(tc.body)) {
					t.Fatalf("an untouched request was rewritten:\n got %s\nwant %s", got, tc.body)
				}
				return
			}
			if id := messageField(t, got, "messageId"); id != `"kmx-test-id"` {
				t.Fatalf("messageId=%s, want the generated ID", id)
			}
			// Everything the caller sent is still what the controller sees.
			var before, after map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.body), &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(got, &after); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"jsonrpc", "id", "method"} {
				if !bytes.Equal(before[key], after[key]) {
					t.Fatalf("%s changed: %s -> %s", key, before[key], after[key])
				}
			}
			for _, key := range []string{"parts", "role", "kind", "contextId"} {
				want := messageField(t, []byte(tc.body), key)
				if got := messageField(t, got, key); got != want {
					t.Fatalf("message.%s changed: %s -> %s", key, want, got)
				}
			}
		})
	}
}

// The generated ID is unique per call: a reused ID is a different bug in the
// same place, because the controller keys the turn on it.
func TestGeneratedMessageIDsAreUniqueAndNonEmpty(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := NewMessageID()
		if strings.TrimSpace(id) == "" {
			t.Fatal("generated an empty message ID")
		}
		if seen[id] {
			t.Fatalf("generated a duplicate message ID %q", id)
		}
		seen[id] = true
	}
}

// A body that is not JSON at all is refused here rather than forwarded: the
// only client on this hop is the pinned CLI, which posts JSON-RPC and
// nothing else.
func TestMalformedBodyIsRejected(t *testing.T) {
	for _, body := range []string{"", "not json", `{"jsonrpc":`, `{"jsonrpc":"2.0",}`} {
		if _, _, err := InjectMessageID([]byte(body), fixedID("x")); err == nil {
			t.Fatalf("malformed body %q was accepted", body)
		}
	}
}

// upstreamRecorder is the controller side of the hop.
type upstreamRecorder struct {
	*httptest.Server
	bodies  []string
	paths   []string
	hosts   []string
	methods []string
}

func newUpstream(t *testing.T, handler http.HandlerFunc) *upstreamRecorder {
	t.Helper()
	rec := &upstreamRecorder{}
	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.bodies = append(rec.bodies, string(body))
		rec.paths = append(rec.paths, r.URL.RequestURI())
		rec.hosts = append(rec.hosts, r.Host)
		rec.methods = append(rec.methods, r.Method)
		if handler != nil {
			handler(w, r)
			return
		}
		io.WriteString(w, `{"jsonrpc":"2.0","id":"1","result":{}}`)
	}))
	t.Cleanup(rec.Close)
	return rec
}

func startProxy(t *testing.T, upstream string, opts ...func(*Options)) *Proxy {
	t.Helper()
	opt := Options{Upstream: upstream, Log: io.Discard}
	for _, apply := range opts {
		apply(&opt)
	}
	proxy, err := Start(opt)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { proxy.Close() })
	return proxy
}

// The seam, end to end: the CLI's exact bytes go in, the controller sees a
// message ID, and the path, method and the rest of the request are intact.
func TestProxyInjectsTheMissingMessageID(t *testing.T) {
	upstream := newUpstream(t, nil)
	proxy := startProxy(t, upstream.URL)

	path := "/api/a2a/kagent/hello/"
	resp, err := http.Post(proxy.URL()+path, "application/json", strings.NewReader(cliSendBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxy answered HTTP %d", resp.StatusCode)
	}
	if len(upstream.bodies) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(upstream.bodies))
	}
	if got := messageField(t, []byte(upstream.bodies[0]), "messageId"); got == `""` || got == "" {
		t.Fatalf("the controller still received no message ID: %s", upstream.bodies[0])
	}
	if upstream.paths[0] != path {
		t.Fatalf("path=%q, want %q", upstream.paths[0], path)
	}
	if upstream.methods[0] != http.MethodPost {
		t.Fatalf("method=%q", upstream.methods[0])
	}
}

// A request that already carries an ID — kmx's own HITL continuation — is
// forwarded byte for byte.
func TestProxyNeverRewritesARequestThatAlreadyHasAnID(t *testing.T) {
	upstream := newUpstream(t, nil)
	proxy := startProxy(t, upstream.URL)
	for _, body := range []string{hitlBody, `{"jsonrpc":"2.0","id":"1","method":"tasks/get","params":{"id":"t"}}`} {
		resp, err := http.Post(proxy.URL()+"/api/a2a/kagent/hello/", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	for i, body := range upstream.bodies {
		if want := []string{hitlBody, `{"jsonrpc":"2.0","id":"1","method":"tasks/get","params":{"id":"t"}}`}[i]; body != want {
			t.Fatalf("request %d was rewritten:\n got %s\nwant %s", i, body, want)
		}
	}
}

// GET traffic — the version probe and the agent lookups the CLI makes before
// it invokes — is a straight passthrough.
func TestProxyPassesNonPostTrafficThrough(t *testing.T) {
	upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"kagent_version":"0.10.1"}`)
	})
	proxy := startProxy(t, upstream.URL)
	resp, err := http.Get(proxy.URL() + "/api/agents?user_id=admin%40kagent.dev")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"kagent_version":"0.10.1"}` {
		t.Fatalf("body=%q", body)
	}
	if upstream.paths[0] != "/api/agents?user_id=admin%40kagent.dev" {
		t.Fatalf("query was not preserved: %q", upstream.paths[0])
	}
}

// The sandbox invoke path the pinned CLI also carries is rewritten too.
func TestTheSandboxInvokePathIsAlsoRewritten(t *testing.T) {
	upstream := newUpstream(t, nil)
	proxy := startProxy(t, upstream.URL)
	resp, err := http.Post(proxy.URL()+"/api/a2a-sandboxes/kagent/hello/", "application/json", strings.NewReader(cliSendBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if id := messageField(t, []byte(upstream.bodies[0]), "messageId"); id == `""` || id == "" {
		t.Fatalf("the sandbox invoke reached the controller without an ID: %s", upstream.bodies[0])
	}
}

// A POST that is not a legacy invoke is none of this hop's business: it is
// forwarded byte for byte, unbuffered and unparsed, whatever it contains. A
// shim for one known CLI defect must not start rejecting controller
// endpoints that did not exist when it was written.
func TestUnrelatedPostPathsPassThroughUntouched(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
	}{
		{"a body that is not JSON at all", "/api/sessions", "not json, and not ours to read"},
		{"a body past the A2A bound", "/api/uploads", strings.Repeat("x", MaxRequestBody+1024)},
		{"a JSON-RPC send on a path that is not an invoke", "/api/sessions", cliSendBody},
		{"a path that only looks like the invoke prefix", "/api/a2a-other/send", cliSendBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newUpstream(t, nil)
			proxy := startProxy(t, upstream.URL)
			resp, err := http.Post(proxy.URL()+tc.path, "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d, want the controller's own answer", resp.StatusCode)
			}
			if len(upstream.bodies) != 1 {
				t.Fatalf("the controller saw %d requests, want 1", len(upstream.bodies))
			}
			if upstream.bodies[0] != tc.body {
				t.Fatalf("the body was altered in flight (%d bytes in, %d out)", len(tc.body), len(upstream.bodies[0]))
			}
			if upstream.paths[0] != tc.path {
				t.Fatalf("path=%q, want %q", upstream.paths[0], tc.path)
			}
		})
	}
}

// Fail closed, both ways: a body the proxy cannot read is refused and NEVER
// reaches the controller. A rewriting hop that passed unparseable bytes
// through would be claiming a guarantee it did not make. This applies to
// the invoke path only — see TestUnrelatedPostPathsPassThroughUntouched.
func TestBoundedAndMalformedBodiesNeverReachTheController(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"a body that is not JSON", "not json at all", http.StatusBadRequest},
		{"a body past the bound", `{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"pad":"` + strings.Repeat("x", MaxRequestBody) + `"}}`, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newUpstream(t, nil)
			proxy := startProxy(t, upstream.URL)
			resp, err := http.Post(proxy.URL()+"/api/a2a/kagent/hello/", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status=%d, want %d", resp.StatusCode, tc.want)
			}
			if len(upstream.bodies) != 0 {
				t.Fatalf("the controller was called anyway: %q", upstream.bodies)
			}
		})
	}
}

// Streaming survives the hop: events reach the client as the controller
// emits them, not after the response ends. A buffering proxy would turn
// every interactive turn into a silent wait.
func TestProxyPreservesServerSentEventStreaming(t *testing.T) {
	release := make(chan struct{})
	upstream := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "data: {\"kind\":\"status-update\",\"taskId\":\"t1\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
		io.WriteString(w, "data: {\"kind\":\"status-update\",\"taskId\":\"t1\",\"final\":true}\n\n")
		w.(http.Flusher).Flush()
	})
	proxy := startProxy(t, upstream.URL)

	req, err := http.NewRequest(http.MethodPost, proxy.URL()+"/api/a2a/kagent/hello/", strings.NewReader(cliStreamBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("first line=%q", line)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the first event was buffered until the stream ended")
	}
	close(release)
	rest, _ := io.ReadAll(reader)
	if !strings.Contains(string(rest), `"final":true`) {
		t.Fatalf("the rest of the stream was lost: %q", rest)
	}
	if got := messageField(t, []byte(upstream.bodies[0]), "messageId"); got == `""` {
		t.Fatalf("the streaming send still had no message ID: %s", upstream.bodies[0])
	}
}

// Fixed target, not a proxy. The hop ignores anything the client says about
// where the request should go.
func TestProxyIsNotAnOpenProxy(t *testing.T) {
	elsewhere := newUpstream(t, nil)
	upstream := newUpstream(t, nil)
	proxy := startProxy(t, upstream.URL)

	req, err := http.NewRequest(http.MethodPost, proxy.URL()+"/api/a2a/kagent/hello/", strings.NewReader(cliSendBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = strings.TrimPrefix(elsewhere.URL, "http://")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(elsewhere.bodies) != 0 {
		t.Fatalf("a Host header redirected the request: %q", elsewhere.bodies)
	}
	if len(upstream.bodies) != 1 {
		t.Fatalf("the fixed target saw %d requests", len(upstream.bodies))
	}

	// The absolute-form request line a real HTTP proxy would accept.
	conn, err := net.Dial("tcp", strings.TrimPrefix(proxy.URL(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s/api/agents HTTP/1.1\r\nHost: %s\r\n\r\n", elsewhere.URL, strings.TrimPrefix(elsewhere.URL, "http://"))
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(status, "200") {
		t.Fatalf("absolute-form request was served: %q", status)
	}
	if len(elsewhere.bodies) != 0 {
		t.Fatalf("absolute-form request was forwarded: %q", elsewhere.bodies)
	}
}

// The upstream is the kmx-owned loopback forward or nothing.
func TestProxyRefusesAnUpstreamItDoesNotOwn(t *testing.T) {
	for _, upstream := range []string{
		"", "http://example.com:8083", "https://10.0.0.1:8083", "ftp://127.0.0.1:8083",
		"http://127.0.0.1:8083/../x", ":::not-a-url",
	} {
		if proxy, err := Start(Options{Upstream: upstream, Log: io.Discard}); err == nil {
			proxy.Close()
			t.Fatalf("upstream %q was accepted", upstream)
		}
	}
}

// Closing releases the port with the forward it fronts. A leaked listener
// would outlive the tunnel and answer later chats with a dead upstream.
func TestProxyCloseStopsListening(t *testing.T) {
	upstream := newUpstream(t, nil)
	proxy := startProxy(t, upstream.URL)
	address := strings.TrimPrefix(proxy.URL(), "http://")
	if err := proxy.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := proxy.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err == nil {
		conn.Close()
		t.Fatalf("the proxy is still listening on %s after Close", address)
	}
}

// messageField returns params.message.<key> as raw JSON, or "" when absent.
func messageField(t *testing.T, body []byte, key string) string {
	t.Helper()
	var envelope struct {
		Params struct {
			Message map[string]json.RawMessage `json:"message"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	return string(envelope.Params.Message[key])
}
