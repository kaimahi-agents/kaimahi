package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

type staticChatBackend struct {
	messages []string
}

type waitingChatBackend struct{}

func (waitingChatBackend) Agent() string { return "orka-agent" }
func (waitingChatBackend) Connect(context.Context, *chatRenderer) ([]cliui.Field, error) {
	return nil, nil
}
func (waitingChatBackend) Send(context.Context, string, *chatRenderer) error {
	time.Sleep(350 * time.Millisecond)
	return nil
}

func TestSharedInteractiveChatBackendAnimatesWhileWaitingAndClears(t *testing.T) {
	var out bytes.Buffer
	renderer := &chatRenderer{out: &out, cursor: true, color: false, verbose: true}
	if err := sendInteractiveChatMessage(context.Background(), waitingChatBackend{}, "hello", renderer); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "WORKING orka-agent") || !strings.Contains(text, "\r\x1b[2K") || !strings.Contains(text, "Responded in") || renderer.transient {
		t.Fatalf("spinner did not animate and clear: %q transient=%v", text, renderer.transient)
	}
}

type failedChatBackend struct{ err error }

func (b failedChatBackend) Agent() string { return "failed" }
func (b failedChatBackend) Connect(context.Context, *chatRenderer) ([]cliui.Field, error) {
	return nil, nil
}
func (b failedChatBackend) Send(context.Context, string, *chatRenderer) error { return b.err }

func TestChatResponseTimingOnlyFollowsSuccessfulRequests(t *testing.T) {
	for _, err := range []error{errors.New("request failed"), context.Canceled} {
		var out bytes.Buffer
		r := newChatRenderer(&out)
		if got := sendInteractiveChatMessage(context.Background(), failedChatBackend{err}, "hello", r); got != err {
			t.Fatalf("err=%v", got)
		}
		if strings.Contains(out.String(), "Responded in") {
			t.Fatalf("failed request got success timing: %s", out.String())
		}
	}
}

func TestChatRendererFullScreenLifecycleAndDeploymentHeader(t *testing.T) {
	var out bytes.Buffer
	r := &chatRenderer{out: &out, cursor: true, ui: cliui.WithCapabilities(cliui.Capabilities{Rich: true, Color: false, Width: 80})}
	r.enterFullScreen()
	r.statusStart("agent", "kind-test")
	r.statusSection("Deployment", "Orka Agent/agent | namespace orka-system")
	r.statusEnd()
	r.leaveFullScreen()
	text := out.String()
	for _, want := range []string{"\x1b[?1049h", "KMX  /  INTERACTIVE CHAT", "Deployment", "Orka Agent/agent", "\x1b[?1049l"} {
		if !strings.Contains(text, want) {
			t.Fatalf("fullscreen chat missing %q: %q", want, text)
		}
	}
}

func (b *staticChatBackend) Agent() string { return "shared-agent" }
func (b *staticChatBackend) Connect(_ context.Context, renderer *chatRenderer) ([]cliui.Field, error) {
	renderer.statusStart(b.Agent(), "kind-test")
	return []cliui.Field{{Label: "Deployment", Value: "test deployment"}, {Label: "Runtime", Value: "test backend"}}, nil
}
func (b *staticChatBackend) Send(_ context.Context, message string, renderer *chatRenderer) error {
	b.messages = append(b.messages, message)
	renderer.beginAssistant(b.Agent())
	renderer.assistant(b.Agent(), "reply to "+message, true)
	return nil
}

func TestSharedInteractiveChatShellDispatchesBackendRetryAndExit(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "chat-input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.WriteString("hello\n/retry\n/help\n/exit\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	backend := &staticChatBackend{}
	a := &App{Stdin: input, Out: &out, Err: &out}
	if err := a.runInteractiveChatBackend(backend); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(backend.messages, ","); got != "hello,hello" {
		t.Fatalf("backend messages=%q", got)
	}
	text := out.String()
	for _, want := range []string{"shared-agent", "test backend", "reply to hello", "[CHAT HELP]", "Commands: /help /retry /exit", "Status: ended"} {
		if !strings.Contains(text, want) {
			t.Fatalf("shared chat output missing %q:\n%s", want, text)
		}
	}
}

type verboseChatBackend struct {
	staticChatBackend
	verbose []bool
}

