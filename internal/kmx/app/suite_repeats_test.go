package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestSuiteRepeatThresholdPipeline(t *testing.T) {
	for _, runtime := range []string{"orka", "kagent"} {
		t.Run(runtime, func(t *testing.T) {
			var a *App
			var opt SuiteWorkspaceEvaluationOptions
			var native *evalFixture
			var sendsDir string
			if runtime == "orka" {
				a, _, native, _ = publicNativeEvaluationFixture(t)
				opt = nativeEvaluationOptions(t)
				opt.ResultPort = native.opt.ResultPort
			} else {
				a, _, _, sendsDir = suiteKagentFixture(t, "")
				opt = suiteKagentEvaluationOptions(t)
				t.Setenv("KMX_KAGENT_TEST_SCENARIO", "eval-success")
			}
			writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), suiteJudgeYAML+"runs: 3\nminPasses: 2\n")
			endpoint, calls := suiteJudgeFixtureServer(t, func(_ *http.Request, n int) int {
				if n <= 2 {
					return 20
				}
				return 90
			})
			opt.JudgeBaseURL = endpoint
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if native != nil {
				for _, f := range p.inventory {
					native.answers[f.Case.Input] = "correct answer"
				}
				native.answers[p.inventory[len(p.inventory)-1].Case.Input] = nativeCanary
			}
			if err = a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 6 {
				t.Fatalf("judge calls %d", calls.Load())
			}
			r, err := readSuitePassingReceipt(t.Context(), opt.Out)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := canonicalSuitePassingReceipt(t.Context(), r, p.suite)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Cases) != 4 || r.Cases[0].Verdict != "fail" || !bytes.Contains(raw, []byte(`"minPasses":2`)) {
				t.Fatal("missing actual run outcomes/threshold")
			}
			if _, err = a.PushSuiteWorkspaceEvaluated(t.Context(), opt.Workspace, "suite:v1", filepath.Join(t.TempDir(), "layout"), false, false, SuitePushProvenance{}, opt.Out); err != nil {
				t.Fatal("evaluated push", err)
			}
			if err = a.DiffAgentEvaluations(EvaluationDiffOptions{Before: opt.Out, After: opt.Out}); err != nil {
				t.Fatal("repeat diff", err)
			}
			// Outcome variation remains comparable, but is not lost when several
			// rows belong to the same logical case.
			alternate, e := parseSuitePassingWire(raw)
			if e != nil {
				t.Fatal(e)
			}
			alternate.Cases[0].Verdict, alternate.Cases[1].Verdict = "pass", "fail"
			alternate.Cases[0].Assertions[0].Verdict, alternate.Cases[1].Assertions[0].Verdict = "pass", "fail"
			alternate.Cases[0].Assertions[0].Judge.Scores = []int{90, 90}
			alternate.Cases[1].Assertions[0].Judge.Scores = []int{20, 20}
			if _, e = canonicalSuitePassingReceipt(t.Context(), &alternate, p.suite); e != nil {
				t.Fatal(e)
			}
			before, _ := normalizeSuitePassingEvaluation(r)
			after, _ := normalizeSuitePassingEvaluation(&alternate)
			if before.SuiteInventory != after.SuiteInventory || before.Cases["first"].Assertions["quality/run/1"].Verdict == after.Cases["first"].Assertions["quality/run/1"].Verdict {
				t.Fatal("repeat diff confused outcomes and identity")
			}
			for _, mutation := range []string{"missing", "reorder", "identity", "unknown", "threshold", "verdict"} {
				var changed suitePassingReceipt
				body, _ := json.Marshal(r)
				changed, err = parseSuitePassingWire(body)
				if err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "missing":
					changed.Cases = changed.Cases[1:]
				case "reorder":
					changed.Cases[0], changed.Cases[1] = changed.Cases[1], changed.Cases[0]
				case "identity":
					if runtime == "orka" {
						changed.Cases[1].TaskUID = changed.Cases[0].TaskUID
					} else {
						changed.Cases[1].KagentExecution.TaskID = changed.Cases[0].KagentExecution.TaskID
					}
				case "unknown":
					changed.Cases[1].Verdict = "unknown"
				case "threshold":
					body = bytes.ReplaceAll(body, []byte(`"minPasses":2`), []byte(`"minPasses":1`))
					changed, err = parseSuitePassingWire(body)
					if err != nil {
						t.Fatal(err)
					}
				case "verdict":
					changed.Cases[0].Verdict = "pass"
				}
				if _, err = canonicalSuitePassingReceipt(t.Context(), &changed, p.suite); err == nil {
					t.Fatalf("accepted %s", mutation)
				}
			}
			sends := 0
			if native != nil {
				files, _ := filepath.Glob(filepath.Join(native.dir, "task-*.json"))
				sends = len(files)
			} else {
				sends = len(evaluationSends(t, sendsDir))
			}
			if sends != 4 {
				t.Fatalf("native sends %d", sends)
			}
			if strings.Contains(fmt.Sprint(a.Out)+fmt.Sprint(a.Err), suiteJudgeCanary) {
				t.Fatal("content leaked")
			}
		})
	}
}

