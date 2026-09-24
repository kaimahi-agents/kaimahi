package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// The exact request bodies the PINNED kagent CLI v0.10.1 puts on the wire,
// captured from the binary against a recording loopback server. `messageId`
// is present and EMPTY; the agent's A2A endpoint answers -32602 `message ID
// is required`, which the CLI then reports as a decode failure.
const (
	legacyCLISendBody   = `{"jsonrpc":"2.0","id":"0f620574-548e-48a5-a90e-1614cbfb82bd","method":"message/send","params":{"message":{"kind":"message","messageId":"","parts":[{"kind":"text","text":"hi"}],"role":"user"}}}`
	legacyCLIStreamBody = `{"jsonrpc":"2.0","id":"bb9ef975-1ada-4d6b-8ca2-ef4feb6de051","method":"message/stream","params":{"message":{"contextId":"sess1","kind":"message","messageId":"","parts":[{"kind":"text","text":"hi"}],"role":"user"}}}`
)

// TestLegacyKagentCLIHelper is not a test: it is the pinned CLI, standing in
// for the real binary in the seam tests below. It is re-executed as a
// subprocess by the stub script fakeLegacyCLI writes, so the request that
// reaches the controller is a REAL HTTP call made against whatever
// --kagent-url kmx chose — not a value a test asserted on directly.
func TestLegacyKagentCLIHelper(t *testing.T) {
	if os.Getenv("KMX_FAKE_LEGACY_KAGENT") != "1" {
		t.Skip("subprocess helper for the legacy kagent CLI seam tests")
	}
	base, agent, stream := "", "agent", false
	for i, arg := range os.Args {
		switch arg {
		case "--kagent-url":
			if i+1 < len(os.Args) {
				base = os.Args[i+1]
			}
		case "--agent":
			if i+1 < len(os.Args) {
				agent = os.Args[i+1]
			}
		case "--stream":
			stream = true
		}
	}
	if log := os.Getenv("KMX_FAKE_LEGACY_KAGENT_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintln(f, base)
			f.Close()
		}
	}
	body := legacyCLISendBody
	if stream {
		body = legacyCLIStreamBody
	}
	resp, err := http.Post(base+"/api/a2a/kagent/"+agent+"/", "application/json", strings.NewReader(body))
	if err != nil {
		// The pinned CLI's own wording for a failed send. ChatRetryable is
		// anchored to this exact line, so the stub must print it the same
		// way — the error text itself is whatever the transport really
		// produced, never a literal the test chose.
		fmt.Fprintf(os.Stderr, "Error invoking session: failed to send HTTP request: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error invoking session: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}
	// The real CLI prints decoded events, one JSON object per line, whether
	// the transport was SSE or a single response.
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			line = strings.TrimSpace(after)
		}
		if line == "" || line == "[DONE]" {
			continue
		}
		fmt.Println(line)
	}
	os.Exit(0)
}

// fakeLegacyCLI writes a stub `kagent` that re-executes this test binary as
// the helper above, and returns its path plus the file each invocation
// records its --kagent-url in.
func fakeLegacyCLI(t *testing.T, dir string) (string, string) {
	t.Helper()
	log := filepath.Join(dir, "kagent-url.log")
	path := filepath.Join(dir, "kagent")
	script := "#!/bin/sh\n" +
		"export KMX_FAKE_LEGACY_KAGENT=1\n" +
		"export KMX_FAKE_LEGACY_KAGENT_LOG=" + log + "\n" +
		"exec " + os.Args[0] + " -test.run='^TestLegacyKagentCLIHelper$' -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, log
}

// legacyController is the kagent controller behind the port-forward: it
// records every A2A request body and answers a completed task.
type legacyController struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newLegacyController(t *testing.T) *legacyController {
	t.Helper()
	return newLegacyControllerWith(t, nil)
}

