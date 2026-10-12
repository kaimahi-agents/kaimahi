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
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	rt "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func workspaceEvaluationFixture(t *testing.T, body string) (*suiteTaskFixture, EvaluateSuiteWorkspaceOptions) {
	t.Helper()
	f := newSuiteTaskFixture(t, body)
	root := filepath.Join(t.TempDir(), "workspace")
	input := filepath.Join(t.TempDir(), "instructions.md")
	if e := os.WriteFile(input, []byte("  Answer exactly.\n\nPackaged whitespace.\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "sample", Description: "Packaged description", Model: "packaged-model", InstructionsFile: input}); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "eval/example.yaml"), []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"Provider", "Agent"} {
		if e := os.Remove(filepath.Join(f.dir, orkaPlural(kind)+".json")); e != nil {
			t.Fatal(e)
		}
	}
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("KMX_HOME", state)
	opt := EvaluateSuiteWorkspaceOptions{Workspace: root, ToContext: f.app.Cfg.KubeContext, ToNamespace: "orka-system", Inference: "provider:inference", Out: filepath.Join(t.TempDir(), "receipt.json"), ResultPort: f.opt.ResultPort, CaseTimeout: 30 * time.Second}
	f.calls = nil
	f.app.guarded = true // Orchestration must reset this and guard the explicit target.
	return f, opt
}

func TestSuiteWorkspaceEvaluatePipelinePinAndPackagedBytes(t *testing.T) {
	f, opt := workspaceEvaluationFixture(t, suiteTaskCase)
	f.app.Cfg.KubeContext = "kind-unrelated"
	f.app.Cfg.Model = "wrong-config-model"
	originalSource, _ := json.Marshal(f.source)
	edited := false
	f.beforeCapture = func(c orkaCall) {
		if c.Document["kind"] == "Task" && slices.Contains(c.Args, "create") && !edited {
			edited = true
			state, _ := config.StateDir()
			writer, e := lockSuiteCandidate(opt.Workspace, state)
			if e != nil {
				t.Fatal("evaluation held candidate lock during native execution", e)
			}
			p, e := writer.load(t.Context())
			if e != nil || p == nil {
				t.Fatal("candidate was not durable before execution", e)
			}
			if e := writer.Close(); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(opt.Workspace, "eval/example.yaml"), []byte("id: changed\ninput: wrong\nexpectContains: [wrong]\n"), 0600); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e := f.app.EvaluateSuiteWorkspace(opt); e != nil {
		t.Fatal(e)
	}
	receipt, e := readSuiteEvaluationReceiptFile(opt.Out)
	if e != nil {
		t.Fatal(e)
	}
	if !receipt.FullCaseSet || !receipt.PromotionEligible || receipt.Result != "pass" || len(receipt.Cases) != 1 || receipt.Cases[0].TaskUID == "" {
		t.Fatalf("receipt %+v", receipt)
	}
	writes := f.writes(0)
	if len(writes) != 3 || writes[0].Document["kind"] != "Provider" || writes[1].Document["kind"] != "Agent" || writes[2].Document["kind"] != "Task" || nativeSpec(writes[2].Document)["prompt"] != "Packaged question" || nativeSpec(writes[2].Document)["type"] != "ai" {
		t.Fatalf("writes %+v", writes)
	}
	source, _ := json.Marshal(f.source)
	if !bytes.Equal(source, originalSource) {
		t.Fatal("selected Provider mutated")
	}
	if f.app.Cfg.KubeContext != "kind-unrelated" {
		t.Fatal("caller config changed")
	}
	for _, c := range f.calls {
		if len(c.Args) < 2 || c.Args[0] != "--context" || c.Args[1] != opt.ToContext {
			t.Fatal("target call lost explicit context")
		}
	}
	if nativeSpec(writes[0].Document)["defaultModel"] != "packaged-model" || nativeSpec(writes[1].Document)["systemPrompt"].(map[string]any)["inline"] != "  Answer exactly.\n\nPackaged whitespace.\n" {
		t.Fatal("prepared model/instructions substituted")
	}
	if !strings.Contains(f.stderr.String(), "about to:") || !strings.Contains(f.stderr.String(), opt.ToContext) || !strings.Contains(f.stderr.String(), opt.ToNamespace) {
		t.Fatal("orchestration reused guard or omitted target")
	}
	for _, canary := range []string{"PRIVATE_ANSWER_CANARY", "PRIVATE_SECRET_VALUE_CANARY", "Packaged question", "Packaged whitespace", orkaTestToken()} {
		if strings.Contains(f.out.String()+f.stderr.String(), canary) {
			t.Fatalf("disclosed %s", canary)
		}
	}
	if !strings.Contains(f.out.String(), receipt.Subject.SuiteDigest) || !strings.Contains(f.out.String(), receipt.RunID) || !strings.Contains(f.out.String(), "requiredImages: []") || strings.Count(f.stderr.String(), "full effective authority") != 1 || !strings.Contains(f.stderr.String(), "Task-read enforcement is unverified") {
		t.Fatalf("output %s\n%s", f.out, f.stderr)
	}
	state, _ := config.StateDir()
	history := filepath.Join(state, "suite-evidence/runs/eval-"+receipt.RunID+".json")
	a, err := os.ReadFile(history)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{history, opt.Out} {
		info, e := os.Stat(path)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatal("receipt is not private 0600", e)
		}
	}
	for _, path := range []string{state, filepath.Join(state, "suite-candidates"), filepath.Join(state, "suite-evidence"), filepath.Join(state, "suite-evidence/runs")} {
		info, e := os.Stat(path)
		if e != nil || info.Mode().Perm() != 0700 {
			t.Fatal("state is not private 0700", e)
		}
	}
	if !bytes.Equal(a, b) {
		t.Fatal("export differs from history")
	}
	f.out.Reset()
	layout := filepath.Join(t.TempDir(), "layout")
	restarted := &App{Out: f.out}
	if _, e := restarted.PushSuiteWorkspace(t.Context(), opt.Workspace, "team:v1", layout, false, false, SuitePushProvenance{}); e == nil || !strings.Contains(e.Error(), "rerun evaluation") {
		t.Fatalf("source drift published: %v", e)
	}
	if _, e := os.Stat(layout); !os.IsNotExist(e) {
		t.Fatal("drift transferred artifact")
	}
}

