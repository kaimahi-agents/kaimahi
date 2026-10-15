package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

const suiteJudgeCanary = "PRIVATE_SUITE_JUDGE_CANARY"
const suiteJudgeYAML = "id: first\ninput: PRIVATE_SUITE_JUDGE_CANARY\nassertions: [{id: quality, type: llmJudge, rubric: PRIVATE_SUITE_JUDGE_CANARY, minScore: 70, judgeModel: judge-model, judgeVersion: v1}]\n"

func suiteJudgeFixtureServer(t *testing.T, sample func(*http.Request, int) int) (string, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := int(calls.Add(1))
		score := sample(r, n)
		if score < 0 {
			http.Error(w, suiteJudgeCanary, http.StatusBadGateway)
			return
		}
		body := fmt.Sprintf(`{"score":%d,"reasoning":"%s"}`, score, suiteJudgeCanary)
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "judge-model", "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": body}}}})
	}))
	t.Cleanup(server.Close)
	return server.URL, calls
}

// A case cannot pass solely because inventory admits the assertion. Exercise the
// real transport, native candidate attribution, export, and offline consumers.
func TestSuiteJudgePipeline(t *testing.T) {
	for _, tc := range []struct{ runtime, outcome string }{
		{"orka", "pass"}, {"kagent", "pass"},
		{"orka", "fail"}, {"kagent", "fail"},
		{"orka", "split"}, {"kagent", "unavailable"},
		{"orka", "post-judge drift"}, {"kagent", "post-judge drift"},
		{"orka", "deadline"}, {"kagent", "deadline"},
	} {
		runtime, outcome := tc.runtime, tc.outcome
		t.Run(runtime+"/"+outcome, func(t *testing.T) {
			var a *App
			var opt SuiteWorkspaceEvaluationOptions
			var native *evalFixture
			var sendsDir string
			if runtime == "orka" {
				a, _, _, native = nativeEvaluationFixture(t)
				opt = nativeEvaluationOptions(t)
				opt.ResultPort = native.opt.ResultPort
			} else {
				a, _, _, sendsDir = suiteKagentFixture(t, "")
				opt = suiteKagentEvaluationOptions(t)
				t.Setenv("KMX_KAGENT_TEST_SCENARIO", "eval-success")
			}
			writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), suiteJudgeYAML)
			var judged atomic.Bool
			endpoint, calls := suiteJudgeFixtureServer(t, func(r *http.Request, n int) int {
				switch outcome {
				case "fail":
					return 20
				case "split":
					if n == 2 {
						return 20
					}
				case "unavailable":
					return -1
				case "deadline":
					select {
					case <-r.Context().Done():
					case <-time.After(2 * time.Second):
					}
					return -1
				}
				if n == 2 {
					judged.Store(true)
				}
				return 90
			})
			opt.JudgeBaseURL = endpoint
			if outcome == "deadline" {
				opt.CaseTimeout = time.Second
			}
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if native != nil {
				for _, c := range p.inventory {
					native.answers[c.Case.Input] = "correct open-ended answer"
				}
			}
			if native != nil {
				native.answers[p.inventory[1].Case.Input] = nativeCanary
			}
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if outcome == "post-judge drift" && judged.Load() && slices.Contains(cmd.Args, "get") && (slices.Contains(cmd.Args, "agents.core.orka.ai") || slices.Contains(cmd.Args, "agents.kagent.dev")) {
					return errors.New(suiteJudgeCanary)
				}
				return capture(ctx, cmd)
			}
			err = a.EvaluateSuiteWorkspace(t.Context(), p)
			candidateSends := 0
			if native != nil {
				tasks, e := filepath.Glob(filepath.Join(native.dir, "task-*.json"))
				if e != nil {
					t.Fatal(e)
				}
				candidateSends = len(tasks)
			} else {
				candidateSends = len(evaluationSends(t, sendsDir))
			}
			if outcome == "pass" {
				if err != nil || calls.Load() != 2 || candidateSends != 2 {
					t.Fatalf("full passing execution: %v samples=%d sends=%d", err, calls.Load(), candidateSends)
				}
				receipt, e := readSuitePassingReceipt(t.Context(), opt.Out)
				if e != nil {
					t.Fatal(e)
				}
				raw, e := canonicalSuitePassingReceipt(t.Context(), receipt, p.suite)
				if e != nil {
					t.Fatal(e)
				}
				var wire map[string]any
				if json.Unmarshal(raw, &wire) != nil {
					t.Fatal("invalid receipt")
				}
				assertion := wire["cases"].([]any)[0].(map[string]any)["assertions"].([]any)[0].(map[string]any)
				judge, ok := assertion["judge"].(map[string]any)
				if !ok || judge["model"] != "judge-model" || judge["modelVersion"] != "v1" || judge["requestStyle"] != "openai-chat-completions-v1" || judge["configurationDigest"] == nil || len(judge["scores"].([]any)) != 2 {
					t.Fatal("receipt lost judge identity/scores")
				}
				if bytes.Contains(raw, []byte(suiteJudgeCanary)) || bytes.Contains(raw, []byte(endpoint)) {
					t.Fatal("receipt leaked content/endpoint")
				}
				if err := a.DiffAgentEvaluations(EvaluationDiffOptions{Before: opt.Out, After: opt.Out}); err != nil {
					t.Fatal(err)
				}
				if _, err := a.PushSuiteWorkspaceEvaluated(t.Context(), opt.Workspace, "suite:v1", filepath.Join(t.TempDir(), "layout"), false, false, SuitePushProvenance{}, opt.Out); err != nil {
					t.Fatal("evaluated push", err)
				}
				if runtime == "orka" {
					if err := os.RemoveAll(opt.Workspace); err != nil {
						t.Fatal(err)
					}
					if _, err := a.PrepareReleaseLift(t.Context(), ReleaseLiftOptions{Reference: "suite@" + p.suite.Digest, FromLayout: p.prepared.Path, ToContext: opt.ToContext, ToNamespace: opt.ToNamespace, Inference: opt.Inference, Evidence: opt.Out}); err != nil {
						t.Fatal("source-free judge evidence lift plan", err)
					}
				}
			} else {
				if err == nil || candidateSends != 1 {
					t.Fatalf("did not stop after one Task: %v sends=%d", err, candidateSends)
				}
				if outcome == "fail" && !errors.Is(err, errSuiteNativeCaseFailed) {
					t.Fatalf("known failure lost: %v", err)
				}
				if outcome != "fail" && errors.Is(err, errSuiteNativeCaseFailed) {
					t.Fatal("unknown became fail")
				}
				if outcome == "deadline" && (!errors.Is(err, context.DeadlineExceeded) || calls.Load() == 0) {
					t.Fatalf("deadline not during judge: %v samples=%d", err, calls.Load())
				}
				if outcome == "post-judge drift" && strings.Contains(fmt.Sprint(a.Out), "first: pass") {
					t.Fatal("post-judge drift credited a case pass")
				}
				if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
					t.Fatal("failure exported passing evidence")
				}
			}
			if strings.Contains(fmt.Sprint(err)+fmt.Sprint(a.Out)+fmt.Sprint(a.Err), suiteJudgeCanary) {
				t.Fatal("judge content escaped to terminal")
			}
		})
	}
}

