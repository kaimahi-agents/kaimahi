//go:build windows

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsWorkspaceTestCandidateManagedProofJourney(t *testing.T) {
	a, _, fixture, state := publicNativeEvaluationFixture(t)
	t.Setenv("KMX_EVAL_REMOTE", "1")
	t.Setenv("KMX_EVAL_FORWARD_TARGET", fixture.resultAddress)
	a.CommandShell = "powershell"
	a.Cfg.Confirm = "kind-test"
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
		t.Fatal(err)
	}
	receipt, record, err := p.store.latest(t.Context(), p.suite)
	if err != nil || record == "" || receipt.CandidateCleanup != "complete" || len(state.objects) != 0 {
		t.Fatalf("proof=%q receipt=%+v objects=%d err=%v", record, receipt, len(state.objects), err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("KMX_HOME"), "suites")); !os.IsNotExist(err) {
		t.Fatalf("Windows workspace Test created an OCI cache: %v", err)
	}
}
