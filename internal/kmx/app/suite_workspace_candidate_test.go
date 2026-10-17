package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// A single repeated case keeps planned runs distinct from executed Tasks.
func repeatedCandidateCase(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: first\ninput: '"+nativeCanary+" first'\nexpectContains: ['"+nativeCanary+"']\nruns: 3\nminPasses: 2\n")
	if err := os.Remove(filepath.Join(root, "eval/second.yaml")); err != nil {
		t.Fatal(err)
	}
}

func candidateAbortScenario(t *testing.T, state *publicCandidateFixture, mode string) *int {
	t.Helper()
	tasks := new(int)
	state.afterCreate = func(key string) {
		if !strings.HasPrefix(key, "Task/") {
			return
		}
		*tasks++
		if mode == "late" && *tasks < 3 {
			return
		}
		phase := "Failed"
		if mode == "Cancelled" {
			phase = mode
		}
		state.externalTasks = false
		state.objects[key]["status"] = map[string]any{"phase": phase, "message": nativeCanary}
	}
	if mode == "no TaskUID" {
		state.change = func(_ string, cmd *exec.Cmd) error {
			if slices.Contains(cmd.Args, "token") && len(state.creates) == 2 {
				return errors.New(nativeCanary)
			}
			return nil
		}
	}
	if mode == "cleanup" {
		state.beforeDelete = func(context.Context, *exec.Cmd) error { return errors.New(nativeCanary) }
	}
	return tasks
}

func TestCandidateExecutionAbortSummary(t *testing.T) {
	for _, mode := range []string{"Failed", "Cancelled", "late", "no TaskUID", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("KMX_HOME", privateSuiteDir(t))
			a, _, f, state := publicNativeEvaluationFixture(t)
			opt := nativeEvaluationOptions(t)
			opt.ResultPort, opt.ManagedPass = f.opt.ResultPort, true
			repeatedCandidateCase(t, opt.Workspace)
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			f.answers[nativeCanary+" first"] = nativeCanary + " answer"
			tasks := candidateAbortScenario(t, state, mode)
			err = a.EvaluateSuiteWorkspace(t.Context(), p)
			wantExecuted, wantPassed := 1, 0
			if mode == "late" {
				wantExecuted, wantPassed = 3, 2
			}
			if mode == "no TaskUID" {
				wantExecuted = 0
			}
			want := fmt.Sprintf("%d of 3 runs executed; %d passed; aborted on execution error; %d not executed", wantExecuted, wantPassed, 3-wantExecuted)
			if err == nil || !strings.Contains(err.Error(), want) || *tasks != wantExecuted {
				t.Fatalf("dishonest abort: tasks=%d err=%v; want %q", *tasks, err, want)
			}
			if mode == "cleanup" {
				if strings.Contains(err.Error(), "cleanup completed") || len(state.objects) == 0 {
					t.Fatal("claimed incomplete cleanup complete", err)
				}
			} else if len(state.objects) != 0 || !strings.Contains(err.Error(), "cleanup completed") {
				t.Fatal("cleanup not verified", err)
			}
			if mode != "no TaskUID" && !strings.Contains(f.out.String(), "execution failed (assertions not evaluated)") {
				t.Fatal("missing execution failure label", f.out.String())
			}
			if strings.Contains(err.Error()+f.out.String()+fmt.Sprint(a.Err), nativeCanary) {
				t.Fatal("payload leaked")
			}
			if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
				t.Fatal("abort exported receipt", e)
			}
			if _, _, e := p.store.latest(t.Context(), p.suite); !errors.Is(e, errSuitePassMissing) {
				t.Fatal("abort registered proof", e)
			}
		})
	}
}

func TestRootEvalJSONRefusesOtherRuntimeCountsOffline(t *testing.T) {
	for _, runtime := range []string{"kagent", "agentsessions"} {
		t.Run(runtime, func(t *testing.T) {
			t.Setenv("KMX_HOME", privateSuiteDir(t))
			var opt SuiteWorkspaceEvaluationOptions
			if runtime == "kagent" {
				opt = suiteKagentEvaluationOptions(t)
			} else {
				opt = nativeEvaluationOptions(t)
				member := filepath.Join(opt.Workspace, "agents/greeter/member.yaml")
				raw, err := os.ReadFile(member)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, member, strings.Split(string(raw), "\nruntime:")[0]+"\nruntime: {id: agentsessions, version: "+workspace.AgentSessionsVersion+", engine: chat}\n")
				opt.ToContext, opt.ToNamespace, opt.Inference, opt.ResultPort = "", "", "", ""
				opt.DevelopmentHost = filepath.Join(t.TempDir(), "missing-host.json")
			}
			opt.JSON = true
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if p != nil {
				p.Close()
			}
			if p != nil || err == nil || err.Error() != "native eval JSON output supports only Orka" {
				t.Fatalf("unsupported runtime counts not refused before execution prerequisites: %v", err)
			}
			if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
				t.Fatal("JSON refusal wrote evidence", e)
			}
		})
	}
}

