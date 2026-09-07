package app

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// An unrecognised --output is refused BEFORE a cluster is created. Being told
// "unknown output" four minutes into a bring-up would be the worst possible
// moment to find out.
func TestQuickstartRejectsAnUnknownOutputBeforeDoingAnything(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KMX_TOOLCHAIN", "off")
	a := &App{Cfg: &config.Config{ContainerEngine: "docker"}, Run: &run.Runner{}, Err: &bytes.Buffer{}, Out: &bytes.Buffer{}}
	err := a.Quickstart(QuickstartOptions{Output: "yaml"})
	if err == nil || !strings.Contains(err.Error(), "unknown --output") {
		t.Fatalf("got %v, want a refusal naming the output", err)
	}
}

// The structured result is what an agent in a harness reads. Two fields carry
// weight beyond their type: `ok` is the whole answer to "did this work", and
// `governed` must be false, because the fast path deliberately deploys no
// plane and a machine-readable claim of governance would be a lie in JSON.
func TestQuickstartResultSaysPlainlyThatNothingIsGoverned(t *testing.T) {
	// A struct literal written by the test says nothing about what kmx
	// reports, so the claim is read where it is actually made: at the one
	// place a result is built.
	source, err := os.ReadFile("quickstart.go")
	if err != nil {
		t.Fatal(err)
	}
	governed := regexp.MustCompile(`Governed:\s*(\w+)`).FindAllStringSubmatch(string(source), -1)
	if len(governed) == 0 {
		t.Fatal("no result sets Governed at all; the field would then marshal as false by accident rather than by decision")
	}
	for _, set := range governed {
		if set[1] != "false" {
			t.Errorf("the fast path deploys no plane, but a result claims Governed: %s", set[1])
		}
	}

	// The wire contract in both directions. A harness parses this document,
	// so a field that vanishes breaks it and a field that appears unannounced
	// is one nobody decided to publish. A zero value is marshalled on purpose:
	// every key must survive it, which is what forbids `omitempty` on the
	// answer to "did this work".
	raw, err := json.Marshal(QuickstartResult{})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(decoded))
	for key := range decoded {
		got = append(got, key)
	}
	sort.Strings(got)
	want := []string{"agent", "answer", "cluster", "context", "elapsed_seconds",
		"governed", "manifest", "next", "ok", "question", "tools"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the structured output's fields are %v, want %v: %s", got, want, raw)
	}
}

// --output json must leave stdout carrying one document and nothing else.
// The regression this pins was real: kind, helm and kubectl all write to
// kmx's stdout, so the caller's parser met `Creating cluster "..."` before it
// met the JSON.
func TestJSONOutputKeepsSubprocessChatterOffStdout(t *testing.T) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KMX_TOOLCHAIN", "off")
	a := &App{Cfg: &config.Config{ContainerEngine: "docker"}, Run: &run.Runner{}, Out: out, Err: errOut}
	a.Run.Stdout = out
	// It fails at preflight on an empty PATH — long after the routing
	// decision, which is the part under test.
	_ = a.Quickstart(QuickstartOptions{Output: "json"})
	if a.Run.Stdout != errOut {
		t.Error("subprocess output still goes to stdout under --output json")
	}
	if out.Len() != 0 {
		t.Errorf("stdout is not empty before the JSON document: %q", out.String())
	}
}

// The answer is read out of the A2A task the same way `chat` reads it, and a
// response with no readable reply must not be reported as an answer.
func TestParseTaskFindsTheReplyAndRefusesRubbish(t *testing.T) {
	good := `some kagent logging
{"artifacts":[{"parts":[{"kind":"text","text":"I am a declarative kagent agent."}]}],"status":{"state":"completed"}}`
	if got := strings.TrimSpace(firstText(parseTask(good))); got != "I am a declarative kagent agent." {
		t.Errorf("reply not found: %q", got)
	}
	for _, bad := range []string{"", "no json here", "{not json}"} {
		if got := firstText(parseTask(bad)); got != "" {
			t.Errorf("%q yielded a reply %q", bad, got)
		}
	}
}

// The first-answer profile must turn off exactly the components the first
// question cannot reach — and must not touch the model provider, which is
// the one thing the answer depends on.
func TestTheFirstAnswerProfileOnlyDefersUnreachableComponents(t *testing.T) {
	joined := strings.Join(quickstartValues, " ")
	for _, off := range []string{"kagent-tools.enabled=false", "kmcp.enabled=false", "ui.replicas=0"} {
		if !strings.Contains(joined, off) {
			t.Errorf("the first-answer profile does not defer %s: %s", off, joined)
		}
	}
	if strings.Contains(joined, "providers") || strings.Contains(joined, "ollama") {
		t.Errorf("the first-answer profile must not touch the model provider: %s", joined)
	}
}