// newLegacyControllerWith is the controller behind the port-forward: it
// records every A2A request body and answers with a completed task unless
// the caller supplies its own answer.
func newLegacyControllerWith(t *testing.T, answer http.HandlerFunc) *legacyController {
	t.Helper()
	c := &legacyController{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			io.WriteString(w, `{}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, string(body))
		c.mu.Unlock()
		if answer != nil {
			answer(w, r)
			return
		}
		if strings.Contains(string(body), `"message/stream"`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"kind\":\"artifact-update\",\"contextId\":\"ctx-1\",\"taskId\":\"task-1\",\"artifact\":{\"parts\":[{\"kind\":\"text\",\"text\":\"hello\"}]}}\n\n")
			io.WriteString(w, "data: {\"kind\":\"status-update\",\"contextId\":\"ctx-1\",\"taskId\":\"task-1\",\"final\":true,\"status\":{\"state\":\"completed\"}}\n\n")
			return
		}
		io.WriteString(w, `{"id":"task-1","kind":"task","contextId":"ctx-1","history":[],"artifacts":[{"parts":[{"kind":"text","text":"hello"}]}],"status":{"state":"completed"}}`)
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *legacyController) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...)
}

func (c *legacyController) port(t *testing.T) string {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(c.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// messageIDOf returns params.message.messageId, or "" when the request never
// carried one.
func messageIDOf(t *testing.T, body string) string {
	t.Helper()
	var envelope struct {
		Params struct {
			Message struct {
				MessageID string `json:"messageId"`
			} `json:"message"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("controller received a body that is not JSON: %s", body)
	}
	return envelope.Params.Message.MessageID
}

// The nonstream seam: `kmx agent chat` runs the pinned CLI, and the request
// that lands on the controller carries a message ID even though the CLI
// never wrote one.
func TestNonstreamChatReachesTheControllerWithAMessageID(t *testing.T) {
	controller := newLegacyController(t)
	dir := t.TempDir()
	kagent, urlLog := fakeLegacyCLI(t, dir)
	fakeTool(t, dir, "kubectl",
		"for arg in \"$@\"; do\n"+
			"  if [ \"$arg\" = port-forward ]; then\n"+
			"    echo 'Forwarding from 127.0.0.1:"+controller.port(t)+" -> 8083'\n"+
			"    sleep 30\n"+
			"    exit 0\n"+
			"  fi\n"+
			"done\n"+
			"echo agent.kagent.dev/hello-world")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_HOME", t.TempDir())
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-kaimahi-p1", ContextSource: config.SourceKubeCtx,
			ChatPort: controller.port(t), KagentBin: kagent},
		Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard},
		Out: io.Discard, Err: io.Discard,
	}

	out, status, err := a.askAgent("hello-world", "hi", "", false, ChatRetryable)
	if err != nil {
		t.Fatalf("askAgent: %v", err)
	}
	if status != 0 {
		t.Fatalf("status=%d, out=%s", status, out)
	}
	seen := controller.seen()
	if len(seen) != 1 {
		t.Fatalf("controller saw %d A2A requests, want 1", len(seen))
	}
	if id := messageIDOf(t, seen[0]); strings.TrimSpace(id) == "" {
		t.Fatalf("the controller received no message ID; legacy chat is still broken:\n%s", seen[0])
	}

	// The CLI was aimed at kmx's compatibility hop, not straight at the
	// forward — and that hop is gone once the chat is.
	given := strings.TrimSpace(readFileString(t, urlLog))
	if given == "" {
		t.Fatal("the CLI was given no --kagent-url")
	}
	if given == controller.URL {
		t.Fatalf("the CLI was pointed straight at the forward: %s", given)
	}
	assertNotListening(t, given)
}

