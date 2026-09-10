package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func TestChatStatusContextAndCommandGroups(t *testing.T) {
	for _, rich := range []bool{false, true} {
		var out bytes.Buffer
		r := &chatRenderer{out: &out, cursor: rich, ui: cliui.WithCapabilities(cliui.Capabilities{Rich: rich, Width: 50})}
		r.statusStart("agent", "kind-test\x1b[2J\nforged")
		text := out.String()
		if !strings.Contains(text, "kind-test forged\n") || strings.Contains(text, "\x1b") {
			t.Fatalf("unsafe/missing context: %q", text)
		}
		if !rich {
			want := "CHAT STATUS\n------------\n  Agent: agent\n  Context: kind-test forged\n  Commands: " + slashCommandSummary() + "\n"
			if text != want {
				t.Fatalf("plain transcript changed beyond context: %q", text)
			}
			continue
		}
		if text != "agent\nContext: kind-test forged\n" {
			t.Fatalf("startup should lead with agent/context only: %q", text)
		}
		r.statusModel("test", true)
		r.statusSection("Tools", "none")
		r.statusEnd()
		if strings.Contains(out.String(), "/resume") || !strings.Contains(out.String(), "/help for commands") {
			t.Fatalf("startup should show a short hint: %q", out.String())
		}
		out.Reset()
		r.help()
		text = out.String()
		for _, group := range []string{"Session:", "Display:", "Governance:", "Conversation:"} {
			if !strings.Contains(text, group) {
				t.Fatalf("missing %s: %q", group, text)
			}
		}
		for _, line := range strings.Split(text, "\n") {
			if lipgloss.Width(line) > 50 {
				t.Fatalf("status overflow: %q", line)
			}
		}
		for _, command := range slashCommandList {
			if !strings.Contains(strings.Join(strings.Fields(text), " "), command.usage) {
				t.Fatalf("missing command %s", command.usage)
			}
		}
	}
}

func TestChatStatusIdentityAndUnknownWidth(t *testing.T) {
	for _, width := range []int{0, 20, 100} {
		for _, color := range []bool{false, true} {
			var out bytes.Buffer
			r := &chatRenderer{out: &out, color: color, ui: cliui.WithCapabilities(cliui.Capabilities{Rich: true, Color: color, Width: width})}
			r.statusStart("agent\x1b[2J", "kind-test\x1b[31m")
			lines := strings.Split(out.String(), "\n")
			if ansi.Strip(lines[0]) != "agent" || lines[1] != r.ui.Muted("Context: kind-test") {
				t.Fatalf("identity/context styling: %q", out.String())
			}
			if color && lines[0] != lipgloss.NewStyle().Bold(true).Render("agent") {
				t.Fatalf("agent should be bold without semantic color: %q", lines[0])
			}
			r.statusEnd()
			hint := "Type a message. /help for commands; /exit to leave"
			if width > 0 {
				hint = ansi.Hardwrap(hint, width, true)
			}
			if !strings.Contains(out.String(), hint) {
				t.Fatalf("startup hint lost/wrapped incorrectly: %q", out.String())
			}
			r.help()
			if width == 0 && !strings.Contains(out.String(), "/tools off|summary|verbose") {
				t.Fatalf("unknown width fragmented help: %q", out.String())
			}
			if !color && strings.Contains(out.String(), "\x1b") {
				t.Fatalf("no-color startup emitted escapes: %q", out.String())
			}
			for _, line := range strings.Split(out.String(), "\n") {
				if width > 0 && lipgloss.Width(line) > width {
					t.Fatalf("width %d overflow: %q", width, line)
				}
			}
		}
	}
}

func TestChatModelStyleUsesVerifiedPostureNotModelName(t *testing.T) {
	for _, color := range []bool{false, true} {
		for _, governed := range []bool{false, true} {
			var out bytes.Buffer
			r := &chatRenderer{out: &out, color: color, ui: cliui.WithCapabilities(cliui.Capabilities{Rich: true, Color: color, Width: 0})}
			model := "governed-ready-direct\x1b[32m\nforged"
			r.statusModel(model, governed)
			posture := r.ui.Warning("direct, not Kaimahi-governed") + "; model plane not used"
			if governed {
				posture = r.ui.Success("governed by Kaimahi; plane Ready")
			}
			want := r.ui.Fields([]cliui.Field{{Label: "Model", Value: "governed-ready-direct forged | " + posture}}) + "\n"
			if out.String() != want {
				t.Fatalf("dynamic name affected trusted status styling: %q; want %q", out.String(), want)
			}
			if color && !strings.Contains(out.String(), "\x1b") {
				t.Fatal("trusted style was sanitized away")
			}
		}
	}
}