// A copied passing judge row must still be bound to the packaged definition,
// and nested wire metadata is as closed as ordinary text receipt rows.
func TestSuiteJudgeReceiptCoherenceAndDiff(t *testing.T) {
	root, _, _, _, _ := passingFixture(t)
	writeFile(t, filepath.Join(root, "eval/example.yaml"), suiteJudgeYAML)
	prepared, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	suite, err := workspace.Load(t.Context(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := nativeFixture(t)
	native, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
	if err != nil {
		t.Fatal(err)
	}
	placement := &suiteNativePlacement{Subject: native.Subject, Provider: suiteNativeOwnedResource{"provider", 1, strings.Repeat("a", 64)}, Agent: suiteNativeOwnedResource{"agent", 1, strings.Repeat("b", 64)}}
	inventory, _, err := suitePassingInventory(suite)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := suiteJudgeFixtureServer(t, func(*http.Request, int) int { return 90 })
	cases := make([]agentruntime.EvaluationCase, len(inventory))
	for i, c := range inventory {
		cases[i] = c.Case
	}
	judge, err := newEvaluationCaseJudge(evaluationJudgeConfig{BaseURL: endpoint}, cases, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	observations := make([]suitePassingObservation, len(inventory))
	for i, frame := range inventory {
		rows := judge(ctx, frame.Case, agentruntime.EvaluationEvidence{Answer: "4", AnswerAvailable: true})
		observations[i] = suitePassingObservation{frame.Metadata.CaseDigest, frame.Metadata.InputDigest, agentruntime.EvaluationReceipt{CaseID: frame.Case.ID, Verdict: agentruntime.EvaluationPass, TaskName: "task-" + frame.Case.ID, TaskUID: "uid-" + frame.Case.ID, AnswerSHA256: agentruntime.EvaluationAnswerDigest("4"), Assertions: rows}}
	}
	r, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), r, suite)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"missing judge", "null score", "extra field", "duplicate field", "wrong threshold", "wrong version", "missing style", "empty style", "wrong style", "missing matches", "null matches", "bad matches"} {
		t.Run(mutation, func(t *testing.T) {
			var d map[string]any
			_ = json.Unmarshal(raw, &d)
			assertion := d["cases"].([]any)[0].(map[string]any)["assertions"].([]any)[0].(map[string]any)
			j := assertion["judge"].(map[string]any)
			switch mutation {
			case "missing judge":
				delete(assertion, "judge")
			case "missing matches":
				delete(j, "modelMatches")
			case "null matches":
				j["modelMatches"] = nil
			case "bad matches":
				j["modelMatches"] = []string{"prefix", "requested"}
			case "missing style":
				delete(j, "requestStyle")
			case "empty style":
				j["requestStyle"] = ""
			case "wrong style":
				j["requestStyle"] = "legacy"
			case "null score":
				j["scores"] = []any{90, nil}
			case "extra field":
				j["reasoning"] = suiteJudgeCanary
			case "wrong threshold":
				j["minScore"] = 60
			case "wrong version":
				j["modelVersion"] = "v2"
			}
			body, _ := json.Marshal(d)
			if mutation == "duplicate field" {
				body = bytes.Replace(body, []byte(`"model":"judge-model"`), []byte(`"model":"judge-model","model":"judge-model"`), 1)
			}
			parsed, e := parseSuitePassingReceipt(body)
			if e == nil {
				_, e = canonicalSuitePassingReceipt(t.Context(), parsed, suite)
			}
			if e == nil {
				t.Fatal("tampered judge evidence accepted")
			}
		})
	}
	before, ok := normalizeSuitePassingEvaluation(r)
	if !ok {
		t.Fatal("cannot normalize judge")
	}
	var document map[string]any
	_ = json.Unmarshal(raw, &document)
	evidence := document["cases"].([]any)[0].(map[string]any)["assertions"].([]any)[0].(map[string]any)["judge"].(map[string]any)
	evidence["scores"] = []int{80, 85}
	evidence["modelMatches"] = []string{"version-qualified", "requested"}
	body, _ := json.Marshal(document)
	var changed suitePassingReceipt
	_ = json.Unmarshal(body, &changed)
	after, ok := normalizeSuitePassingEvaluation(&changed)
	if !ok || before.SuiteInventory != after.SuiteInventory {
		t.Fatal("passing sample variation changed comparison identity")
	}
	evidence["configurationDigest"] = strings.Repeat("f", 64)
	body, _ = json.Marshal(document)
	_ = json.Unmarshal(body, &changed)
	after, ok = normalizeSuitePassingEvaluation(&changed)
	if !ok || before.SuiteInventory == after.SuiteInventory {
		t.Fatal("changed endpoint binding reused comparison identity")
	}
}