// The streaming seam, through the interactive session adapter: every turn
// the pinned CLI sends reaches the controller with a message ID, and the
// controller's events still render.
func TestInteractiveStreamReachesTheControllerWithAMessageID(t *testing.T) {
	controller := newLegacyController(t)
	dir := t.TempDir()
	kagent, urlLog := fakeLegacyCLI(t, dir)

	endpoint, stop, err := a_legacyChatEndpoint(t, controller.URL)
	if err != nil {
		t.Fatalf("legacyChatEndpoint: %v", err)
	}
	defer stop()

	a := &App{Out: io.Discard, Err: io.Discard}
	session := &kagentRuntimeSession{
		app: a, executable: kagent, name: "agent",
		base: controller.URL, cliBase: endpoint,
		renderer: &chatRenderer{out: io.Discard},
	}
	if err := session.Send(t.Context(), agentruntime.Turn{Message: "hi"}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	seen := controller.seen()
	if len(seen) != 1 {
		t.Fatalf("controller saw %d A2A requests, want 1", len(seen))
	}
	if !strings.Contains(seen[0], `"message/stream"`) {
		t.Fatalf("the streaming path was not exercised: %s", seen[0])
	}
	if id := messageIDOf(t, seen[0]); strings.TrimSpace(id) == "" {
		t.Fatalf("the controller received no message ID on the stream:\n%s", seen[0])
	}
	if given := strings.TrimSpace(readFileString(t, urlLog)); given != endpoint {
		t.Fatalf("the CLI was given %q, want the compatibility endpoint %q", given, endpoint)
	}
	if session.session != "ctx-1" {
		t.Fatalf("session=%q, want the context the controller returned", session.session)
	}
}

// The compatibility hop is opened with the forward and closed with it.
func TestLegacyChatEndpointClosesWithTheForward(t *testing.T) {
	controller := newLegacyController(t)
	endpoint, stop, err := a_legacyChatEndpoint(t, controller.URL)
	if err != nil {
		t.Fatalf("legacyChatEndpoint: %v", err)
	}
	if endpoint == controller.URL || endpoint == "" {
		t.Fatalf("endpoint=%q", endpoint)
	}
	resp, err := http.Post(endpoint+"/api/a2a/kagent/agent/", "application/json", strings.NewReader(legacyCLISendBody))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if id := messageIDOf(t, controller.seen()[0]); strings.TrimSpace(id) == "" {
		t.Fatalf("the endpoint forwarded without a message ID: %s", controller.seen()[0])
	}
	stop()
	assertNotListening(t, endpoint)
	stop() // idempotent: Close runs on every chat exit path
}

// Closing the session releases the hop as well as the forward.
func TestKagentSessionCloseReleasesTheCompatibilityHop(t *testing.T) {
	controller := newLegacyController(t)
	endpoint, stop, err := a_legacyChatEndpoint(t, controller.URL)
	if err != nil {
		t.Fatalf("legacyChatEndpoint: %v", err)
	}
	stopped := false
	session := &kagentRuntimeSession{app: &App{Out: io.Discard}, cliBase: endpoint, closeCLIBase: stop,
		stop: func() { stopped = true }}
	session.Close()
	if !stopped {
		t.Fatal("the forward was not stopped")
	}
	assertNotListening(t, endpoint)
	session.Close()
}

func a_legacyChatEndpoint(t *testing.T, upstream string) (string, func(), error) {
	t.Helper()
	a := &App{Out: io.Discard, Err: io.Discard}
	return a.legacyChatEndpoint(upstream)
}

// The transport retry `chat` has always had must survive the compatibility
// hop. This is the port-forward race the policy exists for: the forward is
// gone, so every invoke fails to reach the controller, and askAgent must
// still make its one attempt plus exactly three retries.
//
// Before the hop, the CLI dialled the forward and saw `connection refused`
// itself. With a hop in the path, an upstream failure rendered as the hop's
// own 502 would be a valid HTTP response — ChatRetryable would never match
// and a recoverable race would become a hard failure on the first try.
func TestTransportRetryStillFiresThroughTheCompatibilityHop(t *testing.T) {
	controller := newLegacyController(t)
	port := controller.port(t)
	// The forward's port, with nothing behind it any more.
	controller.Close()

	dir := t.TempDir()
	kagent, urlLog := fakeLegacyCLI(t, dir)
	fakeTool(t, dir, "kubectl",
		"for arg in \"$@\"; do\n"+
			"  if [ \"$arg\" = port-forward ]; then\n"+
			"    echo 'Forwarding from 127.0.0.1:"+port+" -> 8083'\n"+
			"    sleep 60\n"+
			"    exit 0\n"+
			"  fi\n"+
			"done\n"+
			"echo agent.kagent.dev/hello-world")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_HOME", t.TempDir())
	var errOut bytes.Buffer
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-kaimahi-p1", ContextSource: config.SourceKubeCtx,
			ChatPort: port, KagentBin: kagent},
		Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard},
		Out: io.Discard, Err: &errOut,
	}

	out, _, err := a.askAgent("hello-world", "hi", "", false, ChatRetryable)
	if err != nil {
		t.Fatalf("askAgent: %v", err)
	}
	// The policy is one attempt plus three retries; anything else means the
	// hop changed what the CLI reports.
	if got := strings.Count(strings.TrimSpace(readFileString(t, urlLog)), "\n") + 1; got != 4 {
		t.Fatalf("the CLI was invoked %d times, want 4 (1 + 3 transport retries)\nkagent output:\n%s\nkmx notes:\n%s",
			got, out, errOut.String())
	}
	if got := strings.Count(errOut.String(), "transport error"); got != 3 {
		t.Fatalf("announced %d transport retries, want 3:\n%s", got, errOut.String())
	}
	if !ChatRetryable.MatchString(out) {
		t.Fatalf("the failure the hop produced is not one chat retries:\n%s", out)
	}
}

