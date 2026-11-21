//go:build windows

package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWindowsWorkspaceTestCandidateManagedProofJourney(t *testing.T) {
	t.Run("cleanup refuses local context before inventory", func(t *testing.T) {
		a, _, _, state := publicNativeEvaluationFixture(t)
		a.CommandShell = "powershell"
		a.Cfg.Confirm = "kind-test"
		if err := a.CleanupSuiteCandidates(t.Context(), SuiteCandidateCleanupOptions{Namespace: OrkaNamespace}); err == nil || !strings.Contains(err.Error(), "requires an existing remote Orka context") || state.lists != 0 || len(state.deletes) != 0 {
			t.Fatalf("local cleanup reached candidate inventory: err=%v lists=%d deletes=%v", err, state.lists, state.deletes)
		}
	})
	t.Run("cleanup refuses malformed context before inventory", func(t *testing.T) {
		a, _, _, state := publicNativeEvaluationFixture(t)
		a.CommandShell = "powershell"
		capture := a.captureCommand
		a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
			if slices.Contains(cmd.Args, "config") {
				_, err := fmt.Fprint(cmd.Stdout, "{")
				return err
			}
			return capture(ctx, cmd)
		}
		if err := a.CleanupSuiteCandidates(t.Context(), SuiteCandidateCleanupOptions{Namespace: OrkaNamespace}); err == nil || !strings.Contains(err.Error(), "requires an existing remote Orka context") || state.lists != 0 || len(state.deletes) != 0 {
			t.Fatalf("malformed context reached candidate inventory: err=%v lists=%d deletes=%v", err, state.lists, state.deletes)
		}
	})
	t.Run("cleanup rechecks remote posture before consent", func(t *testing.T) {
		a, run, state, _ := candidateCleanupSetup(t)
		cleanupTask(t, run, state)
		a.CommandShell = "powershell"
		a.Cfg.Confirm = "kind-test"
		capture := a.captureCommand
		configReads := 0
		a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
			if slices.Contains(cmd.Args, "config") {
				configReads++
				server := "https://managed.example.invalid"
				if configReads > 1 {
					server = "https://127.0.0.1:6443"
				}
				_, err := fmt.Fprintf(cmd.Stdout, `{"current-context":"kind-test","clusters":[{"name":"kind-test","cluster":{"server":%q}}],"contexts":[{"name":"kind-test","context":{"cluster":"kind-test"}}]}`, server)
				return err
			}
			return capture(ctx, cmd)
		}
		err := a.CleanupSuiteCandidates(t.Context(), SuiteCandidateCleanupOptions{Namespace: OrkaNamespace, RunID: candidateRun})
		if err == nil || !strings.Contains(err.Error(), "requires an existing remote Orka context") || configReads != 2 || state.lists < 6 || len(state.deletes) != 0 {
			t.Fatalf("posture drift authorized cleanup: err=%v configReads=%d lists=%d deletes=%v", err, configReads, state.lists, state.deletes)
		}
	})
	t.Run("candidate managed proof", func(t *testing.T) {
		a, _, fixture, state := publicNativeEvaluationFixture(t)
		t.Setenv("KMX_EVAL_REMOTE", "1")
		t.Setenv("KMX_EVAL_FORWARD_TARGET", fixture.resultAddress)
		a.CommandShell = "powershell"
		a.Cfg.Confirm = "kind-test"
		if err := a.preflightSuiteWorkspaceKubectl(); err != nil {
			t.Fatalf("pinned kubectl preflight: %v", err)
		}
		root := windowsNativeWorkspaceFixture(t)
		opt := SuiteWorkspaceEvaluationOptions{Workspace: root, ToContext: "kind-test", ToNamespace: OrkaNamespace, Inference: "provider:inference", ManagedPass: true, CaseTimeout: 5 * time.Minute}
		p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		for _, frame := range p.inventory {
			fixture.answers[frame.Case.Input] = nativeCanary + " answer"
		}
		if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
			calls := orkaCalls(t, fixture.dir)
			last := "none"
			if len(calls) > 0 {
				last = fmt.Sprint(calls[len(calls)-1].Args)
			}
			t.Fatalf("evaluation: %v; calls=%d last=%s diagnostics=%s", err, len(calls), last, a.Err)
		}
		receipt, record, err := p.store.latest(t.Context(), p.suite)
		if err != nil || record == "" || receipt.CandidateCleanup != "complete" || len(state.objects) != 0 {
			t.Fatalf("proof=%q receipt=%+v objects=%d err=%v", record, receipt, len(state.objects), err)
		}
		if _, err := os.Stat(filepath.Join(os.Getenv("KMX_HOME"), "suites")); !os.IsNotExist(err) {
			t.Fatalf("Windows workspace Test created an OCI cache: %v", err)
		}
	})
}