func TestRootEvalJSONRunSummary(t *testing.T) {
	for _, mode := range []string{"Failed", "Cancelled", "late", "no TaskUID", "cleanup", "threshold pass", "threshold miss"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("KMX_HOME", privateSuiteDir(t))
			a, _, f, state := publicNativeEvaluationFixture(t)
			opt := nativeEvaluationOptions(t)
			opt.ResultPort, opt.ManagedPass, opt.JSON = f.opt.ResultPort, true, true
			repeatedCandidateCase(t, opt.Workspace)
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			f.answers[nativeCanary+" first"] = nativeCanary + " answer"
			executed, passed, aborted := 1, 0, true
			var tasks *int
			if strings.HasPrefix(mode, "threshold") {
				tasks = new(int)
				state.afterCreate = func(key string) {
					if strings.HasPrefix(key, "Task/") {
						*tasks++
						f.answers[nativeCanary+" first"] = nativeCanary + " answer"
						if *tasks == 1 || mode == "threshold miss" && *tasks == 2 {
							f.answers[nativeCanary+" first"] = "wrong"
						}
					}
				}
				executed, passed, aborted = 3, 2, false
				if mode == "threshold miss" {
					passed = 1
				}
			} else {
				tasks = candidateAbortScenario(t, state, mode)
				if mode == "late" {
					executed, passed = 3, 2
				}
				if mode == "no TaskUID" {
					executed = 0
				}
			}
			var out, diagnostics bytes.Buffer
			a.Out, a.Err = &out, &diagnostics
			err = a.EvaluateSuiteWorkspace(t.Context(), p)
			var report struct {
				Status, Detail, Error, Cleanup       string
				Executed, NotExecuted, Passed, Total int
				Aborted                              bool
			}
			if e := json.Unmarshal(out.Bytes(), &report); e != nil {
				t.Fatalf("stdout is not one JSON document: %s (%v)", &out, e)
			}
			if report.Executed != executed || report.NotExecuted != 3-executed || report.Passed != passed || report.Total != 3 || report.Aborted != aborted || *tasks != executed {
				t.Fatalf("dishonest run report: %+v Tasks=%d err=%v", report, *tasks, err)
			}
			if mode == "threshold pass" {
				if err != nil || report.Status != "complete" || !strings.Contains(report.Detail, "2/3 runs passed") {
					t.Fatal("threshold pass refused", report, err)
				}
				if _, _, e := p.store.latest(t.Context(), p.suite); e != nil {
					t.Fatal("pass not registered", e)
				}
			} else {
				if err == nil || report.Status != "failed" || report.Error == "" {
					t.Fatal("abort claimed success", report, err)
				}
				if mode == "threshold miss" && (!strings.Contains(report.Detail, "1/3 runs passed") || strings.Contains(report.Detail, "aborted")) {
					t.Fatal("threshold miss claimed execution abort", report)
				}
				if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
					t.Fatal("failure exported", e)
				}
			}
			if mode == "cleanup" {
				if strings.Contains(report.Cleanup, "completed") || len(state.objects) == 0 {
					t.Fatal("false cleanup claim", report)
				}
			} else if report.Cleanup != "candidate cleanup completed" || len(state.objects) != 0 {
				t.Fatal("missing verified cleanup", report)
			}
			if !strings.Contains(diagnostics.String(), "Workspace:") || strings.Contains(out.String()+diagnostics.String()+fmt.Sprint(err), nativeCanary) {
				t.Fatal("bad JSON channels/privacy", &out, &diagnostics, err)
			}
		})
	}
}

