package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuiteManagedEvaluationRegistersOnlyCompleteCandidate(t *testing.T) {
	for _, mode := range []string{"no output", "explicit output", "case failure", "cleanup failure", "late output", "close output", "source drift"} {
		t.Run(mode, func(t *testing.T) {
			a, _, f, state := publicNativeEvaluationFixture(t)
			opt := nativeEvaluationOptions(t)
			opt.ResultPort = f.opt.ResultPort
			opt.ManagedPass = true
			explicit := opt.Out
			if mode != "explicit output" {
				opt.Out = ""
			}
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			for _, frame := range p.inventory {
				f.answers[frame.Case.Input] = nativeCanary + " answer"
			}
			switch mode {
			case "case failure":
				f.answers[p.inventory[0].Case.Input] = "wrong"
			case "cleanup failure":
				state.beforeDelete = func(context.Context, *exec.Cmd) error { return errors.New("cleanup refused") }
			case "late output":
				a.Out = suiteCaseSummaryFailWriter{needle: "Full passing native"}
			case "close output":
				p.output.directory.Close()
			case "source drift":
				a.Out = suiteSummaryObserver(func(raw []byte) (int, error) {
					if bytes.Contains(raw, []byte("Full passing native")) {
						writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), "id: changed\ninput: changed\nexpectContains: [changed]\n")
					}
					return len(raw), nil
				})
			}
			err = a.EvaluateSuiteWorkspace(t.Context(), p)
			passing := mode == "no output" || mode == "explicit output"
			if (err == nil) != passing {
				t.Fatalf("wrong outcome: %v", err)
			}
			receipt, record, lookupErr := p.store.latest(t.Context(), p.suite)
			if passing {
				if lookupErr != nil || receipt.CandidateCleanup != "complete" || record == "" || len(state.objects) != 0 {
					t.Fatalf("incomplete candidate proof: %s %v", record, lookupErr)
				}
				if mode == "explicit output" {
					if _, err := readSuitePassingReceipt(t.Context(), explicit); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(explicit); err != nil {
						t.Fatal(err)
					}
					if _, _, err := p.store.latest(t.Context(), p.suite); err != nil {
						t.Fatal("managed copy depended on explicit file", err)
					}
				}
			} else if lookupErr == nil {
				t.Fatal("failed eval eligible", record)
			}
		})
	}
}

func TestSuiteManagedEvaluationFailurePreservesOlderPass(t *testing.T) {
	a, _, f, _ := publicNativeEvaluationFixture(t)
	opt := nativeEvaluationOptions(t)
	opt.ManagedPass = true
	opt.Out = ""
	opt.ResultPort = f.opt.ResultPort
	run := func(pass bool) (*PreparedSuiteWorkspaceEvaluation, error) {
		p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := p.Close(); err != nil {
				t.Error(err)
			}
		})
		for _, frame := range p.inventory {
			f.answers[frame.Case.Input] = nativeCanary + " answer"
		}
		if !pass {
			f.answers[p.inventory[0].Case.Input] = "wrong"
		}
		err = a.EvaluateSuiteWorkspace(t.Context(), p)
		return p, err
	}
	first, err := run(true)
	if err != nil {
		t.Fatal(err)
	}
	_, record, err := first.store.latest(t.Context(), first.suite)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := run(false)
	if err == nil {
		t.Fatal("failed case passed")
	}
	_, selected, err := second.store.latest(t.Context(), second.suite)
	if err != nil || selected != record {
		t.Fatalf("older completed pass invalidated: %s %v", selected, err)
	}
}

func TestSuiteManagedSessionsWithoutOutput(t *testing.T) {
	a, opt, calls := suiteSessionsEvaluationFixture(t)
	opt.ManagedPass = true
	opt.Out = ""
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := p.store.latest(t.Context(), p.suite)
	if err != nil || receipt.Sessions == nil || calls.Load() != 2 {
		t.Fatalf("Sessions proof lost: %v", err)
	}
	// Managed proof does not grant Sessions Lift eligibility.
	raw, err := os.ReadFile(p.record)
	if err != nil || !bytes.Contains(raw, []byte(`"native-agentsessions"`)) {
		t.Fatal("runtime not retained", err)
	}
}

func TestSuiteManagedEvaluationPreflightAndFinalization(t *testing.T) {
	a, _, f, _ := publicNativeEvaluationFixture(t)
	opt := nativeEvaluationOptions(t)
	opt.ManagedPass = true
	opt.Out = ""
	opt.ResultPort = f.opt.ResultPort
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, frame := range p.inventory {
		f.answers[frame.Case.Input] = nativeCanary + " answer"
	}
	// Force post-link persistence uncertainty; proof remains valid and identified.
	p.store.completion.directory.Close()
	err = a.EvaluateSuiteWorkspace(t.Context(), p)
	if err == nil || !strings.Contains(err.Error(), "persistence uncertain") {
		t.Fatalf("postcommit error lost: %v", err)
	}
	if _, record, err := p.store.latest(t.Context(), p.suite); err != nil || record == "" {
		t.Fatal("visible record revoked", err)
	}
}

func TestSuiteManagedEvaluationNoCommitAfterEvaluatorError(t *testing.T) {
	// Exercise finalization independently of a runtime: deferred client/output
	// errors are passed into the same finish boundary, never an export hook.
	for _, cause := range []error{errors.New("Sessions client.Close failed"), context.Canceled, io.ErrClosedPipe} {
		t.Run(cause.Error(), func(t *testing.T) {
			store, suite, raw := passStoreFixture(t)
			if err := store.reserve(t.Context()); err != nil {
				t.Fatal(err)
			}
			output, err := preflightSuitePassingExport(t.Context(), filepath.Join(store.partition, ".pending"), store.workspace, store.cache)
			if err != nil {
				t.Fatal(err)
			}
			p := &PreparedSuiteWorkspaceEvaluation{store: store, suite: suite, output: output, completed: raw, root: store.workspace, options: SuiteWorkspaceEvaluationOptions{ManagedPass: true}}
			if err := (&App{Err: io.Discard}).finishManagedEvaluation(t.Context(), p, cause); !errors.Is(err, cause) {
				t.Fatalf("evaluator close error lost: %v", err)
			}
			if _, _, err := store.latest(t.Context(), suite); err == nil {
				t.Fatal("evaluator error indexed proof")
			}
		})
	}
}