func TestChatSessionsHTTPFixtures(t *testing.T) {
	// The snake_case agent_id matches the session history HTTP fixture.
	item := `{"id":"session-1","name":"hello\u001b[2J","agent_id":"kagent__NS__hello_world"}`
	for _, tc := range []struct {
		body, want string
		bad        bool
	}{
		{`{"data":[` + item + `]}`, "SESSION session-1\n  Agent: hello-world\n  Name: hello\n\n", false},
		{`{"data":{"sessions":[` + item + `]}}`, "SESSION session-1\n  Agent: hello-world\n  Name: hello\n\n", false},
		{`{"data":[]}`, "[CHAT]\n  Sessions: none\n\n", false},
		{`{"data":null}`, "[CHAT]\n  Sessions: none\n\n", false},
		{`{"data":{"sessions":[]}}`, "[CHAT]\n  Sessions: none\n\n", false},
		{`{"data":{}}`, "", true},
		{`{"data":{"sessions":{}}}`, "", true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/sessions" || r.Header.Get("x-user-id") != "admin@kagent.dev" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			var out bytes.Buffer
			a := &App{Out: &out}
			r := &chatRenderer{out: &out}
			r.assistant("agent", "durable", true)
			err := a.showSessions(server.URL, r)
			if (err != nil) != tc.bad {
				t.Fatalf("error: %v", err)
			}
			if out.String() != "AGENT (agent)\n  | durable\n\n"+tc.want || r.openActor != "" {
				t.Fatalf("session lifecycle/output: %q", out.String())
			}
		})
	}
}

func TestChatHistoryReplayAndActiveRenderer(t *testing.T) {
	for _, mode := range []string{"off", "summary", "verbose"} {
		t.Run(mode, func(t *testing.T) {
			call := `{"author":"agent","content":{"role":"model","parts":[{"function_call":{"id":"call-1","name":"read","args":{}}}]}}`
			result := `{"author":"agent","content":{"role":"user","parts":[{"function_response":{"id":"call-1","name":"read","response":{"text":"result"}}}]}}`
			var events []map[string]string
			for _, data := range []string{call, call, result, result, strings.Replace(call, "call-1", "call-2", 1), strings.Replace(result, "result", "changed", 1), `{"author":"agent","content":{"role":"agent","parts":[{"text":"last"}]}}`} {
				events = append(events, map[string]string{"data": data, "created_at": "2026-09-08T00:00:00Z"})
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/sessions/session-1" {
					t.Errorf("path: %s", r.URL.Path)
				}
				json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"session": map[string]string{"agent_id": "kagent__NS__agent"}, "events": events}})
			}))
			defer server.Close()
			var out bytes.Buffer
			a := &App{Out: &out}
			r := &chatRenderer{out: &out}
			r.assistant("agent", "before", true)
			if err := a.showSessionHistory(server.URL, "session-1", "agent", mode, r); err != nil {
				t.Fatal(err)
			}
			r.prompt()
			text := out.String()
			if !strings.Contains(text, "  | before\n\nHISTORY") || !strings.HasSuffix(text, "  | last\n\nYOU > ") {
				t.Fatalf("unclosed actor: %q", text)
			}
			want := 2
			if mode == "off" {
				want = 0
			}
			if strings.Count(text, "[TOOL CALL]") != want || strings.Count(text, "[TOOL RESULT]") != want {
				t.Fatalf("replay/different payloads lost: %s", text)
			}
		})
	}
}

func TestChatOrdinaryToolStreamReplay(t *testing.T) {
	for _, mode := range []string{"summary", "verbose"} {
		var out bytes.Buffer
		r := &chatRenderer{out: &out}
		v := newStreamView("agent", mode, r, nil)
		call := json.RawMessage(`{"id":"one","name":"read","args":{}}`)
		result := json.RawMessage(`{"id":"one","name":"read","response":{"content":"result"}}`)
		for range 2 {
			v.consumeTool("function_call", false, call, &out)
			v.consumeTool("function_response", false, result, &out)
		}
		v.consumeTool("function_call", false, json.RawMessage(`{"id":"two","name":"read","args":{}}`), &out)
		v.consumeTool("function_response", false, json.RawMessage(`{"id":"one","name":"read","response":{"content":"changed"}}`), &out)
		r.finish()
		if strings.Count(out.String(), "[TOOL CALL]") != 2 || strings.Count(out.String(), "[TOOL RESULT]") != 2 {
			t.Fatalf("ordinary replay: %s", out.String())
		}
	}
}

