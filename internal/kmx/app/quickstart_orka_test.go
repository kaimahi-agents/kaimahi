package app

import (
	"context"
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
		"agent chat hello-world-agent --runtime orka --namespace orka-system",
		"agent create",
		"orka status",
		"plane",
		"migrate '<deployment>' --namespace '<ns>' --model hello-world-agent/qwen2.5:3b",
	} {
		if !strings.Contains(next[i], want) {
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

// An API read failure is not evidence of drift. Never propose deleting a
// healthy fixed Agent or Provider when kubectl could not read it.
func TestQuickstartReadFailureDoesNotSuggestDeletingTheAgent(t *testing.T) {
	a, _ := quickstartOrkaFixture(t)
	t.Setenv("KMX_ORKA_TEST_SCENARIO", "denied-provider-read")
	err := a.stepQuickstartAgent()
	if err == nil || !strings.Contains(err.Error(), "kubectl request failed") {
		t.Fatalf("the read failure was not reported: %v", err)
	}
	if strings.Contains(err.Error(), "delete") || strings.Contains(err.Error(), "kmx agent create") {
		t.Fatalf("an unreadable object was presented as drift: %v", err)
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

// Removing the shared pre-create probe must fail these cases even when a
// session is reused and earlier quickstart setup has already succeeded.
func TestQuickstartAndNativeChatCheckResultAccessBeforeTaskCreation(t *testing.T) {
	for _, path := range []string{"quickstart", "native-chat"} {
		for _, tc := range []struct {
			name, contentType, body, want string
			status                        int
		}{
			{"denied", "application/json", `{"error":{"code":403,"message":"denied"}}`, "preflight refused (HTTP 403)", http.StatusForbidden},
			{"html", "text/html", `<html>not a result API</html>`, "non-JSON content", http.StatusOK},
			{"invalid-json", "application/json", `{bad`, "malformed JSON", http.StatusNotFound},
			{"invalid-envelope", "application/json", `{"error":{"code":404,"message":"result not found"}}`, "preflight refused (HTTP 404)", http.StatusNotFound},
			{"happy", "application/json", `{"error":{"code":404,"message":"task not found"}}`, "", http.StatusNotFound},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				a, opt, out, _, dir := orkaCreateFixture(t, "lift-reuse")
				a.Cfg.Model = "qwen2.5:3b"
				if err := a.stepQuickstartAgent(); err != nil {
					t.Fatal(err)
				}
				orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
					name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/"), "/result")
					if r.URL.Query().Get("namespace") != OrkaNamespace || r.Header.Get("Authorization") != "Bearer "+orkaTestToken() {
						t.Errorf("wrong result request: %s", r.URL)
					}
					if _, err := os.Stat(filepath.Join(dir, name+"-tasks.core.orka.ai.json")); err == nil {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"result":"Orka says hello."}`)
						return
					}
					w.Header().Set("Content-Type", tc.contentType)
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				})
				a.quickstartResultPort = opt.ResultPort
				var err error
				if path == "quickstart" {
					var answer string
					answer, err = a.quickstartAnswer("Say hello")
					if err == nil && answer != "Orka says hello." {
						t.Errorf("answer=%q", answer)
					}
				} else {
					ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
					defer cancel()
					session, openErr := a.openOrkaResultSession(ctx, opt)
					if openErr != nil {
						t.Fatal(openErr)
					}
					defer session.close()
					backend := &orkaChatBackend{app: a, agent: QuickstartAgent, namespace: OrkaNamespace, resultSession: session}
					err = backend.Send(ctx, "Say hello", newChatRenderer(out))
					if err == nil && !strings.Contains(out.String(), "Orka says hello.") {
						t.Errorf("missing answer: %s", out)
					}
				}
				creates := 0
				for _, call := range orkaCalls(t, dir) {
					if call.Document != nil && call.Document["kind"] == "Task" && slices.Contains(call.Args, "create") && !slices.Contains(call.Args, "--dry-run=server") {
						creates++
					}
				}
				wantCreates := 0
				if tc.want == "" {
					wantCreates = 1
				}
				if creates != wantCreates {
					t.Errorf("Task creates=%d, want %d", creates, wantCreates)
				}
				if tc.want == "" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "no Task created; earlier setup may remain") {
					t.Fatalf("wrong refusal: %v", err)
				}
				if strings.Contains(err.Error(), "No resources created") {
					t.Fatalf("refusal incorrectly promises no earlier setup: %v", err)
				}
			})
		}
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/"), "/result")
		if _, err := os.Stat(filepath.Join(dir, name+"-tasks.core.orka.ai.json")); os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
			return
		}
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
