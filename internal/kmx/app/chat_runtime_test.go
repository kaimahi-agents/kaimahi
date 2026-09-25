package app

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// Auto-detection resolves an Orka Agent or fails. It never falls back to
// another platform, and "no Orka on this cluster" is not an Agent: with the
// legacy runtime gone there is nothing left to fall back TO, so an absent
// kind must be reported rather than answered from somewhere else.
func TestInteractiveRuntimeDiscoveryResolvesOrkaAndPreservesFailures(t *testing.T) {
	for _, tc := range []struct {
		name, script, want string
		fail               bool
	}{
		{"orka", `case "$*" in *api-resources*) printf 'agents.core.orka.ai';; *) printf '{"metadata":{"name":"demo","namespace":"orka-system"},"spec":{}}';; esac`, "orka", false},
		{"no orka", `exit 0`, "", true},
		{"read failure", `case "$*" in *api-resources*) printf 'agents.core.orka.ai';; *) exit 1;; esac`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fakeTool(t, dir, "kubectl", tc.script)
			t.Setenv("PATH", dir)
			a := &App{Cfg: &config.Config{KubeContext: "kind-test"}, Run: &run.Runner{}}
			got, _, err := a.resolveInteractiveChat(ChatOptions{}, "demo")
			if got != tc.want || (err != nil) != tc.fail {
				t.Fatalf("runtime=%q err=%v", got, err)
			}
		})
	}
}

func TestAgentChatOrkaUsesWizardShell(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `case "$*" in
 *"config view"*) printf '{"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}],"clusters":[{"name":"kind-test","cluster":{"server":"https://127.0.0.1:6443"}}]}' ;;
 *"agents.core.orka.ai"*) printf '{"metadata":{"name":"demo","namespace":"orka-system"},"spec":{"tools":[]}}' ;;
 *) exit 0 ;;
esac`)
	t.Setenv("PATH", dir)
	in, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	_, _ = in.WriteString("/help\n/verbose-on\n/exit\n")
	_, _ = in.Seek(0, 0)
	var out bytes.Buffer
	a := &App{Cfg: &config.Config{KubeContext: "kind-test"}, Run: &run.Runner{}, Stdin: in, Out: &out, Err: &out}
	err = a.ChatWithOptions(ChatOptions{Agent: "demo", Runtime: "orka", Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Orka Agent/demo", "/tools", "/agent", "/lift", "/inference-copilot", "Verbose: on", "Status: ended"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q:\n%s", want, out.String())
		}
	}
}

func TestSharedChatInitialMessageSentExactlyOnce(t *testing.T) {
	in, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	_, _ = in.WriteString("/exit\n")
	_, _ = in.Seek(0, 0)
	var out bytes.Buffer
	a := &App{Stdin: in, Out: &out, Err: &out}
	backend := &staticChatBackend{}
	if err := a.runInteractiveChatBackendInitial(backend, "hello"); err != nil {
		t.Fatal(err)
	}
	if len(backend.messages) != 1 || backend.messages[0] != "hello" {
		t.Fatalf("messages=%v", backend.messages)
	}
}