// A model or tool failure is not a transport failure. The controller's own
// answer — including a JSON-RPC error — is returned once, unretried: a
// second invoke spends budget again and can burn a tool grant.
func TestControllerErrorsAreNotRetriedThroughTheHop(t *testing.T) {
	controller := newLegacyControllerWith(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"model provider refused"}}`)
	})
	dir := t.TempDir()
	kagent, urlLog := fakeLegacyCLI(t, dir)
	fakeTool(t, dir, "kubectl",
		"for arg in \"$@\"; do\n"+
			"  if [ \"$arg\" = port-forward ]; then\n"+
			"    echo 'Forwarding from 127.0.0.1:"+controller.port(t)+" -> 8083'\n"+
			"    sleep 30\n"+
			"    exit 0\n"+
			"  fi\n"+
			"done\n"+
			"echo agent.kagent.dev/hello-world")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_HOME", t.TempDir())
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-kaimahi-p1", ContextSource: config.SourceKubeCtx,
			ChatPort: controller.port(t), KagentBin: kagent},
		Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard},
		Out: io.Discard, Err: io.Discard,
	}
	if _, _, err := a.askAgent("hello-world", "hi", "", false, ChatRetryable); err != nil {
		t.Fatalf("askAgent: %v", err)
	}
	if got := strings.Count(strings.TrimSpace(readFileString(t, urlLog)), "\n") + 1; got != 1 {
		t.Fatalf("an application error was invoked %d times, want 1", got)
	}
}

// Interactive chat holds the alternate screen. The hop must not print to the
// terminal behind the renderer's back, whatever the controller does.
func TestTheCompatibilityHopNeverWritesToTheChatTerminal(t *testing.T) {
	controller := newLegacyController(t)
	upstream := controller.URL
	controller.Close() // dead forward: the noisiest case

	var out, errOut bytes.Buffer
	a := &App{Out: &out, Err: &errOut}
	endpoint, stop, err := a.legacyChatEndpoint(upstream)
	if err != nil {
		t.Fatalf("legacyChatEndpoint: %v", err)
	}
	defer stop()
	if resp, err := http.Post(endpoint+"/api/a2a/kagent/agent/", "application/json", strings.NewReader(legacyCLISendBody)); err == nil {
		resp.Body.Close()
	}
	time.Sleep(50 * time.Millisecond)
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("the hop wrote to the chat terminal:\nstdout=%q\nstderr=%q", out.String(), errOut.String())
	}
}

func assertNotListening(t *testing.T, endpoint string) {
	t.Helper()
	address := strings.TrimPrefix(endpoint, "http://")
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err == nil {
		conn.Close()
		t.Fatalf("%s is still listening after the chat ended", endpoint)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}
