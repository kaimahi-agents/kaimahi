package app

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// A real task, trimmed to the fields the renderer reads: the shape kagent
// actually prints (PR #24's, the same one scripts/verify-chat.py fixtures).
const toolTask = `{"artifacts":[{"artifactId":"a1","parts":[{"kind":"text","text":"There are two pods running in the ollama namespace."}]}],` +
	`"history":[` +
	`{"role":"agent","metadata":{"kagent_usage_metadata":{"promptTokenCount":690,"candidatesTokenCount":32,"totalTokenCount":722}},` +
	`"parts":[{"kind":"data","data":{"name":"k8s_get_resources","args":{"namespace":"ollama"},"id":"c1"}}]},` +
	`{"role":"agent","parts":[{"kind":"data","data":{"name":"k8s_get_resources","id":"c1","response":{"isError":false}}}]},` +
	`{"role":"agent","metadata":{"kagent_usage_metadata":{"promptTokenCount":931,"candidatesTokenCount":80,"totalTokenCount":1011}},` +
	`"parts":[{"kind":"text","text":"There are two pods running in the ollama namespace."}]}` +
	`],"status":{"state":"completed"}}`

const plainTask = `{"artifacts":[{"artifactId":"a1","parts":[{"kind":"text","text":"I am the hello_world agent."}]}],` +
	`"history":[],"status":{"state":"completed"}}`

func render(t *testing.T, combined string) (string, bool) {
	t.Helper()
	var buf bytes.Buffer
	ok := renderChat(&buf, combined)
	return buf.String(), ok
}

func TestRenderShowsTheReply(t *testing.T) {
	out, ok := render(t, plainTask)
	if !ok {
		t.Fatal("a well-formed task must render")
	}
	if !strings.Contains(out, "I am the hello_world agent.") {
		t.Errorf("the reply is the point of the command; got:\n%s", out)
	}
	// The reply must not be buried in the payload the user was trying to escape.
	if strings.Contains(out, "artifactId") || strings.Contains(out, `"kind"`) {
		t.Errorf("rendered view still contains raw JSON:\n%s", out)
	}
}

func TestRenderSanitizesTerminalControlSequences(t *testing.T) {
	task := `{"artifacts":[{"parts":[{"kind":"text","text":"safe\u001b]52;c;secret\u0007\u001b[2Jtext"}]}],"status":{"state":"working\u001b[2J"}}`
	out, ok := render(t, task)
	if !ok {
		t.Fatal("task with a readable reply must render")
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "secret") {
		t.Fatalf("terminal control sequence survived human rendering: %q", out)
	}
	for _, want := range []string{"safetext", "state: working"} {
		if !strings.Contains(out, want) {
			t.Errorf("sanitized output lacks %q: %q", want, out)
		}
	}
}

func TestRenderNamesToolsAndTokens(t *testing.T) {
	out, ok := render(t, toolTask)
	if !ok {
		t.Fatal("the tool task must render")
	}
	if !strings.Contains(out, "k8s_get_resources") {
		t.Errorf("a tool call is worth surfacing; got:\n%s", out)
	}
	// 690+931 in, 32+80 out — summed across turns, not just the last.
	if !strings.Contains(out, "1621 in") || !strings.Contains(out, "112 out") {
		t.Errorf("token totals wrong; got:\n%s", out)
	}
	// One call and its response must not read as two calls.
	if strings.Count(out, "k8s_get_resources") != 1 {
		t.Errorf("the call/response pair must count once; got:\n%s", out)
	}
}

func TestRenderMarksAFailedToolCall(t *testing.T) {
	failed := strings.Replace(toolTask, `"isError":false`, `"isError":true`, 1)
	out, ok := render(t, failed)
	if !ok {
		t.Fatal("must still render")
	}
	if !strings.Contains(out, "(failed)") {
		t.Errorf("a failed tool call must say so — 'it called the tool' and "+
			"'the tool worked' are different facts; got:\n%s", out)
	}
}

func TestRenderSurfacesANonCompletedState(t *testing.T) {
	working := strings.Replace(plainTask, `"state":"completed"`, `"state":"working"`, 1)
	out, _ := render(t, working)
	if !strings.Contains(out, "state: working") {
		t.Errorf("an unfinished task must say so; got:\n%s", out)
	}
	// "completed" is the expected case and saying it adds noise.
	if out, _ := render(t, plainTask); strings.Contains(out, "state: completed") {
		t.Errorf("the expected state should not be announced; got:\n%s", out)
	}
}

func TestRenderRefusesWhatItDoesNotRecognise(t *testing.T) {
	for _, in := range []string{
		"",
		"Error invoking session: dial tcp: connection refused",
		"not json at all",
		`{"status":{"state":"completed"}}`, // a task with nothing to show
		`{"broken":`,
	} {
		if _, ok := render(t, in); ok {
			t.Errorf("must fall back to raw output for %q", in)
		}
	}
}

func TestRenderTakesTheLastTaskAfterARetry(t *testing.T) {
	// chat retries transport failures, so the buffer can hold a failure
	// line and then the task that was actually answered.
	combined := "Error invoking session: EOF\n" + plainTask
	out, ok := render(t, combined)
	if !ok || !strings.Contains(out, "I am the hello_world agent.") {
		t.Errorf("the answered task must win; got:\n%s", out)
	}
}

// The guarantee CI depends on: anything that is not a terminal gets bytes.
func TestNonTerminalWritersAreNeverPrettyPrinted(t *testing.T) {
	if isTerminal(&bytes.Buffer{}) {
		t.Fatal("a buffer must not be treated as a terminal")
	}
	r, w, err := osPipe()
	if err != nil {
		t.Skip("no pipe available")
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(w) {
		t.Error("a pipe must not be treated as a terminal — CI pipes this " +
			"output into verify-chat.py and parses it")
	}
}

func osPipe() (*os.File, *os.File, error) { return os.Pipe() }
