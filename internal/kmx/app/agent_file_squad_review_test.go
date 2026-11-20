package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

func TestNativeSquadPreviewFakeConsumesSharedBrief(t *testing.T) {
	t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
	d := nativePreviewFixture(t, "Hello")
	a := &App{FileRunner: NewLocalFileRunner()}
	for _, tc := range []struct {
		word string
		pass bool
	}{{"Hello", true}, {"Goodbye", false}} {
		brief := "Reply with exactly `" + tc.word + ", ` followed by the input name."
		result, err := a.PreviewSquadSnapshot(t.Context(), d, FilePreviewOptions{File: ".squad/agents/leader/charter.md", Operation: "test", SquadBrief: brief})
		if result.State != "complete" || result.FullPass != tc.pass || result.File.Revision != d.Revision || len(result.Examples) != 1 {
			t.Fatalf("brief did not change the production fake result: state=%s pass=%v err=%v", result.State, result.FullPass, err)
		}
		if tc.pass && err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeSquadPreviewRejectsCredentialBriefBeforeExecution(t *testing.T) {
	d := nativePreviewFixture(t, "Hello")
	for _, shape := range secretshapes.All() {
		t.Run(shape.Name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "state")
			t.Setenv("KMX_HOME", home)
			spy := &scriptedPreview{verdict: runtime.EvaluationPass}
			for _, application := range []*App{{FileRunner: spy}, {FileRunner: NewLocalFileRunner()}} {
				result, err := application.PreviewSquadSnapshot(t.Context(), d, FilePreviewOptions{File: ".squad/agents/leader/charter.md", Operation: "test", SquadBrief: "Shared context: " + shape.Example})
				if err == nil || result.State != "refused" || result.FullPass {
					t.Fatal("credential-bearing brief reached execution")
				}
				encoded, _ := json.Marshal(result)
				if strings.Contains(string(encoded), shape.Example) || strings.Contains(err.Error(), shape.Example) {
					t.Fatal("credential appeared in the refusal")
				}
			}
			if spy.calls != 0 {
				t.Fatal("credential-bearing brief reached the runner")
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatal("credential-bearing brief created preview state")
			}
		})
	}
}
