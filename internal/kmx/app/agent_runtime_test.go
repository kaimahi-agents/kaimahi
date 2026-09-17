package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

func runtimeFixture(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_CALLS"
case "$*" in
  *"api-resources"*)
    [ "$KMX_TEST_ORKA" = present ] && printf 'agents.core.orka.ai'
    exit 0 ;;
  *"agents.core.orka.ai"*)
    [ "$KMX_TEST_ORKA" = present ] && printf '{"metadata":{"name":"orka-hello","namespace":"orka-system"},"spec":{}}'
    exit 0 ;;
  *"agents.kagent.dev"*)
    case "$KMX_TEST_KAGENT" in
      present) printf 'agent.kagent.dev/hello-world\n'; exit 0 ;;
      missing) printf 'error: the server doesn'"'"'t have a resource type "agents"\n' >&2; exit 1 ;;
      *) printf 'Error from server (NotFound): agents.kagent.dev "x" not found\n' >&2; exit 1 ;;
    esac ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("KMX_TEST_CALLS", filepath.Join(dir, "calls"))
	var out bytes.Buffer
	return &App{
		Cfg: &config.Config{KubeContext: "kind-test"},
		Run: &run.Runner{Stdout: &out, Stderr: &out},
		Out: &out, Err: &out,
	}
}

// One-shot chat cannot execute Orka Tasks, but interactive chat can. An Orka
// Agent must therefore be sent to the working command rather than reported as
// absent from kagent or offered an unrelated kagent agent.
func TestOneShotChatPointsAnOrkaAgentAtInteractiveChat(t *testing.T) {
	a := runtimeFixture(t)
	t.Setenv("KMX_TEST_ORKA", "present")
	t.Setenv("KMX_TEST_KAGENT", "absent")

	err := a.ChatWithOptions(ChatOptions{Agent: "orka-hello", Task: "hello", Namespace: "orka-system"})
	if err == nil {
		t.Fatal("one-shot chat accepted an Orka Agent")
	}
	for _, want := range []string{
		"is an Orka Agent in namespace orka-system",
		"kmx --context kind-test agent chat --interactive --runtime orka --namespace orka-system orka-hello",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal never says %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "available agents") || strings.Contains(err.Error(), "hello-world") {
		t.Errorf("the refusal offers an unrelated kagent agent:\n%v", err)
	}
}

// A bare one-shot remains the legacy kagent contract. It must not pay an Orka
// discovery cost, fail on unrelated Orka RBAC, or let a same-named Orka Agent
// shadow a valid kagent Agent. Supplying --namespace is the explicit opt-in.
func TestBareOneShotChatDoesNotProbeOrka(t *testing.T) {
	a := runtimeFixture(t)
	t.Setenv("KMX_TEST_ORKA", "present")
	t.Setenv("KMX_TEST_KAGENT", "present")

	if err := a.ensureAgentExists("hello-world"); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(os.Getenv("KMX_TEST_CALLS"))
	if strings.Contains(string(calls), "core.orka.ai") || strings.Contains(string(calls), "api-resources") {
		t.Fatalf("a bare kagent lookup probed Orka:\n%s", calls)
	}
}

func TestExplicitOneShotOrkaChatPrintsTheWorkingCommand(t *testing.T) {
	a := runtimeFixture(t)
	err := a.ChatWithOptions(ChatOptions{Agent: "demo", Runtime: "orka", Namespace: "team-a"})
	if err == nil || !strings.Contains(err.Error(),
		"kmx --context kind-test agent chat --interactive --runtime orka --namespace team-a demo") {
		t.Fatalf("refusal = %v", err)
	}
}

// A runtime that was never installed is not a missing agent. Keep this
// distinction on the remaining kagent-only one-shot path.
func TestChatSeparatesAnAbsentRuntimeFromAnAbsentAgent(t *testing.T) {
	a := runtimeFixture(t)
	t.Setenv("KMX_TEST_KAGENT", "missing")
	t.Setenv("KMX_TEST_ORKA", "")

	err := a.ensureAgentExists("whatever")
	if err == nil {
		t.Fatal("chat accepted an agent on a cluster with no runtime")
	}
	if !strings.Contains(err.Error(), "does not have it installed") {
		t.Errorf("the refusal does not say the runtime is absent:\n%v", err)
	}
	if strings.Contains(err.Error(), "cannot verify") {
		t.Errorf("an absent runtime is reported as an unreadable cluster:\n%v", err)
	}
}

func TestOrkaAgentSourceFindsARealAgentAndNotAMention(t *testing.T) {
	bundle := []byte(`apiVersion: v1
kind: Secret
metadata:
  name: model-key
---
apiVersion: core.orka.ai/v1alpha1
kind: Agent
metadata:
  name: orka-hello
  namespace: orka-system
`)
	source, ok := orkaAgentSource(bundle)
	if !ok || source.Metadata.Name != "orka-hello" || source.Metadata.Namespace != "orka-system" {
		t.Fatalf("source = %#v, ok=%t", source, ok)
	}
	if _, ok := orkaAgentSource([]byte("apiVersion: v1\nkind: ConfigMap\ndata:\n  note: core.orka.ai\n")); ok {
		t.Fatal("an arbitrary mention was classified as an Orka Agent")
	}
	if _, ok := orkaAgentSource([]byte("not: [valid")); ok {
		t.Fatal("invalid YAML was classified as an Orka Agent")
	}
	if _, ok := orkaAgentSource([]byte("apiVersion: core.orka.ai/v99\nkind: Agent\n")); ok {
		t.Fatal("an unknown API version was classified as a supported Orka Agent")
	}
	if _, ok := orkaAgentSource(append(bundle, []byte("---\nnot: [valid")...)); ok {
		t.Fatal("an early Agent masked invalid YAML in a later document")
	}
}

func TestEditDoesNotInventAnOrkaIdentity(t *testing.T) {
	a := runtimeFixture(t)
	path := filepath.Join(t.TempDir(), "demo.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: core.orka.ai/v1alpha1\nkind: Agent\nmetadata: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := a.EditAgent("demo", path)
	if err == nil || !strings.Contains(err.Error(), "without a complete metadata.name and metadata.namespace") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "edit agents.core.orka.ai demo") {
		t.Fatalf("the error invented a live resource identity: %v", err)
	}
}

// `kmx agent create` writes Orka bundles to the path `kmx agent edit` opens,
// and completion offers those filenames. The edit path already validates
// before opening; this makes that pre-editor diagnostic name Orka and print a
// command that operates on the right kind.
func TestEditNamesAnOrkaBundleBeforeOpeningAnEditor(t *testing.T) {
	a := runtimeFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "orka-hello.yaml")
	bundle := "apiVersion: core.orka.ai/v1alpha1\nkind: Agent\nmetadata:\n  name: orka-hello\n  namespace: orka-system\n"
	if err := os.WriteFile(path, []byte(bundle), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", "false")
	t.Setenv("VISUAL", "")

	err := a.EditAgent("orka-hello", path)
	if err == nil {
		t.Fatal("an Orka bundle was accepted for kagent editing")
	}
	for _, want := range []string{
		"contains Orka Agent \"orka-hello\"",
		"Nothing was opened",
		"-n orka-system edit agents.core.orka.ai orka-hello",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal never says %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "must validate") {
		t.Errorf("the Orka bundle was reported as a kagent schema failure:\n%v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != bundle {
		t.Error("the diagnostic modified the source")
	}
}
