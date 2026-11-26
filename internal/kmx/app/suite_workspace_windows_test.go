//go:build windows

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/workspacetest"
)

func TestWindowsWorkspaceTestCandidateManagedProofJourney(t *testing.T) {
	t.Run("Test refuses non-kind loopback before source freeze", func(t *testing.T) {
		home := privateSuiteDir(t)
		t.Setenv("KMX_HOME", home)
		a, _, _, state := publicNativeEvaluationFixture(t)
		t.Setenv("KMX_HOME", home)
		a.CommandShell = "powershell"
		a.Cfg.KubeContext = "docker-desktop"
		a.Cfg.Confirm = "docker-desktop"
		capture := a.captureCommand
		a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
			if slices.Contains(cmd.Args, "config") {
				_, err := fmt.Fprint(cmd.Stdout, `{"current-context":"docker-desktop","clusters":[{"name":"local","cluster":{"server":"https://127.0.0.1:6443"}}],"contexts":[{"name":"docker-desktop","context":{"cluster":"local"}}]}`)
				return err
			}
			return capture(ctx, cmd)
		}
		if err := a.PrepareWindowsSuiteWorkspaceEvaluation(t.Context()); err == nil || !strings.Contains(err.Error(), "requires an existing remote Orka context") || state.lists != 0 || len(state.deletes) != 0 {
			t.Fatalf("local Test preflight reached cluster inventory: err=%v lists=%d deletes=%v", err, state.lists, state.deletes)
		}
		if _, err := os.Stat(filepath.Join(home, "workspace-test")); !os.IsNotExist(err) {
			t.Fatalf("local Test preflight created validation state: %v", err)
		}
	})
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
	t.Run("cleanup refuses non-kind loopback before inventory", func(t *testing.T) {
		a, _, _, state := publicNativeEvaluationFixture(t)
		a.CommandShell = "powershell"
		a.Cfg.KubeContext = "docker-desktop"
		capture := a.captureCommand
		a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
			if slices.Contains(cmd.Args, "config") {
				_, err := fmt.Fprint(cmd.Stdout, `{"current-context":"docker-desktop","clusters":[{"name":"local","cluster":{"server":"https://127.0.0.1:6443"}}],"contexts":[{"name":"docker-desktop","context":{"cluster":"local"}}]}`)
				return err
			}
			return capture(ctx, cmd)
		}
		if err := a.CleanupSuiteCandidates(t.Context(), SuiteCandidateCleanupOptions{Namespace: OrkaNamespace}); err == nil || !strings.Contains(err.Error(), "requires an existing remote Orka context") || state.lists != 0 || len(state.deletes) != 0 {
			t.Fatalf("non-kind loopback reached candidate inventory: err=%v lists=%d deletes=%v", err, state.lists, state.deletes)
		}
	})
	t.Run("cleanup rechecks remote posture before consent", func(t *testing.T) {
		a, _, _, state := publicNativeEvaluationFixture(t)
		state.externalTasks = false
		root := windowsNativeWorkspaceFixture(t)
		operation, err := workspacetest.FreezeOrka(t.Context(), workspacetest.Summary{Workspace: root, Runtime: "orka", Context: "kind-test", Namespace: OrkaNamespace, Inference: "provider:inference"}, filepath.Join(privateSuiteDir(t), "cleanup-validation"))
		if err != nil {
			t.Fatal(err)
		}
		defer operation.Close()
		suite, err := operation.Suite(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		native, err := a.prepareSuiteNativeCandidate(t.Context(), suite, OrkaNamespace, "inference", candidateRun)
		if err != nil {
			t.Fatal(err)
		}
		provider := nativeOwnedDoc(t, native, "Provider")
		addOrkaMarker(provider, suiteNativeOwner(native.Subject).markers())
		state.objects["Provider/"+native.Subject.PhysicalMember] = provider
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
		err = a.CleanupSuiteCandidates(t.Context(), SuiteCandidateCleanupOptions{Namespace: OrkaNamespace, RunID: candidateRun})
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
	t.Run("managed unresolved visibility stays uncertain", func(t *testing.T) {
		root, _, suite, _, raw := windowsPassingFixture(t)
		t.Setenv("KMX_HOME", privateSuiteDir(t))
		parent := filepath.Join(privateSuiteDir(t), "explicit")
		directory, err := fileaccess.EnsurePrivateDirectory(t.Context(), parent)
		if err != nil {
			t.Fatal(err)
		}
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(parent, "receipt.json")
		opt := SuiteWorkspaceEvaluationOptions{Workspace: root, ToContext: "remote", ToNamespace: OrkaNamespace, Inference: "provider:inference", Out: destination, ManagedPass: true, CaseTimeout: 5 * time.Minute}
		p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		if p.suite.Digest != suite.Digest {
			t.Fatal("fixture suite changed during preparation")
		}
		if err := p.output.publishBytes(t.Context(), raw); err != nil {
			t.Fatal(err)
		}
		p.completed = raw
		p.store.completion.publishPrivate = func(context.Context, *fileaccess.Directory, string, []byte) (fileaccess.PublicationResult, error) {
			return fileaccess.PublicationResult{}, errors.New("unresolved publication")
		}
		err = (&App{Err: io.Discard}).finishManagedEvaluation(t.Context(), p, nil)
		var uncertain *suitePassRegistrationUncertain
		if err == nil || p.record != "" || !errors.As(err, &uncertain) || !strings.Contains(err.Error(), "registration is uncertain") || strings.Contains(err.Error(), "not registered") {
			t.Fatalf("unresolved managed registration = record:%q err:%v", p.record, err)
		}
		if _, readErr := readSuitePassingReceipt(t.Context(), destination); readErr != nil {
			t.Fatalf("complete explicit receipt was lost: %v", readErr)
		}
	})
}

func TestWindowsWorkspaceTestRefusesAlternateRuntimeClearly(t *testing.T) {
	for _, profile := range []struct {
		name, declaration string
	}{
		{"kagent", "runtime:\n  id: kagent\n  version: v0.10.3\n  engine: go\n"},
		{"agentsessions", "runtime:\n  id: agentsessions\n  version: " + workspace.AgentSessionsVersion + "\n  engine: chat\n"},
	} {
		t.Run(profile.name, func(t *testing.T) {
			root := windowsNativeWorkspaceFixture(t)
			member := filepath.Join(root, "agents", "greeter", "member.yaml")
			raw, err := os.ReadFile(member)
			if err != nil {
				t.Fatal(err)
			}
			before, _, _ := strings.Cut(string(raw), "runtime:\n")
			if err := os.WriteFile(member, []byte(before+profile.declaration), 0o600); err != nil {
				t.Fatal(err)
			}
			opt := SuiteWorkspaceEvaluationOptions{Workspace: root, ToContext: "remote", ToNamespace: OrkaNamespace, Inference: "provider:inference", ManagedPass: true, CaseTimeout: 5 * time.Minute}
			prepared, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if prepared != nil || err == nil || err.Error() != "native Windows workspace Test supports only explicit remote Orka evaluation" || strings.Contains(err.Error(), "fail-fast") {
				t.Fatalf("alternate runtime = prepared:%v err:%v", prepared, err)
			}
		})
	}
}

func TestWindowsWorkspaceTestValidationResidueNamesRecovery(t *testing.T) {
	home := privateSuiteDir(t)
	t.Setenv("KMX_HOME", home)
	validation := filepath.Join(home, "workspace-test")
	parent, err := fileaccess.EnsurePrivateDirectory(t.Context(), validation)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := fileaccess.EnsurePrivateFileAt(t.Context(), parent, "lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	residue, err := fileaccess.CreatePrivateDirectoryAt(t.Context(), parent, ".workspace-test-private-canary")
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(residue.Close(), parent.Close()); err != nil {
		t.Fatal(err)
	}
	root := windowsNativeWorkspaceFixture(t)
	opt := SuiteWorkspaceEvaluationOptions{Workspace: root, ToContext: "remote", ToNamespace: OrkaNamespace, Inference: "provider:inference", ManagedPass: true, CaseTimeout: 5 * time.Minute}
	prepared, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if prepared != nil || !errors.Is(err, workspacetest.ErrValidationStateRecovery) || strings.Contains(err.Error(), home) || strings.Contains(err.Error(), "private-canary") {
		t.Fatalf("validation residue = prepared:%v err:%v", prepared, err)
	}
	if _, statErr := os.Stat(filepath.Join(home, "passing")); !os.IsNotExist(statErr) {
		t.Fatalf("validation residue reached managed proof state: %v", statErr)
	}
}
