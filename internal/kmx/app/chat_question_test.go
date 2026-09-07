package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The task kagent printed when the e2e-tools shard went red: asked "who are
// you and where are you running?", the agent answered half of it, called the
// runtime's built-in ask_user with "Where are you?" and stopped. There are no
// artifacts, so the reply is empty; the state is input-required. Trimmed to
// the fields the check reads, otherwise verbatim.
const askUserTask = `{"contextId":"ctx-1","history":[` +
	`{"kind":"message","parts":[{"kind":"text","text":"Hello! Who are you and where are you running?"}],"role":"user","taskId":"task-1"},` +
	`{"kind":"message","metadata":{"kagent_author":"hello_world"},"parts":[` +
	`{"kind":"text","text":"I am the \"hello_world\" agent. Here's who I am:\n"},` +
	`{"kind":"data","data":{"args":{"questions":[{"choices":["Kubernetes Cluster","Local Machine"],"multiple":false,"question":"Where are you?"}]},"id":"call_6bgth59d","name":"ask_user"},"metadata":{"kagent_type":"function_call"}}],"role":"agent","taskId":"task-1"},` +
	`{"kind":"message","parts":[{"kind":"data","data":{"args":{"originalFunctionCall":{"args":{"questions":[{"question":"Where are you?"}]},"id":"call_6bgth59d","name":"ask_user"},"toolConfirmation":{"confirmed":false,"hint":"Where are you?"}},"id":"adk-1","name":"adk_request_confirmation"},"metadata":{"kagent_is_long_running":true,"kagent_type":"function_call"}}],"role":"agent","taskId":"task-1"}],` +
	`"id":"task-1","kind":"task","status":{"state":"input-required","message":{"kind":"message","parts":[` +
	`{"kind":"data","data":{"args":{"originalFunctionCall":{"args":{"questions":[{"question":"Where are you?"}]},"id":"call_6bgth59d","name":"ask_user"},"toolConfirmation":{"confirmed":false,"hint":"Where are you?"}},"id":"adk-1","name":"adk_request_confirmation"},"metadata":{"kagent_is_long_running":true,"kagent_type":"function_call"}}],"role":"agent"},"timestamp":"2026-09-07T04:32:46Z"}}`

// The OTHER input-required: a human approval for a real tool call, which the
// interactive chat exists to answer. Same state, same confirmation wrapper,
// and it must never be re-sampled — a second sample throws the decision away
// and may not even produce the call again. Both metadata spellings, because
// kagent renamed the keys.
const approvalTask = `{"id":"task-1","kind":"task","history":[],"status":{"state":"input-required","message":{"kind":"message","parts":[` +
	`{"kind":"data","data":{"args":{"originalFunctionCall":{"args":{"name":"pod-a"},"id":"call-1","name":"delete_pod"}},"id":"adk-1","name":"adk_request_confirmation"},"metadata":{"kagent_is_long_running":true,"kagent_type":"function_call"}}],"role":"agent"}}}`

const approvalTaskADKKeys = `{"id":"task-1","kind":"task","history":[],"status":{"state":"input-required","message":{"kind":"message","parts":[` +
	`{"kind":"data","data":{"args":{"originalFunctionCall":{"args":{},"id":"call-1","name":"delete_pod"}},"id":"adk-1","name":"adk_request_confirmation"},"metadata":{"adk_is_long_running":true,"adk_type":"function_call"}}],"role":"agent"}}}`

// A batched confirmation: the runtime can carry several calls in one HITL
// request. One real tool among the questions makes the whole request an
// approval decision.
const askUserAndApprovalTask = `{"id":"task-1","kind":"task","history":[],"status":{"state":"input-required","message":{"kind":"message","parts":[` +
	`{"kind":"data","data":{"args":{"toolConfirmation":{"payload":{"hitl_parts":[` +
	`{"originalFunctionCall":{"id":"c1","name":"ask_user"}},{"originalFunctionCall":{"id":"c2","name":"delete_pod"}}]}}},"id":"adk-1","name":"adk_request_confirmation"},"metadata":{"kagent_is_long_running":true,"kagent_type":"function_call"}}],"role":"agent"}}}`

// The agent asked a question AFTER a tool call had already run. Re-asking
// would repeat whatever that call did, so it is refused: the re-sample is
// only for an agent that did nothing but ask.
const askUserAfterAToolResponseTask = `{"id":"task-1","kind":"task","history":[` +
	`{"kind":"message","parts":[{"kind":"data","data":{"id":"c0","name":"k8s_get_resources","args":{}},"metadata":{"kagent_type":"function_call"}}],"role":"agent"},` +
	`{"kind":"message","parts":[{"kind":"data","data":{"id":"c0","name":"k8s_get_resources","response":{"isError":false}},"metadata":{"kagent_type":"function_response"}}],"role":"agent"}],` +
	`"status":{"state":"input-required","message":{"kind":"message","parts":[` +
	`{"kind":"data","data":{"args":{"originalFunctionCall":{"id":"c1","name":"ask_user"}},"id":"adk-1","name":"adk_request_confirmation"},"metadata":{"kagent_is_long_running":true,"kagent_type":"function_call"}}],"role":"agent"}}}`