func TestSuiteWorkspaceEvaluateDiagnosticAndHonestFailures(t *testing.T) {
	for _, scenario := range []string{"diagnostic", "missing-case", "native-failed", "timeout", "ambiguous-placement", "final-drift", "export-collision", "history-permission", "guard-refused", "result-preflight"} {
		t.Run(scenario, func(t *testing.T) {
			f, opt := workspaceEvaluationFixture(t, suiteTaskCase)
			switch scenario {
			case "diagnostic":
				opt.Case = "packaged"
			case "missing-case":
				opt.Case = "absent"
			case "native-failed":
				f.phase = "Failed"
			case "timeout":
				f.holdBody = true
				f.app.waitTiming = &waitTiming{timeout: func(parent context.Context, phase string, d time.Duration) (context.Context, context.CancelFunc) {
					if phase == "orka-evaluation-case" {
						d = 400 * time.Millisecond
					}
					if d == 0 {
						return parent, func() {}
					}
					return context.WithTimeout(parent, d)
				}}
			case "ambiguous-placement":
				f.after = func(c orkaCall) error {
					if c.Document["kind"] == "Provider" && slices.Contains(c.Args, "create") && !slices.Contains(c.Args, "--dry-run=server") {
						return context.DeadlineExceeded
					}
					return nil
				}
			case "final-drift":
				agentReads := 0
				f.beforeCapture = func(c orkaCall) {
					if f.resultReads.Load() > 0 && slices.Contains(c.Args, "agents.core.orka.ai") {
						agentReads++
						if agentReads == 3 {
							f.secretRV = "changed"
						}
					}
				}
			case "export-collision":
				f.afterResult = func() {
					if e := os.WriteFile(opt.Out, []byte("occupied"), 0600); e != nil {
						t.Fatal(e)
					}
				}
			case "history-permission":
				f.afterResult = func() {
					state, _ := config.StateDir()
					if e := os.Chmod(filepath.Join(state, "suite-evidence"), 0755); e != nil {
						t.Fatal(e)
					}
				}
			case "guard-refused":
				opt.ToContext = "remote-proof"
				f.app.Cfg.Confirm = ""
			case "result-preflight":
				original := f.app.captureCommand
				f.app.captureCommand = func(ctx context.Context, c *exec.Cmd) error {
					if slices.Contains(c.Args, "serviceaccount") && slices.Contains(c.Args, orkaResultAccount) {
						return errors.New("PRIVATE_AUTH_CANARY")
					}
					return original(ctx, c)
				}
			}
			e := f.app.EvaluateSuiteWorkspace(opt)
			if e == nil {
				t.Fatal("non-full/failed/unknown run succeeded")
			}
			r, readErr := readSuiteEvaluationReceiptFile(opt.Out)
			switch scenario {
			case "missing-case", "guard-refused", "result-preflight":
				if len(f.writes(0)) != 0 {
					t.Fatal("preflight refusal wrote native resources")
				}
			case "export-collision", "history-permission":
				if strings.Contains(f.out.String(), "history:") || strings.Contains(f.out.String(), "receipt:") {
					t.Fatal("late failure printed saved success")
				}
			default:
				if readErr != nil {
					t.Fatal(readErr)
				}
				if r.PromotionEligible {
					t.Fatal("failure qualified")
				}
				if scenario == "diagnostic" {
					if r.FullCaseSet || r.Result != "pass" {
						t.Fatalf("diagnostic %+v", r)
					}
				} else if scenario == "native-failed" {
					if r.Result != "fail" {
						t.Fatalf("native fail %+v", r)
					}
				} else if r.Result != "unknown" {
					t.Fatalf("not unknown %+v", r)
				}
				if scenario == "ambiguous-placement" && (r.Placement.Complete || len(r.Placement.Resources) != 1 || r.Placement.Resources[0].UID != "" || r.Cases[0].TaskUID != "") {
					t.Fatal("invented placement/Task UID")
				}
				if scenario == "final-drift" && r.Detail != "subject unavailable" {
					t.Fatalf("no final check: %+v", r)
				}
			}
		})
	}
}

