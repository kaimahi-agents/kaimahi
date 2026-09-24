package app

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestInteractiveRuntimeDiscoveryPrefersOrkaAndPreservesFailures(t *testing.T) {
	for _, tc := range []struct {
		name, script, want string
		fail               bool
	}{
		{"orka", `case "$*" in *api-resources*) printf 'agents.core.orka.ai';; *) printf '{"metadata":{"name":"demo","namespace":"orka-system"},"spec":{}}';; esac`, "orka", false},
		{"no orka", `exit 0`, "kagent", false},
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

// PR #197 regression, unchanged by Task 6: chat's Agent-level discovery must
// keep refusing an Orka Agent that names an external CLI runtime (spec.runtime
// set) rather than treating it as a chattable Orka AI agent. This exercises
// orkaRuntimeAdapter.Probe directly — the exact chat/session discovery path
// DESIGN.md §1 keeps untouched by W94's lifecycle additions — not through
// list/show/status, which never call Probe at all.
func TestChatDiscoveryRejectsAnOrkaExternalRuntimeAgent(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `case "$*" in
 *api-resources*) printf 'agents.core.orka.ai';;
 *) printf '{"metadata":{"name":"demo","namespace":"orka-system","uid":"agent-uid"},"spec":{"runtime":{"type":"cli","command":["agent"]}}}';;
esac`)
	t.Setenv("PATH", dir)
	a := &App{Cfg: &config.Config{KubeContext: "kind-test"}, Run: &run.Runner{}}
	adapter := orkaRuntimeAdapter{app: a}
	probe, err := adapter.Probe(context.Background(), agentruntime.Target{Context: "kind-test", Namespace: "orka-system", Name: "demo"})
	if err == nil {
		t.Fatal("an external-runtime Orka Agent was accepted for chat discovery")
	}
	if !strings.Contains(err.Error(), "external CLI runtime") {
		t.Fatalf("the refusal does not name the reason: %v", err)
	}
	if probe.Found {
		t.Fatalf("a refused Agent was still reported Found: %+v", probe)
	}
}

// A regular Orka AI Agent (no spec.runtime) remains chattable — the healthy
// counterpart to the refusal above, proving Task 6 changed neither branch.
func TestChatDiscoveryAcceptsAPlainOrkaAgent(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "kubectl", `case "$*" in
 *api-resources*) printf 'agents.core.orka.ai';;
 *) printf '{"metadata":{"name":"demo","namespace":"orka-system","uid":"agent-uid"},"spec":{}}';;
esac`)
	t.Setenv("PATH", dir)
	a := &App{Cfg: &config.Config{KubeContext: "kind-test"}, Run: &run.Runner{}}
	adapter := orkaRuntimeAdapter{app: a}
	probe, err := adapter.Probe(context.Background(), agentruntime.Target{Context: "kind-test", Namespace: "orka-system", Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if !probe.Found || probe.Agent.Runtime != agentruntime.Orka || probe.Agent.UID != "agent-uid" {
		t.Fatalf("probe = %+v, err=%v", probe, err)
	}
}