const completedTask = `{"id":"task-1","kind":"task","history":[],"artifacts":[{"parts":[{"kind":"text","text":"Hello! I am a declarative kagent agent."}]}],"status":{"state":"completed"}}`

func TestOnlyAQuestionAndNothingElseIsReSampled(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want bool
	}{
		{"the agent asked the user a question", askUserTask, true},
		{"a real approval decision", approvalTask, false},
		{"a real approval decision, adk_* metadata keys", approvalTaskADKKeys, false},
		{"a question batched with a real approval", askUserAndApprovalTask, false},
		{"a question after a tool call already ran", askUserAfterAToolResponseTask, false},
		{"a completed answer", completedTask, false},
		{"a transport error, no task at all", refusedLine, false},
		{"nothing kagent printed", "", false},
		{"a task that does not parse", "{not json}", false},
	} {
		if got := agentAskedTheUser(tc.out); got != tc.want {
			t.Errorf("%s: re-sample=%v, want %v", tc.name, got, tc.want)
		}
	}
}

// An explicit --session is excluded: the question is pending IN that session,
// and a fresh message there could be read as its answer.
func TestAResumedSessionIsNeverReSampled(t *testing.T) {
	if !chatShouldResample("", askUserTask) {
		t.Error("a one-shot chat must re-sample the question")
	}
	if chatShouldResample("session-1", askUserTask) {
		t.Error("a resumed session must not be re-sampled")
	}
}

// The re-sample proven end to end, through askAgent itself: a stub kagent
// that always asks the question is invoked once per allowed attempt and no
// more, every re-ask is announced, and the last task is returned unchanged —
// an unanswered question never becomes an answer.
func TestTheReSampleFiresAnnouncesItselfAndStops(t *testing.T) {
	if chatQuestionResamples < 1 || chatQuestionResamples > 3 {
		t.Fatalf("re-samples must stay a small bound, got %d", chatQuestionResamples)
	}
	for _, tc := range []struct {
		name          string
		task          string
		wantInvokes   int
		wantAnnounced int
	}{
		{"a question nothing can answer is re-sampled to the bound", askUserTask, chatQuestionResamples + 1, chatQuestionResamples},
		{"a real approval decision is invoked once and left alone", approvalTask, 1, 0},
		{"a completed answer is invoked once", completedTask, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errOut bytes.Buffer
			a, attempts := stubbedChat(t, tc.task, &errOut)
			out, status, err := a.askAgent("hello-world", "hi", "", false)
			if err != nil {
				t.Fatalf("askAgent: %v\n%s", err, errOut.String())
			}
			if status != 0 {
				t.Errorf("status=%d, want 0", status)
			}
			if got := countLines(t, attempts); got != tc.wantInvokes {
				t.Errorf("kagent invoked %d times, want %d", got, tc.wantInvokes)
			}
			if got := strings.Count(errOut.String(), "chat re-sample"); got != tc.wantAnnounced {
				t.Errorf("announced %d re-samples, want %d:\n%s", got, tc.wantAnnounced, errOut.String())
			}
			if !strings.Contains(out, tc.task) {
				t.Errorf("the task was not returned unchanged:\n%s", out)
			}
			if tc.wantAnnounced > 0 && !strings.Contains(errOut.String(), "--interactive") {
				t.Errorf("giving up must point at the chat that CAN answer:\n%s", errOut.String())
			}
		})
	}
}

// stubbedChat gives askAgent a kubectl that answers the pre-flight checks and
// holds a port-forward open, and a kagent that records each invocation and
// prints the given task. It returns the App and the path the invocations are
// recorded in.
func stubbedChat(t *testing.T, task string, errOut *bytes.Buffer) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	attempts := filepath.Join(dir, "attempts")
	taskFile := filepath.Join(dir, "task.json")
	if err := os.WriteFile(taskFile, []byte(task+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(name, script string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("kubectl", "#!/bin/sh\n"+
		"for arg in \"$@\"; do\n"+
		"  if [ \"$arg\" = port-forward ]; then\n"+
		"    echo 'Forwarding from 127.0.0.1:18083 -> 8083'\n"+
		"    sleep 30\n"+
		"    exit 0\n"+
		"  fi\n"+
		"done\n"+
		"echo agent.kagent.dev/hello-world\n")
	kagent := write("kagent", "#!/bin/sh\necho x >> "+attempts+"\ncat "+taskFile+"\n")
	// The stubs come FIRST so they shadow any real kubectl; the rest of PATH
	// stays so the stub scripts themselves have a shell to run in.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_HOME", t.TempDir())
	return &App{
		Cfg: &config.Config{KubeContext: "kind-kaimahi-p1", ContextSource: config.SourceKubeCtx,
			ChatPort: "18083", KagentBin: kagent},
		Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard},
		Out: io.Discard, Err: errOut,
	}, attempts
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(body), "\n")
}