type suiteCaseSummaryFailure struct{ io.Writer }

func (w suiteCaseSummaryFailure) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("packaged: pass")) {
		return 0, errors.New("PRIVATE_WRITER_CANARY")
	}
	return w.Writer.Write(p)
}

func TestSuiteWorkspaceEvaluateSummaryFailureCannotQualify(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(map[int]string{1: "one", 2: "two"}[count], func(t *testing.T) {
			f, opt := workspaceEvaluationFixture(t, suiteTaskCase)
			if count == 2 {
				body := strings.ReplaceAll(suiteTaskCase, "id: packaged", "id: remaining")
				if e := os.WriteFile(filepath.Join(opt.Workspace, "eval/remaining.yaml"), []byte(body), 0600); e != nil {
					t.Fatal(e)
				}
			}
			f.app.Out = suiteCaseSummaryFailure{f.out}
			if e := f.app.EvaluateSuiteWorkspace(opt); e == nil {
				t.Fatal("failed summary succeeded")
			}
			r, e := readSuiteEvaluationReceiptFile(opt.Out)
			if e != nil {
				t.Fatal(e)
			}
			if r.PromotionEligible || r.Result != "unknown" || r.Detail != "run incomplete" || len(r.Cases) != count || r.Cases[0].TaskUID == "" || r.Cases[0].AnswerSHA256 == "" {
				t.Fatalf("output failure qualified/lost observation: %+v", r)
			}
			if count == 2 && (r.Cases[1].Verdict != "unknown" || r.Cases[1].TaskUID != "" || r.Cases[1].TaskName != "") {
				t.Fatal("unexecuted case was skipped or fabricated")
			}
			if len(f.tasks) != 1 {
				t.Fatal("output failure retried or continued execution")
			}
		})
	}
}

