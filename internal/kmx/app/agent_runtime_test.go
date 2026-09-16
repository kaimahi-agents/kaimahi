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

// runtimeFixture fakes kubectl for the paths that decide WHICH runtime an
// agent belongs to. KMX_TEST_KAGENT and KMX_TEST_ORKA set what each kind
// answers, so a test can state a cluster shape exactly.
func runtimeFixture(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_CALLS"
case "$*" in
  *"agents.core.orka.ai"*)
    case "$KMX_TEST_ORKA" in
      present) printf 'orka-system\n'; exit 0 ;;
      missing) printf 'error: the server doesn'"'"'t have a resource type "agents"\n' >&2; exit 1 ;;
      *) printf ''; exit 0 ;;
    esac ;;
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

// Creating an agent and then chatting to it is the most natural pair of
// commands there is. `create` makes Orka Agents and `chat` talks to the legacy
// runtime, so that pair used to report the operator's own agent as absent —
// and then offer a DIFFERENT agent as the alternative. Being wrong about which
// runtime somebody is on is worse than being unable to help.
func TestChatNamesTheRuntimeAnAgentActuallyBelongsTo(t *testing.T) {
	a := runtimeFixture(t)
	// The shape that matters: absent from the runtime chat speaks to, present
	// in the one `kmx agent create` writes into.
	t.Setenv("KMX_TEST_KAGENT", "absent")
	t.Setenv("KMX_TEST_ORKA", "present")

	err := a.ensureAgentExists("orka-hello")
	if err == nil {
		t.Fatal("chat accepted an Orka Agent it cannot talk to")
	}
	for _, want := range []string{"is an Orka Agent", "orka-system", "kmx agent show orka-hello", "kmx agent list --namespace orka-system"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal never says %q:\n%v", want, err)
		}
	}
	// The old message offered whatever kagent happened to hold. Naming another
	// agent here is the specific thing that misled people.
	if strings.Contains(err.Error(), "available agents") || strings.Contains(err.Error(), "hello-world") {
		t.Errorf("the refusal offers an unrelated agent as an alternative:\n%v", err)
	}
}

// A runtime that was never installed is not a missing agent, and saying "does
// not exist in namespace kagent" about a cluster with no kagent at all reports
// the wrong fact.
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

// The ordinary case must still work: a kagent agent that is simply not there.
func TestChatStillReportsAMissingKagentAgentPlainly(t *testing.T) {
	a := runtimeFixture(t)
	t.Setenv("KMX_TEST_KAGENT", "absent")
	t.Setenv("KMX_TEST_ORKA", "")

	err := a.ensureAgentExists("ghost")
	if err == nil {
		t.Fatal("a missing agent was accepted")
	}
	if !strings.Contains(err.Error(), "does not exist in namespace kagent") {
		t.Errorf("unexpected refusal for a plainly missing agent:\n%v", err)
	}
}

// isMissingKind is the distinction the two messages above rest on: a cluster
// that does not serve a kind has not been shown to be missing an object.
func TestIsMissingKindIsNotIsNotFound(t *testing.T) {
	missing := errorString(`error: the server doesn't have a resource type "agents"`)
	if !isMissingKind(missing) {
		t.Error("an absent CRD is not recognised as a missing kind")
	}
	if isNotFound(missing) {
		t.Error("an absent CRD is being read as an absent object")
	}
	absent := errorString(`Error from server (NotFound): agents.kagent.dev "x" not found`)
	if isMissingKind(absent) {
		t.Error("an absent object is being read as an absent kind")
	}
	if !isNotFound(absent) {
		t.Error("an absent object is no longer recognised")
	}
	if isMissingKind(nil) {
		t.Error("nil is not a missing kind")
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

// `kmx agent create` writes Orka bundles to the path `kmx agent edit` opens,
// and completion offers those filenames. Opening the editor and then failing
// a kagent schema check wastes the edit and explains nothing.
func TestEditRefusesAnOrkaBundleBeforeOpeningAnEditor(t *testing.T) {
	a := runtimeFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "orka-hello.yaml")
	bundle := "apiVersion: core.orka.ai/v1alpha1\nkind: Agent\nmetadata:\n  name: orka-hello\n"
	if err := os.WriteFile(path, []byte(bundle), 0o600); err != nil {
		t.Fatal(err)
	}
	// An editor that would fail loudly if it were ever run.
	t.Setenv("EDITOR", "false")
	t.Setenv("VISUAL", "")

	err := a.EditAgent("orka-hello", path)
	if err == nil {
		t.Fatal("an Orka bundle was accepted for kagent editing")
	}
	for _, want := range []string{"is an Orka bundle", "Nothing was opened", "agents.core.orka.ai"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal never says %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "must validate") {
		t.Errorf("the Orka bundle was reported as a kagent schema failure:\n%v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != bundle {
		t.Error("the refusal modified the file it refused")
	}
}
