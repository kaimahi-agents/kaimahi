package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// `kmx up` is the RUNTIME, and the runtime is Orka. The legacy installer
// steps are gone rather than hidden behind an explicit invocation: there is
// no --legacy-kagent, and no step name that installs one either.
func TestUpIsOrkaOnlyWithNoLegacyStepSurviving(t *testing.T) {
	want := []string{"cluster", "ollama", "model", "orka"}
	if !slices.Equal(UpDefaultSteps, want) {
		t.Fatalf("a bare `kmx up` runs %q, want %q", UpDefaultSteps, want)
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

// The follow-ups are the ones this cluster can actually run now: an Orka
// chat, Orka authoring, and the two commands that put an application's model
// traffic on the seam. Indexes 3 and 4 are the governance pair the text
// ending prints, so their order is part of the contract.
func TestQuickstartFollowUpsAreOrkaActions(t *testing.T) {
	a := &App{Cfg: newQuickstartConfig()}
	next := a.quickstartFollowups()
	if len(next) != 5 {
		t.Fatalf("follow-ups are %q", next)
	}
	for i, want := range []string{
		"agent chat hello-world-agent --interactive --runtime orka --namespace orka-system",
		"agent create",
		"orka status",
		"plane",
		"migrate '<deployment>' --namespace '<ns>' --model local/qwen2.5:3b",
	} {
		if !strings.Contains(next[i], want) {
			t.Errorf("follow-up %d is %q, want it to offer %q", i, next[i], want)
		}
	}
	// The chat follow-up has to be a command that runs. Orka chat is
	// interactive-only and refuses a one-shot by name, so a follow-up without
	// --interactive ends the first answer with an instruction that fails.
	chat := ChatOptions{Agent: QuickstartAgent, Namespace: OrkaNamespace, Runtime: "orka", Task: "ask it something else"}
	if err := a.ChatWithOptions(chat); err == nil || !strings.Contains(err.Error(), "requires --interactive") {
		t.Fatalf("one-shot Orka chat no longer refuses; this test no longer pins the follow-up: %v", err)
	}
	for _, command := range next {
		if !strings.Contains(command, "--context kind-test") {
			t.Errorf("follow-up lost the context this run used: %s", command)
		}
		if strings.Contains(command, "kagent") || strings.Contains(command, "govern ") {
			t.Errorf("follow-up points at the legacy runtime: %s", command)
		}
	}
}

// Rerunning is how an agent in a harness uses this command. An exact match is
// reused and nothing else is: a Provider or Agent whose live spec differs is
// somebody's deliberate change, so quickstart stops rather than overwrite it.
// A half-finished cluster resumes, and every run asks a FRESH Task — reusing
// one would report an earlier run's model call as this run's answer.
func TestQuickstartOrkaBundleReusesExactMatchesAndAsksAFreshTask(t *testing.T) {
	a, dir := quickstartOrkaFixture(t)
	for i, label := range []string{"first run", "rerun"} {
		if err := a.stepQuickstartAgent(); err != nil {
			t.Fatalf("%s (%d): %v", label, i, err)
		}
	}
	if err := os.Remove(filepath.Join(dir, QuickstartAgent+"-agents.core.orka.ai.json")); err != nil {
		t.Fatal(err)
	}
	if err := a.stepQuickstartAgent(); err != nil {
		t.Fatalf("partial resume: %v", err)
	}
	writes := map[string]int{}
	for _, call := range orkaCalls(t, dir) {
		joined := strings.Join(call.Args, " ")
		if call.Document != nil && strings.Contains(joined, "create") && !strings.Contains(joined, "--dry-run=server") {
			writes[call.Document["kind"].(string)]++
		}
	}
	if writes["Provider"] != 1 || writes["Agent"] != 2 {
		t.Fatalf("exact-match reuse did not hold: %v", writes)
	}

	var answers, tasks []string
	for range 2 {
		answer, err := a.quickstartAnswer("Reply with exactly this text: Orka says hello.")
		if err != nil {
			t.Fatal(err)
		}
		answers = append(answers, answer)
	}
	for _, call := range orkaCalls(t, dir) {
		if call.Document != nil && call.Document["kind"] == "Task" {
			tasks = append(tasks, call.Document["metadata"].(map[string]any)["name"].(string))
		}
	}
	if len(tasks) != 2 || tasks[0] == tasks[1] {
		t.Fatalf("a rerun did not create a fresh Task: %q", tasks)
	}
	for _, answer := range answers {
		if strings.TrimSpace(answer) != "Orka says hello." {
			t.Fatalf("answer %q is not the model's reply", answer)
		}
	}

	a.Cfg.Model = "some-other-model"
	err := a.stepQuickstartAgent()
	if err == nil || !strings.Contains(err.Error(), "different configuration") {
		t.Fatalf("a drifted bundle was not refused: %v", err)
	}
}

// A drifted fixed bundle is somebody's deliberate change, so quickstart
// refuses it rather than overwrite it — but it still owes the operator a way
// out: keep the edit under a different Agent name, or delete the fixed
// hello-world-agent so a rerun recreates it. The remedy must be reachable by
// unwrapping (%w), not just readable in the flattened message, so callers
// that inspect the error chain still see the original cause.
func TestQuickstartDriftedBundleOffersRemedies(t *testing.T) {
	a, _ := quickstartOrkaFixture(t)
	if err := a.stepQuickstartAgent(); err != nil {
		t.Fatalf("first run: %v", err)
	}

	a.Cfg.Model = "some-other-model"
	err := a.stepQuickstartAgent()
	if err == nil {
		t.Fatal("a drifted bundle was not refused")
	}
	if !strings.Contains(err.Error(), "different configuration") {
		t.Fatalf("lost the underlying drift explanation: %v", err)
	}
	if !strings.Contains(err.Error(), "kmx agent create") {
		t.Fatalf("missing the keep-and-rename remedy: %v", err)
	}
	if !strings.Contains(err.Error(), "delete the fixed "+QuickstartAgent) {
		t.Fatalf("missing the delete-and-recreate remedy: %v", err)
	}
	if unwrapped := errors.Unwrap(err); unwrapped == nil || !strings.Contains(unwrapped.Error(), "different configuration") {
		t.Fatalf("remedies were not wrapped with %%w over matchingOrkaResource's error: %v", err)
	}
}

// A Task that completed with nothing readable in it is a failed run, not an
// answer. The result here is terminal control sequences only: it is not blank
// on the wire, so it is not mistaken for "the result has not been written
// yet" and retried — it is refused the moment it is read and sanitised.
func TestQuickstartRefusesABlankOrkaAnswer(t *testing.T) {
	a, _ := quickstartOrkaFixture(t, `{"result":"\u001b[2J\u0007"}`)
	if err := a.stepQuickstartAgent(); err != nil {
		t.Fatal(err)
	}
	_, err := a.quickstartAnswer("Say hello")
	if err == nil {
		t.Fatal("a result with no printable answer was reported as an answer")
	}
	if !strings.Contains(err.Error(), "no printable answer") {
		t.Fatalf("refused for the wrong reason (a retry-until-deadline is not a refusal): %v", err)
	}
}

func newQuickstartConfig() *config.Config {
	return &config.Config{KindCluster: "test", KubeContext: "kind-test", ContainerEngine: "docker", Model: "qwen2.5:3b"}
}

// quickstartOrkaFixture drives the real Orka code against the executable
// boundary fake, with a result endpoint this test owns.
func quickstartOrkaFixture(t *testing.T, result ...string) (*App, string) {
	t.Helper()
	body := `{"result":"Orka says hello."}`
	if len(result) > 0 {
		body = result[0]
	}
	a, _, _, _, dir := orkaCreateFixture(t, "lift-reuse")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg.Model = "qwen2.5:3b"
	a.quickstartResultPort = port
	return a, dir
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
