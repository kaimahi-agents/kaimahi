package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runview"
)

func TestOrkaTaskEnforcementOutcomeLeavesAnswerStdoutUntouched(t *testing.T) {
	f := runFixture(t)
	f.resultEnforcementProtocol = "container-v1"
	f.answers["private prompt"] = "protected answer"
	binding := "container:" + strings.Repeat("a", 64)
	digest := "sha256:" + strings.Repeat("b", 64)
	f.app.captureCommand = func(_ context.Context, cmd *exec.Cmd) error {
		taskRead := slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, orkaPlural("Task"))
		objectCreate := slices.Contains(cmd.Args, "create") && slices.Contains(cmd.Args, "-f")
		if !slices.Contains(cmd.Args, "json") || (!taskRead && !objectCreate) {
			return cmd.Run()
		}
		out := cmd.Stdout
		var raw bytes.Buffer
		cmd.Stdout = &raw
		if err := cmd.Run(); err != nil {
			return err
		}
		var task map[string]any
		if err := json.Unmarshal(raw.Bytes(), &task); err != nil {
			return err
		}
		if task["kind"] != "Task" {
			_, err := out.Write(raw.Bytes())
			return err
		}
		task["metadata"].(map[string]any)["generation"] = 1
		generation := task["metadata"].(map[string]any)["generation"]
		status, exists := task["status"].(map[string]any)
		if !exists {
			return json.NewEncoder(out).Encode(task)
		}
		status["enforcementAdmission"] = map[string]any{
			"bindingID": binding, "requirementDigest": digest, "observedGeneration": generation,
		}
		status["conditions"] = []any{map[string]any{
			"type": "EnforcementReady", "status": "True", "reason": "EnforcementVerified",
			"observedGeneration": generation, "message": "private-diagnostic-content",
		}}
		return json.NewEncoder(out).Encode(task)
	}
	opt := runOption(f)
	opt.EnforcementFile = writeTaskEnforcement(t, testTaskEnforcement())
	if err := f.app.RunAgent(opt); err != nil {
		t.Fatal(err)
	}
	if f.out.String() != "protected answer\n" {
		t.Fatalf("answer-only stdout changed: %q", f.out)
	}
	diagnostics := f.app.Err.(*bytes.Buffer).String()
	if !strings.Contains(diagnostics, binding) || !strings.Contains(diagnostics, digest) ||
		!strings.Contains(diagnostics, "not a live provider check") ||
		strings.Count(diagnostics, "enforcement admitted") != 1 ||
		strings.Contains(diagnostics, "private-diagnostic-content") {
		t.Fatalf("missing, repeated or unsafe historical admission: %q", diagnostics)
	}
}

func TestConsoleRunsHistoricalEnforcementDetails(t *testing.T) {
	model := newAgentTUIModel(AgentTUIOptions{Demo: true})
	model = tuiKey(model, 'R', "R")
	model = tuiKey(model, tea.KeyEnter, "")
	binding := "container:" + strings.Repeat("a", 64)
	model.runs.run.Tasks[0].Enforcement = &runview.Enforcement{
		State: "admitted", BindingID: binding, RequirementDigest: "sha256:" + strings.Repeat("b", 64),
	}
	details := strings.Join(model.runDetailLines(), "\n")
	if !strings.Contains(details, binding) || !strings.Contains(details, "historical admission") ||
		!strings.Contains(details, "not a live provider check") {
		t.Fatalf("console lost physical identity or historical scope: %s", details)
	}
}