func TestSuiteWorkspaceEvaluatePreflightBeforeNativeWrites(t *testing.T) {
	for _, scenario := range []string{"out-collision", "out-authored", "out-cache", "out-source", "unsafe-history", "malformed-case", "unsafe-selector", "invalid-judge"} {
		t.Run(scenario, func(t *testing.T) {
			f, opt := workspaceEvaluationFixture(t, suiteTaskCase)
			state, _ := config.StateDir()
			switch scenario {
			case "out-collision":
				if e := os.WriteFile(opt.Out, []byte("private-existing"), 0600); e != nil {
					t.Fatal(e)
				}
			case "out-authored":
				opt.Out = filepath.Join(opt.Workspace, "new.json")
			case "out-cache":
				opt.Out = filepath.Join(state, "suites", "new.json")
			case "out-source":
				opt.Out = filepath.Join(opt.Workspace, "agents/sample/instructions.md")
			case "unsafe-history":
				if e := os.MkdirAll(state, 0755); e != nil {
					t.Fatal(e)
				}
				if e := os.Chmod(state, 0755); e != nil {
					t.Fatal(e)
				}
			case "malformed-case":
				if e := os.WriteFile(filepath.Join(opt.Workspace, "eval/example.yaml"), []byte("PRIVATE_PARSE_CANARY: {bad\n"), 0600); e != nil {
					t.Fatal(e)
				}
			case "unsafe-selector":
				opt.Case = "ghp_" + strings.Repeat("x", 36)
			case "invalid-judge":
				opt.JudgeBaseURL = "http://127.0.0.1:1" // no selected judge
			}
			e := f.app.EvaluateSuiteWorkspace(opt)
			if e == nil || strings.Contains(e.Error(), "PRIVATE_PARSE_CANARY") || len(f.writes(0)) != 0 || len(f.calls) != 0 {
				t.Fatalf("preflight %v calls=%d", e, len(f.calls))
			}
			if _, e := os.Stat(filepath.Join(state, "suites")); scenario != "invalid-judge" && !os.IsNotExist(e) {
				t.Fatal("invalid preflight prepared candidate")
			}
		})
	}
}

func TestSuiteWorkspaceEvaluateExplicitRerunRepins(t *testing.T) {
	f, opt := workspaceEvaluationFixture(t, suiteTaskCase)
	opt.Case = "packaged"
	if e := f.app.EvaluateSuiteWorkspace(opt); e == nil {
		t.Fatal("diagnostic succeeded")
	}
	first, e := readSuiteEvaluationReceiptFile(opt.Out)
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(opt.Workspace, "agents/sample/instructions.md"), []byte("Explicitly reevaluated instructions"), 0600); e != nil {
		t.Fatal(e)
	}
	opt.Out = filepath.Join(t.TempDir(), "rerun.json")
	opt.Case = ""
	if e := f.app.EvaluateSuiteWorkspace(opt); e != nil {
		t.Fatal(e)
	}
	second, e := readSuiteEvaluationReceiptFile(opt.Out)
	if e != nil || second.Subject.SuiteDigest == first.Subject.SuiteDigest || !second.PromotionEligible {
		t.Fatalf("rerun %+v %v", second, e)
	}
	var out bytes.Buffer
	result, e := (&App{Out: &out}).PushSuiteWorkspace(t.Context(), opt.Workspace, "team:v1", filepath.Join(t.TempDir(), "layout"), false, false, SuitePushProvenance{})
	if e != nil || result.Descriptor.Digest.String() != second.Subject.SuiteDigest {
		t.Fatalf("republished wrong candidate %v %v", result, e)
	}
}