func TestWorkspaceLiftExecutionAbortSummary(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		for _, mode := range []string{"Failed", "Cancelled", "late", "no TaskUID", "cleanup"} {
			t.Run(fmt.Sprintf("json=%t/%s", jsonOutput, mode), func(t *testing.T) {
				a, opt, state, _, _, writes := workspaceLiftFixture(t, func(root string) { repeatedCandidateCase(t, root) })
				opt.JSON, opt.Verbose = jsonOutput, !jsonOutput
				var out, diagnostics bytes.Buffer
				a.Out, a.Err = &out, &diagnostics
				tasks := candidateAbortScenario(t, state, mode)
				p, err := PrepareWorkspaceLift(t.Context(), opt)
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close()
				r, err := a.LiftWorkspace(t.Context(), p)
				executed, passed := 1, 0
				if mode == "late" {
					executed, passed = 3, 2
				}
				if mode == "no TaskUID" {
					executed = 0
				}
				want := fmt.Sprintf("%d of 3 runs executed; %d passed; aborted on execution error; %d not executed", executed, passed, 3-executed)
				if err == nil || *tasks != executed || !strings.Contains(r.Eval.Detail, want) || r.Eval.Passed != passed || r.Eval.Total != 3 {
					t.Fatalf("dishonest Lift abort: %+v, %v; Tasks=%d", r, err, *tasks)
				}
				if mode == "cleanup" {
					if strings.Contains(r.Eval.Detail, "cleanup completed") || len(state.objects) == 0 {
						t.Fatal("false cleanup claim", r)
					}
				} else if len(state.objects) != 0 || !strings.Contains(r.Eval.Detail, "cleanup completed") {
					t.Fatal("cleanup not verified", r)
				}
				if *writes != 0 || r.Push.Status != "not-attempted" || r.Lift.Status != "not-attempted" || len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR"))) != 0 {
					t.Fatal("abort advanced pipeline", r)
				}
				// Lift finalizes its store before returning; inspect persisted output.
				if records, e := filepath.Glob(filepath.Join(p.store.partition, "*.json")); e != nil || len(records) != 0 {
					t.Fatal("abort registered proof", records, e)
				}
				if jsonOutput {
					var document struct {
						Eval struct {
							Executed, NotExecuted int
							Aborted               bool
						}
					}
					if e := json.Unmarshal(out.Bytes(), &document); e != nil || document.Eval.Executed != executed || document.Eval.NotExecuted != 3-executed || !document.Eval.Aborted {
						t.Fatalf("missing JSON counts: %s (%v)", &out, e)
					}
				} else if out.Len() != 0 || !strings.Contains(diagnostics.String(), want) {
					t.Fatal("bad text channels", &out, &diagnostics)
				}
				if strings.Contains(out.String()+diagnostics.String()+err.Error(), nativeCanary) {
					t.Fatal("payload leaked")
				}
			})
		}
	}
}

func TestCandidateCoreRepeatThreshold(t *testing.T) {
	for _, scenario := range []string{"threshold pass", "threshold fail", "late cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			a, _, f, state := publicNativeEvaluationFixture(t)
			opt := nativeEvaluationOptions(t)
			opt.ResultPort = f.opt.ResultPort
			casePath := filepath.Join(opt.Workspace, "eval/example.yaml")
			raw, err := os.ReadFile(casePath)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, casePath, string(raw)+"\nruns: 3\nminPasses: 2\n")
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			for _, frame := range p.inventory {
				f.answers[frame.Case.Input] = nativeCanary + " answer"
			}
			tasks := 0
			state.afterCreate = func(key string) {
				if strings.HasPrefix(key, "Task/") {
					tasks++
					if (tasks == 1 && scenario != "late cancellation") || scenario == "threshold fail" {
						f.answers[p.inventory[0].Case.Input] = "wrong"
					} else {
						f.answers[p.inventory[0].Case.Input] = nativeCanary + " answer"
					}
					if scenario == "late cancellation" && tasks == 3 {
						cancel()
					}
				}
			}
			err = a.evaluateSuiteWorkspaceCandidate(ctx, p)
			if len(state.objects) != 0 {
				t.Fatal("candidate resources not cleaned")
			}
			if scenario == "late cancellation" {
				if !errors.Is(err, context.Canceled) || tasks != 3 {
					t.Fatalf("late cancellation did not veto threshold: tasks=%d err=%v", tasks, err)
				}
				if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
					t.Fatal("late unknown exported")
				}
				return
			}
			if scenario == "threshold fail" {
				if !errors.Is(err, errSuiteNativeCaseFailed) || tasks != 3 {
					t.Fatalf("known failure did not complete group: tasks=%d err=%v", tasks, err)
				}
				if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
					t.Fatal("failed threshold exported")
				}
				if !strings.Contains(err.Error(), "3 of 4 runs executed; 0 passed; 1 not executed") || strings.Contains(err.Error(), "aborted on execution error") {
					t.Fatal("partial threshold miss misreported as complete or execution abort", err)
				}
				return
			}
			r, readErr := readSuitePassingReceipt(t.Context(), opt.Out)
			if err != nil || readErr != nil || tasks != len(p.inventory) || r == nil || len(r.Cases) != 4 || r.Cases[0].Verdict != "fail" {
				t.Fatalf("candidate ignored threshold: tasks=%d err=%v receipt=%v", tasks, err, readErr)
			}
			if !strings.Contains(f.out.String(), "run 3/3") {
				t.Fatal("missing repeated-run labels")
			}
		})
	}
}