// These fixed values are also exercised by the independent native CI verifier.
func TestSuiteJudgeCIIdentityContract(t *testing.T) {
	threshold := 70
	version := "aikit-qwen3.5-2b@sha256:d838c5eebf533b5b73f67cc7f4984921f87d64d28e4a52d482740b667f46b8b4"
	rubric := "Score 100 for a scientifically correct explanation that warmer air is less dense than cooler surrounding air and rises through buoyancy. Score 0 if the answer claims warmer air is denser or gravity pulls it upward. Equivalent wording is acceptable; do not require a particular sentence."
	a := agentruntime.EvaluationAssertion{Type: "llmJudge", Rubric: rubric, MinScore: &threshold, JudgeModel: "qwen-3.5-2b", JudgeVersion: version}
	if agentruntime.EvaluationJudgeEvaluatorDigest() != "2866b5c14b0efa8dd13964a40030ffd79877bba4dc00405c1121000113adf793" ||
		agentruntime.EvaluationAssertionDigest(a) != "d58d7753601ecd06b6ace92c80c60fc5078f5eaab749f74fbf81cbcc417c5578" ||
		agentruntime.EvaluationJudgeConfigurationDigest("http://127.0.0.1:18091/v1", a.JudgeModel, version) != "b7ce49e0b23890067e384d903c5deb6425e2909c300411898f2773fa2bd16579" {
		t.Fatal("native CI judge identity contract changed")
	}
}
