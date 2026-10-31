package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func nativePreviewFixture(t *testing.T, word string) *agentfile.SquadCharter {
	t.Helper()
	raw, err := os.ReadFile("../agentfile/testdata/squad-v1.1.0/catalog.md")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(string(raw), "- **Preferred:** auto", "- **Preferred:** fake", 1)
	source += "\n\n## Task Instructions\n\nReply with exactly `" + word + ", ` followed by the input name.\n\n## Examples\n\n```yaml\nexamples:\n  - id: greeting\n    input: Ada\n    assertions:\n      - id: contains-greeting\n        type: contains\n        value: Hello, Ada\n```\n"
	d, err := agentfile.ParseSquad(".squad/agents/leader/charter.md", []byte(source), agentfile.SquadProfile{Release: "1.1.0", MemberID: "leader", Role: "Lead / Architect"})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func TestNativeSquadPreviewFullInstructionsForRealRoute(t *testing.T) {
	d := nativePreviewFixture(t, "Hello")
	raw := strings.Replace(string(d.Source()), "- **Preferred:** fake", "- **Preferred:** native-model", 1)
	d, err := agentfile.ParseSquad(".squad/agents/leader/charter.md", []byte(raw), d.Profile)
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedPreview{verdict: runtime.EvaluationPass}
	a := &App{FileRunner: runner}
	route := &modelaccess.Route{Provider: "compatible", Model: "native-model", Endpoint: "http://localhost:1234/v1", Auth: "none"}
	_, err = a.PreviewSquadSnapshot(context.Background(), d, FilePreviewOptions{File: ".squad/agents/leader/charter.md", Operation: "test", Route: route})
	if err != nil || runner.calls != 1 {
		t.Fatalf("real-route snapshot not shared: %v", err)
	}
	got := runner.request.Agent
	if got.Instructions != d.Instructions || strings.Contains(got.Instructions, "## Examples") || got.Identity.Purpose != "" || got.NativeTaskInstructions != d.TaskInstructions {
		t.Fatal("native full prompt lost or canonical purpose synthesized")
	}
}
func TestNativeSquadPreviewJournaledFakeTask(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "state")
	t.Setenv("KMX_HOME", home)
	d := nativePreviewFixture(t, "Hello")
	a := &App{FileRunner: NewLocalFileRunner()}
	got, err := a.PreviewSquadSnapshot(context.Background(), d, FilePreviewOptions{File: ".squad/agents/leader/charter.md", Operation: "test"})
	if err != nil || !got.FullPass {
		t.Fatalf("production fake adapter did not consume native task: %+v %v", got, err)
	}
}

func TestNativeSquadPreviewIncludesSharedBriefOnce(t *testing.T) {
	d := nativePreviewFixture(t, "Hello")
	runner := &scriptedPreview{verdict: runtime.EvaluationPass}
	a := &App{FileRunner: runner}
	brief := "Preserve every offline edit when reconnecting."
	_, err := a.PreviewSquadSnapshot(t.Context(), d, FilePreviewOptions{File: ".squad/agents/leader/charter.md", Operation: "test", SquadBrief: brief})
	if err != nil || runner.calls != 1 {
		t.Fatal("preview failed", err)
	}
	got := runner.request.Agent
	if strings.Count(got.Instructions, brief) != 1 || strings.Count(got.Instructions, d.TaskInstructions) != 1 || strings.Contains(got.Instructions, "## Examples") {
		t.Fatal("shared context was omitted, duplicated, or mixed with tests")
	}
	if got.Revision != d.Revision {
		t.Fatal("brief changed the member source revision")
	}
}
func TestNativeSquadPreviewTaskOnlyFakeAndRevision(t *testing.T) {
	a := &App{FileRunner: NewDirectFileRunner()}
	for _, tc := range []struct {
		word string
		pass bool
	}{{"Hello", true}, {"Goodbye", false}} {
		d := nativePreviewFixture(t, tc.word)
		got, err := a.PreviewSquadSnapshot(context.Background(), d, FilePreviewOptions{File: ".squad/agents/leader/charter.md", Operation: "test"})
		if got.FullPass != tc.pass || got.File.Revision != d.Revision || len(got.Examples) != 1 {
			t.Fatalf("native preview identity/result: %+v %v", got, err)
		}
		if tc.pass && err != nil {
			t.Fatal(err)
		}
	}
	d := nativePreviewFixture(t, "Hello")
	partial, err := a.PreviewSquadSnapshot(context.Background(), d, FilePreviewOptions{File: ".squad/agents/leader/charter.md", Operation: "test", CaseID: "greeting"})
	if err != nil || partial.FullPass || partial.Selection.Scope != "subset" {
		t.Fatal("native subset became full pass")
	}
}

func TestNativeSquadPreviewZeroExamplesRefusesTest(t *testing.T) {
	raw, err := os.ReadFile("../agentfile/testdata/squad-v1.1.0/catalog.md")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(string(raw), "- **Preferred:** auto", "- **Preferred:** fake", 1) + "\n\n## Task Instructions\n\nReply.\n\n## Examples\n\n```yaml\nexamples: []\n```\n"
	d, err := agentfile.ParseSquad(".squad/agents/leader/charter.md", []byte(source), agentfile.SquadProfile{Release: "1.1.0", MemberID: "leader", Role: "Lead / Architect"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := (&App{FileRunner: NewLocalFileRunner()}).PreviewSquadSnapshot(context.Background(), d, FilePreviewOptions{File: ".squad/agents/leader/charter.md", Operation: "test"})
	if err == nil || got.ReasonCode != "no_tests" || got.FullPass {
		t.Fatalf("zero-example Squad test: %+v / %v", got, err)
	}
}
