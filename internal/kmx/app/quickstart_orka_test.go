package app

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// The supported clean-machine sequence, read off the command rather than
// asserted about it: kind, Ollama, the model, the pinned Orka release, the
// fixed Agent bundle, and a question. Nothing on that path installs the
// legacy runtime, so no phase may name it.
func TestQuickstartReachesAnOrkaAnswerWithoutTheLegacyRuntime(t *testing.T) {
	a := &App{Cfg: newQuickstartConfig()}
	var names []string
	for _, step := range a.quickstartSteps() {
		names = append(names, step.name)
	}
	names = append(names, "Ask "+QuickstartAgent+" a question")
	want := []string{
		"Prepare kind cluster",
		"Deploy Ollama",
		"Pull model qwen2.5:3b",
		"Install Orka " + OrkaVersion,
		"Deploy the hello-world-agent agent",
		"Ask hello-world-agent a question",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("quickstart phases are %q, want %q", names, want)
	}
	for _, name := range names {
		if strings.Contains(strings.ToLower(name), "kagent") {
			t.Errorf("a quickstart phase still promises the legacy runtime: %q", name)
		}
	}
	if QuickstartAgent != "hello-world-agent" {
		t.Errorf("quickstart deploys %q; the fixed Orka bundle name is hello-world-agent", QuickstartAgent)
	}
}

// `kmx local up` is the RUNTIME, and the runtime is Orka. The legacy installer
// steps are gone rather than hidden behind an explicit invocation: there is
// no --legacy-kagent, and no step name that installs one either.
func TestUpIsOrkaOnlyWithNoLegacyStepSurviving(t *testing.T) {
	want := []string{"cluster", "ollama", "model", "orka"}
	if !slices.Equal(UpDefaultSteps, want) {
		t.Fatalf("a bare `kmx local up` runs %q, want %q", UpDefaultSteps, want)
	}
	if !slices.Equal(UpSteps, want) {
		t.Fatalf("the addressable steps are %q, want %q", UpSteps, want)
	}
	for _, step := range []string{"kagent", "agent", "tools-agent"} {
		if slices.Contains(UpSteps, step) {
			t.Errorf("the retired legacy step %q is still addressable", step)
		}
		if upPhaseName(step) != "" {
			t.Errorf("the retired legacy step %q still has a phase name %q", step, upPhaseName(step))
		}
	}
	if !slices.Contains(UpSteps, "orka") || upPhaseName("orka") == "" {
		t.Fatalf("there is no addressable orka step: %q", UpSteps)
	}
	for _, flag := range []string{"--legacy-kagent", "legacy-kagent"} {
		if slices.Contains(UpSteps, flag) {
			t.Errorf("a legacy opt-in was introduced: %q", flag)
		}
	}
	a := &App{Cfg: newQuickstartConfig(), Err: &strings.Builder{}}
	err := a.Up("nonesuch")
	if err == nil || !strings.Contains(err.Error(), "orka") {
		t.Fatalf("the step list an operator is offered omits orka: %v", err)
	}
}

// Follow-ups keep the first-answer journey on native Orka: ask another
// question, author another agent, or inspect the platform.
func TestQuickstartFollowUpsAreOrkaActions(t *testing.T) {
	a := &App{Cfg: newQuickstartConfig()}
	next := a.quickstartFollowups()
	if len(next) != 3 {
		t.Fatalf("follow-ups are %q", next)
	}
	for i, want := range []string{
		"kmx --context kind-test chat hello-world-agent --runtime orka --namespace orka-system 'ask it something else'",
		"kmx --context kind-test create --help",
		"kmx --context kind-test orka status",
	} {
		if next[i] != want {
			t.Errorf("follow-up %d is %q, want it to offer %q", i, next[i], want)
		}
	}
	// Chat is always a session, so the follow-up no longer carries a mode flag.
	if strings.Contains(next[0], "--interactive") {
		t.Errorf("chat follow-up still teaches the compatibility flag: %s", next[0])
	}
	for _, command := range next {
		if !strings.Contains(command, "--context kind-test") {
			t.Errorf("follow-up lost the context this run used: %s", command)
		}
		if strings.Contains(command, "kagent") || strings.Contains(command, "govern ") || strings.Contains(command, " plane") || strings.Contains(command, " migrate") {
			t.Errorf("follow-up points at the legacy runtime: %s", command)
		}
	}
}

func newQuickstartConfig() *config.Config {
	return &config.Config{KindCluster: "test", KubeContext: "kind-test", ContainerEngine: "docker", Model: "qwen2.5:3b"}
}

// The structured document is the wire contract an unattended caller parses.
func TestQuickstartResultNamesTheOrkaBundleItDeployed(t *testing.T) {
	a := &App{Cfg: newQuickstartConfig()}
	result := a.quickstartResult("Who are you?", "Hello.", time.Now())
	if result.Agent != QuickstartAgent {
		t.Errorf("result names agent %q", result.Agent)
	}
	if result.Governed {
		t.Error("the fast path deploys no plane but claims governance")
	}
	for _, want := range []string{OrkaNamespace, QuickstartAgent} {
		if !strings.Contains(result.Manifest, want) {
			t.Errorf("manifest %q does not name %q", result.Manifest, want)
		}
	}
	if strings.Contains(result.Manifest, "kagent") {
		t.Errorf("manifest still points at the legacy example: %s", result.Manifest)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatal("the structured result is not valid JSON")
	}
}
