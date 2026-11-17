//go:build windows

package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/workspacetest"
)

func windowsPassingFixture(t *testing.T) (string, string, *workspace.NativeSuite, *suitePassingReceipt, []byte) {
	t.Helper()
	root := windowsNativeWorkspaceFixture(t)
	operation, err := workspacetest.FreezeOrka(t.Context(), workspacetest.Summary{Workspace: root, Runtime: "orka", Context: "kind-test", Namespace: OrkaNamespace, Inference: "provider:inference"}, filepath.Join(privateSuiteDir(t), "validation"))
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	suite, err := operation.Suite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := nativeFixture(t)
	native, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference")
	if err != nil {
		t.Fatal(err)
	}
	placement := &suiteNativePlacement{Subject: native.Subject, Provider: suiteNativeOwnedResource{"owned-provider", 1, agentruntime.EvaluationAnswerDigest("admitted provider spec")}, Agent: suiteNativeOwnedResource{"owned-agent", 1, agentruntime.EvaluationAnswerDigest("admitted agent spec")}}
	observations := make([]suitePassingObservation, 0, len(suite.Cases))
	for _, file := range suite.Cases {
		c, err := workspace.DecodePackagedCase(file.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		answer := nativeCanary + " answer"
		rows := agentruntime.EvaluateAssertions(c, agentruntime.EvaluationEvidence{AnswerAvailable: true, Answer: answer})
		observations = append(observations, suitePassingObservation{CaseDigest: agentruntime.EvaluationCaseDigest(file.Bytes), InputDigest: agentruntime.EvaluationInputDigest(c.Input), EvaluationReceipt: agentruntime.EvaluationReceipt{CaseID: c.ID, Verdict: agentruntime.EvaluationPass, TaskName: "task-" + c.ID, TaskUID: "uid-" + c.ID, AnswerSHA256: agentruntime.EvaluationAnswerDigest(answer), Assertions: rows}})
	}
	receipt, err := newSuitePassingReceiptFrozen(t.Context(), suite, operation.Summary().SourceFingerprint, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), receipt, suite)
	if err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(privateSuiteDir(t), "cache"), suite, receipt, raw
}

func TestWindowsSuitePassingExportLifecycle(t *testing.T) {
	root, cache, suite, receipt, _ := windowsPassingFixture(t)
	parent := filepath.Join(privateSuiteDir(t), "private")
	directory, err := fileaccess.EnsurePrivateDirectory(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(parent, "receipt.json")
	output, err := preflightSuitePassingExport(t.Context(), destination, root, cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := output.publish(t.Context(), receipt, suite); err != nil {
		t.Fatal(err)
	}
	if !output.linked || !output.visibilityKnown {
		t.Fatalf("publication state = linked:%t known:%t", output.linked, output.visibilityKnown)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readSuitePassingReceipt(t.Context(), destination); err != nil {
		t.Fatal(err)
	}
	if second, err := preflightSuitePassingExport(t.Context(), destination, root, cache); err == nil {
		_ = second.Close()
		t.Fatal("existing receipt accepted")
	}
}

func TestWindowsSuitePassStoreLifecycleAndLock(t *testing.T) {
	root, cache, suite, _, raw := windowsPassingFixture(t)
	t.Setenv("KMX_HOME", filepath.Join(privateSuiteDir(t), "state"))
	store, err := openSuitePassStore(t.Context(), root, cache, suite.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if competing, err := openSuitePassStore(t.Context(), store.workspace, store.cache, suite.Digest); err == nil {
		_ = competing.Close()
		t.Fatal("competing workspace store acquired the lock")
	}
	record := commitStoreReceipt(t, store, suite, raw)
	if _, selected, err := store.latest(t.Context(), suite); err != nil || selected != record {
		t.Fatalf("latest = %q, %v", selected, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openSuitePassStore(t.Context(), root, cache, suite.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, selected, err := reopened.latest(t.Context(), suite); err != nil || selected != record {
		t.Fatalf("reopened latest = %q, %v", selected, err)
	}
}

func TestWindowsSuitePassStoreRefusesUnsafeStateBeforeRecord(t *testing.T) {
	home := privateSuiteDir(t)
	workspace := filepath.Join(home, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(workspace, "state")
	t.Setenv("KMX_HOME", state)
	if _, err := openSuitePassStore(t.Context(), workspace, filepath.Join(home, "cache"), "sha256:"+strings.Repeat("a", 64)); !errors.Is(err, errSuitePassStore) {
		t.Fatalf("state inside workspace = %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("refusal created state: %v", err)
	}
}