func TestSuiteRepeatJudgeModelMatchesAreObservations(t *testing.T) {
	c, err := agentruntime.ParseEvaluationCase([]byte(suiteJudgeYAML + "runs: 3\nminPasses: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]suitePassingCase, 3)
	for i := range rows {
		assertion := suitePassingAssertionDefinition(c.Assertions[0])
		evidence := *assertion.Judge
		evidence.ConfigurationDigest = strings.Repeat("a", 64)
		evidence.Scores = []int{90, 90}
		evidence.ModelMatches = []string{"requested", "requested"}
		if i == 1 {
			evidence.ModelMatches = []string{"version-qualified", "requested"}
		}
		assertion.Judge = &evidence
		if !agentruntime.EvaluationJudgeEvidenceValid(agentruntime.EvaluationAssertionResult{Type: assertion.Type, Verdict: agentruntime.EvaluationPass, Judge: assertion.Judge}) {
			t.Fatal("invalid judge evidence fixture")
		}
		rows[i] = suitePassingCase{
			ID: c.ID, Path: "metadata/kmx/eval/first.yaml",
			CaseDigest: strings.Repeat("b", 64), InputDigest: agentruntime.EvaluationInputDigest(c.Input),
			Runs: 3, MinPasses: 2, Run: i + 1, Verdict: "pass",
			TaskName: fmt.Sprintf("task-%d", i), TaskUID: fmt.Sprintf("uid-%d", i),
			AnswerDigest: agentruntime.EvaluationAnswerDigest("answer"),
			Assertions:   []suitePassingAssertion{assertion},
		}
	}
	if err := validateSuiteRunGroups(rows); err != nil {
		t.Fatalf("accepted model-match variation changed repeated authored identity: %v", err)
	}
	r := &suitePassingReceipt{SchemaVersion: 1, ExecutionMode: "native-orka", Cases: rows}
	before, ok := normalizeSuitePassingEvaluation(r)
	if !ok {
		t.Fatal("cannot normalize repeated judge")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := parseSuitePassingWire(raw)
	if err != nil {
		t.Fatal(err)
	}
	for i := range changed.Cases {
		changed.Cases[i].Assertions[0].Judge.ModelMatches = []string{"requested", "version-qualified"}
		changed.Cases[i].Assertions[0].Judge.Scores = []int{80, 85}
	}
	after, ok := normalizeSuitePassingEvaluation(&changed)
	if !ok || before.SuiteInventory != after.SuiteInventory {
		t.Fatal("repeat sample variation changed comparison identity")
	}
	if len(before.Cases[c.ID].Assertions) != 3 || before.Cases[c.ID].Assertions["quality/run/2"].Judge.ModelMatches[0] != "version-qualified" {
		t.Fatal("run-qualified observations lost or mutated by normalization")
	}
}

func TestSuiteRepeatInventoryBudget(t *testing.T) {
	root, _, _, _, _ := passingFixture(t)
	writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: first\ninput: question\nexpectContains: [answer]\nruns: 100\n")
	p, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := workspace.Load(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	// Many valid assertions times the declared runs must be reserved offline.
	c := s.Cases[0]
	c.Bytes = []byte("id: first\ninput: question\nruns: 100\nexpectContains: [" + strings.Repeat("answer,", 300) + "answer]\n")
	c.Digest = "sha256:" + agentruntime.EvaluationCaseDigest(c.Bytes)
	s.Cases[0] = c
	if _, _, err = suitePassingInventory(s); err == nil {
		t.Fatal("unbounded repeat receipt admitted")
	}
	s.Runtime = workspace.RuntimeProfile{ID: "agentsessions", Version: workspace.AgentSessionsVersion, Engine: "chat"}
	if _, _, err = suitePassingInventory(s); err == nil {
		t.Fatal("unbounded Sessions repeat receipt admitted")
	}
}

func repeatDriverFrames(t *testing.T, policy string) []suitePassingInventoryCase {
	t.Helper()
	c, err := agentruntime.ParseEvaluationCase([]byte("id: first\ninput: question\nassertions: [{id: text, type: contains, value: answer}, {id: extra, type: notContains, value: wrong}]\n" + policy))
	if err != nil {
		t.Fatal(err)
	}
	n, min := c.RunPolicy()
	frames := make([]suitePassingInventoryCase, n)
	for i := range frames {
		metadata := suitePassingCase{ID: c.ID, Path: "metadata/kmx/eval/first.yaml", Verdict: "pass"}
		for _, a := range c.Assertions {
			metadata.Assertions = append(metadata.Assertions, suitePassingAssertionDefinition(a))
		}
		if n > 1 {
			metadata.Runs, metadata.MinPasses, metadata.Run = n, min, i+1
		}
		frames[i] = suitePassingInventoryCase{c, metadata}
	}
	return frames
}

func TestSuiteRunDriverUnknownDeadlineAndFailure(t *testing.T) {
	for _, scenario := range []string{"all runs", "late unknown", "mixed unknown", "execution failure", "missing answer", "stop", "late cancel", "threshold missed", "default all", "per-run deadlines"} {
		t.Run(scenario, func(t *testing.T) {
			policy := "runs: 3\nminPasses: 2\n"
			if scenario == "default all" {
				policy = "runs: 3\n"
			}
			frames := repeatDriverFrames(t, policy)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			var deadline time.Time
			observations, err := evaluateSuiteRuns(ctx, frames, time.Minute, io.Discard, func(runCtx context.Context, f suitePassingInventoryCase) (suitePassingObservation, error) {
				calls++
				d, _ := runCtx.Deadline()
				if calls > 1 && !d.After(deadline) {
					t.Fatal("run inherited the preceding run's deadline")
				}
				deadline = d
				rows := agentruntime.EvaluateAssertions(f.Case, agentruntime.EvaluationEvidence{Answer: "answer", AnswerAvailable: true})
				o := suitePassingObservation{EvaluationReceipt: agentruntime.EvaluationReceipt{CaseID: f.Case.ID, Verdict: agentruntime.EvaluationPass, AnswerSHA256: agentruntime.EvaluationAnswerDigest("answer"), Assertions: rows}}
				if scenario == "execution failure" {
					o.AnswerSHA256 = ""
					o.Assertions = nil
					o.Verdict = agentruntime.EvaluationFail
					return o, errSuiteNativeCaseFailed
				}
				if scenario == "missing answer" {
					o.AnswerSHA256 = ""
					return o, nil
				}
				if scenario == "stop" {
					o.Stop = true
					return o, nil
				}
				if scenario == "threshold missed" && calls <= 2 || scenario == "default all" && calls == 1 {
					o.Assertions[0].Verdict = agentruntime.EvaluationFail
					o.Verdict = agentruntime.EvaluationFail
					return o, errSuiteNativeCaseFailed
				}
				if calls == 3 {
					if scenario == "late cancel" {
						cancel()
					}
					if scenario == "late unknown" || scenario == "mixed unknown" {
						o.Assertions[1].Verdict = agentruntime.EvaluationUnknown
						if scenario == "mixed unknown" {
							o.Assertions[0].Verdict = agentruntime.EvaluationFail
							o.Verdict = agentruntime.EvaluationFail
							return o, errSuiteNativeCaseFailed
						}
					}
				}
				return o, nil
			})
			switch scenario {
			case "all runs", "per-run deadlines":
				if err != nil || len(observations) != 3 {
					t.Fatalf("%v", err)
				}
			case "threshold missed", "default all", "execution failure":
				if !errors.Is(err, errSuiteNativeCaseFailed) {
					t.Fatalf("known failure: %v", err)
				}
			case "late cancel":
				if !errors.Is(err, context.Canceled) || !errors.Is(err, errSuiteNativeEvaluation) {
					t.Fatalf("cancellation classification: %v", err)
				}
			default:
				if err == nil || errors.Is(err, errSuiteNativeCaseFailed) {
					t.Fatalf("unknown credited or failed: %v", err)
				}
			}
			expected := 3
			if scenario == "execution failure" || scenario == "missing answer" || scenario == "stop" {
				expected = 1
			}
			if calls != expected {
				t.Fatalf("executed %d, want %d", calls, expected)
			}
		})
	}
}

func TestSuiteRepeatFailedNativeTaskStillAborts(t *testing.T) {
	a, placement, _, f := nativeEvaluationFixture(t)
	frames := repeatDriverFrames(t, "runs: 3\nminPasses: 1\n")
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "get") && slices.Contains(cmd.Args, "tasks.core.orka.ai") && !slices.Contains(cmd.Args, "name") {
			var response bytes.Buffer
			out := cmd.Stdout
			cmd.Stdout = &response
			err := capture(ctx, cmd)
			cmd.Stdout = out
			if err != nil {
				return err
			}
			var doc map[string]any
			if err = json.Unmarshal(response.Bytes(), &doc); err != nil {
				return err
			}
			doc["status"].(map[string]any)["phase"] = "Failed"
			return json.NewEncoder(out).Encode(doc)
		}
		return capture(ctx, cmd)
	}
	calls := 0
	_, err := evaluateSuiteRuns(t.Context(), frames, time.Second*30, io.Discard, func(ctx context.Context, frame suitePassingInventoryCase) (suitePassingObservation, error) {
		calls++
		return a.evaluateSuiteNative(ctx, placement, frame, f.opt.ResultPort, nil)
	})
	if !errors.Is(err, errSuiteNativeCaseFailed) || calls != 1 {
		t.Fatalf("failed Task treated as tolerated assertion: %v, calls %d", err, calls)
	}
}

// Fixed candidate answers isolate the threshold from both sampling and judging.
func TestSuiteRepeatFiveRunThresholdPatterns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern []bool
		passing bool
	}{
		{"one failure", []bool{true, true, false, true, true}, true},
		{"two failures", []bool{true, false, true, false, true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := repeatDriverFrames(t, "runs: 5\nminPasses: 4\n")
			calls := 0
			var out bytes.Buffer
			observations, err := evaluateSuiteRuns(t.Context(), frames, time.Minute, &out, func(_ context.Context, f suitePassingInventoryCase) (suitePassingObservation, error) {
				answer := "answer"
				if !tc.pattern[calls] {
					answer = "wrong"
				}
				calls++
				assertions := agentruntime.EvaluateAssertions(f.Case, agentruntime.EvaluationEvidence{Answer: answer, AnswerAvailable: true})
				return suitePassingObservation{EvaluationReceipt: agentruntime.EvaluationReceipt{CaseID: f.Case.ID, AnswerSHA256: agentruntime.EvaluationAnswerDigest(answer), Assertions: assertions}}, nil
			})
			if calls != 5 || strings.Count(out.String(), "run ") != 5 {
				t.Fatalf("skipped a declared run: %d %q", calls, out.String())
			}
			if tc.passing {
				if err != nil || len(observations) != 5 || observations[2].Verdict != agentruntime.EvaluationFail {
					t.Fatalf("4/5 must pass retaining failure: %v", err)
				}
			} else if !errors.Is(err, errSuiteNativeCaseFailed) {
				t.Fatalf("3/5 must fail threshold: %v", err)
			}
		})
	}
}