func TestChatSubmittedPipePreservesDurableTranscript(t *testing.T) {
	for _, cursor := range []bool{false, true} {
		in, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer in.Close()
		io.WriteString(writer, "hello\n")
		writer.Close()
		var out bytes.Buffer
		r := &chatRenderer{out: &out, cursor: cursor}
		r.assistant("agent", "previous", true)
		r.prompt()
		input := newChatInput(bufio.NewScanner(in), in, &out, r)
		if line, err := input.readLine(context.Background(), true); err != nil || line != "hello" {
			t.Fatalf("pipe input: %q %v", line, err)
		}
		r.submitted(isInteractiveTerminal(in))
		r.submitted(false)
		r.beginAssistant("agent")
		r.assistant("agent", "next", true)
		r.clearTransient()
		r.finish()
		want := "AGENT (agent)\n  | previous\n\nYOU > \nAGENT (agent)\n  | next\n\n"
		if out.String() != want {
			t.Fatalf("piped submission damaged transcript: %q", out.String())
		}
	}
}

func TestChatTerminalInputRedirectedTranscriptClosesPrompt(t *testing.T) {
	var out bytes.Buffer
	r := newChatRenderer(&out)
	r.prompt()
	r.submitted(true)
	r.assistant("agent", "reply", true)
	r.finish()
	if out.String() != "YOU > \nAGENT (agent)\n  | reply\n\n" {
		t.Fatalf("terminal echo was assumed in redirected stdout: %q", out.String())
	}
}

// All external commands are stubs; unexpected commands fail rather than reaching a cluster.
func chatUXFixture(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `case "$*" in
  *port-forward*) printf 'Forwarding from 127.0.0.1:18083 -> 8083\n'; exec sleep 30 ;;
  *"get agents.kagent.dev agent -o name"*|*"get --raw"*) exit 0 ;;
  *"get agents.kagent.dev agent -o json"*) printf '%s' '{"metadata":{"generation":1},"spec":{"declarative":{"modelConfig":"model"}},"status":{"observedGeneration":1}}' ;;
  *"get deployment agent"*) printf '%s' '{"metadata":{"generation":1},"spec":{"replicas":1},"status":{"observedGeneration":1,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1}}' ;;
  *"get deploy/agent"*) printf '1' ;;
  *"get rs "*) printf '1 hash' ;;
  *"get pods "*) printf 'hash' ;;
  *"get modelconfig model"*) printf '%s' "$KMX_TEST_CHAT_MODEL" ;;
  *"get deployment kaimahi-proxy"*) printf '%s' '{"spec":{"replicas":1},"status":{"readyReplicas":1}}' ;;
  *"get endpoints kaimahi-proxy"*) printf '%s' '{"subsets":[{"addresses":[{}]}]}' ;;
  *) exit 99 ;;
esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TEST_CHAT_MODEL", `{"metadata":{"generation":1},"spec":{},"status":{"observedGeneration":1,"conditions":[{"type":"Accepted","status":"True"}]}}`)
	return &App{Cfg: &config.Config{KubeContext: "kind-test", ChatPort: "18083"}, Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard}, Err: io.Discard}
}

func TestChatHelpExecutesWithoutInvokingAgent(t *testing.T) {
	a := chatUXFixture(t)
	in, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	io.WriteString(writer, "/help\n/exit\n")
	writer.Close()
	var out bytes.Buffer
	a.Stdin, a.Out = in, &out
	if err := a.interactiveChat("/must-not-invoke", "agent", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[CHAT HELP]", "Session:", "Display:", "Governance:", "Conversation:", "Status: ended"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("/help dispatch lacks %q: %s", want, out.String())
		}
	}
}

func TestChatCompactPostureScopesGovernance(t *testing.T) {
	for _, governed := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "governed"}[governed], func(t *testing.T) {
			a := chatUXFixture(t)
			if governed {
				t.Setenv("KMX_TEST_CHAT_MODEL", `{"metadata":{"generation":1},"spec":{"openAI":{"baseUrl":"http://kaimahi-proxy.kaimahi:8080/upstream/model"}},"status":{"observedGeneration":1,"conditions":[{"type":"Accepted","status":"True"}]}}`)
			}
			var out bytes.Buffer
			r := &chatRenderer{out: &out, ui: cliui.WithCapabilities(cliui.Capabilities{Rich: true, Width: 100})}
			posture, err := a.refreshChatPosture("agent", r)
			if err != nil || posture.modelGoverned != governed {
				t.Fatalf("posture: %+v %v", posture, err)
			}
			text := out.String()
			want := "model | direct, not Kaimahi-governed; model plane not used"
			if governed {
				want = "model | governed by Kaimahi; plane Ready"
			}
			if !strings.Contains(text, want) || !strings.Contains(text, "Tools  none") || strings.Contains(text, "Name:") {
				t.Fatalf("posture not compact/scoped: %s", text)
			}
		})
	}
}