func suiteWorkspaceLargeCase(id string, count int) string {
	var body strings.Builder
	fmt.Fprintf(&body, "id: %s\ninput: Packaged question\nassertions:\n", id)
	for i := 0; i < count; i++ {
		fmt.Fprintf(&body, "- {id: row%d, type: contains, value: answer}\n", i)
	}
	return body.String()
}

func TestSuiteWorkspaceEvaluateAggregateBudgetBeforePinAndNativeWrites(t *testing.T) {
	f, opt := workspaceEvaluationFixture(t, suiteTaskCase)
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("large-%d", i)
		path := filepath.Join(opt.Workspace, "eval", name+".yaml")
		if e := os.WriteFile(path, []byte(suiteWorkspaceLargeCase(name, 1000)), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.Remove(filepath.Join(opt.Workspace, "eval/example.yaml")); e != nil {
		t.Fatal(e)
	}
	err := f.app.EvaluateSuiteWorkspace(opt)
	if err == nil || !strings.Contains(err.Error(), "1 MiB receipt budget") || len(f.writes(0)) != 0 || len(f.tasks) != 0 || len(f.calls) != 0 {
		t.Fatalf("aggregate receipt refused after native work: %v writes=%d Tasks=%d reads=%d", err, len(f.writes(0)), len(f.tasks), len(f.calls))
	}
	state, e := config.StateDir()
	if e != nil {
		t.Fatal(e)
	}
	pins, e := filepath.Glob(filepath.Join(state, "suite-candidates", "*.json"))
	if e != nil || len(pins) != 0 {
		t.Fatal("oversized selection published a candidate pin", e)
	}
	runs, e := os.ReadDir(filepath.Join(state, "suite-evidence/runs"))
	if e != nil || len(runs) != 0 {
		t.Fatal("oversized selection invented history", e)
	}
}

func TestSuiteWorkspaceEvaluateLargeCaseStillRunsAndExports(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f, opt := workspaceEvaluationFixture(t, suiteWorkspaceLargeCase("large-valid", 1000))
			if count == 2 {
				if e := os.WriteFile(filepath.Join(opt.Workspace, "eval/second.yaml"), []byte(suiteWorkspaceLargeCase("large-second", 1000)), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if e := f.app.EvaluateSuiteWorkspace(opt); e != nil {
				t.Fatal(e)
			}
			r, e := readSuiteEvaluationReceiptFile(opt.Out)
			if e != nil || !r.PromotionEligible || len(r.Cases) != count || len(f.tasks) != count {
				t.Fatalf("large valid cases arbitrarily refused: %v cases=%d Tasks=%d", e, len(r.Cases), len(f.tasks))
			}
			for _, c := range r.Cases {
				if len(c.Assertions) != 1000 {
					t.Fatal("large case lost assertion rows")
				}
			}
		})
	}
}

func TestSuiteWorkspaceEvaluateRejectsUnserializablePreparedSubjectBeforeWrites(t *testing.T) {
	for _, identity := range []string{"provider", "runtime"} {
		t.Run(identity, func(t *testing.T) {
			f, opt := workspaceEvaluationFixture(t, suiteTaskCase)
			canary := "PRIVATE_METADATA_CANARY-café"
			if identity == "provider" {
				nativeMeta(f.source)["resourceVersion"] = canary
			} else {
				nativeMeta(f.controller)["labels"].(map[string]any)["app.kubernetes.io/instance"] = canary
				nativeMeta(f.account)["labels"].(map[string]any)["app.kubernetes.io/instance"] = canary
			}
			// First establish that the real read-only renderer/admission accepts this
			// live identity, while the existing receipt domain does not.
			state, e := config.StateDir()
			if e != nil {
				t.Fatal(e)
			}
			prepared, e := workspace.Prepare(t.Context(), opt.Workspace, filepath.Join(state, "suites"))
			if e != nil {
				t.Fatal(e)
			}
			suite, e := workspace.Load(t.Context(), prepared)
			if e != nil {
				t.Fatal(e)
			}
			plan, e := f.app.prepareSuiteNative(t.Context(), suite, opt.ToNamespace, "inference")
			if e != nil {
				t.Fatal("fixture did not reach subject serialization boundary", e)
			}
			if validateSuiteEvidenceSubject(suite, plan.Subject) == nil {
				t.Fatal("fixture subject is evidence-compatible")
			}
			if len(f.writes(0)) != 0 {
				t.Fatal("preparation wrote resources")
			}
			f.calls = nil
			err := f.app.EvaluateSuiteWorkspace(opt)
			if err == nil || !strings.Contains(err.Error(), "native evidence subject unavailable") || strings.Contains(err.Error()+f.out.String()+f.stderr.String(), canary) || len(f.writes(0)) != 0 || len(f.tasks) != 0 {
				t.Fatalf("unserializable plan mutated target: %v writes=%d Tasks=%d", err, len(f.writes(0)), len(f.tasks))
			}
		})
	}
}