func TestCandidateCorePrivateLifecycleOrderingAndCancellation(t *testing.T) {
	for _, stage := range []string{"pass", "case fail", "cancel Task", "cancel cleanup", "unknown Agent", "cleanup permission", "final source", "final shared", "frozen config"} {
		t.Run(stage, func(t *testing.T) {
			a, _, _, state, reads, f, p := candidateCoreSetup(t)
			shared := mustJSON(t, reads.objects)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if stage == "case fail" {
				f.answers[p.inventory[0].Case.Input] = "wrong"
			}
			state.afterCreate = func(key string) {
				if strings.HasPrefix(key, "Task/") {
					if stage == "cancel Task" {
						cancel()
					}
					if stage == "frozen config" {
						a.Cfg.KubeContext = "changed-caller-config"
						a.Run.Context = context.Background()
					}
				}
			}
			cleanupObserved, finalFence, guards := false, false, 0
			state.beforeDelete = func(cleanCtx context.Context, cmd *exec.Cmd) error {
				cleanupObserved = true
				deadline, ok := cleanCtx.Deadline()
				if cleanCtx.Err() != nil || !ok || time.Until(deadline) > 90*time.Second || cmd.Cancel == nil || !slices.Contains(cmd.Args, "kind-test") {
					t.Fatal("cleanup Runner/context/scope not fresh bounded and frozen")
				}
				if stage == "cancel cleanup" {
					cancel()
				}
				if stage == "cleanup permission" {
					return errors.New(nativeCanary)
				}
				if _, err := os.Stat(p.options.Out); !os.IsNotExist(err) {
					t.Fatal("export before root absence")
				}
				return nil
			}
			state.afterDelete = func(key string) {
				if strings.HasPrefix(key, "Provider/") {
					if stage == "final source" {
						writeFile(t, filepath.Join(p.root, "eval/example.yaml"), "id: edited\ninput: edited\nexpectContains: [yes]\n")
					}
					if stage == "final shared" {
						nativeMetadata(reads.objects["providers.core.orka.ai/inference"])["resourceVersion"] = "changed"
					}
				}
			}
			capture := a.captureCommand
			a.captureCommand = func(callCtx context.Context, cmd *exec.Cmd) error {
				if slices.Contains(cmd.Args, "config") {
					guards++
				}
				if cleanupObserved && len(state.objects) == 0 && slices.Contains(cmd.Args, "inference") {
					finalFence = true
				}
				if stage == "unknown Agent" && len(state.creates) == 1 && slices.Contains(cmd.Args, "create") && !slices.Contains(cmd.Args, "--dry-run=server") {
					if err := capture(callCtx, cmd); err != nil {
						return err
					}
					return errors.New(nativeCanary)
				}
				return capture(callCtx, cmd)
			}
			a.Out = suiteSummaryObserver(func(raw []byte) (int, error) {
				if bytes.Contains(raw, []byte("Passing native receipt")) && (len(state.objects) != 0 || !finalFence) {
					t.Fatal("export before closure/final fences")
				}
				return f.out.Write(raw)
			})
			err := a.evaluateSuiteWorkspaceCandidate(ctx, p)
			_, statErr := os.Stat(p.options.Out)
			if guards != 1 {
				t.Fatal("did not acquire one fresh outer guard", guards, err)
			}
			if stage == "pass" || stage == "frozen config" {
				receipt, readErr := readSuitePassingReceipt(t.Context(), p.options.Out)
				if err != nil || readErr != nil || receipt.CandidateCleanup != "complete" || receipt.Subject.EvaluationRun != receipt.RunID || len(receipt.Cases) != 2 || len(state.objects) != 0 || !bytes.Equal(shared, mustJSON(t, reads.objects)) {
					t.Fatal("private full pass", err, readErr)
				}
				out, diffErr := diffExecute(t, receipt, receipt)
				if diffErr != nil || !strings.Contains(out, "comparison: complete") {
					t.Fatal("candidate selfdiff", diffErr, out)
				}
				normalized, ok := normalizeSuitePassingEvaluation(receipt)
				raw, _ := agentsuite.Marshal(suitePassingDiffSubject{receipt.Subject, receipt.Provider, receipt.Agent, receipt.Kagent})
				if !ok || normalized.SuiteIdentity != agentruntime.RenderedBundleDigest([][]byte{[]byte("kmx.native-suite.diff-subject/v1"), raw}) {
					t.Fatal("selfdiff hashed scanner mask")
				}
				if _, pushErr := a.PushSuiteWorkspaceEvaluated(t.Context(), p.root, "suite:v1", filepath.Join(t.TempDir(), "layout"), false, false, SuitePushProvenance{}, p.options.Out); pushErr != nil {
					t.Fatal("exact-S consumer", pushErr)
				}
			} else {
				if err == nil || !os.IsNotExist(statErr) || strings.Contains(err.Error(), nativeCanary) {
					t.Fatal("unsafe failure export/diagnostic", err, statErr)
				}
				if stage == "cleanup permission" && (!errors.Is(err, errSuiteCandidateCleanup) || !strings.Contains(err.Error(), "delete conflict or acknowledgement unknown")) {
					t.Fatal("safe cleanup classification lost", err)
				}
				if strings.HasPrefix(stage, "cancel") && (!errors.Is(err, context.Canceled) || len(state.objects) != 0) {
					t.Fatal("caller cancellation lost cleanup", err, state.objects)
				}
				if stage == "case fail" && (!errors.Is(err, errSuiteNativeCaseFailed) || len(state.creates) != 3) {
					t.Fatal("continued after first failed case", err, state.creates)
				}
			}
			if !cleanupObserved {
				t.Fatal("partial effects lacked cleanup")
			}
			before := len(state.creates)
			if err := a.evaluateSuiteWorkspaceCandidate(t.Context(), p); err == nil || len(state.creates) != before {
				t.Fatal("prepared operation reused")
			}
		})
	}
}