func (b *verboseChatBackend) Send(ctx context.Context, message string, r *chatRenderer) error {
	b.verbose = append(b.verbose, r.verboseEnabled())
	r.assistantOperation(b.Agent(), "WORKING", "", colorBlue, "processing "+message)
	r.assistantOperation(b.Agent(), "TIMING", "", colorBlue, "timing "+message)
	return b.staticChatBackend.Send(ctx, message, r)
}

func TestChatVerboseTogglesAreLocalAndApplyToFollowingTurns(t *testing.T) {
	for _, initial := range []bool{false, true} {
		input, err := os.CreateTemp(t.TempDir(), "input")
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		_, _ = input.WriteString("first\n/verbose-on\nsecond\n/verbose-off\nthird\n/exit\n")
		_, _ = input.Seek(0, 0)
		var out bytes.Buffer
		a := &App{Stdin: input, Out: &out, Err: &out, chatVerbose: initial}
		backend := &verboseChatBackend{}
		if err := a.runInteractiveChatBackend(backend); err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(backend.verbose); got != fmt.Sprint([]bool{initial, true, false}) || strings.Join(backend.messages, ",") != "first,second,third" {
			t.Fatalf("verbose=%v messages=%v", backend.verbose, backend.messages)
		}
		text := out.String()
		if strings.Contains(text, "processing first") != initial || !strings.Contains(text, "processing second") || !strings.Contains(text, "timing second") || strings.Contains(text, "processing third") || strings.Contains(text, "timing third") {
			t.Fatalf("verbose output mismatch: %s", text)
		}
		if strings.Count(text, "Responded in") != 3 {
			t.Fatalf("brief response time disappeared: %s", text)
		}
	}
}

func TestChatWorkingAndTimingDefaultToHidden(t *testing.T) {
	var out bytes.Buffer
	r := &chatRenderer{out: &out, cursor: true, ui: cliui.WithCapabilities(cliui.Capabilities{Rich: true, Width: 80})}
	r.working("Connecting")
	r.operation("WORKING", "", colorBlue, "working")
	r.assistantOperation("agent", "WORKING", "", colorBlue, "working")
	r.assistantOperation("agent", "TIMING", "", colorBlue, "timing")
	if out.Len() != 0 {
		t.Fatalf("default chat emitted verbose details: %q", out.String())
	}
	r.spinner("agent", "|", time.Second)
	if !strings.Contains(out.String(), "RESPONDING agent | 1s") || !r.transient {
		t.Fatalf("normal mode lost responding animation: %q", out.String())
	}
	r.verbose = true
	r.spinner("agent", "|", time.Second)
	r.setVerbose(false)
	if r.transient {
		t.Fatal("disabling verbose left a spinner")
	}
}

type switchingChatBackend struct {
	staticChatBackend
	connections int
}

func (b *switchingChatBackend) Configure(context.Context, string, *chatRenderer) (bool, error) {
	return true, nil
}

func (b *switchingChatBackend) ChatCommands() []slashCommand {
	return append(commonChatCommands(), slashCommand{name: "/agent", usage: "/agent — switch test agent"})
}
func (b *switchingChatBackend) Connect(ctx context.Context, renderer *chatRenderer) ([]cliui.Field, error) {
	b.connections++
	return b.staticChatBackend.Connect(ctx, renderer)
}

func TestChatAgentSwitchResetsRetryAndReconnects(t *testing.T) {
	in, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	_, _ = in.WriteString("hello\n/agent\n/retry\n/exit\n")
	_, _ = in.Seek(0, 0)
	var out bytes.Buffer
	a := &App{Stdin: in, Out: &out, Err: &out}
	b := &switchingChatBackend{}
	if err := a.runInteractiveChatBackend(b); err != nil {
		t.Fatal(err)
	}
	if b.connections != 2 || len(b.messages) != 1 || !strings.Contains(out.String(), "no previous message") {
		t.Fatalf("connections=%d messages=%v output=%s", b.connections, b.messages, out.String())
	}
}

// chatUXFixture is an App wired for the SHARED terminal driver and nothing
// else. It deliberately carries no cluster fixture: every remaining caller
// exercises input handling, dispatch and rendering, which the driver owns.
func chatUXFixture(t *testing.T) *App {
	t.Helper()
	return &App{
		Cfg: &config.Config{KubeContext: "kind-test"},
		Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard},
		Err: io.Discard,
	}
}