// Literal upper-bound identity rows exercise the new schema's added evaluator
// metadata against the reused legacy reserve, not a serializer-only count.
func TestSuiteWorkspaceEvaluationAssertionReserveCoversEvidenceRows(t *testing.T) {
	for _, kind := range []string{"contains", "toolNotCalled", "llmJudge"} {
		threshold := 100
		definition := rt.EvaluationAssertion{ID: strings.Repeat("a", 63), Type: kind, JudgeModel: strings.Repeat("<", 256), JudgeVersion: strings.Repeat(">", 256), MinScore: &threshold}
		row := suiteEvidenceAssertion{ID: definition.ID, Type: kind, DefinitionDigest: strings.Repeat("b", 64), EvaluatorVersion: suiteAssertionVersion(kind), Verdict: "unknown", Reason: "runtime does not support tools"}
		if kind == "llmJudge" {
			row.EvaluatorVersion = "kmx.llm-judge/v1"
			row.Reason = "judge response unavailable"
			row.Judge = &rt.EvaluationJudgeEvidence{Model: definition.JudgeModel, ModelVersion: definition.JudgeVersion, Provenance: "user-declared", EvaluatorDigest: strings.Repeat("c", 64), ConfigurationDigest: strings.Repeat("d", 64), MinScore: 100, Scores: []int{100, 100}}
		}
		raw, e := json.MarshalIndent(row, "      ", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if len(raw)+8 > evaluationAssertionReceiptBytes(definition) {
			t.Fatalf("%s suite row exceeds reused assertion reserve: actual=%d reserve=%d", kind, len(raw)+8, evaluationAssertionReceiptBytes(definition))
		}
	}
}

func TestSuiteWorkspaceEvaluateMixedJudgesAndContentOptIn(t *testing.T) {
	for _, show := range []bool{false, true} {
		t.Run(map[bool]string{false: "summary", true: "content"}[show], func(t *testing.T) {
			f, opt := workspaceEvaluationFixture(t, suiteTaskCase)
			judgeBody := strings.ReplaceAll(suiteTaskJudgeCase, "id: packaged", "id: judged")
			if e := os.WriteFile(filepath.Join(opt.Workspace, "eval/judge.yaml"), []byte(judgeBody), 0600); e != nil {
				t.Fatal(e)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(w, judgeEnvelope(`{"score":90,"reasoning":"PRIVATE_REASON_CANARY"}`))
			}))
			defer server.Close()
			opt.JudgeBaseURL = server.URL
			opt.ShowContent = show
			if e := f.app.EvaluateSuiteWorkspace(opt); e != nil {
				t.Fatal(e)
			}
			r, e := readSuiteEvaluationReceiptFile(opt.Out)
			if e != nil || r.Result != "pass" || len(r.Cases) != 2 || calls.Load() != 2 {
				t.Fatalf("mixed judge run %+v %v calls=%d", r, e, calls.Load())
			}
			if strings.Contains(f.out.String(), "PRIVATE_ANSWER_CANARY") != show || strings.Contains(f.out.String(), "PRIVATE_REASON_CANARY") != show {
				t.Fatal("content opt-in boundary")
			}
		})
	}
}
