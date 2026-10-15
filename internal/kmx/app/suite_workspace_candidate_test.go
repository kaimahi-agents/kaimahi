package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

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
				raw, _ := spec.Marshal(suitePassingDiffSubject{receipt.Subject, receipt.Provider, receipt.Agent, receipt.Kagent})
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

func TestCandidateCorePublicEvaluationStaysCanonical(t *testing.T) {
	a, _, _, f := nativeEvaluationFixture(t)
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
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	receipt, err := readSuitePassingReceipt(t.Context(), opt.Out)
	if err != nil || receipt.CandidateCleanup != "" || receipt.Subject.PhysicalMember != "" || receipt.Subject.EvaluationRun != "" {
		t.Fatal("publicly activated candidate", err)
	}
	for _, call := range orkaCalls(t, f.dir) {
		if slices.Contains(call.Args, "delete") {
			t.Fatal("public evaluator cleans up")
		}
		if call.Document != nil && call.Document["kind"] == "Agent" && orkaObjectName(call.Document) != "greeter" {
			t.Fatal("public physical candidate placement")
		}
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