func TestCandidateCorePublicEvaluationUsesCandidate(t *testing.T) {
	a, _, f, state := publicNativeEvaluationFixture(t)
	opt := nativeEvaluationOptions(t)
	opt.ResultPort = f.opt.ResultPort
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, frame := range p.inventory {
		f.answers[frame.Case.Input] = nativeCanary + " answer"
	}
	marked := map[string]string{}
	state.afterCreate = func(key string) {
		doc := state.objects[key]
		kind := doc["kind"].(string)
		run, _ := nativeMetadata(doc)["annotations"].(map[string]any)[suiteCandidateRunMarker].(string)
		if !evaluationRunID(run) {
			t.Fatal("missing full run annotation", kind)
		}
		marked[kind] = run
	}
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if len(marked) != 3 || marked["Provider"] != marked["Agent"] || marked["Agent"] != marked["Task"] {
		t.Fatal("full run annotation not stamped on all KMX roots", marked)
	}
	receipt, err := readSuitePassingReceipt(t.Context(), opt.Out)
	if err != nil || receipt.CandidateCleanup != "complete" || !evaluationRunID(receipt.Subject.EvaluationRun) || receipt.Subject.PhysicalMember == "" || len(state.deletes) != 4 || len(state.objects) != 0 {
		t.Fatal("public candidate lifecycle missing", err, state.deletes)
	}
}

func TestCandidateCoreCleanupParserConsumedKeysOnly(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"apiVersion":"batch/v1","kind":"JobList","metadata":{"resourceVersion":"1"},"items":[]}`, true},
		{`{"apiVersion":"batch/v1","APIVersion":"batch/v1"}`, false},
		{`{"metadata":{"resourceVersion":"1","resourceVersion":"1"}}`, false},
		{`{"items":[{"metadata":{"uid":"a","UID":"a"}}]}`, false},
		{`{"items":[{"spec":{},"Spec":{}}]}`, false},
		{`{"items":[{"metadata":{"annotations":{"kaimahi.dev/suite-evaluation-run":"a","KAIMAHI.DEV/SUITE-EVALUATION-RUN":"b"}}}]}`, false},
		{`{"items":[{"metadata":{"deletionTimestamp":"a","DeletionTimestamp":"b"}}]}`, false},
		{`{"items":[{"metadata":{"ownerReferences":[{"uid":"a","UID":"a"}]}}]}`, false},
		{`{"items":[{"metadata":{"labels":{"orka.ai/task":"a","ORKA.AI/TASK":"a"}}}]}`, false},
		{`{"items":[{"metadata":{"annotations":{"HTTP_PROXY":"secret","http_proxy":"secret"}},"data":{"KEY":"a","key":"b"}}]}`, true},
		{`{"opaque":` + strings.Repeat("[", 34) + `0` + strings.Repeat("]", 34) + `}`, false},
		{`{} {}`, false},
	} {
		if got := suiteCandidateCleanupPageJSON([]byte(tc.raw)); got != tc.want {
			t.Fatalf("consumed ambiguity/opaque depth %s: %v", tc.raw, got)
		}
	}
}