func TestSuiteRepeatTimeBudgetPreflight(t *testing.T) {
	a, _, native, _ := publicNativeEvaluationFixture(t)
	opt := nativeEvaluationOptions(t)
	opt.ResultPort = native.opt.ResultPort
	opt.CaseTimeout = 5 * time.Minute
	writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), "id: first\ninput: question\nexpectContains: [answer]\nruns: 5\nminPasses: 4\n")
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// Stop at the operational guard; the repeat time/cost notice must precede it.
	a.Cfg.Confirm = ""
	a.Stdin = nil
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "config") {
			return errors.New("guard inspection boundary")
		}
		return capture(ctx, cmd)
	}
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err == nil || !strings.Contains(err.Error(), "cannot read context metadata for the mutation guard") {
		t.Fatalf("expected guard metadata refusal: %v", err)
	}
	text := fmt.Sprint(a.Err)
	if !strings.Contains(text, "6 fresh Tasks") || !strings.Contains(text, "5m0s per run") || !strings.Contains(text, "30m0s total") {
		t.Fatalf("missing aggregate run budget: %s", text)
	}
}

func TestSuiteRunAbortKeepsDefaultCaseSummary(t *testing.T) {
	for _, verdict := range []agentruntime.EvaluationVerdict{agentruntime.EvaluationFail, agentruntime.EvaluationUnknown} {
		t.Run(string(verdict), func(t *testing.T) {
			frames := repeatDriverFrames(t, "")
			var out bytes.Buffer
			_, err := evaluateSuiteRuns(t.Context(), frames, time.Minute, &out, func(context.Context, suitePassingInventoryCase) (suitePassingObservation, error) {
				cause := errSuiteNativeEvaluation
				if verdict == agentruntime.EvaluationFail {
					cause = errSuiteNativeCaseFailed
				}
				return suitePassingObservation{EvaluationReceipt: agentruntime.EvaluationReceipt{CaseID: "first", Verdict: verdict}}, cause
			})
			if err == nil || out.String() != "first: "+string(verdict)+"\n" {
				t.Fatalf("lost aborted case summary: %q, %v", out.String(), err)
			}
		})
	}
}

func TestSuiteSessionsForgedRepeatReceiptRefused(t *testing.T) {
	p, subject, observations := sessionsPassingFixture(t)
	r, err := newSuiteSessionsPassingReceipt(t.Context(), p, subject, observations)
	if err != nil {
		t.Fatal(err)
	}
	first := r.Cases[0]
	first.Runs, first.MinPasses, first.Run = 2, 2, 1
	second := first
	second.Run = 2
	execution := *second.SessionsExecution
	execution.SessionUID = "other-session"
	execution.ExecutionID = "other-execution"
	second.SessionsExecution = &execution
	r.Cases = append([]suitePassingCase{first, second}, r.Cases[1:]...)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseSuitePassingReceipt(raw)
	if err != nil {
		t.Fatal("structurally valid repeated proof refused", err)
	}
	if _, err = canonicalSuitePassingReceipt(t.Context(), parsed, p.suite); !errors.Is(err, errSuitePassingReceipt) {
		t.Fatal("reader accepted unauthored AgentSessions repeats", err)
	}
}
